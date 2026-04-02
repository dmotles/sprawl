package memory

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Consolidate reads all session summaries and the current timeline, then spawns
// a one-shot Claude subprocess to produce an updated timeline. Sessions older
// than the 3 most recent are distilled into timeline entries; the 3 most recent
// sessions are left untouched.
//
// This function is designed to be called post-handoff when the sensei is
// restarting. It assumes single-threaded execution — no concurrent access
// protection is needed.
//
// TODO: Wire this into the sensei loop post-handoff path.
func Consolidate(ctx context.Context, dendraRoot string, invoker ClaudeInvoker, cfg *TimelineCompressionConfig, now func() time.Time) error {
	if cfg == nil {
		c := DefaultTimelineCompressionConfig()
		cfg = &c
	}
	if now == nil {
		now = time.Now
	}

	// Load all sessions. ListRecentSessions returns oldest-first.
	sessions, bodies, err := ListRecentSessions(dendraRoot, 1<<30)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	// No-op if fewer than 4 sessions (need >3 to have candidates).
	if len(sessions) <= 3 {
		return nil
	}

	// Partition: candidates are everything except the 3 most recent.
	candidateCount := len(sessions) - 3
	candidateSessions := sessions[:candidateCount]
	candidateBodies := bodies[:candidateCount]

	// Read existing timeline (empty slice if file doesn't exist).
	existingTimeline, err := ReadTimeline(dendraRoot)
	if err != nil {
		return fmt.Errorf("reading timeline: %w", err)
	}

	// Build and invoke the consolidation prompt.
	prompt := buildConsolidationPrompt(existingTimeline, candidateSessions, candidateBodies)

	output, err := invoker.Invoke(ctx, prompt)
	if err != nil {
		return fmt.Errorf("invoking claude for consolidation: %w", err)
	}

	// Parse Claude's output into timeline entries.
	entries, skipped := parseTimelineOutput(output)
	if skipped > 0 {
		log.Printf("consolidation: skipped %d malformed output lines", skipped)
	}

	// Safety: refuse to overwrite a non-empty timeline with zero entries.
	if len(entries) == 0 && len(existingTimeline) > 0 {
		return fmt.Errorf("consolidation produced no valid timeline entries; refusing to overwrite existing timeline")
	}

	// Merge parsed entries with existing timeline, deduplicating overlaps.
	merged := mergeTimelines(existingTimeline, entries)

	// Apply compression and pruning.
	compressed := CompressTimeline(merged, *cfg, now())
	final := PruneTimeline(compressed, *cfg, now())

	// Skip write if the result matches the existing timeline exactly.
	if timelineEqual(existingTimeline, final) {
		return nil
	}

	if err := WriteTimeline(dendraRoot, final); err != nil {
		return fmt.Errorf("writing consolidated timeline: %w", err)
	}

	return nil
}

// buildConsolidationPrompt constructs the prompt sent to Claude for timeline
// distillation. It includes the existing timeline (if any) and the session
// summaries that need distillation.
func buildConsolidationPrompt(existingTimeline []TimelineEntry, sessions []Session, bodies []string) string {
	var b strings.Builder

	b.WriteString("You are a timeline distillation agent for the Dendra AI orchestration system.\n\n")
	b.WriteString("Your job is to produce an updated session timeline by incorporating the session summaries below into the existing timeline.\n\n")

	// Existing timeline section.
	b.WriteString("## Current Timeline\n\n")
	if len(existingTimeline) == 0 {
		b.WriteString("No existing timeline.\n\n")
	} else {
		for _, e := range existingTimeline {
			fmt.Fprintf(&b, "- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
		}
		b.WriteString("\n")
	}

	// Session summaries section.
	b.WriteString("## Session Summaries to Distill\n\n")
	for i, s := range sessions {
		fmt.Fprintf(&b, "### Session %s (%s)\n\n", s.SessionID, s.Timestamp.UTC().Format(time.RFC3339))
		if i < len(bodies) {
			b.WriteString(bodies[i])
			b.WriteString("\n\n")
		}
	}

	// Instructions.
	b.WriteString("## Instructions\n\n")
	b.WriteString("Produce an updated timeline that incorporates the session summaries above into the existing timeline.\n\n")
	b.WriteString("Each entry MUST be on its own line in this exact format (RFC3339 timestamps):\n\n")
	b.WriteString("- {ISO-8601/RFC3339 timestamp}: {summary}\n\n")
	b.WriteString("Guidelines:\n")
	b.WriteString("- Merge and deduplicate entries when sessions cover overlapping work.\n")
	b.WriteString("- Preserve important architectural decisions and lessons learned.\n")
	b.WriteString("- Weigh recent sessions more heavily.\n")
	b.WriteString("- Identify recurring themes and pain points.\n")
	b.WriteString("- Tag entries with [recurring] when they represent recurring themes or patterns.\n")
	b.WriteString("- Tag entries with [pain-point] when they represent pain points or persistent problems.\n")
	b.WriteString("- Keep the timeline concise — one entry per significant event.\n")
	b.WriteString("- Sort entries chronologically.\n\n")
	b.WriteString("Output ONLY the bullet lines. No headers, no explanation, no other text.\n")

	return b.String()
}

// parseTimelineOutput parses Claude's raw output into TimelineEntry structs.
// Lines matching "- {RFC3339}: {summary}" are parsed; others are silently
// skipped (counted in the returned int). Blank lines are ignored without
// counting as skipped.
func parseTimelineOutput(raw string) ([]TimelineEntry, int) {
	var entries []TimelineEntry
	skipped := 0

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "- ") {
			skipped++
			continue
		}

		rest := line[2:] // strip "- "
		colonIdx := strings.Index(rest, ": ")
		if colonIdx < 0 {
			skipped++
			continue
		}

		tsStr := rest[:colonIdx]
		summary := rest[colonIdx+2:]

		t, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			skipped++
			continue
		}

		entries = append(entries, TimelineEntry{
			Timestamp: t.UTC(),
			Summary:   summary,
		})
	}

	return entries, skipped
}

// mergeTimelines combines existing and new timeline entries, deduplicating
// entries that have the same timestamp (at second precision) and summary.
// The result is sorted chronologically.
func mergeTimelines(existing, newEntries []TimelineEntry) []TimelineEntry {
	type key struct {
		ts      int64
		summary string
	}
	seen := make(map[key]struct{}, len(existing))
	result := make([]TimelineEntry, 0, len(existing)+len(newEntries))
	for _, e := range existing {
		k := key{e.Timestamp.Unix(), e.Summary}
		seen[k] = struct{}{}
		result = append(result, e)
	}
	for _, e := range newEntries {
		k := key{e.Timestamp.Unix(), e.Summary}
		if _, ok := seen[k]; !ok {
			result = append(result, e)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	return result
}

// timelineEqual returns true if two timeline slices have identical entries.
func timelineEqual(a, b []TimelineEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Timestamp.Equal(b[i].Timestamp) || a[i].Summary != b[i].Summary {
			return false
		}
	}
	return true
}
