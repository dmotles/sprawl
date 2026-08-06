package config

import "testing"

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"validate", "validate", 0},
		{"vlaidate", "validate", 2},  // transposition = 2 substitutions
		{"validat", "validate", 1},   // deletion
		{"validatee", "validate", 1}, // insertion
		{"", "validate", 8},          // all insertions
		{"validate", "", 8},          // all deletions
		{"hub.url", "hub_url", 1},    // the dot-vs-underscore mistake
		{"worktree_setup", "worktree.setup", 1},
		{"kitten", "sitting", 3}, // the canonical case
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		// Symmetry, pinned against the same expected value so the check is not
		// satisfiable by any two equal-but-wrong results.
		if got := levenshtein(c.b, c.a); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d (must be symmetric)", c.b, c.a, got, c.want)
		}
	}
}

// TestSuggest states the suggestion POLICY, not just a few outcomes, so the
// implementation does not have to reverse-engineer a threshold from examples:
//
//	suggest returns the closest recognized key when the edit distance is at
//	most suggestMaxDistance AND strictly less than len(unknown) -- the second
//	condition is what stops a very short key matching everything. Ties are
//	broken by sorted key order so the error text is deterministic.
func TestSuggest(t *testing.T) {
	cases := []struct {
		unknown string
		want    string // "" = no suggestion
	}{
		{"vlaidate", "validate"},
		{"hub.url", "hub_url"},
		{"worktree_setup", "worktree.setup"},
		{"memory_mdoel", "memory_model"},
		{"a-completely-unrelated-key-name", ""}, // far beyond the threshold
		{"x", ""},                               // distance >= len(unknown): too short to be a plausible typo
		{"validate", "validate"},                // distance 0
	}
	for _, c := range cases {
		if got := suggest(c.unknown); got != c.want {
			t.Errorf("suggest(%q) = %q, want %q", c.unknown, got, c.want)
		}
	}
}

// TestSuggest_TieIsBrokenDeterministically: the error text's stability (and the
// shape-identity assertion in TestLoad_UnknownShapesAreIdentical) depends on
// suggest never varying across calls. The failure mode that matters is a
// genuine TIE — Go map iteration is randomised, so an implementation that
// ranges over a map and keeps the first best answer is nondeterministic exactly
// when two keys are equidistant.
//
// This drives suggestFrom(unknown, candidates), the seam suggest() is built on,
// with a hand-made tie asserted to BE a tie. The lexicographically lower key
// must win, regardless of the order the candidates arrive in.
func TestSuggest_TieIsBrokenDeterministically(t *testing.T) {
	// Two candidates, equidistant from the input, supplied in the "wrong" order.
	tied := []string{"zzz_bbb", "aaa_bbb"}
	const in = "qqq_bbb"
	if a, b := levenshtein(in, tied[0]), levenshtein(in, tied[1]); a != b {
		t.Fatalf("fixture is not a tie: %d vs %d; the test would not exercise tie-breaking", a, b)
	}
	first := suggestFrom(in, tied)
	if first != "aaa_bbb" {
		t.Errorf("suggestFrom must break ties by sorted key order: got %q, want %q", first, "aaa_bbb")
	}
	// Reversing the input order must not change the answer.
	if got := suggestFrom(in, []string{"aaa_bbb", "zzz_bbb"}); got != first {
		t.Errorf("suggestFrom depends on candidate order: %q vs %q", got, first)
	}
	// And repeated calls over the real key set must be stable.
	for _, s := range []string{"hub_xrl", "validate_timeoux", "vlaidate", "zzz_unknown"} {
		want := suggest(s)
		for i := 0; i < 50; i++ {
			if got := suggest(s); got != want {
				t.Fatalf("suggest(%q) is not deterministic: %q then %q", s, want, got)
			}
		}
	}
}

// TestSuggest_ThresholdBoundary pins the threshold constant against behaviour
// so it cannot be widened by accident into suggesting nonsense.
func TestSuggest_ThresholdBoundary(t *testing.T) {
	// "validate" with exactly suggestMaxDistance edits must still match...
	near := "validate"
	for i := 0; i < suggestMaxDistance; i++ {
		near += "z"
	}
	if got := suggest(near); got != "validate" {
		t.Errorf("suggest(%q) = %q, want %q (distance == suggestMaxDistance must match)", near, got, "validate")
	}
	// ...and one edit further must not.
	far := near + "z"
	if got := suggest(far); got == "validate" {
		t.Errorf("suggest(%q) = %q, want no suggestion (distance > suggestMaxDistance)", far, got)
	}
}
