// Package githooks contains the regression coverage for sprawl's git hook
// guards. The guard logic itself lives in shell scripts under scripts/ (so it
// is exec'd directly by git's hook machinery); this package execs those real
// scripts against throwaway repos to prove their behavior.
//
// See scripts/guard-main-commit (QUM-808): it refuses a commit on branch "main"
// by a non-root agent (identity via $SPRAWL_AGENT_IDENTITY).
package githooks

import "path/filepath"

// GuardMainCommitScript returns the path to the guard-main-commit script given
// the repository root.
func GuardMainCommitScript(repoRoot string) string {
	return filepath.Join(repoRoot, "scripts", "guard-main-commit")
}

// PreCommitScript returns the path to the pre-commit hook script given the
// repository root.
func PreCommitScript(repoRoot string) string {
	return filepath.Join(repoRoot, "scripts", "pre-commit")
}
