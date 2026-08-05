package core_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// liveBin resolves the claude binary for the live tests, skipping when they are
// not explicitly enabled (they spend real tokens).
func liveBin(t *testing.T) string {
	t.Helper()
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live token-usage tests")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	return bin
}

// gitRepo makes a small git repository. Git status is one of the per-machine
// sections the CLI puts in its system prompt, so a repo is what lets the test
// dirty that section between turns.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, out)
		}
	}
	return dir
}

// TestLiveResumeKeepsPromptCache guards the per-turn cost of a conversation.
//
// Each turn spawns a fresh `claude -p --resume`, which rebuilds the system
// prompt from scratch. The prompt cache takes a couple of turns to settle, and
// once it has, a resumed turn should be reading its context back rather than
// writing it again — measured, the third turn of a thread reads essentially all
// of its input from cache.
//
// The test dirties git status between turns, which is what an agent turn does,
// and asserts the cache still settles. If per-turn churn is ever reintroduced
// into the prompt, the steady state never arrives and this fails.
func TestLiveResumeKeepsPromptCache(t *testing.T) {
	bin := liveBin(t)
	cwd := gitRepo(t)

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	store, err := core.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject("live", cwd)
	if err != nil {
		t.Fatal(err)
	}

	// Memory upkeep is disabled so this measures the turn itself.
	app := core.NewApp(store, core.Config{AutoMemoryDisabled: true},
		&core.Engine{BinPath: bin, Model: "haiku", Mode: core.ModeChat})
	app.OpenProjectObj(p)

	send := func(text string) core.TokenUsage {
		t.Helper()
		_, ch, err := app.SendTurn(context.Background(), text, nil)
		if err != nil {
			t.Fatal(err)
		}
		var usage core.TokenUsage
		for ev := range ch {
			switch ev.Kind {
			case core.EvResult:
				usage = ev.Usage
			case core.EvError:
				t.Fatalf("turn failed: %v", ev.Err)
			}
		}
		return usage
	}

	// Three turns: the first two populate the cache, the third is the steady
	// state a long conversation actually spends its life in.
	const turns = 3
	var last core.TokenUsage
	for i := 1; i <= turns; i++ {
		u := send(fmt.Sprintf("Ответь одним словом: шаг%d", i))
		if u.TotalIn() == 0 {
			t.Fatalf("turn %d reported no input tokens — usage is not being parsed", i)
		}
		t.Logf("turn %d: in=%d cache_read=%d cache_create=%d out=%d (hit %d%%)",
			i, u.Input, u.CacheRead, u.CacheCreate, u.Output, u.CacheHitPct())
		last = u

		// Dirty the working tree between turns, exactly as an agent turn would.
		name := filepath.Join(cwd, "touched.txt")
		if err := os.WriteFile(name, fmt.Appendf(nil, "changed %d\n", i), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if hit := last.CacheHitPct(); hit < 85 {
		t.Errorf("by turn %d only %d%% of input came from cache (want >= 85%%): "+
			"the prompt is churning between turns and never reaches a steady state",
			turns, hit)
	}
}

// TestLiveSideCallIsCheap guards the other half of the per-turn cost. Memory
// upkeep runs after every substantive turn, and running it as an ordinary
// `claude -p` drags in the full system prompt, CLAUDE.md, every tool definition
// and every ambient MCP server — tens of thousands of tokens for a request that
// rewrites a bullet list.
func TestLiveSideCallIsCheap(t *testing.T) {
	bin := liveBin(t)

	call := core.SideCall{
		Bin:          bin,
		Model:        "haiku",
		Effort:       "low",
		SystemPrompt: "Ты отвечаешь ровно одним словом.",
		Prompt:       "Ответь одним словом: ок",
	}
	out, usage, err := call.Run(context.Background())
	if err != nil {
		t.Fatalf("side call failed: %v", err)
	}
	t.Logf("side call: result=%q in=%d cache_read=%d cache_create=%d out=%d (total in %d)",
		out, usage.Input, usage.CacheRead, usage.CacheCreate, usage.Output, usage.TotalIn())

	if usage.TotalIn() == 0 {
		t.Fatal("side call reported no input tokens — usage is not being parsed")
	}
	// The unrestricted form of this call measured just over 30k input tokens.
	// Anything near that means the lockdown flags stopped being applied.
	const budget = 6000
	if got := usage.TotalIn(); got > budget {
		t.Errorf("side call consumed %d input tokens (budget %d): "+
			"it is loading the full coding-session environment again", got, budget)
	}
}
