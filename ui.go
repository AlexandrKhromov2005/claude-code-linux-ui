package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type role int

const (
	roleUser role = iota
	roleAssistant
	roleSystem
)

type message struct {
	role     role
	content  string // raw text / markdown
	rendered string // styled string ready for the viewport
}

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayPicker
	overlayProjects
	overlayThreads
	overlayApprove
	overlaySearch
)

// tea.Msg types
type eventMsg Event
type streamClosedMsg struct{}
type memoryEditedMsg struct{ err error }

const helpText = "Команды: /project [имя] (Ctrl+P), /threads (Ctrl+T), /new, /resume <id>, /mode chat|agent (Tab), " +
	"/search <текст>, /export [путь], /memory, /attach <путь> (Ctrl+O), /files [clear], /detach [N], " +
	"/theme [имя], /budget [usd], /mcp, /help, /quit. " +
	"В agent-режиме правки и команды проходят через модалку подтверждения. " +
	"В сообщении можно писать @/путь напрямую. Enter — отправить, Ctrl+J — перенос строки, Esc — отменить ответ."

type model struct {
	store  *Store
	config Config
	engine *Engine
	perm   *PermissionServer
	permErr error

	project *Project // active project (nil only at the startup switcher)
	thread  *Thread  // active thread
	mode    Mode

	vp        viewport.Model
	ta        textarea.Model
	sp        spinner.Model
	fp        filepicker.Model
	ruleInput textinput.Model
	glam      *glamour.TermRenderer

	messages    []message
	attachments []string

	streamBuf     string
	streaming     bool
	turnHadResult bool
	overlay       overlayKind
	status        string
	pendingHint   string

	pending     *ApprovalRequest // tool call awaiting an approve/deny answer
	remembering bool             // editing the allow-rule before allowing

	projList   selList
	thrList    selList
	searchList selList

	budgetWarned bool

	sessionShort string
	modelName    string
	costUSD      float64

	width, height int
	ready         bool

	streamCh <-chan Event
	cancel   context.CancelFunc
}

