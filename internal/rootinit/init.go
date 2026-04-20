package rootinit

import (
	"context"
	"fmt"
	"io"

	"github.com/dmotles/sprawl/internal/agent"
)

// Mode identifies which launch mode the caller runs in. It is threaded
// through Prepare so the system-prompt template picks the right
// mode-specific blocks.
type Mode string

const (
	ModeTmux Mode = "tmux"
	ModeTUI  Mode = "tui"
)

// PreparedSession is the output of Prepare. Callers use it to build the
// mode-specific claude.LaunchOpts.
type PreparedSession struct {
	PromptPath string   // path to the persisted SYSTEM.md
	SessionID  string   // fresh UUID written to last-session-id
	Model      string   // DefaultModel (currently "opus[1m]")
	RootTools  []string // tools available to the root agent
	Disallowed []string // tools explicitly denied
}

// Prepare runs Phase A (pre-launch housekeeping) for the root weave agent:
//
//  1. Detects a missed handoff from the previous session and auto-summarizes
//     or consolidates as appropriate.
//  2. Builds the context blob (best-effort).
//  3. Builds the system prompt with the caller's Mode and persists it to
//     disk.
//  4. Generates a fresh session ID and writes it to last-session-id.
//
// stdout receives progress / warning log lines. The returned
// PreparedSession is ready to feed into a mode-specific claude.LaunchOpts.
func Prepare(ctx context.Context, deps *Deps, mode Mode, sprawlRoot, rootName string, stdout io.Writer) (*PreparedSession, error) {
	// 0. Detect missed handoff from previous session.
	prevSessionID, _ := deps.ReadLastSessionID(sprawlRoot)
	if prevSessionID != "" {
		alreadySummarized, _ := deps.HasSessionSummary(sprawlRoot, prevSessionID)
		if alreadySummarized {
			// Summary exists but consolidation may not have run (e.g., session
			// killed after handoff before post-session housekeeping).
			runConsolidationPipeline(ctx, deps, sprawlRoot, stdout)
			_ = deps.WriteLastSessionID(sprawlRoot, "")
		} else {
			homeDir, homeErr := deps.UserHomeDir()
			if homeErr != nil {
				fmt.Fprintf(stdout, "[root-loop] warning: could not determine home directory, skipping auto-summarize: %v\n", homeErr)
			} else {
				fmt.Fprintf(stdout, "[root-loop] Detected missed handoff from previous session\n")
				sp := startSpinner(stdout, "auto-summarizing...")
				summarized, sumErr := deps.AutoSummarize(ctx, sprawlRoot, sprawlRoot, homeDir, prevSessionID, deps.NewCLIInvoker())
				sp.stop()
				if sumErr != nil {
					fmt.Fprintf(stdout, "[root-loop] warning: auto-summarize failed for %s: %v\n", prevSessionID, sumErr)
				} else if summarized {
					fmt.Fprintf(stdout, "[root-loop] auto-summarized missed handoff for session %s\n", prevSessionID)
					runConsolidationPipeline(ctx, deps, sprawlRoot, stdout)
				}
			}
		}
	}

	// 1. Build context blob (best-effort).
	contextBlob, ctxErr := deps.BuildContextBlob(sprawlRoot, rootName)
	if ctxErr != nil {
		fmt.Fprintf(stdout, "[root-loop] warning: context blob partial or failed: %v\n", ctxErr)
	}

	// 2. Build system prompt.
	systemPrompt := deps.BuildPrompt(agent.PromptConfig{
		RootName:    rootName,
		AgentCLI:    "claude-code",
		ContextBlob: contextBlob,
		TestMode:    deps.Getenv("SPRAWL_TEST_MODE") == "1",
		Mode:        string(mode),
	})
	promptPath, err := deps.WriteSystemPrompt(sprawlRoot, rootName, systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("writing system prompt: %w", err)
	}

	// 3. Generate session ID.
	sessionID, err := deps.NewUUID()
	if err != nil {
		return nil, fmt.Errorf("generating session ID: %w", err)
	}
	if err := deps.WriteLastSessionID(sprawlRoot, sessionID); err != nil {
		return nil, fmt.Errorf("writing session ID: %w", err)
	}

	return &PreparedSession{
		PromptPath: promptPath,
		SessionID:  sessionID,
		Model:      DefaultModel,
		RootTools:  RootTools,
		Disallowed: DisallowedTools,
	}, nil
}
