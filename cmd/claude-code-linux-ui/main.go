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
		addr, rotate := parseServeArgs(os.Args[2:])
		err = runServe(addr, rotate)
	case len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help"):
		fmt.Println("claude-code-linux-ui — TUI-клиент для Claude (без аргументов).")
		fmt.Println("Подкоманды:")
		fmt.Println("  serve [addr]   локальный веб-сервер (по умолчанию " + defaultServeAddr + ")")
		fmt.Println("    --new-token  выпустить новый токен; все выданные ссылки перестанут работать")
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

// reopenLastProject restores the project that was open when the server last
// ran. Best-effort: a project that has since been deleted or moved just leaves
// the server with none, exactly as before.
func reopenLastProject(app *core.App) {
	slug := app.LastProjectSlug()
	if slug == "" {
		return
	}
	if _, err := app.OpenProject(slug); err != nil {
		return
	}
	// Now that a conversation exists again, hand over anything a background job
	// finished while nothing was listening.
	go app.DeliverPendingJobNotices()
}

// parseServeArgs reads `serve`'s arguments: an optional address and the
// --new-token flag, in either order.
func parseServeArgs(args []string) (addr string, rotate bool) {
	addr = defaultServeAddr
	for _, a := range args {
		if a == "--new-token" {
			rotate = true
			continue
		}
		addr = a
	}
	return addr, rotate
}

// resolveToken returns the bearer token to serve with, minting a new one when
// asked or when none is usable yet. fresh reports that previously issued links
// have just stopped working, which is worth telling the user.
func resolveToken(store *core.Store, rotate bool) (token string, fresh bool, err error) {
	if rotate {
		tok, err := store.RotateToken()
		return tok, true, err
	}
	return store.LoadOrCreateToken()
}

func runServe(addr string, rotate bool) error {
	app, perm, err := buildApp()
	if err != nil {
		return err
	}
	defer perm.Stop()

	srv := web.New(app, webAssets())
	// The token is persisted so a restart does not invalidate open tabs; the
	// token lives in each tab's URL, and a rebuilt binary is a routine event.
	tok, fresh, err := resolveToken(app.Store(), rotate)
	if err != nil {
		// Reported, not fatal: a working server with a fresh token beats no server.
		fmt.Fprintln(os.Stderr, "предупреждение:", err)
	}
	srv.UseToken(tok)
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
	// Reopen whatever was last in use. Without this a restart leaves the server
	// with no project, so a reconnecting tab lands on "нет проекта" and its next
	// message goes nowhere — and a background job that finished meanwhile has no
	// conversation to report into.
	reopenLastProject(app)
	fmt.Println("claude-code-linux-ui — локальный веб-сервер")
	fmt.Println("Откройте в браузере (токен в URL, не сохраняйте его в истории):")
	fmt.Println("  " + srv.URL())
	// Whether old tabs survive is the first thing anyone wants to know after a
	// restart, and it is the thing the previous behaviour got silently wrong.
	if fresh {
		fmt.Println("Выпущен новый токен — ранее открытые вкладки больше не работают.")
	} else {
		fmt.Println("Токен прежний — уже открытые вкладки продолжают работать.")
	}
	fmt.Println("Только loopback. Для удалённого доступа используйте SSH-туннель.")
	fmt.Println("Сменить токен: " + os.Args[0] + " serve --new-token")
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
