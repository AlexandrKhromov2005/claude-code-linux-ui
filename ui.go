package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
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

// tea.Msg types
type eventMsg Event
type streamClosedMsg struct{}

const helpText = "Команды: /attach <путь> (или Ctrl+O — пикер), /files, /clear (новая сессия), /help, /quit. " +
	"В сообщении можно писать @/путь/к/файлу напрямую. Enter — отправить, Ctrl+J — перенос строки, Esc — отменить ответ."

type model struct {
	engine *Engine

	vp   viewport.Model
	ta   textarea.Model
	sp   spinner.Model
	fp   filepicker.Model
	glam *glamour.TermRenderer

	messages    []message
	attachments []string

	streamBuf     string
	streaming     bool
	turnHadResult bool
	picker        bool
	status        string

	sessionShort string
	modelName    string
	costUSD      float64

	width, height int
	ready         bool

	streamCh <-chan Event
	cancel   context.CancelFunc
}

func newModel() (model, error) {
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	tools := os.Getenv("CLAUDE_TUI_TOOLS")
	if tools == "" {
		// Read-only defaults: Claude can read attachments and explore, but
		// won't silently edit files or run shell commands. Add Bash,Edit,Write
		// (or set CLAUDE_TUI_TOOLS) to enable agentic actions without prompts.
		tools = "Read,Grep,Glob"
	}

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

	m := model{
		engine: &Engine{
			BinPath:      bin,
			Model:        os.Getenv("CLAUDE_TUI_MODEL"),
			AllowedTools: tools,
		},
		ta: ta,
		sp: sp,
		fp: fp,
	}
	return m, nil
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
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
	m.fp.Height = vpH - 1

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

// renderAssistantLive shows in-flight text without glamour (avoids reflow flicker).
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
	// File picker overlay grabs input while open.
	if m.picker {
		var cmd tea.Cmd
		m.fp, cmd = m.fp.Update(msg)
		if ok, path := m.fp.DidSelectFile(msg); ok {
			m.attachments = append(m.attachments, path)
			m.picker = false
			m.ta.Focus()
			return m, textarea.Blink
		}
		if k, isKey := msg.(tea.KeyMsg); isKey && (k.Type == tea.KeyEsc || k.String() == "ctrl+c") {
			m.picker = false
			m.ta.Focus()
			return m, textarea.Blink
		}
		return m, cmd
	}

	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.rerenderAll()
		m.refreshViewport()
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "esc":
			if m.streaming && m.cancel != nil {
				m.cancel()
				m.status = "отменено"
			}
			return m, nil

		case "ctrl+o":
			if !m.streaming {
				m.picker = true
				m.ta.Blur()
				return m, m.fp.Init()
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
			if ev.SessionID != "" {
				m.sessionShort = shortID(ev.SessionID)
			}
			if ev.Model != "" {
				m.modelName = ev.Model
			}
			m.status = "генерация…"
		case EvText:
			m.streamBuf += ev.Text
			m.refreshViewport()
		case EvToolStart:
			m.status = "🔧 " + ev.Tool
		case EvRetry:
			m.status = fmt.Sprintf("повтор запроса (#%d)…", ev.Attempt)
		case EvResult:
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
			}
			m.streamBuf = ""
			if m.status != "отменено" {
				m.status = ""
			}
			m.refreshViewport()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		if m.streaming {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	// Scrolling keys go to the viewport; everything else to the textarea.
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
	ch := m.engine.Send(ctx, prompt)
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
	switch fields[0] {
	case "/quit", "/q", "/exit":
		return true, tea.Quit
	case "/clear":
		m.messages = nil
		m.engine.ResetSession()
		m.sessionShort = ""
		m.costUSD = 0
		m.commitSystem("новая сессия")
		return true, nil
	case "/attach", "/a":
		if len(fields) >= 2 {
			p := expandPath(strings.TrimSpace(strings.TrimPrefix(val, fields[0])))
			if _, err := os.Stat(p); err == nil {
				m.attachments = append(m.attachments, p)
			} else {
				m.commitSystem("файл не найден: " + p)
			}
			return true, nil
		}
		m.picker = true
		m.ta.Blur()
		return true, m.fp.Init()
	case "/files":
		if len(m.attachments) == 0 {
			m.commitSystem("вложений нет")
		} else {
			m.commitSystem("вложения: " + strings.Join(m.attachments, ", "))
		}
		return true, nil
	case "/help":
		m.commitSystem(helpText)
		return true, nil
	}
	return false, nil
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

func attachmentLine(paths []string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = "📎 " + filepath.Base(p)
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

func (m model) View() string {
	if !m.ready {
		return "загрузка…"
	}
	if m.picker {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.headerView(),
			pickerTitleStyle.Render(" 📎 Выбери файл/картинку  ·  Enter — выбрать  ·  Esc — отмена"),
			m.fp.View(),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		m.vp.View(),
		m.attachmentsView(),
		m.inputView(),
		m.footerView(),
	)
}

func (m model) headerView() string {
	left := titleStyle.Render("⚡ Claude TUI")
	var meta []string
	if m.modelName != "" {
		meta = append(meta, m.modelName)
	}
	if m.sessionShort != "" {
		meta = append(meta, "sess:"+m.sessionShort)
	}
	if m.costUSD > 0 {
		meta = append(meta, fmt.Sprintf("$%.4f", m.costUSD))
	}
	right := metaStyle.Render(strings.Join(meta, "  ·  "))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right
}

func (m model) attachmentsView() string {
	if len(m.attachments) == 0 {
		return hintStyle.Render(" нет вложений")
	}
	chips := make([]string, len(m.attachments))
	for i, a := range m.attachments {
		chips[i] = chipStyle.Render("📎 " + filepath.Base(a))
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
		status = hintStyle.Render("Enter — отправить · Ctrl+O — файл · /help — команды · Ctrl+C — выход")
	}
	return " " + status
}
