package memory

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CompressTimeline applies recency-weighted compression to timeline entries.
// Recent entries are kept as-is, older entries are grouped into weekly or monthly
// summaries. Tagged entries ([recurring], [pain-point]) and already-compressed
// entries are preserved verbatim.
func CompressTimeline(entries []TimelineEntry, cfg TimelineCompressionConfig, now time.Time) []TimelineEntry {
	if len(entries) == 0 {
		return nil
	}

	var result []TimelineEntry

	type weekKey struct {
		year, week int
	}
	type monthKey struct {
		year  int
		month time.Month
	}

	weeklyGroups := make(map[weekKey][]TimelineEntry)
	monthlyGroups := make(map[monthKey][]TimelineEntry)
	// Track insertion order for deterministic iteration.
	var weeklyOrder []weekKey
	var monthlyOrder []monthKey

	for _, e := range entries {
		if isTaggedEntry(e.Summary) || isAlreadyCompressed(e.Summary) {
			result = append(result, e)
			continue
		}

		age := now.Sub(e.Timestamp)
		switch {
		case age < cfg.WeeklySummaryAge:
			result = append(result, e)
		case age < cfg.MonthlySummaryAge:
			y, w := e.Timestamp.ISOWeek()
			k := weekKey{y, w}
			if _, ok := weeklyGroups[k]; !ok {
				weeklyOrder = append(weeklyOrder, k)
			}
			weeklyGroups[k] = append(weeklyGroups[k], e)
		default:
			k := monthKey{e.Timestamp.Year(), e.Timestamp.Month()}
			if _, ok := monthlyGroups[k]; !ok {
				monthlyOrder = append(monthlyOrder, k)
			}
			monthlyGroups[k] = append(monthlyGroups[k], e)
		}
	}

	// Compress weekly groups.
	for _, k := range weeklyOrder {
		group := weeklyGroups[k]
		sortEntriesChronological(group)
		monday := startOfISOWeek(k.year, k.week)
		prefix := fmt.Sprintf("[Week of %s]", monday.Format("2006-01-02"))
		joined := joinSummaries(group)
		result = append(result, TimelineEntry{
			Timestamp: monday,
			Summary:   prefix + " " + joined,
		})
	}

	// Compress monthly groups.
	for _, k := range monthlyOrder {
		group := monthlyGroups[k]
		sortEntriesChronological(group)
		startOfMonth := time.Date(k.year, k.month, 1, 0, 0, 0, 0, time.UTC)
		prefix := fmt.Sprintf("[%s %d]", k.month.String(), k.year)
		joined := joinSummaries(group)
		result = append(result, TimelineEntry{
			Timestamp: startOfMonth,
			Summary:   prefix + " " + joined,
		})
	}

	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})

	return result
}

var omissionNoteRe = regexp.MustCompile(`^\[\.\.\.\d+ earliest entries omitted\]$`)

// isOmissionNote returns true if the summary is a pruning omission note.
func isOmissionNote(summary string) bool {
	return omissionNoteRe.MatchString(summary)
}

// entryFormattedSize returns the byte size of an entry as it would appear in timeline.md.
func entryFormattedSize(e TimelineEntry) int {
	return MeasureBytes(fmt.Sprintf("- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary))
}

// PruneTimeline enforces MaxEntries and MaxSizeChars limits on timeline entries.
// It removes existing omission notes, drops lowest-priority entries (oldest untagged
// first, then oldest tagged), and prepends an omission note when entries are dropped.
// A single remaining entry is never dropped even if it exceeds MaxSizeChars.
func PruneTimeline(entries []TimelineEntry, cfg TimelineCompressionConfig, now time.Time) []TimelineEntry {
	if len(entries) == 0 {
		return nil
	}

	// Step 1: Remove existing omission notes.
	filtered := make([]TimelineEntry, 0, len(entries))
	for _, e := range entries {
		if !isOmissionNote(e.Summary) {
			filtered = append(filtered, e)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	// Step 2: Partition into tagged and untagged, sorted oldest-first.
	var tagged, untagged []TimelineEntry
	for _, e := range filtered {
		if isTaggedEntry(e.Summary) {
			tagged = append(tagged, e)
		} else {
			untagged = append(untagged, e)
		}
	}
	sortEntriesChronological(tagged)
	sortEntriesChronological(untagged)

	dropped := 0

	// Step 3: Enforce MaxEntries.
	if cfg.MaxEntries > 0 {
		total := len(tagged) + len(untagged)
		for total > cfg.MaxEntries && len(untagged) > 0 {
			untagged = untagged[1:] // drop oldest untagged
			dropped++
			total--
		}
		for total > cfg.MaxEntries && len(tagged) > 0 {
			tagged = tagged[1:] // drop oldest tagged (hard limit)
			dropped++
			total--
		}
	}

	// Step 4: Enforce MaxSizeChars.
	if cfg.MaxSizeChars > 0 {
		totalSize := 0
		for _, e := range untagged {
			totalSize += entryFormattedSize(e)
		}
		for _, e := range tagged {
			totalSize += entryFormattedSize(e)
		}

		remaining := len(tagged) + len(untagged)
		for totalSize > cfg.MaxSizeChars && remaining > 1 {
			if len(untagged) > 0 {
				totalSize -= entryFormattedSize(untagged[0])
				untagged = untagged[1:]
				dropped++
				remaining--
			} else if len(tagged) > 0 {
				totalSize -= entryFormattedSize(tagged[0])
				tagged = tagged[1:]
				dropped++
				remaining--
			}
		}
	}

	// Step 5: Reassemble and add omission note.
	result := make([]TimelineEntry, 0, len(tagged)+len(untagged)+1)
	result = append(result, tagged...)
	result = append(result, untagged...)

	if dropped > 0 {
		result = append(result, TimelineEntry{
			Timestamp: now,
			Summary:   fmt.Sprintf("[...%d earliest entries omitted]", dropped),
		})
	}

	// Step 6: Sort chronologically.
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})

	return result
}

func isTaggedEntry(summary string) bool {
	return strings.Contains(summary, "[recurring]") || strings.Contains(summary, "[pain-point]")
}

func isAlreadyCompressed(summary string) bool {
	if strings.HasPrefix(summary, "[Week of ") {
		return true
	}
	return isMonthPrefix(summary)
}

// isMonthPrefix checks if a summary starts with a month-year prefix like [January 2026].
func isMonthPrefix(summary string) bool {
	months := [...]string{
		"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December",
	}
	for _, m := range months {
		if strings.HasPrefix(summary, "["+m+" ") {
			return true
		}
	}
	return false
}

// startOfISOWeek returns the Monday of the given ISO year/week in UTC.
func startOfISOWeek(isoYear, isoWeek int) time.Time {
	// January 4 is always in ISO week 1.
	jan4 := time.Date(isoYear, 1, 4, 0, 0, 0, 0, time.UTC)
	weekday := jan4.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	// Monday of week 1.
	mondayW1 := jan4.AddDate(0, 0, -int(weekday-time.Monday))
	// Offset to desired week.
	return mondayW1.AddDate(0, 0, (isoWeek-1)*7)
}

func sortEntriesChronological(entries []TimelineEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
}

func joinSummaries(entries []TimelineEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.Summary
	}
	return strings.Join(parts, "; ")
}
