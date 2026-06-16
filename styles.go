package main

import "github.com/charmbracelet/lipgloss"

// Palette — Claude-ish warm coral accent over muted neutrals.
var (
	colAccent = lipgloss.Color("#D97757")
	colUser   = lipgloss.Color("#82AAFF")
	colText   = lipgloss.Color("#E6E6E6")
	colDim    = lipgloss.Color("#8A8A8A")
	colFaint  = lipgloss.Color("#5C5C5C")
	colErr    = lipgloss.Color("#E5534B")
	colChipBg = lipgloss.Color("#2A2A2A")
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#1A1A1A")).
			Background(colAccent).
			Padding(0, 1)

	metaStyle = lipgloss.NewStyle().Foreground(colDim)

	asstLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	userLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(colUser)

	bodyStyle = lipgloss.NewStyle().Foreground(colText)
	sysStyle  = lipgloss.NewStyle().Italic(true).Foreground(colDim)
	errStyle  = lipgloss.NewStyle().Foreground(colErr)

	statusStyle = lipgloss.NewStyle().Foreground(colDim)
	hintStyle   = lipgloss.NewStyle().Foreground(colFaint)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent)

	chipStyle = lipgloss.NewStyle().
			Foreground(colAccent).
			Background(colChipBg).
			Padding(0, 1)

	pickerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
)
