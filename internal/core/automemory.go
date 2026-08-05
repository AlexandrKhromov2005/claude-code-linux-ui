package core

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Exchange is one user/assistant round trip offered to the project's memory.
type Exchange struct {
	User      string
	Assistant string
}

// memoryQueue serialises cross-thread memory upkeep for a single project and
// coalesces whatever arrives while an update is already running.
//
// Two things make this necessary. Memory updates rewrite one shared file, so two
// of them in flight at once means last-writer-wins and a silently dropped
// exchange — easy to hit, because turns in different threads of the same project
// run concurrently by design. And an update is a model call: starting a second
// one while the first is still going costs twice for a strictly worse result,
// since the second cannot see what the first is about to write.
//
// So at most one update runs per project, and everything that lands meanwhile is
// folded into the next one rather than being dropped or racing.
type memoryQueue struct {
	mu      sync.Mutex
	pending []Exchange
	running bool
}

// Push queues an exchange. It returns true when the caller takes ownership of
// the update loop, meaning no other goroutine is running it. Exactly one caller
// ever gets true until that owner finishes draining.
func (q *memoryQueue) Push(ex Exchange) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, ex)
	if q.running {
		return false
	}
	q.running = true
	return true
}

// Take hands the owner everything queued so far. When nothing is left it clears
// the running flag and returns false, releasing ownership under the same lock
// that Push takes — so an exchange arriving at that moment either joins this
// batch or starts a fresh loop, and can never fall between the two.
func (q *memoryQueue) Take() ([]Exchange, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		q.running = false
		return nil, false
	}
	batch := q.pending
	q.pending = nil
	return batch, true
}

// minMemorableRunes is the length below which an exchange is treated as small
// talk. Short confirmations ("ок", "спасибо", "да, давай") carry no durable fact
// worth a model call, and folding them in only dilutes the memory.
const minMemorableRunes = 40

// worthRemembering reports whether an exchange could plausibly contain something
// durable. It is deliberately generous: the summariser is the real filter, and
// this only skips the cases where there is obviously nothing to extract.
func worthRemembering(userText, assistantText string) bool {
	u := strings.TrimSpace(userText)
	a := strings.TrimSpace(assistantText)
	if a == "" {
		return false
	}
	// A substantial answer is worth reading even when the question was terse
	// ("продолжай" against a long design explanation), and vice versa.
	return utf8.RuneCountInString(u)+utf8.RuneCountInString(a) >= minMemorableRunes
}

// renderExchanges formats a batch for the summariser, clipping each side so one
// enormous turn cannot dominate the request. The marked framing matters: the
// text comes from a conversation and is handed to a model, so it is labelled as
// data throughout and the summariser is told not to act on it.
func renderExchanges(batch []Exchange) string {
	clip := func(s string) string {
		s = strings.TrimSpace(s)
		if r := []rune(s); len(r) > 2000 {
			return string(r[:2000]) + "…"
		}
		return s
	}
	var sb strings.Builder
	for i, ex := range batch {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Пользователь: ")
		sb.WriteString(clip(ex.User))
		sb.WriteString("\nАссистент: ")
		sb.WriteString(clip(ex.Assistant))
	}
	return sb.String()
}
