package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
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
	if got, want := OrbitalHeight(50, nodes), 1; got != want {
		t.Fatalf("OrbitalHeight(50, nodes) = %d, want %d", got, want)
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

// QUM-679 / QUM-686: when weave has manager-typed children, each manager gets
// its own row below weave, with that manager's engineers (depth>=2) rendered
// inline on the same row. Weave keeps row 0 (as the original spike layout
// shows). When the count of pivoted manager rows exceeds the remaining row
// budget (3 total rows minus weave's row 0 = 2 manager rows visible), the
// last visible row gets a `+K more (name, …)` overflow indicator.
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

	// Row 0 anchors weave (no inline orbits — all depth-1 children are
	// managers, which pivot to their own rows).
	row0 := stripAnsi(lines[0])
	if !strings.Contains(row0, "weave") {
		t.Errorf("row 0 must anchor weave; got: %q", row0)
	}
	for _, n := range []string{"bastion", "forge", "tower", "finn", "ratz", "moss", "scout", "zone", "ghost"} {
		if strings.Contains(row0, n) {
			t.Errorf("row 0 should not contain %q (manager subtree belongs on a later row); got: %q", n, row0)
		}
	}

	// Rows 1 and 2 each anchor one manager with that manager's engineers.
	type want struct {
		manager   string
		engineers []string
		notIn     []string
	}
	wants := []want{
		{"bastion", []string{"finn", "ratz"}, []string{"forge", "tower", "moss", "scout", "zone", "ghost"}},
		{"forge", []string{"moss", "scout"}, []string{"bastion", "finn", "ratz", "zone", "ghost"}},
	}
	for i, w := range wants {
		stripped := stripAnsi(lines[i+1])
		if !strings.Contains(stripped, w.manager) {
			t.Errorf("line %d should contain manager %q, got: %q", i+1, w.manager, stripped)
		}
		for _, e := range w.engineers {
			if !strings.Contains(stripped, e) {
				t.Errorf("line %d (%s) should contain engineer %q, got: %q", i+1, w.manager, e, stripped)
			}
		}
		for _, n := range w.notIn {
			if strings.Contains(stripped, n) {
				t.Errorf("line %d (%s) should NOT contain %q (belongs to sibling subtree), got: %q", i+1, w.manager, n, stripped)
			}
		}
	}

	// Row 2 should surface tower as overflow ("+1 more (tower)").
	row2 := stripAnsi(lines[2])
	if !strings.Contains(row2, "tower") {
		t.Errorf("row 2 should surface overflowed manager 'tower' via the `+K more` indicator; got: %q", row2)
	}
	if !strings.Contains(row2, "+1 more") {
		t.Errorf("row 2 should contain '+1 more' overflow indicator; got: %q", row2)
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

// QUM-686: the multi-agent topology that escaped QUM-679 — weave + a
// researcher sibling + a manager sibling, all at depth 1, none with their own
// children. Run through the REAL pipeline (buildTreeNodes → PrependWeaveRoot →
// RenderTreeOrbital) so we exercise what `tickAgentsCmd` actually feeds the
// renderer, not a hand-mocked TreeNode slice.
//
// Target per spike: weave stays as the row-0 anchor with the researcher
// orbiting it; the manager gets its own anchored row below. The QUM-679
// implementation discarded weave entirely and promoted EVERY direct child
// (researcher included) into a separate top-level row.
func TestRenderTreeOrbital_QUM686_LivePipeline_WeaveResearcherManager(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "weave", Type: "root", Parent: "", Status: "active"},
		{Name: "ghost", Type: "researcher", Parent: "weave", Status: "active"},
		{Name: "tower", Type: "manager", Parent: "weave", Status: "active"},
	}
	// Mirror tickAgentsCmd: filter the synthetic-twin weave entry before
	// PrependWeaveRoot re-adds it.
	filtered := make([]supervisor.AgentInfo, 0, len(agents))
	for _, a := range agents {
		if a.Name != "weave" {
			filtered = append(filtered, a)
		}
	}
	nodes := buildTreeNodes(filtered, nil)
	nodes = PrependWeaveRoot(nodes, "active", 0)

	lines := RenderTreeOrbital(nodes, "", 120)
	if len(lines) < 3 {
		t.Fatalf("expected 3 rendered rows, got %d", len(lines))
	}
	row0 := stripAnsi(lines[0])
	row1 := stripAnsi(lines[1])

	// Row 0 anchors weave with the researcher inline.
	if !strings.Contains(row0, "weave") {
		t.Errorf("row 0 must anchor weave; got %q", row0)
	}
	if !strings.Contains(row0, "ghost") {
		t.Errorf("row 0 must contain the researcher 'ghost' inline; got %q", row0)
	}
	if strings.Contains(row0, "tower") {
		t.Errorf("row 0 must NOT contain the manager 'tower' (it belongs on its own row); got %q", row0)
	}

	// Row 1 anchors the manager.
	if !strings.Contains(row1, "tower") {
		t.Errorf("row 1 must anchor the manager 'tower'; got %q", row1)
	}
	if strings.Contains(row1, "ghost") {
		t.Errorf("row 1 must NOT contain 'ghost' (it stays on weave's row); got %q", row1)
	}
}