func newModel() (model, error) {
	store, err := NewStore()
	if err != nil {
		return model{}, err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return model{}, err
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

	applyTheme(cfg.Theme)

	ta := textarea.New()
	ta.Placeholder = "Спроси что-нибудь…  (Ctrl+O — прикрепить файл)"
	ta.Prompt = "┃ "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	fp := filepicker.New()
	if home, err := os.UserHomeDir(); err == nil {
		fp.CurrentDirectory = home
	}
	fp.ShowHidden = false
	fp.DirAllowed = false
	fp.FileAllowed = true
	fp.AutoHeight = false

	ri := textinput.New()
	ri.Prompt = "правило: "
	ri.CharLimit = 256

	// The approval server is in-process so gated tool calls surface in this
	// same UI. It binds a loopback port now; the decider is attached in main
	// once the Bubble Tea program exists.
	perm := NewPermissionServer(nil)
	permErr := perm.Start()

	m := model{
		store:     store,
		config:    cfg,
		engine:    &Engine{BinPath: bin, Model: mdl, Mode: ModeChat},
		perm:      perm,
		permErr:   permErr,
		mode:      parseMode(cfg.DefaultMode),
		ta:        ta,
		sp:        sp,
		fp:        fp,
		ruleInput: ri,
	}

	cwd, _ := os.Getwd()
	switch existing, _ := store.ProjectForCwd(cwd); {
	case existing != nil:
		m.openProject(existing)
	case cfg.LastProject != "":
		if p, err := store.LoadProject(cfg.LastProject); err == nil {
			m.openProject(p)
			m.pendingHint = "Текущая папка не проект. Ctrl+P → «использовать текущую папку»: " + cwd
		} else {
			m.openProjectSwitcher()
		}
	default:
		m.openProjectSwitcher()
	}
	return m, nil
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

// ---- project / thread lifecycle -------------------------------------------

func (m *model) configureEngine() {
	if m.project == nil {
		return
	}
	m.engine.Cwd = m.project.Cwd
	m.engine.MemoryFile = m.store.MemoryPath(m.project.Slug())
	m.engine.Mode = m.mode
	if m.project.Model != "" {
		m.engine.Model = m.project.Model
	} else {
		m.engine.Model = m.config.DefaultModel
	}

	// Agent-mode wiring: remembered allow/deny rules plus the approval server.
	m.engine.PermPromptTool = ""
	m.engine.MCPConfig = ""
	m.engine.SettingsJSON = ""
	if m.mode == ModeAgent {
		m.engine.SettingsJSON = settingsJSON(m.project)
		if m.perm != nil && m.perm.Addr() != "" {
			m.engine.PermPromptTool = permPromptTool()
			m.engine.MCPConfig = m.perm.MCPConfigJSON()
		}
	}
}

// setMode switches chat/agent, persists it on the project, and rewires the
// engine. It warns when agent mode lacks a working approval server.
func (m *model) setMode(mode Mode) {
	m.mode = mode
	if m.project != nil {
		m.project.Mode = mode.String()
		_ = m.store.SaveProject(m.project)
	}
	m.configureEngine()
	if mode == ModeAgent && (m.perm == nil || m.perm.Addr() == "") {
		m.commitSystem("⚠ approval-сервер недоступен: мутации будут отклонены. " + errString(m.permErr))
	} else {
		m.commitSystem("режим: " + mode.String())
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (m *model) openProject(p *Project) {
	m.project = p
	m.mode = parseMode(p.Mode)
	m.configureEngine()
	m.config.LastProject = p.Slug()
	_ = m.store.SaveConfig(m.config)
	m.startNewThread()
}

func (m *model) startNewThread() {
	m.thread = m.store.NewThread()
	m.messages = nil
	m.streamBuf = ""
	m.streaming = false
	m.sessionShort = ""
	m.costUSD = 0
	if m.ready {
		m.rerenderAll()
		m.refreshViewport()
	}
}

func (m *model) openThread(t *Thread) {
	m.thread = t
	m.messages = m.messagesFromThread(t)
	m.streamBuf = ""
	m.sessionShort = ""
	if t.ClaudeSessionID != "" {
		m.sessionShort = shortID(t.ClaudeSessionID)
	}
	if m.ready {
		m.rerenderAll()
		m.refreshViewport()
	}
}

func (m *model) messagesFromThread(t *Thread) []message {
	out := make([]message, 0, len(t.Messages))
	for _, msg := range t.Messages {
		var r role
		switch msg.Role {
		case "user":
			r = roleUser
		case "assistant":
			r = roleAssistant
		default:
			r = roleSystem
		}
		out = append(out, message{role: r, content: msg.Content})
	}
	return out
}

func (m *model) findProject(q string) *Project {
	projects, _ := m.store.ListProjects()
	q = strings.ToLower(strings.TrimSpace(q))
	for _, p := range projects {
		if p.Slug() == q || strings.ToLower(p.Name) == q {
			return p
		}
	}
	return nil
}

func (m *model) persistUser(content string) {
	if m.project == nil || m.thread == nil {
		return
	}
	m.thread.Messages = append(m.thread.Messages, Msg{Role: "user", Content: content, Ts: time.Now()})
	if m.thread.Title == "" {
		m.thread.Title = makeTitle(content)
	}
	_ = m.store.SaveThread(m.project.Slug(), m.thread)
}

func (m *model) persistAssistant(content string) {
	if m.project == nil || m.thread == nil {
		return
	}
	m.thread.Messages = append(m.thread.Messages, Msg{Role: "assistant", Content: content, Ts: time.Now()})
	_ = m.store.SaveThread(m.project.Slug(), m.thread)
}

func (m *model) setSession(id string) {
	if id == "" {
		return
	}
	m.sessionShort = shortID(id)
	if m.thread != nil {
		m.thread.ClaudeSessionID = id
		if m.project != nil {
			_ = m.store.SaveThread(m.project.Slug(), m.thread)
		}
	}
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

// ---- overlays -------------------------------------------------------------

func (m *model) openProjectSwitcher() {
	cwd, _ := os.Getwd()
	var items []selItem
	if existing, _ := m.store.ProjectForCwd(cwd); existing == nil {
		items = append(items, selItem{title: "+ Использовать текущую папку", subtitle: cwd, action: "use-cwd"})
	}
	projects, _ := m.store.ListProjects()
	for _, p := range projects {
		title := p.Name
		if m.project != nil && p.Slug() == m.project.Slug() {
			title += " (текущий)"
		}
		items = append(items, selItem{id: p.Slug(), title: title, subtitle: p.Cwd})
	}
	m.projList = selList{title: "Проекты", items: items, hint: "↑↓ · Enter — открыть · Esc — закрыть"}
	m.overlay = overlayProjects
	m.ta.Blur()
}

func (m *model) buildThreadList() {
	items := []selItem{{title: "+ Новый тред", action: "new-thread"}}
	threads, _ := m.store.ListThreads(m.project.Slug())
	for _, t := range threads {
		title := t.Title
		if title == "" {
			title = "(без названия)"
		}
		if m.thread != nil && t.ID == m.thread.ID {
			title += " (текущий)"
		}
		sub := t.Updated.Format("2006-01-02 15:04") + "  ·  " + fmt.Sprintf("%d сообщ. · id:%s", len(t.Messages), t.ID)
		items = append(items, selItem{id: t.ID, title: title, subtitle: sub})
	}
	m.thrList = selList{
		title: "Треды — " + m.project.Name,
		items: items,
		hint:  "↑↓ · Enter — открыть · d — удалить · Esc — закрыть",
	}
}

func (m *model) openThreadBrowser() {
	if m.project == nil {
		return
	}
	m.buildThreadList()
	m.overlay = overlayThreads
	m.ta.Blur()
}

// openPicker shows the file picker rooted at the project directory.
func (m *model) openPicker() tea.Cmd {
	dir := ""
	if m.project != nil {
		dir = m.project.Cwd
	}
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		}
	}
	m.fp.CurrentDirectory = dir
	m.overlay = overlayPicker
	m.ta.Blur()
	return m.fp.Init()
}

// runSearch scans the project's thread transcripts for q and opens a results list.
func (m *model) runSearch(q string) {
	ql := strings.ToLower(q)
	threads, _ := m.store.ListThreads(m.project.Slug())
	var items []selItem
	for _, t := range threads {
		snippet := ""
		matched := strings.Contains(strings.ToLower(t.Title), ql)
		for _, msg := range t.Messages {
			if idx := strings.Index(strings.ToLower(msg.Content), ql); idx >= 0 {
				matched = true
				snippet = makeSnippet(msg.Content, idx, len(q))
				break
			}
		}
		if !matched {
			continue
		}
		title := t.Title
		if title == "" {
			title = "(без названия)"
		}
		sub := snippet
		if sub == "" {
			sub = t.Updated.Format("2006-01-02 15:04")
		}
		items = append(items, selItem{id: t.ID, title: title, subtitle: sub})
	}
	m.searchList = selList{
		title: fmt.Sprintf("Поиск «%s» — найдено %d", q, len(items)),
		items: items,
		hint:  "↑↓ · Enter — открыть · Esc — закрыть",
	}
	m.overlay = overlaySearch
	m.ta.Blur()
}

// makeSnippet returns a one-line excerpt around a match.
func makeSnippet(content string, idx, qlen int) string {
	content = strings.ReplaceAll(content, "\n", " ")
	start := idx - 20
	if start < 0 {
		start = 0
	}
	end := idx + qlen + 30
	if end > len(content) {
		end = len(content)
	}
	// Snap to rune boundaries so multibyte (e.g. Cyrillic) text is not split.
	for start > 0 && !utf8.RuneStart(content[start]) {
		start--
	}
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end++
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(content) {
		suffix = "…"
	}
	return prefix + strings.TrimSpace(content[start:end]) + suffix
}

func (m model) useCurrentFolder() (tea.Model, tea.Cmd) {
	cwd, err := os.Getwd()
	if err != nil {
		m.commitSystem("не удалось определить текущую папку: " + err.Error())
		return m, nil
	}
	p, err := m.store.ProjectForCwd(cwd)
	if err == nil && p == nil {
		p, err = m.store.CreateProject("", cwd)
	}
	if err != nil {
		m.commitSystem("не удалось создать проект: " + err.Error())
		return m, nil
	}
	m.openProject(p)
	m.overlay = overlayNone
	m.ta.Focus()
	m.commitSystem("проект: " + p.Name + "  ·  " + p.Cwd)
	m.refreshViewport()
	return m, textarea.Blink
}

// ---- layout & rendering ---------------------------------------------------

const (
	headerH = 1
	attachH = 1
	footerH = 1
	taLines = 3
)

func (m *model) layout() {
	inputBoxH := taLines + 2 // rounded border top+bottom
	vpH := m.height - headerH - attachH - footerH - inputBoxH
	if vpH < 3 {
		vpH = 3
	}
	if m.vp.Width == 0 && m.vp.Height == 0 {
		m.vp = viewport.New(m.width, vpH)
	} else {
		m.vp.Width = m.width
		m.vp.Height = vpH
	}
	m.ta.SetWidth(m.width - 4)
	m.fp.SetHeight(vpH - 1)

	cw := m.contentWidth()
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(cw))
	if err == nil {
		m.glam = r
	}
}

func (m model) contentWidth() int {
	cw := m.width - 2
	if cw < 20 {
		cw = 20
	}
	return cw
}

func (m *model) renderUser(s string) string {
	body := bodyStyle.Width(m.contentWidth()).Render(s)
	return lipgloss.JoinVertical(lipgloss.Left, userLabelStyle.Render("▌ You"), body)
}

func (m *model) renderAssistant(s string) string {
	body := s
	if m.glam != nil {
		if out, err := m.glam.Render(s); err == nil {
			body = strings.TrimRight(out, "\n")
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, asstLabelStyle.Render("▌ Claude"), body)
}

func (m *model) renderAssistantLive(s string) string {
	body := bodyStyle.Width(m.contentWidth()).Render(s)
	return lipgloss.JoinVertical(lipgloss.Left, asstLabelStyle.Render("▌ Claude"), body)
}

func (m *model) renderSystem(s string) string {
	st := sysStyle
	if strings.Contains(s, "ошибка") || strings.HasPrefix(s, "⚠") {
		st = errStyle
	}
	return st.Width(m.contentWidth()).Render(s)
}

func (m *model) rerenderAll() {
	for i := range m.messages {
		switch m.messages[i].role {
		case roleAssistant:
			m.messages[i].rendered = m.renderAssistant(m.messages[i].content)
		case roleUser:
			m.messages[i].rendered = m.renderUser(m.messages[i].content)
		default:
			m.messages[i].rendered = m.renderSystem(m.messages[i].content)
		}
	}
}

func (m *model) refreshViewport() {
	if m.vp.Width == 0 && m.vp.Height == 0 {
		return // not laid out yet
	}
	var b strings.Builder
	for _, msg := range m.messages {
		b.WriteString(msg.rendered)
		b.WriteString("\n\n")
	}
	if m.streaming && m.streamBuf != "" {
		b.WriteString(m.renderAssistantLive(m.streamBuf))
	}
	m.vp.SetContent(strings.TrimRight(b.String(), "\n"))
	m.vp.GotoBottom()
}

// ---- update ---------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "ctrl+c" {
		return m, tea.Quit
	}

	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		m.layout()
		m.rerenderAll()
		m.refreshViewport()
		if !m.ready {
			m.ready = true
			if m.pendingHint != "" {
				m.commitSystem(m.pendingHint)
				m.pendingHint = ""
				m.refreshViewport()
			}
		}
		return m, nil
	}

	// An approval request preempts whatever overlay is open: Claude is blocked
	// waiting for the answer.
	if am, ok := msg.(approvalReqMsg); ok {
		return m.beginApproval(am.req)
	}

	// Stream-pump messages must always reach the main handler so the event loop
	// keeps draining even while an overlay (e.g. the approval modal) is open.
	switch msg.(type) {
	case eventMsg, streamClosedMsg, spinner.TickMsg, memoryEditedMsg:
		return m.updateMain(msg)
	}

	switch m.overlay {
	case overlayPicker:
		return m.updatePicker(msg)
	case overlayProjects:
		return m.updateProjects(msg)
	case overlayThreads:
		return m.updateThreads(msg)
	case overlaySearch:
		return m.updateSearch(msg)
	case overlayApprove:
		return m.updateApprove(msg)
	}
	return m.updateMain(msg)
}

