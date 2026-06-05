package tui

import (
	"testing"
)

func TestSelectionState_RangeNormalizedWhenCursorBeforeAnchor(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 3, Cursor: 1}
	lo, hi := s.Range()
	if lo != 1 || hi != 3 {
		t.Errorf("Range() = (%d, %d), want (1, 3)", lo, hi)
	}
}

func TestSelectionState_RangeSingleMessage(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 2, Cursor: 2}
	lo, hi := s.Range()
	if lo != 2 || hi != 2 {
		t.Errorf("Range() = (%d, %d), want (2, 2)", lo, hi)
	}
}

func TestSelectionState_MoveCursorClampsLow(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 2, Cursor: 1}
	s = s.MoveCursor(-5, 4)
	if s.Cursor != 0 {
		t.Errorf("MoveCursor(-5, 4).Cursor = %d, want 0", s.Cursor)
	}
}

func TestSelectionState_MoveCursorClampsHigh(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 2, Cursor: 2}
	s = s.MoveCursor(10, 4)
	if s.Cursor != 3 {
		t.Errorf("MoveCursor(10, 4).Cursor = %d, want 3", s.Cursor)
	}
}

func TestSelectionState_MoveCursorNoMessages(t *testing.T) {
	s := SelectionState{Active: true}
	s = s.MoveCursor(1, 0)
	if s.Cursor != 0 {
		t.Errorf("MoveCursor on empty buffer should stay at 0, got %d", s.Cursor)
	}
}

// AssembleRawMarkdown tests (legacy MessageEntry-based) were removed in
// QUM-676 along with the legacy function. The ChatList items-based
// equivalents (AssembleRawMarkdownFromItems) are covered by
// selection_chatlist_test.go.
