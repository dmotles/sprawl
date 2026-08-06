package config

import "sort"

// suggestMaxDistance is the largest edit distance at which a did-you-mean is
// offered. Beyond it the reference table alone is more useful than a wrong
// guess.
const suggestMaxDistance = 2

// suggest returns the recognized key nearest to unknown, or "" when nothing is
// close enough to be a plausible typo.
//
// The policy, stated once so it does not have to be inferred from test cases:
// a suggestion is offered when the edit distance is at most suggestMaxDistance
// AND strictly less than len(unknown). The second condition is what stops a
// very short key matching almost everything.
func suggest(unknown string) string {
	best := suggestFrom(unknown, KnownKeys())
	if best == "" {
		return ""
	}
	d := levenshtein(unknown, best)
	if d > suggestMaxDistance || d >= len(unknown) {
		return ""
	}
	return best
}

// suggestFrom returns the candidate closest to unknown by edit distance,
// breaking ties by lexicographic order so the rendered error text is
// deterministic (Go map iteration is not).
//
// It applies NO distance threshold — that is suggest()'s job. The split is
// deliberate: suggestFrom answers "which candidate is nearest" and is testable
// against hand-made ties at any distance, while suggest answers "is anything
// near enough to be worth showing".
func suggestFrom(unknown string, candidates []string) string {
	sorted := make([]string, len(candidates))
	copy(sorted, candidates)
	sort.Strings(sorted)

	best, bestDist := "", -1
	for _, c := range sorted {
		d := levenshtein(unknown, c)
		if bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// levenshtein returns the edit distance between a and b, computed with a single
// rolling row.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
