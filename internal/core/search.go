package core

import (
	"strings"
	"time"
	"unicode/utf8"
)

// SearchHit is one transcript match within a project.
type SearchHit struct {
	ThreadID string
	Title    string
	Snippet  string
	Updated  time.Time
}

// Search scans the current project's transcripts for q (case-insensitive),
// matching titles and message bodies.
func (a *App) Search(q string) ([]SearchHit, error) {
	a.mu.Lock()
	slug := ""
	if a.project != nil {
		slug = a.project.Slug()
	}
	a.mu.Unlock()
	if slug == "" {
		return nil, ErrNoProject
	}
	threads, err := a.store.ListThreads(slug)
	if err != nil {
		return nil, err
	}
	ql := strings.ToLower(q)
	var hits []SearchHit
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
		hits = append(hits, SearchHit{ThreadID: t.ID, Title: t.Title, Snippet: snippet, Updated: t.Updated})
	}
	return hits, nil
}

// makeSnippet returns a one-line excerpt around a match, snapped to rune
// boundaries so multibyte text is not split.
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
