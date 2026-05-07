// cmd/memory_show.go — hidden inspection subcommands for `sprawl memory`
// (QUM-516 slice 3): `show-context-blob` dumps the rendered system-prompt
// context blob, and `show-arc-summary` emits the LLM-generated project arc.
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

// showContextBlobDeps holds the dependencies for `sprawl memory
// show-context-blob`.
type showContextBlobDeps struct {
	Getenv func(string) string
	Stdout io.Writer
	Build  func(sprawlRoot, rootName string) (string, error)
}

// showArcSummaryDeps holds the dependencies for `sprawl memory
// show-arc-summary`.
type showArcSummaryDeps struct {
	Getenv  func(string) string
	Stdout  io.Writer
	Invoker memory.ClaudeInvoker
	Run     func(ctx context.Context, opts memory.ArcOptions) (string, error)
}

var (
	defaultShowContextBlobDeps *showContextBlobDeps
	defaultShowArcSummaryDeps  *showArcSummaryDeps
)

var (
	showArcTimelinePath string
	showArcModel        string
	showArcTimeout      time.Duration
)

func init() {
	showArcSummaryCmd.Flags().StringVar(&showArcTimelinePath, "timeline", "", "Path to a timeline file (default <sprawl-root>/.sprawl/memory/timeline.md)")
	showArcSummaryCmd.Flags().StringVar(&showArcModel, "model", "haiku", "Claude model override (default: haiku)")
	showArcSummaryCmd.Flags().DurationVar(&showArcTimeout, "timeout", memory.DefaultInvokeTimeout, "LLM invocation timeout")

	memoryCmd.AddCommand(showContextBlobCmd)
	memoryCmd.AddCommand(showArcSummaryCmd)
}

var showContextBlobCmd = &cobra.Command{
	Use:    "show-context-blob",
	Short:  "Print the rendered system-prompt context blob to stdout",
	Hidden: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveShowContextBlobDeps()
		root := deps.Getenv("SPRAWL_ROOT")
		if root == "" {
			return fmt.Errorf("SPRAWL_ROOT environment variable is not set; run from within a sprawl project or set SPRAWL_ROOT")
		}
		rootName := filepath.Base(root)
		blob, err := deps.Build(root, rootName)
		if err != nil {
			return err
		}
		_, werr := io.WriteString(deps.Stdout, blob)
		return werr
	},
}

var showArcSummaryCmd = &cobra.Command{
	Use:    "show-arc-summary",
	Short:  "Print the LLM-generated project arc summary to stdout",
	Hidden: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveShowArcSummaryDeps()
		root := deps.Getenv("SPRAWL_ROOT")
		if root == "" {
			return fmt.Errorf("SPRAWL_ROOT environment variable is not set; run from within a sprawl project or set SPRAWL_ROOT")
		}
		model := showArcModel
		if model == "" {
			model = "haiku"
		}
		opts := memory.ArcOptions{
			SprawlRoot:   root,
			TimelinePath: showArcTimelinePath,
			Invoker:      deps.Invoker,
			Cfg: memory.ArcConfig{
				Model:         model,
				InvokeTimeout: showArcTimeout,
			},
		}
		out, err := deps.Run(context.Background(), opts)
		if err != nil {
			return err
		}
		_, werr := io.WriteString(deps.Stdout, out)
		return werr
	},
}

func resolveShowContextBlobDeps() *showContextBlobDeps {
	if defaultShowContextBlobDeps != nil {
		return defaultShowContextBlobDeps
	}
	return &showContextBlobDeps{
		Getenv: os.Getenv,
		Stdout: os.Stdout,
		Build: func(root, name string) (string, error) {
			return memory.BuildContextBlob(root, name)
		},
	}
}

func resolveShowArcSummaryDeps() *showArcSummaryDeps {
	if defaultShowArcSummaryDeps != nil {
		return defaultShowArcSummaryDeps
	}
	return &showArcSummaryDeps{
		Getenv:  os.Getenv,
		Stdout:  os.Stdout,
		Invoker: memory.NewCLIInvoker(),
		Run:     memory.SummarizeProjectArc,
	}
}
