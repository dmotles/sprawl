package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestActivityAge_Pure(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if got := activityAge(TreeNode{}, now); got != "" {
		t.Errorf("activityAge(zero LastActivityAt) = %q, want empty", got)
	}
	n := TreeNode{LastActivityAt: now.Add(-15 * time.Hour)}
	if got := activityAge(n, now); got != " (15h)" {
		t.Errorf("activityAge(now-15h) = %q, want %q", got, " (15h)")
	}
}

// Today the tree renders LastActivityAt only as a dot colour — a staleness
// signal with no number behind it. The row must carry the age as text.
func TestTreeView_RendersActivityAge(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(80, 20)
	m.SetNodes([]TreeNode{{
		Name:              "ratz",
		Type:              "engineer",
		Status:            "running",
		LastReportMessage: "working",
		LastActivityAt:    time.Now().Add(-15 * time.Hour),
	}})
	view := m.View()
	if !strings.Contains(view, "15h") {
		t.Errorf("tree row has no age tag; want it to contain %q, got:\n%s", "15h", view)
	}
}

// A node that has never been observed must not render a bogus age.
func TestTreeView_NoAgeWhenNeverActive(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(80, 20)
	m.SetNodes([]TreeNode{{
		Name:              "ratz",
		Type:              "engineer",
		Status:            "running",
		LastReportMessage: "working",
	}})
	view := m.View()
	// Match ANY age tag, not just "(0s)": an unguarded zero LastActivityAt
	// renders "(489000h)", which no literal "(0s)" check would catch.
	if tag := regexp.MustCompile(`\(\d+[smhd]\)`).FindString(view); tag != "" {
		t.Errorf("tree row rendered age %q for a never-active node, got:\n%s", tag, view)
	}
}
