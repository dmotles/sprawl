package tui

import (
	"strings"
)

// SelectionState tracks a contiguous, message-level selection in the viewport.
// Kept as a plain value (no bubbletea dependency) so it can be unit-tested in
// isolation. See docs/designs/viewport-yank.md.
type SelectionState struct {
	Active bool
	Anchor int // message index where selection started
	Cursor int // message index the user is moving
}

// Range returns the inclusive [lo, hi] message-index range, normalized so that
// lo <= hi regardless of which side of the anchor the cursor sits on.
func (s SelectionState) Range() (lo, hi int) {
	lo, hi = s.Anchor, s.Cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi
}

// MoveCursor returns a copy of s with Cursor shifted by delta and clamped to
// [0, n-1]. When n == 0 the cursor is held at 0.
func (s SelectionState) MoveCursor(delta, n int) SelectionState {
	if n <= 0 {
		s.Cursor = 0
		return s
	}
	c := s.Cursor + delta
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	s.Cursor = c
	return s
}

// AssembleRawMarkdownFromItems returns the raw-markdown concatenation of
// items whose indices lie in [lo, hi] (inclusive, clamped to the slice).
// Items emit their copy-for-selection payload via Item.RawMarkdown(); empty
// payloads (e.g. ThinkingItem with no text) are skipped so the joined output
// doesn't carry blank-line clusters. Adjacent non-empty payloads are
// separated by a single blank line ("\n\n"). QUM-676 — replaces the legacy
// AssembleRawMarkdown(msgs []MessageEntry, lo, hi) once ChatList is the
// sole transcript store.
func AssembleRawMarkdownFromItems(items []Item, lo, hi int) string {
	n := len(items)
	if n == 0 {
		return ""
	}
	if lo < 0 {
		lo = 0
	}
	if hi > n-1 {
		hi = n - 1
	}
	if lo > hi {
		return ""
	}
	var parts []string
	for i := lo; i <= hi; i++ {
		raw := items[i].RawMarkdown()
		if raw == "" {
			continue
		}
		parts = append(parts, raw)
	}
	return strings.Join(parts, "\n\n")
}
