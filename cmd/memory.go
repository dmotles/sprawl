// cmd/memory.go — `sprawl memory` command group (QUM-514).
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

// regenerateTimelineDeps holds the dependencies for `sprawl memory
// regenerate-timeline`, enabling testability without a real claude binary.
type regenerateTimelineDeps struct {
	Getenv  func(string) string
	Stdout  io.Writer
	Invoker memory.ClaudeInvoker
	Run     func(ctx context.Context, opts memory.RegenerateOptions) error
}

var defaultRegenerateTimelineDeps *regenerateTimelineDeps

var (
	regenerateOutPath string
	regenerateDryRun  bool
	regenerateForce   bool
	regenerateModel   string
	regenerateTimeout time.Duration
)

func init() {
	regenerateTimelineCmd.Flags().StringVar(&regenerateOutPath, "out", "", "Output path (default <sprawl-root>/.sprawl/memory/timeline.md.next)")
	regenerateTimelineCmd.Flags().BoolVar(&regenerateDryRun, "dry-run", false, "Print rendered timeline to stdout; do not write a file")
	regenerateTimelineCmd.Flags().BoolVar(&regenerateForce, "force", false, "Overwrite the output path if it already exists")
	regenerateTimelineCmd.Flags().StringVar(&regenerateModel, "model", "haiku", "Claude model override (default: haiku)")
	regenerateTimelineCmd.Flags().DurationVar(&regenerateTimeout, "timeout", memory.DefaultInvokeTimeout, "Per-session LLM invocation timeout")

	memoryCmd.AddCommand(regenerateTimelineCmd)
	rootCmd.AddCommand(memoryCmd)
}

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Memory subsystem operations",
	Long:  "Tools for inspecting and rebuilding the session-memory pipeline (timeline, persistent knowledge, etc.).",
}

var regenerateTimelineCmd = &cobra.Command{
	Use:   "regenerate-timeline",
	Short: "Regenerate timeline.md from session summaries (non-destructive)",
	Long: `Read every session summary under .sprawl/memory/sessions/ and emit a
re-shaped timeline.md to a separate output path.

Non-destructive by default: writes to .sprawl/memory/timeline.md.next so the
output can be reviewed against the live timeline.md before any cutover.`,
	RunE: func(c *cobra.Command, _ []string) error {
		deps := resolveRegenerateTimelineDeps()
		root := deps.Getenv("SPRAWL_ROOT")
		if root == "" {
			return fmt.Errorf("SPRAWL_ROOT environment variable is not set; run from within a sprawl project or set SPRAWL_ROOT")
		}
		out := regenerateOutPath
		if out == "" {
			out = filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
		}

		opts := memory.RegenerateOptions{
			SprawlRoot: root,
			OutPath:    out,
			DryRun:     regenerateDryRun,
			Force:      regenerateForce,
			Stdout:     deps.Stdout,
			Invoker:    deps.Invoker,
			Cfg: memory.RegenerateConfig{
				Model:         regenerateModel,
				InvokeTimeout: regenerateTimeout,
			},
		}

		if err := deps.Run(context.Background(), opts); err != nil {
			return err
		}

		// Next-action hint to stderr (per /cli-ux-best-practices).
		stderr := c.ErrOrStderr()
		if regenerateDryRun {
			fmt.Fprintf(stderr, "Dry-run complete. Re-run without --dry-run (and pass --out / --force as needed) to write a file.\n")
		} else {
			liveTimeline := filepath.Join(root, ".sprawl", "memory", "timeline.md")
			fmt.Fprintf(stderr, "Wrote regenerated timeline to %s. Inspect: diff %s %s\n", out, liveTimeline, out)
		}
		return nil
	},
}

func resolveRegenerateTimelineDeps() *regenerateTimelineDeps {
	if defaultRegenerateTimelineDeps != nil {
		return defaultRegenerateTimelineDeps
	}
	return &regenerateTimelineDeps{
		Getenv:  os.Getenv,
		Stdout:  os.Stdout,
		Invoker: memory.NewCLIInvoker(),
		Run:     memory.RegenerateTimeline,
	}
}
