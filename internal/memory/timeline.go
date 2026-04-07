package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TimelineEntry represents a single entry in the session timeline.
type TimelineEntry struct {
	Timestamp time.Time
	Summary   string
}

func timelinePath(sprawlRoot string) string {
	return filepath.Join(memoryDir(sprawlRoot), "timeline.md")
}

const timelineHeader = "# Session Timeline"

// ReadTimeline parses .sprawl/memory/timeline.md and returns entries.
// Returns an empty slice (not error) if the file doesn't exist.
// Lines not matching the expected format are silently skipped.
func ReadTimeline(sprawlRoot string) ([]TimelineEntry, error) {
	data, err := os.ReadFile(timelinePath(sprawlRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return []TimelineEntry{}, nil
		}
		return nil, fmt.Errorf("reading timeline: %w", err)
	}

	var entries []TimelineEntry
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		rest := line[2:] // strip "- "
		before, after, ok := strings.Cut(rest, ": ")
		if !ok {
			continue
		}
		tsStr := before
		summary := after

		t, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			continue
		}
		entries = append(entries, TimelineEntry{
			Timestamp: t.UTC(),
			Summary:   summary,
		})
	}

	if entries == nil {
		entries = []TimelineEntry{}
	}
	return entries, nil
}

// WriteTimeline writes entries to .sprawl/memory/timeline.md, creating parent
// directories if needed. Timestamps are normalized to UTC. If called with an
// empty slice, writes just the header.
func WriteTimeline(sprawlRoot string, entries []TimelineEntry) error {
	p := timelinePath(sprawlRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: world-readable memory dir is intentional
		return fmt.Errorf("creating memory directory: %w", err)
	}

	var b strings.Builder
	b.WriteString(timelineHeader)
	b.WriteString("\n")

	if len(entries) > 0 {
		b.WriteString("\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
		}
	}

	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil { //nolint:gosec // G306: world-readable timeline file is intentional
		return fmt.Errorf("writing timeline: %w", err)
	}
	return nil
}

// AppendTimelineEntries appends new entries to the existing timeline.
// It reads existing entries, merges with new ones, sorts chronologically,
// and writes back. No deduplication is performed.
func AppendTimelineEntries(sprawlRoot string, entries []TimelineEntry) error {
	existing, err := ReadTimeline(sprawlRoot)
	if err != nil {
		return fmt.Errorf("reading existing timeline: %w", err)
	}

	merged := append(existing, entries...) //nolint:gocritic // intentionally creating a new slice
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp.Before(merged[j].Timestamp)
	})

	return WriteTimeline(sprawlRoot, merged)
}
