package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/permctl"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/tui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			fmt.Fprintln(os.Stderr, "serve: ещё не реализовано")
			os.Exit(1)
		case "-h", "--help", "help":
			fmt.Println("claude-code-linux-ui — TUI-клиент для Claude (без аргументов).")
			fmt.Println("Подкоманды: serve — локальный веб-сервер.")
			return
		}
	}
	if err := runTUI(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func runTUI() error {
	app, perm, err := buildApp()
	if err != nil {
		return err
	}
	defer perm.Stop()

	m := tui.New(app)
	p := tea.NewProgram(m, tea.WithAltScreen())
	app.SetBroker(tui.NewBroker(p.Send))
	_, runErr := p.Run()
	return runErr
}

// buildApp assembles the store, engine, core App and permission server shared by
// every client.
func buildApp() (*core.App, *permctl.Server, error) {
	store, err := core.NewStore()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, nil, err
	}

	bin := cfg.ClaudeBin
	if v := os.Getenv("CLAUDE_BIN"); v != "" {
		bin = v
	}
	if bin == "" {
		bin = "claude"
	}
	mdl := cfg.DefaultModel
	if v := os.Getenv("CLAUDE_TUI_MODEL"); v != "" {
		mdl = v
	}

	engine := &core.Engine{BinPath: bin, Model: mdl, Mode: core.ModeChat}
	app := core.NewApp(store, cfg, engine)

	perm := permctl.New(app.HandleApproval)
	if err := perm.Start(); err != nil {
		// Agent mode will warn; chat mode is unaffected.
		fmt.Fprintln(os.Stderr, "предупреждение: approval-сервер не запущен:", err)
	}
	app.SetPermission(perm)

	return app, perm, nil
}