func (m model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, isKey := msg.(tea.KeyMsg); isKey {
		switch k.String() {
		case "esc":
			m.overlay = overlayNone
			m.ta.Focus()
			return m, textarea.Blink
		case "ctrl+h":
			m.fp.ShowHidden = !m.fp.ShowHidden
			return m, m.fp.Init()
		}
	}
	var cmd tea.Cmd
	m.fp, cmd = m.fp.Update(msg)
	// Selecting a file queues it and keeps the picker open so several files can
	// be attached in one visit; Esc closes it.
	if ok, path := m.fp.DidSelectFile(msg); ok {
		m.addAttachment(path)
	}
	return m, cmd
}

func (m model) updateProjects(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		m.projList.move(-1)
	case "down", "j":
		m.projList.move(1)
	case "esc":
		if m.project == nil {
			return m.useCurrentFolder()
		}
		m.overlay = overlayNone
		m.ta.Focus()
		return m, textarea.Blink
	case "enter":
		it, ok := m.projList.selected()
		if !ok {
			return m, nil
		}
		if it.action == "use-cwd" {
			return m.useCurrentFolder()
		}
		p, err := m.store.LoadProject(it.id)
		if err != nil {
			m.overlay = overlayNone
			m.commitSystem("не удалось открыть проект: " + err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.openProject(p)
		m.overlay = overlayNone
		m.ta.Focus()
		m.commitSystem("проект: " + p.Name + "  ·  " + p.Cwd)
		m.refreshViewport()
		return m, textarea.Blink
	}
	return m, nil
}

func (m model) updateThreads(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		m.thrList.move(-1)
	case "down", "j":
		m.thrList.move(1)
	case "esc":
		m.overlay = overlayNone
		m.ta.Focus()
		return m, textarea.Blink
	case "d":
		it, ok := m.thrList.selected()
		if ok && it.action == "" && it.id != "" {
			_ = m.store.DeleteThread(m.project.Slug(), it.id)
			if m.thread != nil && m.thread.ID == it.id {
				m.startNewThread()
			}
			m.buildThreadList()
			if m.thrList.cursor >= len(m.thrList.items) {
				m.thrList.cursor = len(m.thrList.items) - 1
			}
		}
	case "enter":
		it, ok := m.thrList.selected()
		if !ok {
			return m, nil
		}
		if it.action == "new-thread" {
			m.startNewThread()
			m.overlay = overlayNone
			m.ta.Focus()
			m.commitSystem("новый тред")
			m.refreshViewport()
			return m, textarea.Blink
		}
		t, err := m.store.LoadThread(m.project.Slug(), it.id)
		if err != nil {
			m.overlay = overlayNone
			m.commitSystem("не удалось открыть тред: " + err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.openThread(t)
		m.overlay = overlayNone
		m.ta.Focus()
		m.refreshViewport()
		return m, textarea.Blink
	}
	return m, nil
}

func (m model) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "up", "k":
		m.searchList.move(-1)
	case "down", "j":
		m.searchList.move(1)
	case "esc":
		m.overlay = overlayNone
		m.ta.Focus()
		return m, textarea.Blink
	case "enter":
		it, ok := m.searchList.selected()
		if !ok {
			return m, nil
		}
		t, err := m.store.LoadThread(m.project.Slug(), it.id)
		if err != nil {
			m.overlay = overlayNone
			m.commitSystem("не удалось открыть тред: " + err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.openThread(t)
		m.overlay = overlayNone
		m.ta.Focus()
		m.refreshViewport()
		return m, textarea.Blink
	}
	return m, nil
}

func (m model) beginApproval(req *ApprovalRequest) (tea.Model, tea.Cmd) {
	m.pending = req
	m.remembering = false
	m.ruleInput.Blur()
	m.overlay = overlayApprove
	return m, nil
}

func (m model) updateApprove(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.remembering {
		switch k.String() {
		case "enter":
			rule := strings.TrimSpace(m.ruleInput.Value())
			if rule != "" && m.project != nil && addAllowRule(m.project, rule) {
				_ = m.store.SaveProject(m.project)
				m.configureEngine() // refresh inline --settings
			}
			return m.resolveApproval(true)
		case "esc":
			m.remembering = false
			m.ruleInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.ruleInput, cmd = m.ruleInput.Update(msg)
		return m, cmd
	}
	switch k.String() {
	case "a", "y", "enter":
		return m.resolveApproval(true)
	case "d", "n", "esc":
		return m.resolveApproval(false)
	case "r":
		if m.project != nil {
			m.remembering = true
			m.ruleInput.SetValue(suggestRule(m.project, m.pending.ToolName, m.pending.Input))
			m.ruleInput.CursorEnd()
			m.ruleInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m model) resolveApproval(allow bool) (tea.Model, tea.Cmd) {
	req := m.pending
	if req == nil {
		m.overlay = overlayNone
		return m, nil
	}
	dec := ApprovalDecision{Allow: allow}
	if !allow {
		dec.Message = "Отклонено пользователем"
	}
	if req.Reply != nil {
		req.Reply <- dec
	}
	m.commitApproval(req, allow)
	m.pending = nil
	m.remembering = false
	m.ruleInput.Blur()
	m.overlay = overlayNone
	m.refreshViewport()
	return m, nil
}

// commitApproval records a gated tool decision in the conversation timeline and
// the persisted transcript.
func (m *model) commitApproval(req *ApprovalRequest, allow bool) {
	mark, verdict := "✗", "отклонено"
	if allow {
		mark, verdict = "✓", "разрешено"
	}
	target := toolTarget(req.ToolName, req.Input)
	line := strings.TrimSpace(fmt.Sprintf("%s %s %s · %s", mark, req.ToolName, target, verdict))
	m.messages = append(m.messages, message{role: roleSystem, content: line, rendered: m.renderSystem(line)})
	if m.project != nil && m.thread != nil {
		m.thread.Messages = append(m.thread.Messages, Msg{
			Role:     "tool",
			Content:  line,
			Ts:       time.Now(),
			ToolMeta: map[string]any{"tool": req.ToolName, "allow": allow, "target": target},
		})
		_ = m.store.SaveThread(m.project.Slug(), m.thread)
	}
}

func (m model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.streaming && m.cancel != nil {
				m.cancel()
				m.status = "отменено"
			}
			return m, nil

		case "ctrl+o":
			if !m.streaming {
				return m, m.openPicker()
			}
			return m, nil

		case "ctrl+p":
			m.openProjectSwitcher()
			return m, nil

		case "ctrl+t":
			m.openThreadBrowser()
			return m, nil

		case "tab", "ctrl+g":
			if !m.streaming {
				next := ModeAgent
				if m.mode == ModeAgent {
					next = ModeChat
				}
				m.setMode(next)
				m.refreshViewport()
			}
			return m, nil

		case "ctrl+j":
			m.ta.InsertString("\n")
			return m, nil

		case "enter":
			if m.streaming {
				return m, nil
			}
			val := strings.TrimSpace(m.ta.Value())
			if val == "" {
				return m, nil
			}
			if handled, cmd := m.handleCommand(val); handled {
				m.ta.Reset()
				m.refreshViewport()
				return m, cmd
			}
			if m.project == nil {
				m.commitSystem("сначала выберите проект (Ctrl+P)")
				m.ta.Reset()
				m.refreshViewport()
				return m, nil
			}

			prompt := m.buildPrompt(val)
			disp := val
			if len(m.attachments) > 0 {
				disp += "\n" + attachmentLine(m.attachments)
			}
			m.messages = append(m.messages, message{
				role:     roleUser,
				content:  disp,
				rendered: m.renderUser(disp),
			})
			m.persistUser(disp)
			m.attachments = nil
			m.ta.Reset()

			m.streaming = true
			m.turnHadResult = false
			m.streamBuf = ""
			m.status = "думаю…"
			m.refreshViewport()

			cmds = append(cmds, m.sp.Tick, m.startTurn(prompt))
			return m, tea.Batch(cmds...)
		}

	case eventMsg:
		ev := Event(msg)
		switch ev.Kind {
		case EvSystemInit:
			m.setSession(ev.SessionID)
			if ev.Model != "" {
				m.modelName = ev.Model
			}
			m.status = "генерация…"
		case EvText:
			m.streamBuf += ev.Text
			m.refreshViewport()
		case EvToolStart:
			m.status = "⚙ " + ev.Tool
		case EvRetry:
			m.status = fmt.Sprintf("повтор запроса (#%d)…", ev.Attempt)
		case EvResult:
			m.turnHadResult = true
			m.setSession(ev.SessionID)
			m.costUSD += ev.CostUSD
			if m.config.BudgetWarnUSD > 0 && !m.budgetWarned && m.costUSD >= m.config.BudgetWarnUSD {
				m.budgetWarned = true
				m.commitSystem(fmt.Sprintf("⚠ бюджет: израсходовано $%.4f (порог $%.2f). С 15.06.2026 расход идёт из месячного Agent SDK-кредита.", m.costUSD, m.config.BudgetWarnUSD))
			}
			final := m.streamBuf
			if strings.TrimSpace(final) == "" {
				final = ev.Text
			}
			m.commitAssistant(final)
			m.persistAssistant(final)
			m.streaming = false
			m.status = ""
			m.refreshViewport()
		case EvError:
			if !m.turnHadResult {
				m.commitSystem("⚠ ошибка: " + ev.Err.Error())
			}
			m.streaming = false
			if m.status != "отменено" {
				m.status = ""
			}
			m.refreshViewport()
		}
		return m, waitEvent(m.streamCh)

	case streamClosedMsg:
		if m.streaming {
			m.streaming = false
			if !m.turnHadResult && strings.TrimSpace(m.streamBuf) != "" {
				m.commitAssistant(m.streamBuf)
				m.persistAssistant(m.streamBuf)
			}
			m.streamBuf = ""
			if m.status != "отменено" {
				m.status = ""
			}
			m.refreshViewport()
		}
		return m, nil

	case memoryEditedMsg:
		if msg.err != nil {
			m.commitSystem("ошибка редактора: " + msg.err.Error())
		} else {
			m.commitSystem("memory.md обновлён")
		}
		m.refreshViewport()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		if m.streaming {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var vcmd tea.Cmd
			m.vp, vcmd = m.vp.Update(msg)
			return m, vcmd
		}
	}
	var tcmd tea.Cmd
	m.ta, tcmd = m.ta.Update(msg)
	cmds = append(cmds, tcmd)
	return m, tea.Batch(cmds...)
}

func (m *model) commitAssistant(s string) {
	m.messages = append(m.messages, message{role: roleAssistant, content: s, rendered: m.renderAssistant(s)})
	m.streamBuf = ""
}

func (m *model) commitSystem(s string) {
	m.messages = append(m.messages, message{role: roleSystem, content: s, rendered: m.renderSystem(s)})
}

func (m *model) startTurn(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	resume := ""
	if m.thread != nil {
		resume = m.thread.ClaudeSessionID
	}
	ch := m.engine.Send(ctx, prompt, resume)
	m.streamCh = ch
	return waitEvent(ch)
}

func waitEvent(ch <-chan Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return eventMsg(ev)
	}
}

// ---- commands & attachments ----------------------------------------------

func (m *model) handleCommand(val string) (bool, tea.Cmd) {
	if !strings.HasPrefix(val, "/") {
		return false, nil
	}
	fields := strings.Fields(val)
	arg := strings.TrimSpace(strings.TrimPrefix(val, fields[0]))
	switch fields[0] {
	case "/quit", "/q", "/exit":
		return true, tea.Quit

	case "/new", "/clear":
		m.startNewThread()
		m.commitSystem("новый тред")
		return true, nil

	case "/project", "/p":
		if arg != "" {
			if p := m.findProject(arg); p != nil {
				m.openProject(p)
				m.commitSystem("проект: " + p.Name + "  ·  " + p.Cwd)
				return true, nil
			}
			m.commitSystem("проект не найден: " + arg)
		}
		m.openProjectSwitcher()
		return true, nil

	case "/threads", "/t":
		if m.project == nil {
			m.commitSystem("нет активного проекта")
			return true, nil
		}
		m.openThreadBrowser()
		return true, nil

	case "/resume":
		if m.project == nil {
			m.commitSystem("нет активного проекта")
			return true, nil
		}
		if arg == "" {
			m.commitSystem("использование: /resume <id>")
			return true, nil
		}
		t, err := m.store.LoadThread(m.project.Slug(), arg)
		if err != nil {
			m.commitSystem("тред не найден: " + arg)
			return true, nil
		}
		m.openThread(t)
		m.commitSystem("возобновлён тред: " + t.Title)
		return true, nil

	case "/mode":
		switch strings.ToLower(arg) {
		case "chat":
			m.setMode(ModeChat)
		case "agent":
			m.setMode(ModeAgent)
		case "":
			next := ModeAgent
			if m.mode == ModeAgent {
				next = ModeChat
			}
			m.setMode(next)
		default:
			m.commitSystem("использование: /mode chat|agent")
		}
		return true, nil

	case "/memory", "/m":
		return true, m.cmdEditMemory()

	case "/search", "/s":
		if m.project == nil {
			m.commitSystem("нет активного проекта")
			return true, nil
		}
		if arg == "" {
			m.commitSystem("использование: /search <текст>")
			return true, nil
		}
		m.runSearch(arg)
		return true, nil

	case "/export":
		if m.thread == nil || len(m.thread.Messages) == 0 {
			m.commitSystem("нечего экспортировать")
			return true, nil
		}
		path := defaultExportPath(m.project, m.thread)
		if arg != "" {
			path = expandPath(arg)
		}
		if err := exportThreadMarkdown(m.thread, m.project, path); err != nil {
			m.commitSystem("ошибка экспорта: " + err.Error())
		} else {
			m.commitSystem("экспортировано: " + path)
		}
		return true, nil

	case "/theme":
		if arg == "" {
			m.commitSystem("темы: " + strings.Join(themeNames(), ", ") + " · текущая: " + m.config.Theme)
			return true, nil
		}
		if _, ok := themes[arg]; !ok {
			m.commitSystem("неизвестная тема: " + arg + " (есть: " + strings.Join(themeNames(), ", ") + ")")
			return true, nil
		}
		applyTheme(arg)
		m.config.Theme = arg
		_ = m.store.SaveConfig(m.config)
		m.sp.Style = lipgloss.NewStyle().Foreground(colAccent)
		m.layout()
		m.rerenderAll()
		m.commitSystem("тема: " + arg)
		return true, nil

	case "/budget":
		if arg == "" {
			if m.config.BudgetWarnUSD > 0 {
				m.commitSystem(fmt.Sprintf("порог: $%.2f · сессия: $%.4f", m.config.BudgetWarnUSD, m.costUSD))
			} else {
				m.commitSystem("предупреждение о бюджете выключено · /budget <usd> — задать порог")
			}
			return true, nil
		}
		v, err := strconv.ParseFloat(arg, 64)
		if err != nil || v < 0 {
			m.commitSystem("использование: /budget <долл.>")
			return true, nil
		}
		m.config.BudgetWarnUSD = v
		m.budgetWarned = false
		_ = m.store.SaveConfig(m.config)
		if v == 0 {
			m.commitSystem("предупреждение о бюджете выключено")
		} else {
			m.commitSystem(fmt.Sprintf("порог предупреждения: $%.2f", v))
		}
		return true, nil

	case "/mcp":
		note := "approval-сервер: недоступен"
		if m.perm != nil && m.perm.Addr() != "" {
			note = "approval-сервер: " + permServerName + " @ " + m.perm.Addr() + " (только в agent)"
		}
		m.commitSystem(note + "\nОстальные MCP-серверы наследуются из конфигурации Claude Code (~/.claude.json, .mcp.json).")
		return true, nil

	case "/attach", "/a":
		if arg != "" {
			m.addAttachment(arg)
			return true, nil
		}
		return true, m.openPicker()

	case "/files":
		if arg == "clear" {
			m.attachments = nil
			m.commitSystem("вложения очищены")
			return true, nil
		}
		if len(m.attachments) == 0 {
			m.commitSystem("вложений нет")
			return true, nil
		}
		var b strings.Builder
		b.WriteString("вложения (/detach <N> — убрать, /files clear — очистить):")
		for i, a := range m.attachments {
			b.WriteString(fmt.Sprintf("\n  %d. %s %s", i+1, attachIcon(a), a))
		}
		m.commitSystem(b.String())
		return true, nil

	case "/detach", "/d":
		if len(m.attachments) == 0 {
			m.commitSystem("вложений нет")
			return true, nil
		}
		idx := len(m.attachments) // default: last
		if arg != "" {
			n, err := strconv.Atoi(arg)
			if err != nil {
				m.commitSystem("использование: /detach <N>")
				return true, nil
			}
			idx = n
		}
		if idx < 1 || idx > len(m.attachments) {
			m.commitSystem("нет вложения №" + arg)
			return true, nil
		}
		removed := m.attachments[idx-1]
		m.attachments = append(m.attachments[:idx-1], m.attachments[idx:]...)
		m.commitSystem("убрано: " + filepath.Base(removed))
		return true, nil

	case "/help":
		m.commitSystem(helpText)
		return true, nil
	}
	return false, nil
}

func (m *model) cmdEditMemory() tea.Cmd {
	if m.project == nil {
		m.commitSystem("нет активного проекта")
		return nil
	}
	path := m.store.MemoryPath(m.project.Slug())
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		m.commitSystem("$EDITOR не задан; правьте вручную: " + path)
		return nil
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg { return memoryEditedMsg{err} })
}

// buildPrompt appends attachments as @-references so Claude Code reads/attaches them.
func (m *model) buildPrompt(val string) string {
	var sb strings.Builder
	sb.WriteString(val)
	for _, a := range m.attachments {
		sb.WriteString(" @")
		sb.WriteString(a)
	}
	return sb.String()
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".bmp": true, ".svg": true,
}

func isImagePath(p string) bool {
	return imageExts[strings.ToLower(filepath.Ext(p))]
}

func attachIcon(p string) string {
	if isImagePath(p) {
		return "🖼"
	}
	return "📎"
}

// addAttachment validates and de-duplicates a path before queuing it.
func (m *model) addAttachment(p string) {
	p = expandPath(p)
	info, err := os.Stat(p)
	if err != nil {
		m.commitSystem("файл не найден: " + p)
		return
	}
	if info.IsDir() {
		m.commitSystem("это директория, не файл: " + p)
		return
	}
	if slices.Contains(m.attachments, p) {
		return
	}
	m.attachments = append(m.attachments, p)
}

// displayName shortens a path relative to the project when possible.
func (m model) displayName(p string) string {
	if m.project != nil {
		if rel, err := filepath.Rel(m.project.Cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(p)
}

func attachmentLine(paths []string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = attachIcon(p) + " " + filepath.Base(p)
	}
	return strings.Join(names, "  ")
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ---- view -----------------------------------------------------------------

func (m model) overlayHeight() int {
	h := m.height - headerH - 1
	if h < 4 {
		h = 4
	}
	return h
}

func (m model) View() string {
	if !m.ready {
		return "загрузка…"
	}
	switch m.overlay {
	case overlayPicker:
		title := " 📎 Файл/картинка · Enter — добавить · Ctrl+H — скрытые · Esc — готово"
		if n := len(m.attachments); n > 0 {
			title = fmt.Sprintf(" 📎 Выбрано: %d · Enter — добавить · Ctrl+H — скрытые · Esc — готово", n)
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			m.headerView(),
			pickerTitleStyle.Render(title),
			m.fp.View(),
		)
	case overlayProjects:
		return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.projList.view(m.width, m.overlayHeight()))
	case overlayThreads:
		return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.thrList.view(m.width, m.overlayHeight()))
	case overlaySearch:
		return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.searchList.view(m.width, m.overlayHeight()))
	case overlayApprove:
		return m.approveView()
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		m.vp.View(),
		m.attachmentsView(),
		m.inputView(),
		m.footerView(),
	)
}

