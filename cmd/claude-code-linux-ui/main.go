package main

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/jobctl"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/permctl"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/tui"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/web"
)

const defaultServeAddr = "127.0.0.1:8765"

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "serve":
		addr := defaultServeAddr
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		err = runServe(addr)
	case len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help"):
		fmt.Println("claude-code-linux-ui — TUI-клиент для Claude (без аргументов).")
		fmt.Println("Подкоманды:")
		fmt.Println("  serve [addr]   локальный веб-сервер (по умолчанию " + defaultServeAddr + ")")
		return
	default:
		err = runTUI()
	}
	if err != nil {
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

func runServe(addr string) error {
	app, perm, err := buildApp()
	if err != nil {
		return err
	}
	defer perm.Stop()

	srv := web.New(app, webAssets())
	app.SetBroker(srv)
	app.SetTurnDispatcher(srv.DispatchTurn)
	mgr, jc := startJobs(app, srv.BroadcastJobs)
	if mgr != nil {
		defer mgr.Close()
	}
	if jc != nil {
		defer jc.Stop()
	}
	if dev := os.Getenv("CCLU_DEV_SERVER"); dev != "" {
		if err := srv.SetDevProxy(dev); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "dev: проксирование статики на", dev)
	}
	if err := srv.Listen(addr); err != nil {
		return err
	}
	fmt.Println("claude-code-linux-ui — локальный веб-сервер")
	fmt.Println("Откройте в браузере (токен в URL, не сохраняйте его в истории):")
	fmt.Println("  " + srv.URL())
	fmt.Println("Только loopback. Для удалённого доступа используйте SSH-туннель.")
	return srv.Serve()
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

// startJobs brings up the background-job supervisor and its MCP server. Failure
// is not fatal: without it the app behaves exactly as it did before, minus the
// ability to run work that outlives a turn.
func startJobs(app *core.App, onChange func()) (*core.JobManager, *jobctl.Server) {
	mgr, err := core.NewJobManager(app.Store().JobsDir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "предупреждение: менеджер фоновых задач не запущен:", err)
		return nil, nil
	}
	// Settle anything left running when this server last exited before the watch
	// loop starts, so a job whose process died unobserved is not shown as alive.
	mgr.AdoptOrphans()
	go mgr.Watch()
	// Finished jobs are kept for a week so their logs stay readable, then dropped.
	mgr.Prune(7 * 24 * time.Hour)

	jc := jobctl.New(app)
	if err := jc.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "предупреждение: MCP-сервер задач не запущен:", err)
		app.SetJobs(mgr, nil, onChange)
		return mgr, nil
	}
	app.SetJobs(mgr, jc.MCPServers(), onChange)
	return mgr, jc
}
