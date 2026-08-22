package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/store"
)

var rootCmd = &cobra.Command{
	Use:   "sprawl",
	Short: "Tree-governance for AI agents",
	Long:  "Sprawl — a self-organizing AI agent orchestration system built on Claude Code.",
	// Execute() below already prints the error, so letting cobra print it too
	// emitted every error TWICE. Harmless while errors were one line; QUM-1086's
	// config error is a 24-line key reference, so the duplicate was ~48 lines for
	// one typo. Suppressing cobra's copy also puts the error LAST, nearest the
	// prompt, instead of above the usage block.
	//
	// Deliberately NOT also setting SilenceUsage: usage is genuinely useful for a
	// wrong-arguments error, and suppressing it would degrade every command to
	// save noise on one. That leaves a usage block between the header and the
	// error for RunE failures — a wider fix, and a repo-wide output decision
	// rather than something to fold into this issue.
	SilenceErrors: true,
}

func Execute() {
	if code := executeTo(os.Stderr, rootCmd); code != 0 {
		os.Exit(code)
	}
}

// executeTo runs cmd and reports the process exit code, so the sink below is
// reachable from a test without os.Exit.
func executeTo(w io.Writer, cmd *cobra.Command) int {
	if err := cmd.Execute(); err != nil {
		writeExecError(w, err)
		return 1
	}
	return 0
}

// writeExecError renders err with DSN-shaped text removed (QUM-1280).
//
// This is the sink for EVERY error any cobra command returns, which makes it the
// widest credential carrier among the paths that RETURN an error: `store migrate`'s raw pgx
// failure is produced FROM the DSN, and `store dispatch`'s degraded refusal
// wraps a pgx connect error. This repo is public and the real DSN arrives at
// runtime, so the concrete failure mode is an operator pasting a terminal error
// into an issue.
//
// REDACTION AT THE PRINT, NEVER AT THE RETURN. Sanitising where the error is
// produced replaces the value with a string and breaks errors.Is for in-process
// callers — TestStoreDispatch_PropagatesAnOpenFailure depends on that identity.
//
// RedactSecrets is safe to apply to every command's errors, not only the store
// ones: internal/store/redact_test.go carries a negative control proving it
// leaves ordinary text alone, and the multi-line `next:` hints this repo prints
// survive verbatim (pinned in root_test.go).
//
// The nil guard matters: RedactError(nil) is "", so an unconditional Fprintln
// would print a blank line.
//
// THIS IS NOT THE ONLY SINK, and an earlier version of this comment implied it
// was the last one left. A path that SWALLOWS its error and logs it never
// arrives here: internal/memory/handoff_event.go does exactly that through
// slog.Default(). That sink is now redacted at its own print (QUM-1294) — by a
// wrapper installed THERE, not by anything here — and cmd/hubd is a separate
// main with its own print (QUM-1292, still open). Do not read this function as
// coverage of the binary.
func writeExecError(w io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(w, store.RedactError(err))
}
