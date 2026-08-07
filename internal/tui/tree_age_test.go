package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// ageTagRe matches any rendered staleness tag, in any bucket. Asserting on a
// literal "·0s" would miss the "·106751d" a zero LastActivityAt produces.
var ageTagRe = regexp.MustCompile(`·\d+[smhd]`)

func TestActivityAge_Pure(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if got := activityAge(TreeNode{}, now); got != "" {
		t.Errorf("activityAge(zero LastActivityAt) = %q, want empty", got)
	}
	n := TreeNode{LastActivityAt: now.Add(-15 * time.Hour)}
	if got := activityAge(n, now); got != " ·15h" {
		t.Errorf("activityAge(now-15h) = %q, want %q", got, " ·15h")
	}
}

// RenderTreeOrbital is the renderer the running TUI actually draws
// (app.go:2708). TreeModel.View() is discarded into `_`, so a test against it
// would pass while no user ever saw the tag.
func TestRenderTreeOrbital_RendersActivityAge(t *testing.T) {
	nodes := []TreeNode{{
		Name:           "ratz",
		Type:           "engineer",
		Status:         "running",
		LastActivityAt: time.Now().Add(-15 * time.Hour),
	}}
	out := strings.Join(RenderTreeOrbital(nodes, "", 120, 0), "\n")
	if !strings.Contains(out, "·15h") {
		t.Errorf("orbital pill has no age tag; want %q in:\n%s", "·15h", out)
	}
}

// A node that has never been observed must not render a bogus age.
func TestRenderTreeOrbital_NoAgeWhenNeverActive(t *testing.T) {
	nodes := []TreeNode{{Name: "ratz", Type: "engineer", Status: "running"}}
	out := strings.Join(RenderTreeOrbital(nodes, "", 120, 0), "\n")
	if tag := ageTagRe.FindString(out); tag != "" {
		t.Errorf("orbital pill rendered age %q for a never-active node:\n%s", tag, out)
	}
}

// The age tag sits immediately before the unread badge, and
// scripts/e2e-tests/notify-tui.sh reads that badge as `weave[^│]*\([1-9]`. A
// parenthesised age would match that regex and report a maildir leak that
// never happened; a `·`-delimited one cannot.
func TestRenderTreeOrbital_AgeTagDoesNotForgeAnUnreadBadge(t *testing.T) {
	badgeRe := regexp.MustCompile(`weave[^│]*\([1-9]`)

	quiet := []TreeNode{{
		Name: "weave", Type: "weave", Status: "idle",
		LastActivityAt: time.Now().Add(-5 * time.Second),
	}}
	out := strings.Join(RenderTreeOrbital(quiet, "", 120, 0), "\n")
	if !strings.Contains(out, "·5s") {
		t.Fatalf("precondition: expected an age tag to be present in:\n%s", out)
	}
	if badge := badgeRe.FindString(out); badge != "" {
		t.Errorf("age tag matched notify-tui.sh's unread-badge regex (%q) with zero unread:\n%s", badge, out)
	}

	// Positive control for the same regex: a real unread badge MUST still be
	// detected, or the assertion above would be satisfied by a renderer that
	// emits no badge at all.
	unread := []TreeNode{{
		Name: "weave", Type: "weave", Status: "idle", Unread: 1,
		LastActivityAt: time.Now().Add(-5 * time.Second),
	}}
	out = strings.Join(RenderTreeOrbital(unread, "", 120, 0), "\n")
	if badge := badgeRe.FindString(out); badge == "" {
		t.Errorf("a real unread badge is no longer detected by notify-tui.sh's regex:\n%s", out)
	}
}

// The tag must not squeeze the agent name out of a width-clipped pill row —
// the name is the row's identity and is worth more than the age.
func TestRenderTreeOrbital_AgeTagDoesNotClipAgentName(t *testing.T) {
	for _, width := range []int{40, 60, 120, 200} {
		nodes := []TreeNode{
			{Name: "weave", Type: "weave", Status: "idle", LastActivityAt: time.Now().Add(-15 * time.Hour)},
			{Name: "ratz", Type: "engineer", Status: "running", LastActivityAt: time.Now().Add(-3 * time.Minute)},
		}
		lines := RenderTreeOrbital(nodes, "", width, 0)
		out := strings.Join(lines, "\n")
		if !strings.Contains(out, "weave") {
			t.Errorf("width=%d: first agent name clipped out:\n%s", width, out)
		}
		for _, line := range lines {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width=%d: pill row is %d cells wide, want <= %d: %q", width, w, width, line)
			}
		}
	}
}
