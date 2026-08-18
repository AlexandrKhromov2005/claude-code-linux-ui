package jobctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// fakeRunner satisfies Runner without a real App: it records calls and answers
// with canned text.
type fakeRunner struct {
	mgr *core.JobManager

	awaitID      string
	awaitTimeout int
	awaitText    string
	awaitDelay   time.Duration
	awaitBlocks  bool
	released     chan struct{}
}

func (f *fakeRunner) StartJob(command, description string) (*core.Job, error) {
	return &core.Job{ID: "job-test", PID: 42, Command: command, Description: description}, nil
}

func (f *fakeRunner) AwaitJob(ctx context.Context, id string, timeoutSeconds int) (string, error) {
	f.awaitID = id
	f.awaitTimeout = timeoutSeconds
	if f.awaitBlocks {
		<-ctx.Done()
		if f.released != nil {
			close(f.released)
		}
		return "", ctx.Err()
	}
	if f.awaitDelay > 0 {
		time.Sleep(f.awaitDelay)
	}
	return f.awaitText, nil
}

func (f *fakeRunner) Jobs() *core.JobManager { return f.mgr }

func startServer(t *testing.T, run Runner) *Server {
	t.Helper()
	s := New(run)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// rpc posts one JSON-RPC request and decodes the text content of the result.
func rpc(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	resp, err := http.Post("http://"+s.Addr()+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result map[string]any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Result
}

// toolText digs the first text block out of a tools/call result.
func toolText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in result: %v", result)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

func TestToolsListIncludesWait(t *testing.T) {
	s := startServer(t, &fakeRunner{})
	result := rpc(t, s, "tools/list", map[string]any{})
	b, _ := json.Marshal(result)
	for _, name := range []string{"start", "wait", "status", "logs", "stop"} {
		if !strings.Contains(string(b), fmt.Sprintf("%q", name)) {
			t.Errorf("tools/list is missing %q", name)
		}
	}
	// The wait description must tell a subagent it is the right caller — the
	// tool description is the only channel that reaches one.
	if !strings.Contains(string(b), "сабагент") {
		t.Error("wait description does not address subagents")
	}
}

// waitRPC posts a wait call and parses the SSE stream it answers with,
// returning the final result plus the raw body for keepalive assertions.
func waitRPC(t *testing.T, s *Server, args map[string]any) (map[string]any, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "wait", "arguments": args},
	})
	resp, err := http.Post("http://"+s.Addr()+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("wait answered with %q, want an SSE stream", ct)
	}
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var data string
	for _, line := range strings.Split(string(rawBody), "\n") {
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			data = after
		}
	}
	if data == "" {
		t.Fatalf("no data event in SSE body:\n%s", rawBody)
	}
	var out struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		t.Fatalf("bad final event %q: %v", data, err)
	}
	return out.Result, string(rawBody)
}

func TestWaitCallReachesRunner(t *testing.T) {
	run := &fakeRunner{awaitText: "Задача job-9 завершилась: успешно (код 0)"}
	s := startServer(t, run)

	result, _ := waitRPC(t, s, map[string]any{"id": "job-9", "timeout_seconds": 120})
	if got := toolText(t, result); !strings.Contains(got, "завершилась") {
		t.Errorf("wait returned %q", got)
	}
	if run.awaitID != "job-9" || run.awaitTimeout != 120 {
		t.Errorf("runner got (%q, %d), want (job-9, 120)", run.awaitID, run.awaitTimeout)
	}
}

func TestWaitWithoutIDIsAnError(t *testing.T) {
	s := startServer(t, &fakeRunner{})
	result, _ := waitRPC(t, s, map[string]any{})
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("wait without id should be a tool error, got %v", result)
	}
}

// The stream must carry keepalives while the wait is parked — they are what
// stops the CLI's transport from cutting a silent connection (~5 minutes,
// measured on 2.1.235) long before a real build finishes.
func TestWaitStreamsKeepalivesWhileParked(t *testing.T) {
	run := &fakeRunner{awaitText: "готово", awaitDelay: 80 * time.Millisecond}
	s := startServer(t, run)
	s.keepalive = 10 * time.Millisecond

	result, raw := waitRPC(t, s, map[string]any{"id": "job-k"})
	if got := toolText(t, result); got != "готово" {
		t.Errorf("wait returned %q after keepalives", got)
	}
	if !strings.Contains(raw, ": keepalive") {
		t.Errorf("no keepalive comments in the stream:\n%s", raw)
	}
}

// The wait must die with its HTTP request: a caller that vanished mid-block
// (cancelled turn, killed process) releases the parked waiter instead of
// holding the job's ending for no one. The SSE stream answers its headers at
// once, so the death shows up while reading the body — and must reach the
// blocked runner as a context cancel.
func TestWaitReleasedWhenRequestDies(t *testing.T) {
	run := &fakeRunner{awaitBlocks: true, released: make(chan struct{})}
	s := startServer(t, run)

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "wait", "arguments": map[string]any{"id": "job-x"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+s.Addr()+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if _, err := io.ReadAll(resp.Body); err == nil {
			t.Fatal("reading the stream should have died with the request context")
		}
	}
	select {
	case <-run.released:
	case <-time.After(5 * time.Second):
		t.Fatal("the vanished caller did not release the parked waiter")
	}
}

func TestMCPServersEntryRaisesTimeout(t *testing.T) {
	s := startServer(t, &fakeRunner{})
	servers := s.MCPServers()
	entry, ok := servers["jobs"]
	if !ok {
		t.Fatalf("no jobs entry: %v", servers)
	}
	var cfg struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(entry, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Type != "http" || cfg.URL == "" {
		t.Errorf("entry = %+v, want an http server with a url", cfg)
	}
	// Without this the CLI cuts every call at its 60-second default, which
	// silently breaks any wait longer than a minute (measured on 2.1.235).
	if cfg.Timeout < 60*60*1000 {
		t.Errorf("timeout = %d ms, want at least an hour to cover a full wait slice", cfg.Timeout)
	}
}
