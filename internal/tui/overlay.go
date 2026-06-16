package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// selItem is one row in a selectable overlay list.
type selItem struct {
	id       string // slug / thread id (empty for synthetic actions)
	title    string
	subtitle string
	action   string // non-empty marks a synthetic action row (e.g. "use-cwd")
}

// selList is a minimal keyboard-driven list used by the project switcher and
// the thread browser. It keeps the selected row visible within a window.
type selList struct {
	title  string
	hint   string
	items  []selItem
	cursor int
	top    int // index of the first visible row
}

func (l *selList) move(d int) {
	if len(l.items) == 0 {
		return
	}
	l.cursor += d
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= len(l.items) {
		l.cursor = len(l.items) - 1
	}
}

func (l *selList) selected() (selItem, bool) {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		return selItem{}, false
	}
	return l.items[l.cursor], true
}

// view renders the list within the given box, scrolling to keep the cursor in
// view. Each row takes two lines (title + subtitle).
func (l *selList) view(width, height int) string {
	rows := height - 2 // leave room for title and hint
	if rows < 1 {
		rows = 1
	}
	perItem := 2
	window := rows / perItem
	if window < 1 {
		window = 1
	}
	if l.cursor < l.top {
		l.top = l.cursor
	}
	if l.cursor >= l.top+window {
		l.top = l.cursor - window + 1
	}

	var b strings.Builder
	b.WriteString(overlayTitleStyle.Render(l.title))
	b.WriteByte('\n')

	if len(l.items) == 0 {
		b.WriteString(hintStyle.Render("  пусто"))
		b.WriteByte('\n')
	}
	end := l.top + window
	if end > len(l.items) {
		end = len(l.items)
	}
	for i := l.top; i < end; i++ {
		it := l.items[i]
		title := it.title
		sub := it.subtitle
		if i == l.cursor {
			b.WriteString(selCursorStyle.Render("▌ " + title))
			b.WriteByte('\n')
			if sub != "" {
				b.WriteString(selSubSelStyle.Render("  " + sub))
				b.WriteByte('\n')
			}
		} else {
			b.WriteString(selTitleStyle.Render("  " + title))
			b.WriteByte('\n')
			if sub != "" {
				b.WriteString(selSubStyle.Render("  " + sub))
				b.WriteByte('\n')
			}
		}
	}
	hint := l.hint
	if hint == "" {
		hint = "↑↓ — выбрать · Enter — открыть · Esc — закрыть"
	}
	b.WriteString(hintStyle.Render(hint))
	return lipgloss.NewStyle().Width(width).Render(strings.TrimRight(b.String(), "\n"))
}
