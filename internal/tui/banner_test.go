package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSprawlBanner_Width(t *testing.T) {
	lines := strings.Split(sprawlBanner, "\n")
	for i, line := range lines {
		w := ansi.StringWidth(line)
		if w > 80 {
			t.Errorf("banner line %d is %d columns wide (max 80): %q", i, w, line)
		}
	}
}

func TestSprawlBanner_Height(t *testing.T) {
	lines := strings.Split(strings.TrimRight(sprawlBanner, "\n"), "\n")
	if len(lines) < 6 || len(lines) > 10 {
		t.Errorf("banner is %d lines tall, want 6-10", len(lines))
	}
}

func TestSessionBanner_WithSessionAndVersion(t *testing.T) {
	out := SessionBanner("abc-1234", "v0.2.0")
	if !strings.Contains(out, "abc-1234") {
		t.Error("SessionBanner output should contain the session ID")
	}
	if !strings.Contains(out, "v0.2.0") {
		t.Error("SessionBanner output should contain the version")
	}
	if !strings.Contains(out, sprawlBanner) {
		t.Error("SessionBanner output should contain the ASCII art banner")
	}
}

func TestSessionBanner_EmptySessionID(t *testing.T) {
	out := SessionBanner("", "v0.2.0")
	if !strings.Contains(out, "v0.2.0") {
		t.Error("SessionBanner with empty session ID should still show version")
	}
	if strings.Contains(out, "()") {
		t.Error("SessionBanner with empty session ID should not show empty parens")
	}
	if strings.Contains(out, "  ") {
		// Allow leading whitespace for centering, but no double-spaces in the tagline itself
		tagline := extractTagline(out)
		if strings.Contains(strings.TrimSpace(tagline), "  ") {
			t.Errorf("tagline should not have double-spaces: %q", tagline)
		}
	}
}

func TestSessionBanner_EmptyVersion(t *testing.T) {
	out := SessionBanner("abc-1234", "")
	if !strings.Contains(out, "abc-1234") {
		t.Error("SessionBanner with empty version should still show session ID")
	}
}

func TestSessionBanner_BothEmpty(t *testing.T) {
	out := SessionBanner("", "")
	if !strings.Contains(out, sprawlBanner) {
		t.Error("SessionBanner with both empty should still show the banner art")
	}
}

func TestSessionBanner_AllLinesUnder80Cols(t *testing.T) {
	out := SessionBanner("abcdef12-3456-7890-abcd-ef1234567890", "v0.2.0")
	for i, line := range strings.Split(out, "\n") {
		w := ansi.StringWidth(line)
		if w > 80 {
			t.Errorf("SessionBanner line %d is %d columns wide (max 80): %q", i, w, line)
		}
	}
}

// extractTagline returns the last non-empty line of the banner output,
// which should be the version/session tagline.
func extractTagline(banner string) string {
	lines := strings.Split(strings.TrimRight(banner, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
