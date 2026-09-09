package memory

import (
	"fmt"
	"os"
	"path/filepath"
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
