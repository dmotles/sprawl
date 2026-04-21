package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
)

func sampleActivity(t *testing.T) []agentloop.ActivityEntry {
	t.Helper()
	base := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	return []agentloop.ActivityEntry{
		{TS: base, Kind: "system", Summary: "init"},
		{TS: base.Add(1 * time.Second), Kind: "assistant_text", Summary: "I'll read the file first."},
		{TS: base.Add(2 * time.Second), Kind: "tool_use", Tool: "Read", Summary: `Read {"file":"/tmp/x"}`},
		{TS: base.Add(3 * time.Second), Kind: "tool_use", Tool: "Bash", Summary: `Bash {"command":"ls"}`},
		{TS: base.Add(4 * time.Second), Kind: "result", Summary: "success stop=end_turn turns=2"},
		{TS: base.Add(5 * time.Second), Kind: "rate_limit", Summary: "status=warn type=primary"},
	}
}

func TestActivityPanel_EmptyStateRendersPlaceholder(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	p.SetSize(40, 10)
	p.SetAgent("weave")
	out := stripANSI(p.View())
	if strings.TrimSpace(out) == "" {
		t.Fatalf("View() should render a non-empty placeholder; got %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "no activity") {
		t.Errorf("empty panel should mention 'no activity', got:\n%s", out)
	}
}

func TestActivityPanel_RendersEachKind(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	p.SetSize(60, 20)
	p.SetAgent("ghost")
	p.SetEntries(sampleActivity(t))

	out := stripANSI(p.View())
	// Every entry kind's distinguishing content should appear.
	wants := []string{
		"09:00:00",  // first timestamp
		"I'll read", // assistant text
		"Read",      // tool name
		"Bash",      // tool name
		"success",   // result
		"warn",      // rate limit summary
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("expected panel output to contain %q; got:\n%s", w, out)
		}
	}
}

func TestActivityPanel_ToolUseIsColored(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	p.SetSize(60, 20)
	p.SetAgent("ghost")
	p.SetEntries(sampleActivity(t))

	raw := p.View()
	// Must contain ANSI escape sequences — rendering is themed.
	if !strings.Contains(raw, "\x1b[") {
		t.Errorf("expected themed output to contain ANSI escapes, got none in:\n%s", raw)
	}
}

func TestActivityPanel_TruncatesLongSummary(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	p.SetSize(40, 10)
	p.SetAgent("ghost")
	long := strings.Repeat("x", 300)
	p.SetEntries([]agentloop.ActivityEntry{
		{TS: time.Now(), Kind: "assistant_text", Summary: long},
	})

	out := stripANSI(p.View())
	for _, line := range strings.Split(out, "\n") {
		// Line shouldn't exceed panel width by a wide margin.
		if len([]rune(line)) > 50 {
			t.Errorf("line width %d exceeds panel width of 40 (with some slack); line=%q", len([]rune(line)), line)
		}
	}
}

func TestActivityPanel_TailsWhenMoreEntriesThanHeight(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	// height = 4 content lines (minus any header).
	p.SetSize(60, 4)
	p.SetAgent("ghost")

	base := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	entries := make([]agentloop.ActivityEntry, 10)
	for i := range entries {
		entries[i] = agentloop.ActivityEntry{
			TS:      base.Add(time.Duration(i) * time.Second),
			Kind:    "assistant_text",
			Summary: "entry-" + string(rune('0'+i)),
		}
	}
	p.SetEntries(entries)
	out := stripANSI(p.View())

	// The last entry must be visible (newest at bottom / tail semantics).
	if !strings.Contains(out, "entry-9") {
		t.Errorf("expected newest entry 'entry-9' visible; got:\n%s", out)
	}
	// The very oldest should have been dropped to fit the height.
	if strings.Contains(out, "entry-0") {
		t.Errorf("expected oldest entry 'entry-0' clipped out when height=4; got:\n%s", out)
	}
}

func TestActivityPanel_SetAgentShowsAgentName(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	p.SetSize(50, 10)
	p.SetAgent("ghost")
	p.SetEntries(sampleActivity(t))
	out := stripANSI(p.View())
	if !strings.Contains(out, "ghost") {
		t.Errorf("expected panel header to mention observed agent 'ghost'; got:\n%s", out)
	}
}

// TestActivityPanel_MockRender is the §9 mini-mock: it renders a realistic
// sample panel and prints the output via t.Log so operators can eyeball the
// layout with `go test -v -run TestActivityPanel_MockRender ./internal/tui`.
func TestActivityPanel_MockRender(t *testing.T) {
	theme := NewTheme("212")
	p := NewActivityPanelModel(&theme)
	p.SetSize(45, 18)
	p.SetAgent("ghost")

	base := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	p.SetEntries([]agentloop.ActivityEntry{
		{TS: base, Kind: "system", Summary: "init"},
		{TS: base.Add(2 * time.Second), Kind: "assistant_text", Summary: "Reading the design doc first…"},
		{TS: base.Add(3 * time.Second), Kind: "tool_use", Tool: "Read", Summary: `Read {"file_path":"docs/designs/messaging-overhaul.md"}`},
		{TS: base.Add(5 * time.Second), Kind: "assistant_text", Summary: "Now I'll grep for PeekActivity usages."},
		{TS: base.Add(6 * time.Second), Kind: "tool_use", Tool: "Grep", Summary: `Grep {"pattern":"PeekActivity"}`},
		{TS: base.Add(8 * time.Second), Kind: "tool_use", Tool: "Bash", Summary: `Bash {"command":"make validate"}`},
		{TS: base.Add(15 * time.Second), Kind: "rate_limit", Summary: "status=warn type=primary"},
		{TS: base.Add(18 * time.Second), Kind: "result", Summary: "success stop=end_turn turns=4"},
	})

	t.Log("\n" + p.View())
}