func (m model) approveView() string {
	req := m.pending
	if req == nil {
		return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.vp.View())
	}
	preview := clampLines(toolPreview(req.ToolName, req.Input), m.overlayHeight()-7)
	lines := []string{
		approveTitleStyle.Render("⚠ Запрос доступа — " + req.ToolName),
		"",
		preview,
		"",
	}
	if m.remembering {
		lines = append(lines,
			warnStyle.Render("allow-правило (Enter — сохранить и разрешить · Esc — назад):"),
			ruleInputStyle.Render(m.ruleInput.View()),
		)
	} else {
		lines = append(lines, hintStyle.Render("[a] разрешить · [r] запомнить+разрешить · [d] отклонить"))
	}
	box := approveBoxStyle.Width(m.width - 4).Render(strings.Join(lines, "\n"))
	return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), box)
}

func (m model) headerView() string {
	left := titleStyle.Render(" claude-tui ")
	sep := metaStyle.Render("  ·  ")
	var parts []string
	if m.project != nil {
		parts = append(parts, metaStyle.Render(m.project.Name))
	}
	if m.mode == ModeAgent {
		parts = append(parts, modeAgentStyle.Render("agent"))
	} else {
		parts = append(parts, modeChatStyle.Render("chat"))
	}
	if m.modelName != "" {
		parts = append(parts, metaStyle.Render(m.modelName))
	}
	if m.sessionShort != "" {
		parts = append(parts, metaStyle.Render("sess:"+m.sessionShort))
	}
	if m.costUSD > 0 {
		parts = append(parts, metaStyle.Render(fmt.Sprintf("$%.4f", m.costUSD)))
	}
	right := strings.Join(parts, sep)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right
}

func (m model) attachmentsView() string {
	if len(m.attachments) == 0 {
		return hintStyle.Render(" нет вложений · Ctrl+O — прикрепить")
	}
	chips := make([]string, len(m.attachments))
	for i, a := range m.attachments {
		chips[i] = chipStyle.Render(fmt.Sprintf("%s %d %s", attachIcon(a), i+1, m.displayName(a)))
	}
	return " " + strings.Join(chips, " ")
}

func (m model) inputView() string {
	return inputBoxStyle.Render(m.ta.View())
}

func (m model) footerView() string {
	var status string
	switch {
	case m.streaming:
		status = m.sp.View() + " " + statusStyle.Render(m.status)
	case m.status != "":
		status = statusStyle.Render(m.status)
	default:
		status = hintStyle.Render("Enter — отправить · Ctrl+P — проекты · Ctrl+T — треды · Ctrl+O — файл · /help")
	}
	return " " + status
}
