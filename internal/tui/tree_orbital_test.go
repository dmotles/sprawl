package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// QUM-733 5a: RenderTreeOrbital now emits a horizontal-wrapped pill list in
// agentNames() order (weave first, then m.childNodes in DFS order). No
// orbital scaffolding glyphs (──●, ↳, ·, →) and no manager-pivot rows.
// Pills are `<name> <glyph>` separated by a dim two-space gap, wrapping to
// additional rows within the header row budget (WordmarkHeight: 3 wide / 1
// narrow). When the total content exceeds the budget the trailing row is
// truncated with an ellipsis.

func TestRenderTreeOrbital_HorizontalPillList(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, InTurn: true},
		{Name: "scout", Type: "researcher", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	joined := stripAnsi(strings.Join(lines, "\n"))
	for _, want := range []string{"weave", "finn", "scout"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in output, got: %s", want, joined)
		}
	}
}

func TestRenderTreeOrbital_DropsOrbitalScaffolding(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
		{Name: "radar", Type: "engineer", Depth: 2},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	joined := stripAnsi(strings.Join(lines, "\n"))
	for _, glyph := range []string{"──●", "↳", " → ", " · "} {
		if strings.Contains(joined, glyph) {
			t.Errorf("expected no orbital scaffolding glyph %q, got: %s", glyph, joined)
		}
	}
}

func TestRenderTreeOrbital_OrderMatchesNodeSlice(t *testing.T) {
	// Input order is the canonical agentNames() order — weave first, then
	// DFS children. The renderer must preserve this order in the output.
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "alpha", Type: "engineer", Depth: 1},
		{Name: "bravo", Type: "engineer", Depth: 1},
		{Name: "charlie", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	stripped := stripAnsi(strings.Join(lines, " "))
	idxs := map[string]int{}
	for _, n := range []string{"weave", "alpha", "bravo", "charlie"} {
		idxs[n] = strings.Index(stripped, n)
		if idxs[n] < 0 {
			t.Fatalf("missing %q in %q", n, stripped)
		}
	}
	if idxs["weave"] >= idxs["alpha"] || idxs["alpha"] >= idxs["bravo"] || idxs["bravo"] >= idxs["charlie"] {
		t.Errorf("expected weave < alpha < bravo < charlie order in %q", stripped)
	}
}

func TestRenderTreeOrbital_SelectionPill_RendersReverseAndCyan(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, InTurn: true},
	}
	lines := RenderTreeOrbital(nodes, "finn", 200)
	out := strings.Join(lines, "\n")
	expected := lipgloss.NewStyle().
		Reverse(true).
		Foreground(lipgloss.Color("#0B0B12")).
		Background(lipgloss.Color("#22D3EE")).
		Bold(true).
		Padding(0, 1).
		Render("finn ⚙")
	if !strings.Contains(out, expected) {
		t.Errorf("expected selReverseStyle pill for selected 'finn ⚙'; raw:\n%q", out)
	}
}

func TestRenderTreeOrbital_NoSelection_NoReverseSGR(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	out := strings.Join(lines, "\n")
	reverseRe := regexp.MustCompile(`\x1b\[(?:[\d;]*;)?7[m;]`)
	if reverseRe.MatchString(out) {
		t.Errorf("did not expect SGR Reverse with empty selection; raw:\n%q", out)
	}
}

func TestRenderTreeOrbital_PillLabel_NameAndGlyph(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, InTurn: true},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	joined := stripAnsi(strings.Join(lines, "\n"))
	// Pill label is "<name> <glyph>" — weave gets ●, finn gets ⚙ (working).
	if !strings.Contains(joined, "weave ●") {
		t.Errorf("expected 'weave ●' pill, got: %s", joined)
	}
	if !strings.Contains(joined, "finn ⚙") {
		t.Errorf("expected 'finn ⚙' pill, got: %s", joined)
	}
}

func TestRenderTreeOrbital_WidthRespected(t *testing.T) {
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	for _, n := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		nodes = append(nodes, TreeNode{Name: n, Type: "engineer", Depth: 1})
	}
	lines := RenderTreeOrbital(nodes, "", 80)
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("lines[%d] width = %d, want 80; line=%q", i, w, l)
		}
	}
}

func TestRenderTreeOrbital_OverflowTruncates(t *testing.T) {
	// Many long pills must overflow the row budget; expect an ellipsis or
	// a missing late sibling.
	siblings := []string{"alphalonger", "bravolonger", "charlielonger", "deltalonger", "echolonger", "foxtrotlong", "golflonger", "hotellonger"}
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	for _, n := range siblings {
		nodes = append(nodes, TreeNode{Name: n, Type: "engineer", Depth: 1})
	}
	lines := RenderTreeOrbital(nodes, "", 60)
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("lines[%d] width = %d, exceeds budget 60", i, w)
		}
	}
	stripped := stripAnsi(strings.Join(lines, "\n"))
	hasEllipsis := strings.Contains(stripped, "…")
	lateMissing := false
	for _, n := range siblings[len(siblings)-2:] {
		if !strings.Contains(stripped, n) {
			lateMissing = true
			break
		}
	}
	if !hasEllipsis && !lateMissing {
		t.Errorf("expected truncation (ellipsis or missing tail), got: %s", stripped)
	}
}

