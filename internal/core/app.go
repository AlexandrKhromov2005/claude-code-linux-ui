package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNoProject is returned when a turn is attempted without an open project.
var ErrNoProject = errors.New("нет активного проекта")

// App is the UI-agnostic orchestration layer. It owns the engine, store, the
// current project/thread/mode and the turn lifecycle. All mutable state is
// guarded by mu so the TUI (single goroutine) and the web server (many
// goroutines) can share one App safely.
type App struct {
	mu     sync.Mutex
	store  *Store
	cfg    Config
	engine *Engine
	perm   PermissionService
	broker ApprovalBroker

	project *Project
	thread  *Thread
	mode    Mode

	// skipPerms enables --dangerously-skip-permissions in agent mode for this
	// session: tools run with no approval prompt. Off by default, never persisted.
	skipPerms bool

	// effort is the reasoning-effort level passed via --effort ("" = model
	// default). Persisted in config.
	effort string

	cost         float64
	budgetWarned bool
}

// NewApp builds an App over a store, config and engine.
func NewApp(store *Store, cfg Config, engine *Engine) *App {
	return &App{store: store, cfg: cfg, engine: engine, mode: ParseMode(cfg.DefaultMode), skipPerms: cfg.SkipPerms, effort: cfg.Effort}
}

// SetPermission attaches the approval transport used in agent mode.
func (a *App) SetPermission(p PermissionService) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.perm = p
	a.configureEngineLocked()
}

// SetBroker registers the client that answers approval requests.
func (a *App) SetBroker(b ApprovalBroker) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broker = b
}

// Store exposes the underlying store for path lookups (e.g. memory.md).
func (a *App) Store() *Store { return a.store }

// Config returns a copy of the current config.
func (a *App) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// CurrentProject returns the open project, or nil.
func (a *App) CurrentProject() *Project {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.project
}

// CurrentThread returns the open thread, or nil.
func (a *App) CurrentThread() *Thread {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.thread
}

// Mode returns the active mode.
func (a *App) Mode() Mode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// SkipPermissions reports whether agent turns bypass the approval prompt.
func (a *App) SkipPermissions() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.skipPerms
}

// Effort returns the current reasoning-effort level ("" = model default).
func (a *App) Effort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effort
}

// SetEffort persists the reasoning-effort level and rewires the engine. An empty
// level restores the model default. It errors on an unknown level.
func (a *App) SetEffort(level string) error {
	if !ValidEffort(level) {
		return fmt.Errorf("недопустимый effort: %q", level)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.effort = level
	a.cfg.Effort = level
	_ = a.store.SaveConfig(a.cfg)
	a.configureEngineLocked()
	return nil
}

// Cost returns the accumulated session cost in USD.
func (a *App) Cost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cost
}

// MemoryPath returns the current project's memory.md path ("" without a project).
func (a *App) MemoryPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.project == nil {
		return ""
	}
	return a.store.MemoryPath(a.project.Slug())
}

// PermissionInfo reports the approval server address and whether it is running.
func (a *App) PermissionInfo() (addr string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.perm == nil || a.perm.Addr() == "" {
		return "", false
	}
	return a.perm.Addr(), true
}

// ---- engine configuration -------------------------------------------------

// configureEngineLocked syncs the engine with the current project and mode. The
// caller must hold mu.
func (a *App) configureEngineLocked() {
	if a.project == nil {
		return
	}
	a.engine.Cwd = a.project.Cwd
	a.engine.MemoryFile = a.store.MemoryPath(a.project.Slug())
	a.engine.Mode = a.mode
	a.engine.Effort = a.effort
	if a.project.Model != "" {
		a.engine.Model = a.project.Model
	} else {
		a.engine.Model = a.cfg.DefaultModel
	}

	a.engine.PermPromptTool = ""
	a.engine.MCPConfig = ""
	a.engine.SettingsJSON = ""
	a.engine.SkipPermissions = false
	if a.mode == ModeAgent {
		if a.skipPerms {
			// Bypass the approval broker entirely; nothing else is wired.
			a.engine.SkipPermissions = true
		} else {
			a.engine.SettingsJSON = settingsJSON(a.project)
			if a.perm != nil && a.perm.Addr() != "" {
				a.engine.PermPromptTool = a.perm.PromptTool()
				a.engine.MCPConfig = a.perm.MCPConfigJSON()
			}
		}
	}
}

// ---- projects / threads ---------------------------------------------------

// ListProjects returns all known projects, most recent first.
func (a *App) ListProjects() ([]*Project, error) { return a.store.ListProjects() }

// ProjectForCwd finds a project whose cwd matches.
func (a *App) ProjectForCwd(cwd string) (*Project, error) { return a.store.ProjectForCwd(cwd) }

// FindProject resolves a project by slug or name.
func (a *App) FindProject(q string) *Project {
	projects, _ := a.store.ListProjects()
	q = strings.ToLower(strings.TrimSpace(q))
	for _, p := range projects {
		if p.Slug() == q || strings.ToLower(p.Name) == q {
			return p
		}
	}
	return nil
}

