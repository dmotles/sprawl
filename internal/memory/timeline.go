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

func timelinePath(dendraRoot string) string {
	return filepath.Join(memoryDir(dendraRoot), "timeline.md")
}

const timelineHeader = "# Session Timeline"

// ReadTimeline parses .dendra/memory/timeline.md and returns entries.
// Returns an empty slice (not error) if the file doesn't exist.
// Lines not matching the expected format are silently skipped.
func ReadTimeline(dendraRoot string) ([]TimelineEntry, error) {
	data, err := os.ReadFile(timelinePath(dendraRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return []TimelineEntry{}, nil
		}
		return nil, fmt.Errorf("reading timeline: %w", err)
	}

	var entries []TimelineEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		rest := line[2:] // strip "- "
		colonIdx := strings.Index(rest, ": ")
		if colonIdx < 0 {
			continue
		}
		tsStr := rest[:colonIdx]
		summary := rest[colonIdx+2:]

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

// WriteTimeline writes entries to .dendra/memory/timeline.md, creating parent
// directories if needed. Timestamps are normalized to UTC. If called with an
// empty slice, writes just the header.
func WriteTimeline(dendraRoot string, entries []TimelineEntry) error {
	p := timelinePath(dendraRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
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

	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing timeline: %w", err)
	}
	return nil
}

// AppendTimelineEntries appends new entries to the existing timeline.
// It reads existing entries, merges with new ones, sorts chronologically,
// and writes back. No deduplication is performed.
func AppendTimelineEntries(dendraRoot string, entries []TimelineEntry) error {
	existing, err := ReadTimeline(dendraRoot)
	if err != nil {
		return fmt.Errorf("reading existing timeline: %w", err)
	}

	merged := append(existing, entries...)
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp.Before(merged[j].Timestamp)
	})

	return WriteTimeline(dendraRoot, merged)
}
