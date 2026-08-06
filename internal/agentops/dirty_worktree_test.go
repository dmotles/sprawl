package agentops_test

import (
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
)

// TestStagedOnlyPorcelain — the classifier behind QUM-1100's message split.
//
// "A  k.txt" is the exact shape verified in the live incident and reproduced
// in a scratch repo: after `git reset --soft <mergeBase>` and a hook-rejected
// commit, every path reads as staged (X set, Y blank) because the INDEX is
// untouched and only HEAD moved.
//
// Negative controls: invert the return (fails the true rows and A2/A3);
// `return status != ""` (fails every false row and A3).
func TestStagedOnlyPorcelain(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{"the live incident shape", "A  k.txt", true},
		{"mixed staged kinds", "M  a.go\nA  b.go\nD  c.go", true},
		{"staged rename", "R  old -> new", true},
		{"staged copy", "C  src -> dst", true},
		{"staged typechange (file -> symlink)", "T  typ.txt", true},
		{"quoted path with a space", "A  \"sub/b c.txt\"", true},
		{"trailing newline tolerated", "A  k.txt\n", true},
		{"worktree-modified only", " M a.go", false},
		{"staged and then modified", "MM a.go", false},
		{"untracked", "?? scratch.log", false},
		{"unmerged", "UU a.go", false},
		// Untracked strays say NOTHING about whether the index is orphaned,
		// and agent worktrees routinely carry them (QUM-989). Disqualifying
		// on one would silently restore the dangerous message.
		{"staged plus an untracked stray", "A  a.go\n?? b.log", true},
		{"staged plus an ignored file", "A  a.go\n!! x.tmp", true},
		{"untracked only", "?? b.log", false},
		{"clean", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentops.StagedOnlyPorcelain(tc.status); got != tc.want {
				t.Errorf("StagedOnlyPorcelain(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}
