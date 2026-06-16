package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	m, err := newModel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка инициализации:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	// Route gated tool approvals through the running program so the modal shows
	// in this UI.
	if m.perm != nil {
		m.perm.SetUIDecider(p.Send)
	}
	_, runErr := p.Run()
	if m.perm != nil {
		m.perm.Stop()
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", runErr)
		os.Exit(1)
	}
}
