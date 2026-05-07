// cmd/memory_append.go — `sprawl memory append-session` (QUM-515).
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
	"github.com/spf13/cobra"
)

// appendSessionDeps holds dependencies for `sprawl memory append-session`,
// enabling testability without a real claude binary or filesystem.
type appendSessionDeps struct {
	Getenv  func(string) string
	Stdout  io.Writer
	Invoker memory.ClaudeInvoker
	Run     func(ctx context.Context, opts memory.AppendOptions) (memory.AppendResult, error)
}

var defaultAppendSessionDeps *appendSessionDeps

var (
	appendSessionDryRun  bool
	appendSessionModel   string
	appendSessionTimeout time.Duration
	appendSessionLockTO  time.Duration
)

func init() {
	appendSessionCmd.Flags().BoolVar(&appendSessionDryRun, "dry-run", false, "Print candidate row to stdout; do not modify timeline.md")
	appendSessionCmd.Flags().StringVar(&appendSessionModel, "model", "haiku", "Claude model override (default: haiku)")
	appendSessionCmd.Flags().DurationVar(&appendSessionTimeout, "timeout", memory.DefaultInvokeTimeout, "Per-session LLM invocation timeout")
	appendSessionCmd.Flags().DurationVar(&appendSessionLockTO, "lock-timeout", memory.DefaultAppendLockTimeout, "Maximum time to wait for timeline.md flock")

	memoryCmd.AddCommand(appendSessionCmd)
}

var appendSessionCmd = &cobra.Command{
	Use:    "append-session <session-id>",
	Short:  "Append one session summary to timeline.md (hidden dev tool)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		deps := resolveAppendSessionDeps()
		root := deps.Getenv("SPRAWL_ROOT")
		if root == "" {
			return fmt.Errorf("SPRAWL_ROOT environment variable is not set; run from within a sprawl project or set SPRAWL_ROOT")
		}

		opts := memory.AppendOptions{
			SprawlRoot: root,
			SessionID:  args[0],
			DryRun:     appendSessionDryRun,
			Stdout:     deps.Stdout,
			Invoker:    deps.Invoker,
			Cfg: memory.RegenerateConfig{
				Model:         appendSessionModel,
				InvokeTimeout: appendSessionTimeout,
			},
			LockTimeout: appendSessionLockTO,
		}

		res, err := deps.Run(context.Background(), opts)
		if err != nil {
			return err
		}

		stderr := c.ErrOrStderr()
		switch {
		case appendSessionDryRun:
			fmt.Fprintf(stderr, "Dry-run complete. Re-run without --dry-run to write the row to timeline.md.\n")
		case res.NoOp:
			fmt.Fprintf(stderr, "Session %s is already present in timeline.md (no-op).\n", opts.SessionID)
		default:
			liveTimeline := filepath.Join(root, ".sprawl", "memory", "timeline.md")
			fmt.Fprintf(stderr, "Appended session %s to %s. Inspect: git diff %s\n", opts.SessionID, liveTimeline, liveTimeline)
		}
		return nil
	},
}

func resolveAppendSessionDeps() *appendSessionDeps {
	if defaultAppendSessionDeps != nil {
		return defaultAppendSessionDeps
	}
	return &appendSessionDeps{
		Getenv:  os.Getenv,
		Stdout:  os.Stdout,
		Invoker: memory.NewCLIInvoker(),
		Run:     memory.AppendSessionWithOptions,
	}
}
