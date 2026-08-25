package usage

import "fmt"

// CacheHitPct renders the share of input-side tokens served from the prompt
// cache, as "98.7%", or "—" when there are no input-side tokens to take a share
// of — "0.0%" there would claim a cache that was never consulted.
//
// Summed across turns, cache_read_input_tokens is roughly (context size × turn
// count) — a proxy for turn count wearing the units of context size, which is
// why the raw figure read as an implausible context beside INPUT (QUM-1258).
// The rate is bounded 0-100 by construction, so it cannot be misread that way.
// Output tokens are excluded: they are not served from the input cache.
//
// This lives here, beside TokenTotals, rather than in each presentation layer:
// the sentinel, the precision, and the denominator's exclusion of output tokens
// are three conventions that must agree across every surface that renders the
// rate, and a duplicated body is where they drift.
func CacheHitPct(t TokenTotals) string {
	denom := t.InputTokens + t.CacheReadInputTokens + t.CacheCreationInputTokens
	if denom <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(t.CacheReadInputTokens)/float64(denom))
}
