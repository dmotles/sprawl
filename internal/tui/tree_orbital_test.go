package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestRenderTreeOrbital_SingleRoot(t *testing.T) {
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	lines := RenderTreeOrbital(nodes, "", 80)

	if got, want := len(lines), 3; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("lines[%d] width = %d, want 80; line=%q", i, w, l)
		}
	}
	joined := stripAnsi(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "weave") {
		t.Errorf("expected 'weave' in output, got:\n%s", joined)
	}
	if !strings.Contains(joined, "●") {
		t.Errorf("expected root anchor glyph '●' in output, got:\n%s", joined)
	}
}

func TestRenderTreeOrbital_RootNoChildren_NoAnchor(t *testing.T) {
	// QUM-657: when the root has no children, the ──● orbital anchor should
	// be suppressed and the status glyph appended directly to the root name.
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	lines := RenderTreeOrbital(nodes, "", 80)
	stripped := stripAnsi(strings.Join(lines, "\n"))

	if strings.Contains(stripped, "──") {
		t.Errorf("expected no ──● anchor when root has no children, got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "weave ●") {
		t.Errorf("expected 'weave ●' (name + glyph) when root has no children, got:\n%s", stripped)
	}
}

func TestRenderTreeOrbital_RootWithChildren_KeepsAnchor(t *testing.T) {
	// QUM-657: when the root has children, the ──● anchor is preserved.
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 100)
	stripped := stripAnsi(strings.Join(lines, "\n"))
	if !strings.Contains(stripped, "──●") {
		t.Errorf("expected ──● anchor when root has children, got:\n%s", stripped)
	}
}

func TestRenderTreeOrbital_RootWithChildren(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, LastReportState: "working"},
		{Name: "scout", Type: "researcher", Depth: 1, LastReportState: ""},
	}
	lines := RenderTreeOrbital(nodes, "", 100)

	if got, want := len(lines), 3; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	stripped := stripAnsi(lines[0])
	if !strings.Contains(stripped, "finn") || !strings.Contains(stripped, "scout") {
		t.Errorf("line 0 should contain both 'finn' and 'scout', got: %q", stripped)
	}
}

func TestRenderTreeOrbital_DeepNesting(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
		{Name: "radar", Type: "engineer", Depth: 2},
	}
	lines := RenderTreeOrbital(nodes, "", 120)

	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	stripped := stripAnsi(lines[0])
	if !strings.Contains(stripped, "radar") {
		t.Errorf("line 0 should contain 'radar', got: %q", stripped)
	}
	if !strings.Contains(stripped, "↳") {
		t.Errorf("line 0 should contain grandchild glyph '↳', got: %q", stripped)
	}
}

func TestRenderTreeOrbital_MultipleRoots(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "alpha", Type: "engineer", Depth: 1},
		{Name: "tower", Type: "weave", Depth: 0},
		{Name: "beta", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 120)
	if got, want := len(lines), 3; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	joined := stripAnsi(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "weave") {
		t.Errorf("expected root 'weave' in output, got:\n%s", joined)
	}
	if !strings.Contains(joined, "tower") {
		t.Errorf("expected root 'tower' in output, got:\n%s", joined)
	}
}

func TestRenderTreeOrbital_SelectionPill_RendersReverseAndCyan(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, InAutonomousTurn: true},
	}
	lines := RenderTreeOrbital(nodes, "finn", 100)
	out := strings.Join(lines, "\n")

	// The styled pill substring (lipgloss-built expected string) is the
	// canonical oracle — exact match implies reverse + cyan SGR are present.
	expected := lipgloss.NewStyle().
		Reverse(true).
		Foreground(lipgloss.Color("#0B0B12")).
		Background(lipgloss.Color("#22D3EE")).
		Bold(true).
		Padding(0, 1).
		Render("finn ⚙")
	if !strings.Contains(out, expected) {
		t.Errorf("expected exact selReverseStyle substring for 'finn ⚙'.\n want substring: %q\n raw out: %q", expected, out)
	}
}

func TestRenderTreeOrbital_NoSelection_NoPill(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 100)
	out := strings.Join(lines, "\n")

	// Match SGR Reverse (code 7) as a complete parameter — either alone
	// (`\x1b[7m`) or as a `;7;` / `;7m` within a chained CSI sequence. The
	// previous form `[\d;]*7[\dm;]` false-positived on color components
	// containing the digit 7 (e.g. RGB 167 in `#A78BFA`).
	reverseRe := regexp.MustCompile(`\x1b\[(?:[\d;]*;)?7[m;]`)
	if reverseRe.MatchString(out) {
		t.Errorf("did not expect SGR Reverse (selection pill) when selection is empty; raw:\n%q", out)
	}
}