// LastProjectSlug returns the slug remembered from the previous session.
func (a *App) LastProjectSlug() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.LastProject
}

// LoadProject reads a project by slug without opening it.
func (a *App) LoadProject(slug string) (*Project, error) { return a.store.LoadProject(slug) }

// OpenProject opens the project with the given slug and starts a fresh thread.
func (a *App) OpenProject(slug string) (*Project, error) {
	p, err := a.store.LoadProject(slug)
	if err != nil {
		return nil, err
	}
	a.openLocked(p)
	return p, nil
}

// OpenProjectObj opens an already-loaded project.
func (a *App) OpenProjectObj(p *Project) {
	a.openLocked(p)
}

// UseCwd opens (creating if needed) the project rooted at cwd. The path is
// expanded and validated as an existing directory first.
func (a *App) UseCwd(cwd string) (*Project, error) {
	cwd, err := ExpandDir(cwd)
	if err != nil {
		return nil, err
	}
	p, err := a.store.ProjectForCwd(cwd)
	if err == nil && p == nil {
		p, err = a.store.CreateProject("", cwd)
	}
	if err != nil {
		return nil, err
	}
	a.openLocked(p)
	return p, nil
}

func (a *App) openLocked(p *Project) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.project = p
	a.mode = ParseMode(p.Mode)
	a.cost = 0
	a.budgetWarned = false
	a.configureEngineLocked()
	a.cfg.LastProject = p.Slug()
	_ = a.store.SaveConfig(a.cfg)
	a.thread = a.store.NewThread()
}

// NewThread starts a fresh, empty thread in the current project.
func (a *App) NewThread() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.thread = a.store.NewThread()
}

// OpenThread loads and activates a thread by id.
func (a *App) OpenThread(id string) (*Thread, error) {
	a.mu.Lock()
	slug := ""
	if a.project != nil {
		slug = a.project.Slug()
	}
	a.mu.Unlock()
	if slug == "" {
		return nil, ErrNoProject
	}
	t, err := a.store.LoadThread(slug, id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.thread = t
	a.mu.Unlock()
	return t, nil
}

// ListThreads returns the current project's threads.
func (a *App) ListThreads() ([]*Thread, error) {
	a.mu.Lock()
	slug := ""
	if a.project != nil {
		slug = a.project.Slug()
	}
	a.mu.Unlock()
	if slug == "" {
		return nil, ErrNoProject
	}
	return a.store.ListThreads(slug)
}

// DeleteThread removes a thread; if it was active, a fresh thread is started.
func (a *App) DeleteThread(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.project == nil {
		return ErrNoProject
	}
	if err := a.store.DeleteThread(a.project.Slug(), id); err != nil {
		return err
	}
	if a.thread != nil && a.thread.ID == id {
		a.thread = a.store.NewThread()
	}
	return nil
}

// ---- mode -----------------------------------------------------------------

// SetMode switches chat/agent, persists it on the project and rewires the
// engine. It returns a non-empty warning when agent mode lacks an approval
// server.
func (a *App) SetMode(mode Mode) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = mode
	if a.project != nil {
		a.project.Mode = mode.String()
		_ = a.store.SaveProject(a.project)
	}
	a.configureEngineLocked()
	if mode == ModeAgent && (a.perm == nil || a.perm.Addr() == "") {
		return "approval-сервер недоступен: мутации будут отклонены"
	}
	return ""
}

// SetSkipPermissions toggles --dangerously-skip-permissions for agent turns and
// rewires the engine. It returns a non-empty warning when enabling it, since it
// removes the approval prompt for every tool.
func (a *App) SetSkipPermissions(v bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skipPerms = v
	a.cfg.SkipPerms = v
	_ = a.store.SaveConfig(a.cfg)
	if v {
		// Skip only has meaning in agent mode (chat is read-only), so enabling
		// it engages agent mode too: one switch = "act without asking".
		a.mode = ModeAgent
		if a.project != nil {
			a.project.Mode = a.mode.String()
			_ = a.store.SaveProject(a.project)
		}
	}
	a.configureEngineLocked()
	if v {
		return "пропуск подтверждений включён (сохранено): агент выполняет правки и команды без запроса"
	}
	return ""
}

// ---- turn lifecycle -------------------------------------------------------

