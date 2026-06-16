package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

type role int

const (
	roleUser role = iota
	roleAssistant
	roleSystem
)

type message struct {
	role     role
	content  string
	rendered string
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
type eventMsg core.Event
type streamClosedMsg struct{}
type memoryEditedMsg struct{ err error }
type approvalReqMsg struct {
	req   core.ApprovalRequest
	reply chan core.ApprovalDecision
}

const helpText = "Команды: /project [имя] (Ctrl+P), /threads (Ctrl+T), /new, /resume <id>, /mode chat|agent (Tab), " +
	"/search <текст>, /export [путь], /memory, /attach <путь> (Ctrl+O), /files [clear], /detach [N], " +
	"/theme [имя], /budget [usd], /mcp, /help, /quit. " +
	"В agent-режиме правки и команды проходят через модалку подтверждения. " +
	"В сообщении можно писать @/путь напрямую. Enter — отправить, Ctrl+J — перенос строки, Esc — отменить ответ."

type model struct {
	app *core.App

	mode core.Mode

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

	pending      *core.ApprovalRequest
	pendingReply chan core.ApprovalDecision
	remembering  bool

	projList   selList
	thrList    selList
	searchList selList

	sessionShort string
	modelName    string
	costUSD      float64

	width, height int
	ready         bool

	streamCh <-chan core.Event
	cancel   context.CancelFunc
}

// New builds the TUI model over a configured core App and performs startup
// project selection.
func New(app *core.App) tea.Model {
	applyTheme(app.Config().Theme)

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

	m := model{app: app, mode: app.Mode(), ta: ta, sp: sp, fp: fp, ruleInput: ri}

	cwd, _ := os.Getwd()
	switch existing, _ := app.ProjectForCwd(cwd); {
	case existing != nil:
		app.OpenProjectObj(existing)
		m.syncAfterOpen()
	case app.LastProjectSlug() != "":
		if _, err := app.OpenProject(app.LastProjectSlug()); err == nil {
			m.syncAfterOpen()
			m.pendingHint = "Текущая папка не проект. Ctrl+P → «использовать текущую папку»: " + cwd
		} else {
			m.openProjectSwitcher()
		}
	default:
		m.openProjectSwitcher()
	}
	return m
}

// NewBroker returns an ApprovalBroker that surfaces requests in the running
// program and blocks until the user answers.
func NewBroker(send func(tea.Msg)) core.ApprovalBroker {
	return broker{send: send}
}

type broker struct{ send func(tea.Msg) }

func (b broker) RequestApproval(ctx context.Context, req core.ApprovalRequest) core.ApprovalDecision {
	reply := make(chan core.ApprovalDecision, 1)
	b.send(approvalReqMsg{req: req, reply: reply})
	select {
	case dec := <-reply:
		return dec
	case <-ctx.Done():
		return core.ApprovalDecision{Allow: false, Message: "отменено"}
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

// ---- lifecycle helpers ----------------------------------------------------

// syncAfterOpen refreshes display mirrors after the app opens a project.
func (m *model) syncAfterOpen() {
	m.mode = m.app.Mode()
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

func (m *model) startNewThread() {
	m.app.NewThread()
	m.messages = nil
	m.streamBuf = ""
	m.streaming = false
	m.sessionShort = ""
	if m.ready {
		m.rerenderAll()
		m.refreshViewport()
	}
}

func (m *model) showThread(t *core.Thread) {
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

func (m *model) messagesFromThread(t *core.Thread) []message {
	out := make([]message, 0, len(t.Messages))
	for _, msg := range t.Messages {
		switch msg.Role {
		case "user":
			out = append(out, message{role: roleUser, content: userDisplay(msg.Content, msg.Attachments)})
		case "assistant":
			out = append(out, message{role: roleAssistant, content: msg.Content})
		case "tool":
			out = append(out, message{role: roleSystem, content: formatApprovalLine(msg.ToolMeta)})
		default:
			out = append(out, message{role: roleSystem, content: msg.Content})
		}
	}
	return out
}

func (m *model) setMode(mode core.Mode) {
	warn := m.app.SetMode(mode)
	m.mode = mode
	if warn != "" {
		m.commitSystem("⚠ " + warn)
	} else {
		m.commitSystem("режим: " + mode.String())
	}
}

// ---- overlays -------------------------------------------------------------

func (m *model) openProjectSwitcher() {
	cwd, _ := os.Getwd()
	var items []selItem
	if existing, _ := m.app.ProjectForCwd(cwd); existing == nil {
		items = append(items, selItem{title: "+ Использовать текущую папку", subtitle: cwd, action: "use-cwd"})
	}
	projects, _ := m.app.ListProjects()
	cur := m.app.CurrentProject()
	for _, p := range projects {
		title := p.Name
		if cur != nil && p.Slug() == cur.Slug() {
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
	threads, _ := m.app.ListThreads()
	cur := m.app.CurrentThread()
	for _, t := range threads {
		title := t.Title
		if title == "" {
			title = "(без названия)"
		}
		if cur != nil && t.ID == cur.ID {
			title += " (текущий)"
		}
		sub := t.Updated.Format("2006-01-02 15:04") + "  ·  " + fmt.Sprintf("%d сообщ. · id:%s", len(t.Messages), t.ID)
		items = append(items, selItem{id: t.ID, title: title, subtitle: sub})
	}
	name := ""
	if cur := m.app.CurrentProject(); cur != nil {
		name = cur.Name
	}
	m.thrList = selList{
		title: "Треды — " + name,
		items: items,
		hint:  "↑↓ · Enter — открыть · d — удалить · Esc — закрыть",
	}
}

func (m *model) openThreadBrowser() {
	if m.app.CurrentProject() == nil {
		return
	}
	m.buildThreadList()
	m.overlay = overlayThreads
	m.ta.Blur()
}

// openPicker shows the file picker rooted at the project directory.
func (m *model) openPicker() tea.Cmd {
	dir := ""
	if p := m.app.CurrentProject(); p != nil {
		dir = p.Cwd
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

// runSearch queries the core and opens a results list.
func (m *model) runSearch(q string) {
	hits, _ := m.app.Search(q)
	var items []selItem
	for _, h := range hits {
		title := h.Title
		if title == "" {
			title = "(без названия)"
		}
		sub := h.Snippet
		if sub == "" {
			sub = h.Updated.Format("2006-01-02 15:04")
		}
		items = append(items, selItem{id: h.ThreadID, title: title, subtitle: sub})
	}
	m.searchList = selList{
		title: fmt.Sprintf("Поиск «%s» — найдено %d", q, len(items)),
		items: items,
		hint:  "↑↓ · Enter — открыть · Esc — закрыть",
	}
	m.overlay = overlaySearch
	m.ta.Blur()
}

func (m model) useCurrentFolder() (tea.Model, tea.Cmd) {
	cwd, err := os.Getwd()
	if err != nil {
		m.commitSystem("не удалось определить текущую папку: " + err.Error())
		return m, nil
	}
	p, err := m.app.UseCwd(cwd)
	if err != nil {
		m.commitSystem("не удалось создать проект: " + err.Error())
		return m, nil
	}
	m.syncAfterOpen()
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
	inputBoxH := taLines + 2
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
		return
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

	if am, ok := msg.(approvalReqMsg); ok {
		return m.beginApproval(am)
	}

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
		if m.app.CurrentProject() == nil {
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
		p, err := m.app.OpenProject(it.id)
		if err != nil {
			m.overlay = overlayNone
			m.commitSystem("не удалось открыть проект: " + err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.syncAfterOpen()
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
			_ = m.app.DeleteThread(it.id)
			if cur := m.app.CurrentThread(); cur == nil || cur.ID != it.id {
				// deleting a non-active thread keeps the current view
			} else {
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
		t, err := m.app.OpenThread(it.id)
		if err != nil {
			m.overlay = overlayNone
			m.commitSystem("не удалось открыть тред: " + err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.showThread(t)
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
		t, err := m.app.OpenThread(it.id)
		if err != nil {
			m.overlay = overlayNone
			m.commitSystem("не удалось открыть тред: " + err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.showThread(t)
		m.overlay = overlayNone
		m.ta.Focus()
		m.refreshViewport()
		return m, textarea.Blink
	}
	return m, nil
}

func (m model) beginApproval(am approvalReqMsg) (tea.Model, tea.Cmd) {
	req := am.req
	m.pending = &req
	m.pendingReply = am.reply
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
			return m.resolveApproval(true, strings.TrimSpace(m.ruleInput.Value()))
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
		return m.resolveApproval(true, "")
	case "d", "n", "esc":
		return m.resolveApproval(false, "")
	case "r":
		if p := m.app.CurrentProject(); p != nil {
			m.remembering = true
			m.ruleInput.SetValue(core.SuggestRule(p, m.pending.ToolName, m.pending.Input))
			m.ruleInput.CursorEnd()
			m.ruleInput.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m model) resolveApproval(allow bool, rule string) (tea.Model, tea.Cmd) {
	req := m.pending
	if req == nil {
		m.overlay = overlayNone
		return m, nil
	}
	dec := core.ApprovalDecision{Allow: allow, RememberRule: rule}
	if !allow {
		dec.Message = "Отклонено пользователем"
	}
	if m.pendingReply != nil {
		m.pendingReply <- dec
	}
	m.addApprovalLine(req, allow)
	m.pending = nil
	m.pendingReply = nil
	m.remembering = false
	m.ruleInput.Blur()
	m.overlay = overlayNone
	m.refreshViewport()
	return m, nil
}

// addApprovalLine shows a gated tool decision in the conversation. The core
// persists the transcript entry; this is the live display.
func (m *model) addApprovalLine(req *core.ApprovalRequest, allow bool) {
	line := formatApprovalLine(map[string]any{
		"tool":   req.ToolName,
		"target": core.ToolTarget(req.ToolName, req.Input),
		"allow":  allow,
	})
	m.commitSystem(line)
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
				next := core.ModeAgent
				if m.mode == core.ModeAgent {
					next = core.ModeChat
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
			if m.app.CurrentProject() == nil {
				m.commitSystem("сначала выберите проект (Ctrl+P)")
				m.ta.Reset()
				m.refreshViewport()
				return m, nil
			}

			attachments := m.attachments
			disp := userDisplay(val, attachments)
			m.messages = append(m.messages, message{role: roleUser, content: disp, rendered: m.renderUser(disp)})
			m.attachments = nil
			m.ta.Reset()

			m.streaming = true
			m.turnHadResult = false
			m.streamBuf = ""
			m.status = "думаю…"
			m.refreshViewport()

			cmds = append(cmds, m.sp.Tick, m.startTurn(val, attachments))
			return m, tea.Batch(cmds...)
		}

	case eventMsg:
		ev := core.Event(msg)
		switch ev.Kind {
		case core.EvSystemInit:
			if ev.SessionID != "" {
				m.sessionShort = shortID(ev.SessionID)
			}
			if ev.Model != "" {
				m.modelName = ev.Model
			}
			m.status = "генерация…"
		case core.EvText:
			m.streamBuf += ev.Text
			m.refreshViewport()
		case core.EvToolStart:
			m.status = "⚙ " + ev.Tool
		case core.EvRetry:
			m.status = fmt.Sprintf("повтор запроса (#%d)…", ev.Attempt)
		case core.EvResult:
			m.turnHadResult = true
			if ev.SessionID != "" {
				m.sessionShort = shortID(ev.SessionID)
			}
			m.costUSD += ev.CostUSD
			final := m.streamBuf
			if strings.TrimSpace(final) == "" {
				final = ev.Text
			}
			m.commitAssistant(final)
			m.streaming = false
			m.status = ""
			m.refreshViewport()
		case core.EvNotice:
			m.commitSystem("⚠ " + ev.Text)
			m.refreshViewport()
		case core.EvError:
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

func (m *model) startTurn(text string, attachments []string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ch, err := m.app.SendTurn(ctx, text, attachments)
	if err != nil {
		cancel()
		return func() tea.Msg { return eventMsg(core.Event{Kind: core.EvError, Err: err}) }
	}
	m.streamCh = ch
	return waitEvent(ch)
}

func waitEvent(ch <-chan core.Event) tea.Cmd {
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
			if p := m.app.FindProject(arg); p != nil {
				m.app.OpenProjectObj(p)
				m.syncAfterOpen()
				m.commitSystem("проект: " + p.Name + "  ·  " + p.Cwd)
				return true, nil
			}
			m.commitSystem("проект не найден: " + arg)
		}
		m.openProjectSwitcher()
		return true, nil

	case "/threads", "/t":
		if m.app.CurrentProject() == nil {
			m.commitSystem("нет активного проекта")
			return true, nil
		}
		m.openThreadBrowser()
		return true, nil

	case "/resume":
		if m.app.CurrentProject() == nil {
			m.commitSystem("нет активного проекта")
			return true, nil
		}
		if arg == "" {
			m.commitSystem("использование: /resume <id>")
			return true, nil
		}
		t, err := m.app.OpenThread(arg)
		if err != nil {
			m.commitSystem("тред не найден: " + arg)
			return true, nil
		}
		m.showThread(t)
		m.commitSystem("возобновлён тред: " + t.Title)
		return true, nil

	case "/mode":
		switch strings.ToLower(arg) {
		case "chat":
			m.setMode(core.ModeChat)
		case "agent":
			m.setMode(core.ModeAgent)
		case "":
			next := core.ModeAgent
			if m.mode == core.ModeAgent {
				next = core.ModeChat
			}
			m.setMode(next)
		default:
			m.commitSystem("использование: /mode chat|agent")
		}
		return true, nil

	case "/memory", "/m":
		return true, m.cmdEditMemory()

	case "/search", "/s":
		if m.app.CurrentProject() == nil {
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
		path := arg
		if path == "" {
			cur := m.app.CurrentThread()
			if cur == nil || len(cur.Messages) == 0 {
				m.commitSystem("нечего экспортировать")
				return true, nil
			}
			path = core.DefaultExportPath(m.app.CurrentProject(), cur)
		} else {
			path = core.ExpandPath(path)
		}
		if err := m.app.ExportCurrentThread(path); err != nil {
			m.commitSystem("ошибка экспорта: " + err.Error())
		} else {
			m.commitSystem("экспортировано: " + path)
		}
		return true, nil

	case "/theme":
		cfg := m.app.Config()
		if arg == "" {
			m.commitSystem("темы: " + strings.Join(themeNames(), ", ") + " · текущая: " + cfg.Theme)
			return true, nil
		}
		if _, ok := themes[arg]; !ok {
			m.commitSystem("неизвестная тема: " + arg + " (есть: " + strings.Join(themeNames(), ", ") + ")")
			return true, nil
		}
		applyTheme(arg)
		_ = m.app.SetTheme(arg)
		m.sp.Style = lipgloss.NewStyle().Foreground(colAccent)
		m.layout()
		m.rerenderAll()
		m.commitSystem("тема: " + arg)
		return true, nil

	case "/budget":
		cfg := m.app.Config()
		if arg == "" {
			if cfg.BudgetWarnUSD > 0 {
				m.commitSystem(fmt.Sprintf("порог: $%.2f · сессия: $%.4f", cfg.BudgetWarnUSD, m.costUSD))
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
		_ = m.app.SetBudget(v)
		if v == 0 {
			m.commitSystem("предупреждение о бюджете выключено")
		} else {
			m.commitSystem(fmt.Sprintf("порог предупреждения: $%.2f", v))
		}
		return true, nil

	case "/mcp":
		note := "approval-сервер: недоступен"
		if addr, ok := m.app.PermissionInfo(); ok {
			note = "approval-сервер: permctl @ " + addr + " (только в agent)"
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
		idx := len(m.attachments)
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
	path := m.app.MemoryPath()
	if path == "" {
		m.commitSystem("нет активного проекта")
		return nil
	}
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

func attachIcon(p string) string {
	if core.IsImagePath(p) {
		return "🖼"
	}
	return "📎"
}

// userDisplay renders a user message with its attachment chips appended.
func userDisplay(content string, attachments []string) string {
	if len(attachments) == 0 {
		return content
	}
	names := make([]string, len(attachments))
	for i, p := range attachments {
		names[i] = attachIcon(p) + " " + filepath.Base(p)
	}
	return content + "\n" + strings.Join(names, "  ")
}

// addAttachment validates and de-duplicates a path before queuing it.
func (m *model) addAttachment(p string) {
	clean, err := core.ValidateAttachmentPath(p)
	if err != nil {
		m.commitSystem(err.Error())
		return
	}
	for _, a := range m.attachments {
		if a == clean {
			return
		}
	}
	m.attachments = append(m.attachments, clean)
}

func (m model) displayName(p string) string {
	cwd := ""
	if pr := m.app.CurrentProject(); pr != nil {
		cwd = pr.Cwd
	}
	return core.RelDisplay(cwd, p)
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
	preview := clampLines(renderPreview(core.BuildToolPreview(req.ToolName, req.Input)), m.overlayHeight()-7)
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
	left := titleStyle.Render(" claude-code-linux-ui ")
	sep := metaStyle.Render("  ·  ")
	var parts []string
	if p := m.app.CurrentProject(); p != nil {
		parts = append(parts, metaStyle.Render(p.Name))
	}
	if m.mode == core.ModeAgent {
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
