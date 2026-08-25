package usage

import "testing"

// TestCacheHitPct pins the three conventions that must agree across every
// surface rendering a cache hit rate (QUM-1258): the em-dash sentinel, the one
// decimal place, and a denominator that excludes output tokens. Two
// presentation layers rendered this figure from byte-identical private copies;
// one owner means the conventions cannot drift apart.
func TestCacheHitPct(t *testing.T) {
	cases := []struct {
		name string
		t    TokenTotals
		want string
	}{
		{"no tokens at all", TokenTotals{}, "—"},
		{
			"output tokens only — nothing was read from cache, and no denominator to divide by",
			TokenTotals{OutputTokens: 500},
			"—",
		},
		{
			"output tokens are excluded from the denominator: 500/(500+500) would be 50.0%",
			TokenTotals{InputTokens: 500, OutputTokens: 100_000, CacheReadInputTokens: 500},
			"50.0%",
		},
		{"entirely cache-cold", TokenTotals{InputTokens: 1000}, "0.0%"},
		{"entirely cache-hot", TokenTotals{CacheReadInputTokens: 1000}, "100.0%"},
		{
			"cache creation counts toward the denominator, not the numerator",
			TokenTotals{InputTokens: 100, CacheReadInputTokens: 700, CacheCreationInputTokens: 200},
			"70.0%",
		},
		{
			"one decimal, rounded: 2100000/2422000",
			TokenTotals{InputTokens: 298_000, CacheReadInputTokens: 2_100_000, CacheCreationInputTokens: 24_000},
			"86.7%",
		},
	}
	for _, tc := range cases {
		if got := CacheHitPct(tc.t); got != tc.want {
			t.Errorf("%s: CacheHitPct(%+v) = %q, want %q", tc.name, tc.t, got, tc.want)
		}
	}
}
