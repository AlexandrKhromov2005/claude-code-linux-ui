package core

// TokenUsage is the token accounting for one or more turns. The four counters
// are kept apart because they are priced very differently: a cache read costs a
// fraction of a fresh input token, while writing the cache costs more than one.
// Collapsing them into a single "input" number would hide exactly the effect
// this app tries to optimise.
type TokenUsage struct {
	Input       int `json:"input"`       // fresh input tokens
	CacheRead   int `json:"cacheRead"`   // served from the prompt cache
	CacheCreate int `json:"cacheCreate"` // written into the prompt cache
	Output      int `json:"output"`      // generated tokens
}

// Add accumulates another turn's usage.
func (u *TokenUsage) Add(o TokenUsage) {
	u.Input += o.Input
	u.CacheRead += o.CacheRead
	u.CacheCreate += o.CacheCreate
	u.Output += o.Output
}

// TotalIn returns every input-side token, however it was priced.
func (u TokenUsage) TotalIn() int { return u.Input + u.CacheRead + u.CacheCreate }

// Total returns all tokens the turn touched.
func (u TokenUsage) Total() int { return u.TotalIn() + u.Output }

// IsZero reports whether nothing has been recorded yet.
func (u TokenUsage) IsZero() bool { return u.Total() == 0 }

// CacheHitPct is the share of input-side tokens served from cache, 0..100. It is
// the single most useful number for spotting a broken cache: a resumed thread
// that is caching properly sits well above 90%, and a thread whose prefix is
// being invalidated every turn drops toward the 50s.
func (u TokenUsage) CacheHitPct() int {
	in := u.TotalIn()
	if in == 0 {
		return 0
	}
	return u.CacheRead * 100 / in
}

// BilledInputEquivalent expresses input-side usage in units of one fresh input
// token, applying Anthropic's standard cache multipliers: a cache read costs
// 0.1x and a 5-minute cache write 1.25x. It lets the UI compare "what this turn
// cost" against "what it would have cost with no caching at all" without needing
// per-model prices, which differ per model and change over time.
//
// It is returned in hundredths so the arithmetic stays in integers.
func (u TokenUsage) BilledInputEquivalent() int {
	return u.Input*100 + u.CacheRead*10 + u.CacheCreate*125
}

// UncachedInputEquivalent is what the same input would have cost with every
// token billed as fresh input, in the same hundredths unit.
func (u TokenUsage) UncachedInputEquivalent() int { return u.TotalIn() * 100 }

// SavedByCachePct reports how much of the potential input bill prompt caching
// actually avoided, 0..100. It is 0 when caching saved nothing.
func (u TokenUsage) SavedByCachePct() int {
	full := u.UncachedInputEquivalent()
	if full == 0 {
		return 0
	}
	saved := full - u.BilledInputEquivalent()
	if saved <= 0 {
		return 0
	}
	return saved * 100 / full
}