// SendTurn persists the user message, spawns a turn and returns a stream of
// events. Persistence side effects (assistant message, session id, cost) happen
// in the background against the thread captured at send time.
func (a *App) SendTurn(ctx context.Context, text string, attachments []string) (<-chan Event, error) {
	a.mu.Lock()
	if a.project == nil || a.thread == nil {
		a.mu.Unlock()
		return nil, ErrNoProject
	}
	slug := a.project.Slug()
	th := a.thread
	th.Messages = append(th.Messages, Msg{Role: "user", Content: text, Attachments: attachments, Ts: time.Now()})
	if th.Title == "" {
		th.Title = makeTitle(text)
	}
	_ = a.store.SaveThread(slug, th)
	resume := th.ClaudeSessionID
	prompt := BuildPrompt(text, attachments)
	src := a.engine.Send(ctx, prompt, resume)
	a.mu.Unlock()

	out := make(chan Event, 128)
	go func() {
		defer close(out)
		var buf strings.Builder
		hadResult := false
		for ev := range src {
			switch ev.Kind {
			case EvText:
				buf.WriteString(ev.Text)
			case EvSystemInit:
				a.setSessionID(slug, th, ev.SessionID)
			case EvResult:
				hadResult = true
				a.setSessionID(slug, th, ev.SessionID)
				final := buf.String()
				if strings.TrimSpace(final) == "" {
					final = ev.Text
				}
				a.persistAssistant(slug, th, final)
				notice := a.addCost(ev.CostUSD)
				out <- ev
				if notice != "" {
					out <- Event{Kind: EvNotice, Text: notice}
				}
				continue
			}
			out <- ev
		}
		if !hadResult && strings.TrimSpace(buf.String()) != "" {
			a.persistAssistant(slug, th, buf.String())
		}
	}()
	return out, nil
}

func (a *App) setSessionID(slug string, th *Thread, id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	th.ClaudeSessionID = id
	_ = a.store.SaveThread(slug, th)
}

func (a *App) persistAssistant(slug string, th *Thread, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	th.Messages = append(th.Messages, Msg{Role: "assistant", Content: content, Ts: time.Now()})
	_ = a.store.SaveThread(slug, th)
}

// addCost accumulates session cost and returns a budget notice the first time
// the configured threshold is crossed.
func (a *App) addCost(usd float64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cost += usd
	if a.cfg.BudgetWarnUSD > 0 && !a.budgetWarned && a.cost >= a.cfg.BudgetWarnUSD {
		a.budgetWarned = true
		return fmt.Sprintf("бюджет: израсходовано $%.4f (порог $%.2f). С 15.06.2026 расход идёт из месячного Agent SDK-кредита.", a.cost, a.cfg.BudgetWarnUSD)
	}
	return ""
}

// ---- approvals ------------------------------------------------------------

// HandleApproval is the entry point the permission service calls for each gated
// tool invocation. It routes to the connected client, applies a remembered rule
// and records the decision in the transcript.
func (a *App) HandleApproval(req ApprovalRequest) ApprovalDecision {
	a.mu.Lock()
	b := a.broker
	th := a.thread
	slug := ""
	if a.project != nil {
		slug = a.project.Slug()
	}
	a.mu.Unlock()

	if b == nil {
		return ApprovalDecision{Allow: false, Message: "Нет подключённого клиента"}
	}
	dec := b.RequestApproval(context.Background(), req)

	if dec.Allow && dec.RememberRule != "" {
		a.mu.Lock()
		if a.project != nil && addAllowRule(a.project, dec.RememberRule) {
			_ = a.store.SaveProject(a.project)
			a.configureEngineLocked()
		}
		a.mu.Unlock()
	}

	if slug != "" && th != nil {
		a.recordApproval(slug, th, req, dec.Allow)
	}
	return dec
}

func (a *App) recordApproval(slug string, th *Thread, req ApprovalRequest, allow bool) {
	target := ToolTarget(req.ToolName, req.Input)
	a.mu.Lock()
	defer a.mu.Unlock()
	th.Messages = append(th.Messages, Msg{
		Role:     "tool",
		Content:  strings.TrimSpace(req.ToolName + " " + target),
		Ts:       time.Now(),
		ToolMeta: map[string]any{"tool": req.ToolName, "allow": allow, "target": target},
	})
	_ = a.store.SaveThread(slug, th)
}

// ---- memory / config / export ---------------------------------------------

// ReadMemory returns the current project's memory text.
func (a *App) ReadMemory() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.project == nil {
		return "", ErrNoProject
	}
	return a.store.ReadMemory(a.project.Slug())
}

// WriteMemory replaces the current project's memory text.
func (a *App) WriteMemory(content string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.project == nil {
		return ErrNoProject
	}
	return a.store.WriteMemory(a.project.Slug(), content)
}

// SetTheme persists a theme name (the client validates and applies it).
func (a *App) SetTheme(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.Theme = name
	return a.store.SaveConfig(a.cfg)
}

// SetBudget persists the budget-warning threshold (0 disables it).
func (a *App) SetBudget(v float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.BudgetWarnUSD = v
	a.budgetWarned = false
	return a.store.SaveConfig(a.cfg)
}

// ExportCurrentThread writes the active thread to a Markdown file.
func (a *App) ExportCurrentThread(path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.thread == nil {
		return errors.New("нет активного треда")
	}
	return ExportThreadMarkdown(a.thread, a.project, path)
}

func makeTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:57]) + "…"
	}
	return s
}
