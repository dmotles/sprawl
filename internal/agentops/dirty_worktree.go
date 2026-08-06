// Package agentops: dirty_worktree.go classifies `git status --porcelain`
// output so a merge precondition failure can distinguish an agent's own
// uncommitted edits from the wreckage of a previous failed merge (QUM-1100).
package agentops

import "strings"

// StagedOnlyPorcelain reports whether every TRACKED entry in `git status
// --porcelain` output is staged with no worktree modification: an XY pair
// with Y == ' ' and X one of "MADRCT". Unmerged ("U…") and any Y != ' '
// disqualify. Untracked ("??") and ignored ("!!") entries are IGNORED rather
// than disqualifying. Input with no staged entry is false — there is nothing
// to explain.
//
// That is the exact shape a merge that died between `git reset --soft
// <mergeBase>` and its squash commit leaves behind: the index is untouched
// and only HEAD moved, so every path reads as staged and nothing reads as
// modified-in-worktree.
//
// It is a SHAPE test, not proof of cause: an agent that ran `git add` without
// committing produces the same output. The message this selects therefore
// names both possibilities and prescribes a non-destructive check rather than
// asserting a cause. Being wrong in the safe direction costs a sentence;
// being wrong in the other direction is what the 2026-08-06 incident nearly
// did with 3026 lines.
func StagedOnlyPorcelain(status string) bool {
	lines := strings.Split(strings.TrimRight(status, "\n"), "\n")
	saw := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		if len(line) < 3 {
			continue // not porcelain XY; says nothing either way
		}
		x, y := line[0], line[1]
		// Untracked and ignored entries say NOTHING about whether the index
		// is orphaned, and agent worktrees routinely carry strays (QUM-989).
		// Disqualifying on one would silently restore the dangerous message
		// in exactly the case this classifier exists for.
		if x == '?' || x == '!' {
			continue
		}
		// T is a typechange (e.g. file -> symlink) and appears in a squash's
		// staged set like any other kind; omitting it was a bug found in
		// review.
		if y != ' ' || !strings.ContainsRune("MADRCT", rune(x)) {
			return false
		}
		saw = true
	}
	return saw
}
