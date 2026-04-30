package rootinit

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
	"golang.org/x/sync/errgroup"
)

// FinalizeHandoff runs Phase D (post-launch housekeeping):
//
//  1. Waits briefly for any in-flight background consolidation from the
//     prior handoff (flock on .sprawl/memory/.consolidating). Rapid back-
//     to-back handoffs serialize through this wait.
//  2. Checks for the handoff-signal marker file.
//  3. If present, fires the consolidation pipeline in the background
//     (flock-guarded goroutine — see QUM-282) and immediately removes
//     the signal file + clears last-session-id so the caller can relaunch
//     without waiting on the LLM round trips.
//  4. If absent, logs a "session ended" notice.
//
// Returns nil on success (including the no-handoff-signal path). Individual
// consolidation / persistent-knowledge errors are logged as warnings rather
// than propagated, matching the original behavior: the caller should
// proceed to restart regardless.
//
// Crash safety: if the process exits before the background goroutine
// completes, the partially-consolidated sessions remain in
// .sprawl/memory/sessions and are picked up automatically by the next
// handoff's consolidation run.
func FinalizeHandoff(_ context.Context, deps *Deps, sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) error {
	prefix := deps.LogPrefix

	// Make sure any prior in-flight consolidation has finished before we
	// schedule another. In the common case this is instantaneous (no
	// lockfile or lockfile already released).
	WaitForBackgroundConsolidation(sprawlRoot, BackgroundConsolidationTimeout, stdout, prefix)

	handoffPath := filepath.Join(sprawlRoot, ".sprawl", "memory", "handoff-signal")
	if _, readErr := deps.ReadFile(handoffPath); readErr == nil {
		fmt.Fprintf(stdout, "%s handoff signal detected, restarting\n", prefix)

		// Fire-and-forget: the returned channel is ignored here so the
		// caller returns to the user (launches the next session) without
		// blocking on two LLM round-trips.
		_ = deps.BackgroundConsolidate(sprawlRoot, stdout, events)

		_ = deps.RemoveFile(handoffPath)
		_ = deps.WriteLastSessionID(sprawlRoot, "")
	} else {
		fmt.Fprintf(stdout, "%s session ended, restarting\n", prefix)
	}
	return nil
}

// runConsolidationPipeline runs timeline consolidation and persistent
// knowledge update concurrently (QUM-283). Both steps are best-effort:
// failures are logged as warnings. Used by Prepare (missed-handoff path)
// and by the background goroutine launched from FinalizeHandoff.
//
// PK reads the pre-consolidation timeline (the run runs in parallel, so
// there is no temporal ordering between them). This is deliberate: PK is
// a multi-session distillation and a one-handoff skew on the timeline it
// ingests has negligible impact on the output. See QUM-283 for context.
func runConsolidationPipeline(ctx context.Context, deps *Deps, sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) {
	prefix := deps.LogPrefix

	// Pre-read the latest session body + existing timeline once, up front.
	// PK receives these as inputs rather than waiting on Consolidate to
	// rewrite the timeline — see function docstring.
	var sessionSummary string
	if sessions, bodies, err := deps.ListRecentSessions(sprawlRoot, 1); err != nil {
		fmt.Fprintf(stdout, "%s warning: reading latest session for persistent knowledge: %v\n", prefix, err)
	} else if len(sessions) > 0 && len(bodies) > 0 {
		sessionSummary = bodies[0]
	}

	var timelineBullets string
	if entries, err := deps.ReadTimeline(sprawlRoot); err != nil {
		fmt.Fprintf(stdout, "%s warning: reading timeline for persistent knowledge: %v\n", prefix, err)
	} else {
		var tlb strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&tlb, "- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
		}
		timelineBullets = tlb.String()
	}

	tlCfg := memory.DefaultTimelineCompressionConfig()
	pkCfg := memory.DefaultPersistentKnowledgeConfig()
	model := deps.MemoryModel
	if deps.LoadMemoryModel != nil {
		if override := deps.LoadMemoryModel(sprawlRoot); override != "" {
			model = override
		}
	}
	if model != "" {
		tlCfg.Model = model
		pkCfg.Model = model
	}

	var eg errgroup.Group
	eg.Go(func() error {
		sendConsolidationEvent(events, ConsolidationEvent{Phase: "Consolidating timeline..."})
		sp := startSpinner(stdout, prefix, "consolidating timeline...")
		defer sp.stop()
		if err := deps.Consolidate(ctx, sprawlRoot, deps.NewCLIInvoker(), &tlCfg, nil); err != nil {
			fmt.Fprintf(stdout, "%s warning: consolidation failed: %v\n", prefix, err)
		}
		return nil
	})
	eg.Go(func() error {
		sendConsolidationEvent(events, ConsolidationEvent{Phase: "Updating persistent knowledge..."})
		sp := startSpinner(stdout, prefix, "updating persistent knowledge...")
		defer sp.stop()
		if err := deps.UpdatePersistentKnowledge(ctx, sprawlRoot, deps.NewCLIInvoker(), &pkCfg, sessionSummary, timelineBullets); err != nil {
			fmt.Fprintf(stdout, "%s warning: persistent knowledge update failed: %v\n", prefix, err)
		}
		return nil
	})
	_ = eg.Wait()
}
