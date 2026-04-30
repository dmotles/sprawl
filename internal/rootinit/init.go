package rootinit

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
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
//
// When Resume is true the prior session's transcript is still live on
// Claude's side. The caller should launch with `--resume SessionID`.
// PromptPath is set to the existing SYSTEM.md if one was written by a
// prior fresh start, so the resumed session picks up the current system
// prompt. No new last-session-id is written.
type PreparedSession struct {
	Resume     bool     // if true, launch via --resume SessionID
	PromptPath string   // path to persisted SYSTEM.md (empty when Resume==true)
	SessionID  string   // resumed session ID or freshly generated UUID
	Model      string   // DefaultModel for weave/root launch
	RootTools  []string // tools available to the root agent
	Disallowed []string // tools explicitly denied
}

// Prepare runs Phase A (pre-launch housekeeping) for the root weave agent.
//
// Decision logic:
//
//  1. If last-session-id is present AND no session summary exists for it,
//     return a resume-mode PreparedSession pointing at the prior ID. No
//     new prompt file is written, no new session ID is generated.
//  2. If last-session-id is present AND a session summary exists (i.e. the
//     previous session ended via /handoff), run the consolidation pipeline,
//     clear last-session-id, and fall through to the fresh path.
//  3. Otherwise (no prior ID), take the fresh path directly: build the
//     context blob, render the system prompt, persist it to disk, generate a
//     new session ID, and write it to last-session-id.
//
// Handoff is now the explicit "start fresh next time" trigger. Ordinary
// crash/restart cases resume the prior session instead of starting over.
func Prepare(ctx context.Context, deps *Deps, mode Mode, sprawlRoot, rootName string, stdout io.Writer) (*PreparedSession, error) {
	return prepare(ctx, deps, mode, sprawlRoot, rootName, stdout, false)
}

// PrepareFresh is the "force fresh" entry point used as a fallback when
// `--resume` fails at launch time (e.g. Claude's server evicted the prior
// session, or the stored transcript is corrupt). It skips the resume
// decision and takes the fresh path directly. If a prior session ID exists
// without a summary, it is auto-summarized into memory so its context is
// preserved before the new session launches. Chosen over a `forceFresh bool`
// parameter so call sites read clearly at the use point.
func PrepareFresh(ctx context.Context, deps *Deps, mode Mode, sprawlRoot, rootName string, stdout io.Writer) (*PreparedSession, error) {
	return prepare(ctx, deps, mode, sprawlRoot, rootName, stdout, true)
}

