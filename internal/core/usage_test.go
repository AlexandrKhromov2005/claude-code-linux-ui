package core

import "testing"

func TestTokenUsageTotals(t *testing.T) {
	u := TokenUsage{Input: 100, CacheRead: 900, CacheCreate: 50, Output: 200}
	if got := u.TotalIn(); got != 1050 {
		t.Errorf("TotalIn = %d, want 1050", got)
	}
	if got := u.Total(); got != 1250 {
		t.Errorf("Total = %d, want 1250", got)
	}
	if u.IsZero() {
		t.Error("IsZero on a populated usage")
	}
	if !(TokenUsage{}).IsZero() {
		t.Error("IsZero false on an empty usage")
	}
}

func TestTokenUsageAdd(t *testing.T) {
	u := TokenUsage{Input: 1, CacheRead: 2, CacheCreate: 3, Output: 4}
	u.Add(TokenUsage{Input: 10, CacheRead: 20, CacheCreate: 30, Output: 40})
	want := TokenUsage{Input: 11, CacheRead: 22, CacheCreate: 33, Output: 44}
	if u != want {
		t.Fatalf("Add = %+v, want %+v", u, want)
	}
}

func TestCacheHitPct(t *testing.T) {
	tests := []struct {
		name string
		u    TokenUsage
		want int
	}{
		// The measured shape of a settled thread: nearly all input read back.
		{"settled thread", TokenUsage{CacheRead: 32608, CacheCreate: 21}, 99},
		// The measured shape of a thread still warming up, where a large share of
		// the prompt is being written rather than read.
		{"warming up", TokenUsage{CacheRead: 16834, CacheCreate: 15774}, 51},
		{"nothing recorded", TokenUsage{}, 0},
		{"no cache at all", TokenUsage{Input: 500}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.u.CacheHitPct(); got != tt.want {
				t.Errorf("CacheHitPct = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBilledInputEquivalent(t *testing.T) {
	// 1000 fresh, 1000 read (0.1x), 1000 written (1.25x) = 1000 + 100 + 1250.
	u := TokenUsage{Input: 1000, CacheRead: 1000, CacheCreate: 1000}
	if got, want := u.BilledInputEquivalent(), 235000; got != want {
		t.Errorf("BilledInputEquivalent = %d, want %d", got, want)
	}
	if got, want := u.UncachedInputEquivalent(), 300000; got != want {
		t.Errorf("UncachedInputEquivalent = %d, want %d", got, want)
	}
}

func TestSavedByCachePct(t *testing.T) {
	// A well-cached turn: almost everything read back at a tenth of the price.
	healthy := TokenUsage{CacheRead: 28575, CacheCreate: 670}
	if got := healthy.SavedByCachePct(); got < 80 {
		t.Errorf("healthy turn saved only %d%%, want >= 80%%", got)
	}
	// Writing the cache costs more than plain input, so a turn that only writes
	// saves nothing and must not report a negative saving.
	if got := (TokenUsage{CacheCreate: 1000}).SavedByCachePct(); got != 0 {
		t.Errorf("write-only turn = %d%%, want 0%%", got)
	}
	if got := (TokenUsage{}).SavedByCachePct(); got != 0 {
		t.Errorf("empty usage = %d%%, want 0%%", got)
	}
}

func TestSessionUsageTotal(t *testing.T) {
	s := SessionUsage{
		Turns: TokenUsage{Input: 10, Output: 20},
		Side:  TokenUsage{Input: 1, Output: 2},
	}
	total := s.Total()
	if total.Input != 11 || total.Output != 22 {
		t.Fatalf("Total = %+v, want Input 11 / Output 22", total)
	}
	// Total must not mutate the components it sums.
	if s.Turns.Input != 10 {
		t.Fatalf("Total mutated Turns: %+v", s.Turns)
	}
}
