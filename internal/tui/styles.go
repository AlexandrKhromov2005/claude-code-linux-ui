package tui

import "github.com/charmbracelet/lipgloss"

// theme is a named color palette. Styles are rebuilt from the active theme at
// startup and whenever the user runs /theme.
type theme struct {
	accent, user, text, dim, faint, err, chipBg, add, warn lipgloss.Color
}

var themes = map[string]theme{
	// Claude-ish warm coral accent over muted neutrals (default).
	"dark": {
		accent: "#D97757", user: "#82AAFF", text: "#E6E6E6", dim: "#8A8A8A",
		faint: "#5C5C5C", err: "#E5534B", chipBg: "#2A2A2A", add: "#7FB069", warn: "#E5C07B",
	},
	"light": {
		accent: "#C2410C", user: "#2563EB", text: "#1A1A1A", dim: "#6B7280",
		faint: "#9CA3AF", err: "#DC2626", chipBg: "#E5E7EB", add: "#15803D", warn: "#B45309",
	},
	"nord": {
		accent: "#88C0D0", user: "#81A1C1", text: "#ECEFF4", dim: "#7B88A1",
		faint: "#4C566A", err: "#BF616A", chipBg: "#3B4252", add: "#A3BE8C", warn: "#EBCB8B",
	},
}

func themeNames() []string { return []string{"dark", "light", "nord"} }

// Active palette colors, set by applyTheme.
var (
	colAccent lipgloss.Color
	colUser   lipgloss.Color
	colText   lipgloss.Color
	colDim    lipgloss.Color
	colFaint  lipgloss.Color
	colErr    lipgloss.Color
	colChipBg lipgloss.Color
	colAdd    lipgloss.Color
	colWarn   lipgloss.Color
)

// Style vars, rebuilt by buildStyles from the active palette.
var (
	titleStyle lipgloss.Style
	metaStyle  lipgloss.Style

	asstLabelStyle lipgloss.Style
	userLabelStyle lipgloss.Style

	bodyStyle lipgloss.Style
	sysStyle  lipgloss.Style
	errStyle  lipgloss.Style

	statusStyle lipgloss.Style
	hintStyle   lipgloss.Style

	inputBoxStyle lipgloss.Style
	chipStyle     lipgloss.Style

	pickerTitleStyle lipgloss.Style

	overlayTitleStyle lipgloss.Style
	selCursorStyle    lipgloss.Style
	selSubSelStyle    lipgloss.Style
	selTitleStyle     lipgloss.Style
	selSubStyle       lipgloss.Style

	modeChatStyle  lipgloss.Style
	modeAgentStyle lipgloss.Style

	diffAddStyle     lipgloss.Style
	diffDelStyle     lipgloss.Style
	cmdStyle         lipgloss.Style
	previewPathStyle lipgloss.Style
	previewMetaStyle lipgloss.Style

	approveBoxStyle   lipgloss.Style
	approveTitleStyle lipgloss.Style
	ruleInputStyle    lipgloss.Style
	warnStyle         lipgloss.Style
)

// applyTheme switches the active palette and rebuilds every style. Unknown names
// fall back to "dark".
func applyTheme(name string) {
	th, ok := themes[name]
	if !ok {
		th = themes["dark"]
	}
	colAccent, colUser, colText = th.accent, th.user, th.text
	colDim, colFaint, colErr = th.dim, th.faint, th.err
	colChipBg, colAdd, colWarn = th.chipBg, th.add, th.warn
	buildStyles()
}

func buildStyles() {
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1A1A1A")).Background(colAccent).Padding(0, 1)
	metaStyle = lipgloss.NewStyle().Foreground(colDim)

	asstLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	userLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(colUser)

	bodyStyle = lipgloss.NewStyle().Foreground(colText)
	sysStyle = lipgloss.NewStyle().Italic(true).Foreground(colDim)
	errStyle = lipgloss.NewStyle().Foreground(colErr)

	statusStyle = lipgloss.NewStyle().Foreground(colDim)
	hintStyle = lipgloss.NewStyle().Foreground(colFaint)

	inputBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent)
	chipStyle = lipgloss.NewStyle().Foreground(colAccent).Background(colChipBg).Padding(0, 1)

	pickerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	overlayTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent).Padding(0, 1)
	selCursorStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	selSubSelStyle = lipgloss.NewStyle().Foreground(colText)
	selTitleStyle = lipgloss.NewStyle().Foreground(colText)
	selSubStyle = lipgloss.NewStyle().Foreground(colDim)

	modeChatStyle = lipgloss.NewStyle().Foreground(colUser)
	modeAgentStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	diffAddStyle = lipgloss.NewStyle().Foreground(colAdd)
	diffDelStyle = lipgloss.NewStyle().Foreground(colErr)
	cmdStyle = lipgloss.NewStyle().Foreground(colText)
	previewPathStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	previewMetaStyle = lipgloss.NewStyle().Italic(true).Foreground(colDim)

	approveBoxStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colWarn).Padding(0, 1)
	approveTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colWarn)
	ruleInputStyle = lipgloss.NewStyle().Foreground(colAccent)
	warnStyle = lipgloss.NewStyle().Foreground(colWarn)
}

func init() { applyTheme("dark") }