func TestRenderTreeOrbital_Empty(t *testing.T) {
	lines := RenderTreeOrbital(nil, "", 80)
	if len(lines) == 0 {
		t.Fatal("expected blank-padded output for empty node list, got 0 lines")
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("lines[%d] width = %d, want 80 (blank-padded)", i, w)
		}
		if strings.TrimSpace(stripAnsi(l)) != "" {
			t.Errorf("lines[%d] stripped should be blank, got: %q", i, stripAnsi(l))
		}
	}
}

func TestRenderTreeOrbital_ZeroWidth_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderTreeOrbital(_, _, 0) panicked: %v", r)
		}
	}()
	lines := RenderTreeOrbital([]TreeNode{{Name: "weave", Type: "weave"}}, "", 0)
	if len(lines) != 0 {
		t.Errorf("expected empty slice for width=0, got %d lines", len(lines))
	}
}

// QUM-733 5a: OrbitalHeight stays a function of (width, nodes) where the
// result equals len(RenderTreeOrbital(nodes, "", width)). Header row budget
// caps at WordmarkHeight (3 wide / 1 narrow); excess pills truncate.
func TestOrbitalHeight_ParityWithRender(t *testing.T) {
	topologies := map[string][]TreeNode{
		"weave-only": {
			{Name: "weave", Type: "weave", Depth: 0},
		},
		"weave+engineer": {
			{Name: "weave", Type: "weave", Depth: 0},
			{Name: "finn", Type: "engineer", Depth: 1},
		},
		"weave+three-children": {
			{Name: "weave", Type: "weave", Depth: 0},
			{Name: "alpha", Type: "engineer", Depth: 1},
			{Name: "bravo", Type: "engineer", Depth: 1},
			{Name: "charlie", Type: "engineer", Depth: 1},
		},
	}
	for name, nodes := range topologies {
		for _, w := range []int{40, 60, 90, 120, 200} {
			got := OrbitalHeight(w, nodes)
			rendered := len(RenderTreeOrbital(nodes, "", w))
			if got != rendered {
				t.Errorf("topology=%s width=%d: OrbitalHeight=%d, len(RenderTreeOrbital)=%d", name, w, got, rendered)
			}
		}
	}
}

func TestOrbitalHeight_Boundary(t *testing.T) {
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	cases := []struct {
		width, want int
	}{
		{0, 0},
		{69, 1},
		{70, 3},
		{200, 3},
	}
	for _, tc := range cases {
		if got := OrbitalHeight(tc.width, nodes); got != tc.want {
			t.Errorf("OrbitalHeight(%d, weave-only) = %d, want %d", tc.width, got, tc.want)
		}
	}
}

func TestTreeNodeAgentState_Classifier(t *testing.T) {
	now := time.Now()
	ptr := func(b bool) *bool { return &b }
	cases := []struct {
		name string
		node TreeNode
		want AgentState
	}{
		{"weave type → Root", TreeNode{Name: "weave", Type: "weave"}, StateRoot},
		{"working self-report no longer special-cased (no liveness)", TreeNode{Name: "a", Type: "engineer", LastReportState: "working"}, StateIdle},
		{"complete state", TreeNode{Name: "a", Type: "engineer", LastReportState: "complete"}, StateDone},
		{"blocked state", TreeNode{Name: "a", Type: "engineer", LastReportState: "blocked"}, StateBlocked},
		{"failure state", TreeNode{Name: "a", Type: "engineer", LastReportState: "failure"}, StateFailure},
		{"empty state → Idle", TreeNode{Name: "a", Type: "engineer", LastReportState: ""}, StateIdle},
		{"unknown state → Idle", TreeNode{Name: "a", Type: "engineer", LastReportState: "bogus"}, StateIdle},
		{"fault on idle (empty state) → Failure", TreeNode{Name: "a", Type: "engineer", LastReportState: "", FaultClass: "HangTimeout"}, StateFailure},
		{"fault overrides working", TreeNode{Name: "a", Type: "engineer", LastReportState: "working", FaultClass: "HangTimeout"}, StateFailure},
		{"fault overrides complete", TreeNode{Name: "a", Type: "engineer", LastReportState: "complete", FaultClass: "HangTimeout"}, StateFailure},
		{"in_autonomous_turn → Working", TreeNode{Name: "a", Type: "engineer", InTurn: true}, StateWorking},
		{"recent activity → Working", TreeNode{Name: "a", Type: "engineer", LastActivityAt: now.Add(-1 * time.Second)}, StateWorking},
		{"recent activity beats stale blocked report (QUM-665 repro)", TreeNode{Name: "a", Type: "engineer", LastReportState: "blocked", LastActivityAt: now.Add(-1 * time.Second)}, StateWorking},
		{"process_alive=false stays Idle", TreeNode{Name: "a", Type: "engineer", ProcessAlive: ptr(false), InTurn: true, LastReportState: "working"}, StateIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TreeNodeAgentState(tc.node, now)
			if got != tc.want {
				t.Errorf("TreeNodeAgentState(%+v) = %v, want %v", tc.node, got, tc.want)
			}
		})
	}
}

// Keep an unused-import-style hint for tea so test infra remains familiar.
var _ = tea.KeyPressMsg{}
