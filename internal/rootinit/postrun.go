package rootinit

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// FinalizeHandoff runs Phase D (post-launch housekeeping):
//
//  1. Checks for the handoff-signal marker file.
//  2. If present, runs the consolidation pipeline (timeline consolidate +
//     persistent-knowledge update), then removes the signal file and clears
//     last-session-id.
//  3. If absent, logs a "session ended" notice.
//
// Returns nil on success (including the no-handoff-signal path). Individual
// consolidation / persistent-knowledge errors are logged as warnings rather
// than propagated, matching the original behavior: the caller should
// proceed to restart regardless.
func FinalizeHandoff(ctx context.Context, deps *Deps, sprawlRoot string, stdout io.Writer) error {
	handoffPath := filepath.Join(sprawlRoot, ".sprawl", "memory", "handoff-signal")
	if _, readErr := deps.ReadFile(handoffPath); readErr == nil {
		fmt.Fprintf(stdout, "[root-loop] handoff signal detected, restarting\n")

		runConsolidationPipeline(ctx, deps, sprawlRoot, stdout)

		// Clean up after consolidation for crash safety — if killed during
		// consolidation, the next session retries.
		_ = deps.RemoveFile(handoffPath)
		_ = deps.WriteLastSessionID(sprawlRoot, "")
	} else {
		fmt.Fprintf(stdout, "[root-loop] session ended, restarting\n")
	}
	return nil
}

// runConsolidationPipeline runs timeline consolidation and persistent
// knowledge update. Both steps are best-effort: failures are logged as
// warnings. Used by Prepare (missed-handoff path) and FinalizeHandoff.
func runConsolidationPipeline(ctx context.Context, deps *Deps, sprawlRoot string, stdout io.Writer) {
	sp := startSpinner(stdout, "consolidating timeline...")
	cErr := deps.Consolidate(ctx, sprawlRoot, deps.NewCLIInvoker(), nil, nil)
	sp.stop()
	if cErr != nil {
		fmt.Fprintf(stdout, "[root-loop] warning: consolidation failed: %v\n", cErr)
	}

	var sessionSummary string
	if sessions, bodies, err := deps.ListRecentSessions(sprawlRoot, 1); err != nil {
		fmt.Fprintf(stdout, "[root-loop] warning: reading latest session for persistent knowledge: %v\n", err)
	} else if len(sessions) > 0 && len(bodies) > 0 {
		sessionSummary = bodies[0]
	}

	var timelineBullets string
	if entries, err := deps.ReadTimeline(sprawlRoot); err != nil {
		fmt.Fprintf(stdout, "[root-loop] warning: reading timeline for persistent knowledge: %v\n", err)
	} else {
		var tlb strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&tlb, "- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
		}
		timelineBullets = tlb.String()
	}

	sp = startSpinner(stdout, "updating persistent knowledge...")
	pkErr := deps.UpdatePersistentKnowledge(ctx, sprawlRoot, deps.NewCLIInvoker(), nil, sessionSummary, timelineBullets)
	sp.stop()
	if pkErr != nil {
		fmt.Fprintf(stdout, "[root-loop] warning: persistent knowledge update failed: %v\n", pkErr)
	}
}