// QUM-686: single-agent regression — when weave has only one engineer child
// (no managers), the layout must stay weave-anchored on a single row. Run
// through the real pipeline so we lock in the simple-case behavior.
func TestRenderTreeOrbital_QUM686_LivePipeline_SingleEngineer_Regression(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "weave", Type: "root", Parent: "", Status: "active"},
		{Name: "finn", Type: "engineer", Parent: "weave", Status: "active"},
	}
	filtered := make([]supervisor.AgentInfo, 0, len(agents))
	for _, a := range agents {
		if a.Name != "weave" {
			filtered = append(filtered, a)
		}
	}
	nodes := buildTreeNodes(filtered, nil)
	nodes = PrependWeaveRoot(nodes, "active", 0)

	lines := RenderTreeOrbital(nodes, "", 120)
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 rendered row, got %d", len(lines))
	}
	row0 := stripAnsi(lines[0])
	if !strings.Contains(row0, "weave") {
		t.Errorf("row 0 must anchor weave; got %q", row0)
	}
	if !strings.Contains(row0, "finn") {
		t.Errorf("row 0 must contain the engineer 'finn' inline; got %q", row0)
	}
	// Row 1 and 2 must be blank-only (no spurious anchors).
	if len(lines) >= 2 {
		row1 := stripAnsi(lines[1])
		if strings.TrimSpace(row1) != "" {
			t.Errorf("row 1 should be blank in single-engineer case; got %q", row1)
		}
	}
}

// QUM-688: at narrow widths (W < wordmarkNarrowThreshold), if weave has a
// manager-typed child the tree must render multi-row (weave on row 0, manager
// on its own row below) — NOT collapse to a single breadcrumb. The 2026-06-04
// live tmux capture (81-cell pane, tree budget < 70) hit the narrow branch and
// merged weave + researcher + manager into one line; QUM-686's pivot was
// unreachable.
func TestRenderTreeOrbital_NarrowKeepsManagerRows(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "ghost", Type: "researcher", Depth: 1},
		{Name: "tower", Type: "manager", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 40)
	if len(lines) < 2 {
		t.Fatalf("narrow + manager-child topology must render ≥2 rows; got %d row(s): %q", len(lines), lines)
	}
	row0 := stripAnsi(lines[0])
	if !strings.Contains(row0, "weave") {
		t.Errorf("row 0 must anchor weave; got: %q", row0)
	}
	// Manager 'tower' must appear on a row OTHER than row 0.
	if strings.Contains(row0, "tower") {
		t.Errorf("row 0 must NOT contain manager 'tower' (it belongs on its own row); got: %q", row0)
	}
	towerOnSomeOtherRow := false
	for i := 1; i < len(lines); i++ {
		if strings.Contains(stripAnsi(lines[i]), "tower") {
			towerOnSomeOtherRow = true
			break
		}
	}
	if !towerOnSomeOtherRow {
		t.Errorf("manager 'tower' must appear on a row distinct from weave's; lines: %q", lines)
	}
}

// QUM-688: width sweep. Same topology (weave + researcher + manager + a
// depth-2 engineer under the manager) through a range of widths. Whenever
// managers are present, the renderer must produce multi-row output and place
// the manager on its own row above some small minimum width.
func TestRenderTreeOrbital_WidthSweep_ManagerKeepsOwnRow(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "ghost", Type: "researcher", Depth: 1},
		{Name: "tower", Type: "manager", Depth: 1},
		{Name: "ratz", Type: "engineer", Depth: 2},
	}
	for _, w := range []int{20, 40, 60, 70, 90, 120} {
		lines := RenderTreeOrbital(nodes, "", w)
		if len(lines) < 2 {
			t.Errorf("width=%d: managers present must yield ≥2 rows; got %d: %q", w, len(lines), lines)
			continue
		}
		// Manager name on its own row (not on row 0).
		row0 := stripAnsi(lines[0])
		if strings.Contains(row0, "tower") {
			t.Errorf("width=%d: row 0 should not contain manager 'tower'; got: %q", w, row0)
		}
		found := false
		for i := 1; i < len(lines); i++ {
			if strings.Contains(stripAnsi(lines[i]), "tower") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("width=%d: manager 'tower' missing from non-row-0; lines: %q", w, lines)
		}
	}
}

// QUM-688: OrbitalHeight(width, nodes) must equal len(RenderTreeOrbital(nodes,
// "", width)) at every width. This is the parity invariant the layout sizing
// code depends on.
func TestOrbitalHeight_ParityWithRender(t *testing.T) {
	topologies := map[string][]TreeNode{
		"weave-only": {
			{Name: "weave", Type: "weave", Depth: 0},
		},
		"weave+engineer": {
			{Name: "weave", Type: "weave", Depth: 0},
			{Name: "finn", Type: "engineer", Depth: 1},
		},
		"weave+researcher+manager+engineer": {
			{Name: "weave", Type: "weave", Depth: 0},
			{Name: "ghost", Type: "researcher", Depth: 1},
			{Name: "tower", Type: "manager", Depth: 1},
			{Name: "ratz", Type: "engineer", Depth: 2},
		},
	}
	for name, nodes := range topologies {
		for _, w := range []int{20, 40, 60, 69, 70, 90, 120} {
			got := OrbitalHeight(w, nodes)
			rendered := len(RenderTreeOrbital(nodes, "", w))
			if got != rendered {
				t.Errorf("topology=%s width=%d: OrbitalHeight=%d, len(RenderTreeOrbital)=%d", name, w, got, rendered)
			}
		}
	}
}

// QUM-688: weave-only narrow case must still collapse to a single breadcrumb
// line — the narrow optimization is preserved when no managers force a pivot.
func TestRenderTreeOrbital_Narrow_WeaveOnly_StillSingleBreadcrumb(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	lines := RenderTreeOrbital(nodes, "", 40)
	if got, want := len(lines), 1; got != want {
		t.Fatalf("weave + non-manager engineer at narrow width must stay single breadcrumb; got %d rows: %q", got, lines)
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
	// Simple weave-only topology — narrow widths collapse to 1 row.
	nodes := []TreeNode{{Name: "weave", Type: "weave", Depth: 0}}
	cases := []struct {
		width, want int
	}{
		{0, 0},
		{69, 1},
		{70, 3},
		{120, 3},
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
