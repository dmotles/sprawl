package memory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Consolidate brings the project's timeline.md up to date with the on-disk
// session corpus. In the append-only model (QUM-517) this is a thin loop:
//
//  1. Read timeline.md and collect the set of session ids already present.
//  2. List every session under .sprawl/memory/sessions/.
//  3. For each session not already in the timeline, call
//     AppendSessionWithOptions, which performs a single LLM round-trip and
//     merges one canonical row.
//
// Per-session errors are logged and skipped — Consolidate is best-effort and
// never aborts mid-loop. Callers that need stricter semantics should use
// AppendSessionWithOptions directly.
//
// The cfg / now parameters are kept on the public signature for backwards
// compatibility with rootinit's Deps wiring; only cfg.Model and
// cfg.InvokeTimeout are honored in the new pipeline.
func Consolidate(ctx context.Context, sprawlRoot string, invoker ClaudeInvoker, cfg *TimelineCompressionConfig, now func() time.Time) error {
	return ConsolidateExcluding(ctx, sprawlRoot, invoker, cfg, now, nil)
}

// ConsolidateExcluding behaves like Consolidate but skips any sessions whose
// id is present in excludeIDs. Used by the rootinit pipeline to "hold back"
// the most recent sealed session so it can be rendered verbatim under the
// "## Last Session" block in the next system prompt (QUM-521).
func ConsolidateExcluding(ctx context.Context, sprawlRoot string, invoker ClaudeInvoker, cfg *TimelineCompressionConfig, _ func() time.Time, excludeIDs map[string]bool) error {
	if invoker == nil {
		return errors.New("Consolidate: invoker is required")
	}
	if cfg == nil {
		c := DefaultTimelineCompressionConfig()
		cfg = &c
	}

	timelinePath := filepath.Join(sprawlRoot, ".sprawl", "memory", "timeline.md")

	// Build the set of session ids already present in timeline.md.
	seen := make(map[string]bool)
	data, err := os.ReadFile(timelinePath) //nolint:gosec // G304: caller-controlled sprawlRoot, by design
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading timeline.md: %w", err)
	}
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if id := extractSessionID(line); id != "" {
				seen[id] = true
			}
		}
	}

	sessions, _, err := ListRecentSessions(sprawlRoot, math.MaxInt32)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	for _, s := range sessions {
		if seen[s.SessionID] {
			continue
		}
		if excludeIDs[s.SessionID] {
			continue
		}
		appendCfg := RegenerateConfig{
			Model:         cfg.Model,
			InvokeTimeout: cfg.InvokeTimeout,
		}
		if _, aerr := AppendSessionWithOptions(ctx, AppendOptions{
			SprawlRoot:  sprawlRoot,
			SessionID:   s.SessionID,
			Invoker:     invoker,
			Cfg:         appendCfg,
			LockTimeout: DefaultAppendLockTimeout,
		}); aerr != nil {
			log.Printf("consolidate: append session %s: %v", s.SessionID, aerr)
			continue
		}
	}
	return nil
}

// extractSessionID returns the session UUID embedded in a canonical
// timeline row, or "" if line is not a canonical row. The row format is:
//
//	YYYY-MM-DD <session-id> | <summary>
//
// (see TimelineRowRE). We use the regex's group 2 as the source of truth.
func extractSessionID(line string) string {
	m := TimelineRowRE.FindStringSubmatch(line)
	if len(m) < 3 {
		return ""
	}
	return m[2]
}
