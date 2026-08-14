// Package jobctl runs the in-process MCP server that lets Claude hand a
// long-running command to the supervisor instead of blocking a turn on it.
//
// It mirrors permctl deliberately: same transport, same loopback binding, same
// JSON-RPC shape. The two are separate servers because they answer to different
// things — permctl is asked whether an action is allowed, jobctl is asked to
// remember an action for longer than the caller will exist.
package jobctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

const (
	serverName         = "jobs"
	mcpProtocolVersion = "2025-06-18"
)

// Runner is what jobctl needs from the core App.
type Runner interface {
	StartJob(command, description string) (*core.Job, error)
	Jobs() *core.JobManager
}

// Server exposes the job tools over streamable HTTP on loopback.
type Server struct {
	run Runner

	ln  net.Listener
	srv *http.Server
}

// New creates a server over the given runner.
func New(run Runner) *Server { return &Server{run: run} }

// Start binds an ephemeral loopback port and serves until Stop.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

// Stop shuts the server down. Jobs are untouched: they are detached by design.
func (s *Server) Stop() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// Addr returns the bound address, empty before Start.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// MCPServers returns this server's entry for an inline --mcp-config, keyed by
// name so it can be merged alongside the permission server.
func (s *Server) MCPServers() map[string]json.RawMessage {
	if s.Addr() == "" {
		return nil
	}
	entry, err := json.Marshal(map[string]any{
		"type": "http",
		"url":  fmt.Sprintf("http://%s/mcp", s.Addr()),
	})
	if err != nil {
		return nil
	}
	return map[string]json.RawMessage{serverName: entry}
}

// ---- JSON-RPC over streamable HTTP ----------------------------------------

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req rpcReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	notification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		pv := params.ProtocolVersion
		if pv == "" {
			pv = mcpProtocolVersion
		}
		w.Header().Set("Mcp-Session-Id", serverName)
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": "1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		s.handleToolsCall(w, req)
	default:
		if notification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) handleToolsCall(w http.ResponseWriter, req rpcReq) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &params)

	text, err := s.callTool(params.Name, params.Arguments)
	if err != nil {
		// Reported as tool output rather than an RPC error: the model should see
		// what went wrong and adapt, not have the call fail underneath it.
		writeRPCResult(w, req.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Ошибка: " + err.Error()}},
			"isError": true,
		})
		return
	}
	writeRPCResult(w, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
}

func (s *Server) callTool(name string, raw json.RawMessage) (string, error) {
	switch name {
	case "start":
		var args struct {
			Command     string `json:"command"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(raw, &args)
		j, err := s.run.StartJob(args.Command, args.Description)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"Задача запущена: %s (pid %d).\n"+
				"Она переживёт этот ход и работает независимо. НЕ жди её здесь и не опрашивай в цикле — "+
				"заверши ответ. Когда задача закончится, ты получишь сообщение с кодом возврата и хвостом вывода.",
			j.ID, j.PID), nil

	case "status":
		var args struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &args)
		return s.statusText(args.ID)

	case "logs":
		var args struct {
			ID    string `json:"id"`
			Lines int    `json:"lines"`
		}
		_ = json.Unmarshal(raw, &args)
		mgr := s.run.Jobs()
		if mgr == nil {
			return "", fmt.Errorf("менеджер задач не запущен")
		}
		if args.Lines <= 0 {
			args.Lines = 50
		}
		out, err := mgr.Logs(args.ID, args.Lines)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) == "" {
			return "Вывода пока нет.", nil
		}
		return out, nil

	case "stop":
		var args struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &args)
		mgr := s.run.Jobs()
		if mgr == nil {
			return "", fmt.Errorf("менеджер задач не запущен")
		}
		if err := mgr.Stop(args.ID); err != nil {
			return "", err
		}
		return "Задача " + args.ID + " остановлена.", nil

	default:
		return "", fmt.Errorf("неизвестный инструмент: %s", name)
	}
}

// statusText renders one job, or all of them when no id is given.
func (s *Server) statusText(id string) (string, error) {
	mgr := s.run.Jobs()
	if mgr == nil {
		return "", fmt.Errorf("менеджер задач не запущен")
	}
	if id != "" {
		j, ok := mgr.Get(id)
		if !ok {
			return "", fmt.Errorf("задача %s не найдена", id)
		}
		return describe(mgr, j), nil
	}
	list := mgr.List()
	if len(list) == 0 {
		return "Фоновых задач нет.", nil
	}
	var sb strings.Builder
	for _, v := range list {
		sb.WriteString(describe(mgr, v.Job))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func describe(mgr *core.JobManager, j core.Job) string {
	line := fmt.Sprintf("%s [%s] %s", j.ID, j.Status, firstLine(j.Command))
	if j.Description != "" {
		line = fmt.Sprintf("%s [%s] %s — %s", j.ID, j.Status, j.Description, firstLine(j.Command))
	}
	line += fmt.Sprintf(" · %s", roundDuration(j.Elapsed(time.Now())))
	if j.Status.Terminal() && j.Status != core.JobStopped {
		line += fmt.Sprintf(" · код %d", j.ExitCode)
	}
	if tail, err := mgr.Logs(j.ID, 1); err == nil {
		if t := strings.TrimSpace(tail); t != "" {
			line += "\n    последняя строка: " + t
		}
	}
	return line
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

func roundDuration(d time.Duration) time.Duration { return d.Round(time.Second) }

func toolDefs() []any {
	return []any{
		map[string]any{
			"name": "start",
			"description": "Запустить долгую команду в фоне под присмотром сервера: сборку, прогон тестов, " +
				"фаззинг, обучение — всё, что длится дольше одного ответа. Команда переживает текущий ход " +
				"и перезапуск сервера. Возвращает id сразу, не дожидаясь завершения. " +
				"Когда команда закончится, ты автоматически получишь сообщение с кодом возврата и хвостом вывода — " +
				"опрашивать статус в цикле не нужно. " +
				"Используй этот инструмент вместо Bash для всего, что заведомо дольше пары минут: " +
				"обычный Bash умрёт вместе с этим ходом.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Команда для sh -c. Выполняется в рабочей папке проекта.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Короткое описание для пользователя, например «фаззинг парсера, 2 часа».",
					},
				},
				"required": []string{"command"},
			},
		},
		map[string]any{
			"name": "status",
			"description": "Состояние фоновых задач: статус, время работы, код возврата, последняя строка вывода. " +
				"Без id — все задачи.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
			},
		},
		map[string]any{
			"name":        "logs",
			"description": "Хвост вывода фоновой задачи.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":    map[string]any{"type": "string"},
					"lines": map[string]any{"type": "integer", "description": "сколько последних строк (по умолчанию 50)"},
				},
				"required": []string{"id"},
			},
		},
		map[string]any{
			"name":        "stop",
			"description": "Остановить фоновую задачу вместе со всеми её дочерними процессами.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
				"required":   []string{"id"},
			},
		},
	}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}