func TestRenderTreeOrbital_OnlyOnePill(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, InAutonomousTurn: true},
		{Name: "scout", Type: "researcher", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "finn", 100)
	out := strings.Join(lines, "\n")

	expected := lipgloss.NewStyle().
		Reverse(true).
		Foreground(lipgloss.Color("#0B0B12")).
		Background(lipgloss.Color("#22D3EE")).
		Bold(true).
		Padding(0, 1).
		Render("finn ⚙")
	if c := strings.Count(out, expected); c != 1 {
		t.Errorf("expected exactly 1 selection pill, got %d; raw:\n%q", c, out)
	}
}

func TestRenderTreeOrbital_NarrowWidth_SingleLine(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	if got, want := OrbitalHeight(50), 1; got != want {
		t.Fatalf("OrbitalHeight(50) = %d, want %d", got, want)
	}
	lines := RenderTreeOrbital(nodes, "", 50)
	if got, want := len(lines), 1; got != want {
		t.Fatalf("len(lines) = %d, want %d (narrow mode)", got, want)
	}
	if w := lipgloss.Width(lines[0]); w != 50 {
		t.Errorf("width = %d, want 50", w)
	}
	stripped := stripAnsi(lines[0])
	if !strings.Contains(stripped, "weave") {
		t.Errorf("narrow breadcrumb should contain root name 'weave', got: %q", stripped)
	}
}

func TestRenderTreeOrbital_RespectsWidthBudget_WideTruncation(t *testing.T) {
	siblings := []string{"alphalonger", "bravolonger", "charlielonger", "deltalonger", "echolonger", "foxtrotlong", "golflonger", "hotellonger", "indialonger", "julietlonger", "kilolonger", "limalonger"}
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	for _, n := range siblings {
		nodes = append(nodes, TreeNode{Name: n, Type: "engineer", Depth: 1})
	}
	lines := RenderTreeOrbital(nodes, "", 80)
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("lines[%d] width = %d, exceeds budget 80; line=%q", i, w, l)
		}
	}

	stripped := stripAnsi(strings.Join(lines, "\n"))
	// Trivially-empty render must not pass: the first sibling must appear.
	if !strings.Contains(stripped, siblings[0]) {
		t.Errorf("expected first sibling %q to appear in output, got:\n%s", siblings[0], stripped)
	}

	// Truncation must have occurred: either an ellipsis glyph appears OR at
	// least one late sibling is absent.
	hasEllipsis := strings.Contains(stripped, "…")
	lateMissing := false
	for _, n := range siblings[len(siblings)-3:] {
		if !strings.Contains(stripped, n) {
			lateMissing = true
			break
		}
	}
	if !hasEllipsis && !lateMissing {
		t.Errorf("expected truncation (ellipsis '…' or missing late sibling) given 12 long siblings in 80-cell budget; got:\n%s", stripped)
	}
}

// QUM-679: when weave has manager-typed children, each manager gets its own
// row, with that manager's engineers (depth>=2) rendered inline on the same
// row. This matches the spike's row-per-manager layout and replaces the
// previous "everything orbits weave on a single row" behavior.
func TestRenderTreeOrbital_WeavePivot_OneRowPerManager(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "bastion", Type: "manager", Depth: 1},
		{Name: "finn", Type: "engineer", Depth: 2},
		{Name: "ratz", Type: "engineer", Depth: 2},
		{Name: "forge", Type: "manager", Depth: 1},
		{Name: "moss", Type: "engineer", Depth: 2},
		{Name: "scout", Type: "engineer", Depth: 2},
		{Name: "tower", Type: "manager", Depth: 1},
		{Name: "zone", Type: "engineer", Depth: 2},
		{Name: "ghost", Type: "engineer", Depth: 2},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 rendered rows, got %d", len(lines))
	}

	// Each of the first 3 lines should be anchored on its manager and contain
	// only that manager's engineers.
	type want struct {
		manager   string
		engineers []string
		notIn     []string
	}
	wants := []want{
		{"bastion", []string{"finn", "ratz"}, []string{"forge", "tower", "moss", "scout", "zone", "ghost"}},
		{"forge", []string{"moss", "scout"}, []string{"bastion", "tower", "finn", "ratz", "zone", "ghost"}},
		{"tower", []string{"zone", "ghost"}, []string{"bastion", "forge", "finn", "ratz", "moss", "scout"}},
	}
	for i, w := range wants {
		stripped := stripAnsi(lines[i])
		if !strings.Contains(stripped, w.manager) {
			t.Errorf("line %d should contain manager %q, got: %q", i, w.manager, stripped)
		}
		for _, e := range w.engineers {
			if !strings.Contains(stripped, e) {
				t.Errorf("line %d (%s) should contain engineer %q, got: %q", i, w.manager, e, stripped)
			}
		}
		for _, n := range w.notIn {
			if strings.Contains(stripped, n) {
				t.Errorf("line %d (%s) should NOT contain %q (belongs to sibling subtree), got: %q", i, w.manager, n, stripped)
			}
		}
	}
}