func prepare(ctx context.Context, deps *Deps, mode Mode, sprawlRoot, rootName string, stdout io.Writer, forceFresh bool) (*PreparedSession, error) {
	prefix := deps.LogPrefix
	prevSessionID, _ := deps.ReadLastSessionID(sprawlRoot)

	if prevSessionID != "" && !forceFresh {
		alreadySummarized, _ := deps.HasSessionSummary(sprawlRoot, prevSessionID)
		if !alreadySummarized {
			// Resume path: prior session is still live on Claude's side.
			fmt.Fprintf(stdout, "%s resuming session %s\n", prefix, prevSessionID)

			// Point at the existing SYSTEM.md written by a prior fresh start
			// so the resumed session picks up the current system prompt.
			var promptPath string
			existingPrompt := filepath.Join(sprawlRoot, ".sprawl", "agents", rootName, "SYSTEM.md")
			if _, err := deps.ReadFile(existingPrompt); err == nil {
				promptPath = existingPrompt
			}

			ps := &PreparedSession{
				Resume:     true,
				PromptPath: promptPath,
				SessionID:  prevSessionID,
				Model:      DefaultModel,
				RootTools:  RootTools,
				Disallowed: DisallowedTools,
			}
			if err := saveRootAgentState(deps, sprawlRoot, rootName, ps.SessionID); err != nil {
				return nil, err
			}
			return ps, nil
		}
		// Consolidate-then-fresh: summary exists (prior session handed off),
		// run consolidation and fall through to the fresh path below.
		runConsolidationPipeline(ctx, deps, sprawlRoot, stdout, nil)
		_ = deps.WriteLastSessionID(sprawlRoot, "")
	} else if forceFresh && prevSessionID != "" {
		// Fresh fallback after a failed resume: the prior session ID is now
		// effectively dead on Claude's side. If it has no summary yet,
		// preserve whatever context we can by running the missed-handoff
		// auto-summarize path.
		alreadySummarized, _ := deps.HasSessionSummary(sprawlRoot, prevSessionID)
		if !alreadySummarized {
			homeDir, homeErr := deps.UserHomeDir()
			if homeErr != nil {
				fmt.Fprintf(stdout, "%s warning: could not determine home directory, skipping auto-summarize: %v\n", prefix, homeErr)
			} else {
				fmt.Fprintf(stdout, "%s Detected missed handoff from previous session\n", prefix)
				sp := startSpinner(stdout, prefix, "auto-summarizing...")
				summarized, sumErr := deps.AutoSummarize(ctx, sprawlRoot, sprawlRoot, homeDir, prevSessionID, deps.NewCLIInvoker())
				sp.stop()
				if sumErr != nil {
					fmt.Fprintf(stdout, "%s warning: auto-summarize failed for %s: %v\n", prefix, prevSessionID, sumErr)
				} else if summarized {
					fmt.Fprintf(stdout, "%s auto-summarized missed handoff for session %s\n", prefix, prevSessionID)
					runConsolidationPipeline(ctx, deps, sprawlRoot, stdout, nil)
				}
			}
		} else {
			// Summary exists but consolidation may not have run.
			runConsolidationPipeline(ctx, deps, sprawlRoot, stdout, nil)
		}
		_ = deps.WriteLastSessionID(sprawlRoot, "")
	}

	// Fresh path.
	contextBlob, ctxErr := deps.BuildContextBlob(sprawlRoot, rootName)
	if ctxErr != nil {
		fmt.Fprintf(stdout, "%s warning: context blob partial or failed: %v\n", prefix, ctxErr)
	}

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

	sessionID, err := deps.NewUUID()
	if err != nil {
		return nil, fmt.Errorf("generating session ID: %w", err)
	}
	if err := deps.WriteLastSessionID(sprawlRoot, sessionID); err != nil {
		return nil, fmt.Errorf("writing session ID: %w", err)
	}

	ps := &PreparedSession{
		Resume:     false,
		PromptPath: promptPath,
		SessionID:  sessionID,
		Model:      DefaultModel,
		RootTools:  RootTools,
		Disallowed: DisallowedTools,
	}
	if err := saveRootAgentState(deps, sprawlRoot, rootName, ps.SessionID); err != nil {
		return nil, err
	}
	return ps, nil
}

// saveRootAgentState persists the root agent's state file. On resume it
// loads the existing state and updates only SessionID and Status, preserving
// cost/report fields. On first launch it creates a new state file.
func saveRootAgentState(deps *Deps, sprawlRoot, rootName, sessionID string) error {
	branch, _ := deps.CurrentBranch(sprawlRoot)

	existing, loadErr := deps.LoadAgent(sprawlRoot, rootName)
	if loadErr == nil && existing != nil {
		// Update mutable fields, preserve the rest.
		existing.SessionID = sessionID
		existing.Status = "active"
		if branch != "" {
			existing.Branch = branch
		}
		return wrapSaveErr(deps.SaveAgent(sprawlRoot, existing))
	}

	// No existing state — create fresh.
	agentState := &state.AgentState{
		Name:      rootName,
		Status:    "active",
		Branch:    branch,
		SessionID: sessionID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return wrapSaveErr(deps.SaveAgent(sprawlRoot, agentState))
}

func wrapSaveErr(err error) error {
	if err != nil {
		return fmt.Errorf("saving root agent state: %w", err)
	}
	return nil
}