// QUM-679: a single-engineer (no-managers) session must keep its existing
// weave-anchored layout — don't regress the simple case.
func TestRenderTreeOrbital_WeaveDirectEngineer_NoPivot(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 100)
	stripped := stripAnsi(strings.Join(lines, "\n"))
	if !strings.Contains(stripped, "weave") {
		t.Errorf("single-engineer session should still surface 'weave' anchor; got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "finn") {
		t.Errorf("single-engineer session should surface engineer name; got:\n%s", stripped)
	}
}

// QUM-679: pivot preserves weave's Unread badge by surfacing it as a dim
// prefix on the first manager row. This keeps the e2e notify-tui regex
// `weave[^│]*\([1-9]` matchable after pivot.
func TestRenderTreeOrbital_WeavePivot_PreservesWeaveUnreadBadge(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0, Unread: 4},
		{Name: "tower", Type: "manager", Depth: 1},
		{Name: "forge", Type: "manager", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 200)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 rows, got %d", len(lines))
	}
	// e2e regex literal: weave...(4)
	row0 := stripAnsi(lines[0])
	if !strings.Contains(row0, "weave") || !strings.Contains(row0, "(4)") {
		t.Errorf("first row should contain weave's unread badge `weave ... (4)`; got: %q", row0)
	}
	row1 := stripAnsi(lines[1])
	if strings.Contains(row1, "(4)") {
		t.Errorf("subsequent rows must not repeat weave's unread badge; got: %q", row1)
	}
}

// QUM-679: selection pill must render correctly on a manager row anchor.
func TestRenderTreeOrbital_WeavePivot_SelectionOnManager(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "tower", Type: "manager", Depth: 1, InAutonomousTurn: true},
		{Name: "forge", Type: "manager", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "tower", 200)
	out := strings.Join(lines, "\n")
	expected := lipgloss.NewStyle().
		Reverse(true).
		Foreground(lipgloss.Color("#0B0B12")).
		Background(lipgloss.Color("#22D3EE")).
		Bold(true).
		Padding(0, 1).
		Render("tower ⚙")
	if !strings.Contains(out, expected) {
		t.Errorf("expected selReverseStyle pill for selected manager 'tower'; raw:\n%q", out)
	}
}

func TestRenderTreeOrbital_Empty(t *testing.T) {
	lines := RenderTreeOrbital(nil, "", 80)
	if got, want := len(lines), 3; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
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

func TestRenderTreeOrbital_ZeroWidth(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderTreeOrbital(nil, \"\", 0) panicked: %v", r)
		}
	}()
	lines := RenderTreeOrbital([]TreeNode{{Name: "weave", Type: "weave"}}, "", 0)
	if len(lines) != 0 {
		t.Errorf("expected empty/nil slice for width=0, got %d lines", len(lines))
	}
}

func TestOrbitalHeight_Boundary(t *testing.T) {
	cases := []struct {
		width, want int
	}{
		{0, 0},
		{69, 1},
		{70, 3},
		{120, 3},
	}
	for _, tc := range cases {
		if got := OrbitalHeight(tc.width); got != tc.want {
			t.Errorf("OrbitalHeight(%d) = %d, want %d", tc.width, got, tc.want)
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
		{"in_autonomous_turn → Working", TreeNode{Name: "a", Type: "engineer", InAutonomousTurn: true}, StateWorking},
		{"recent activity → Working", TreeNode{Name: "a", Type: "engineer", LastActivityAt: now.Add(-1 * time.Second)}, StateWorking},
		{"recent activity beats stale blocked report (QUM-665 repro)", TreeNode{Name: "a", Type: "engineer", LastReportState: "blocked", LastActivityAt: now.Add(-1 * time.Second)}, StateWorking},
		{"process_alive=false stays Idle", TreeNode{Name: "a", Type: "engineer", ProcessAlive: ptr(false), InAutonomousTurn: true, LastReportState: "working"}, StateIdle},
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
