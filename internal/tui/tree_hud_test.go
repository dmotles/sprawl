package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// strikeRe matches a strikethrough (SGR 9) parameter inside any escape
// sequence, whether emitted alone (\x1b[9m) or combined (\x1b[2;9m). The
// leading [ or ; guards against false matches like 39/49.
var strikeRe = regexp.MustCompile(`[\[;]9[;m]`)

// --- diffTreeNodes -------------------------------------------------------

func names(nodes []TreeNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return out
}

func TestDiffTreeNodes_AddedOnly(t *testing.T) {
	prev := []TreeNode{{Name: "a"}}
	next := []TreeNode{{Name: "a"}, {Name: "b", Type: "engineer"}}
	added, removed := diffTreeNodes(prev, next)
	if got := names(added); len(got) != 1 || got[0] != "b" {
		t.Errorf("added = %v, want [b]", got)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want []", names(removed))
	}
	if added[0].Type != "engineer" {
		t.Errorf("added node should carry full TreeNode (Type=%q), want engineer", added[0].Type)
	}
}

func TestDiffTreeNodes_RemovedOnly(t *testing.T) {
	prev := []TreeNode{{Name: "a"}, {Name: "b"}}
	next := []TreeNode{{Name: "a"}}
	added, removed := diffTreeNodes(prev, next)
	if len(added) != 0 {
		t.Errorf("added = %v, want []", names(added))
	}
	if got := names(removed); len(got) != 1 || got[0] != "b" {
		t.Errorf("removed = %v, want [b]", got)
	}
}

func TestDiffTreeNodes_Both(t *testing.T) {
	prev := []TreeNode{{Name: "a"}, {Name: "b"}}
	next := []TreeNode{{Name: "a"}, {Name: "c"}}
	added, removed := diffTreeNodes(prev, next)
	if got := names(added); len(got) != 1 || got[0] != "c" {
		t.Errorf("added = %v, want [c]", got)
	}
	if got := names(removed); len(got) != 1 || got[0] != "b" {
		t.Errorf("removed = %v, want [b]", got)
	}
}

func TestDiffTreeNodes_NoChange(t *testing.T) {
	prev := []TreeNode{{Name: "a"}, {Name: "b"}}
	next := []TreeNode{{Name: "a"}, {Name: "b"}}
	added, removed := diffTreeNodes(prev, next)
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("added=%v removed=%v, want both empty", names(added), names(removed))
	}
}

func TestDiffTreeNodes_NilPrevSeedsEverythingAsAdded(t *testing.T) {
	// Why the caller needs treeSeeded: with no previous tree, every node looks
	// freshly spawned.
	next := []TreeNode{{Name: "a"}, {Name: "b"}}
	added, removed := diffTreeNodes(nil, next)
	if len(added) != 2 {
		t.Errorf("added = %v, want both seeded nodes", names(added))
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want []", names(removed))
	}
}

// --- treeHud generation logic -------------------------------------------

func TestTreeHud_ZeroValueHidden(t *testing.T) {
	var h treeHud
	if h.visible {
		t.Error("zero-value treeHud should not be visible")
	}
}

func TestTreeHud_TriggerNavShowsAndBumpsGen(t *testing.T) {
	var h treeHud
	g1 := h.triggerNav()
	if !h.visible {
		t.Error("triggerNav should make the HUD visible")
	}
	g2 := h.triggerNav()
	if g2 <= g1 {
		t.Errorf("each triggerNav must bump the generation: g1=%d g2=%d", g1, g2)
	}
}

func TestTreeHud_TriggerChangeRecordsChange(t *testing.T) {
	var h treeHud
	h.triggerChange("ghost", hudChangeRetired)
	if !h.visible {
		t.Error("triggerChange should make the HUD visible")
	}
	if h.changes["ghost"] != hudChangeRetired {
		t.Errorf("changes[ghost] = %v, want retired", h.changes["ghost"])
	}
}

func TestTreeHud_TriggerChangeBumpsGen(t *testing.T) {
	var h treeHud
	g1 := h.triggerChange("a", hudChangeSpawned)
	g2 := h.triggerChange("b", hudChangeSpawned)
	if g1 == 0 {
		t.Error("triggerChange should return a non-zero generation token")
	}
	if g2 <= g1 {
		t.Errorf("each triggerChange must bump the generation: g1=%d g2=%d", g1, g2)
	}
	if g2 != h.gen {
		t.Errorf("triggerChange should return the current generation: returned %d, gen %d", g2, h.gen)
	}
}

func TestTreeHud_ExpireStaleGenIsNoop(t *testing.T) {
	var h treeHud
	g1 := h.triggerNav()
	h.triggerNav() // bumps to a newer generation
	if h.expire(g1) {
		t.Error("expire with a stale generation should return false (no hide)")
	}
	if !h.visible {
		t.Error("expire with a stale generation must NOT hide the HUD")
	}
}

func TestTreeHud_ExpireCurrentGenHidesAndClears(t *testing.T) {
	var h treeHud
	h.triggerChange("ghost", hudChangeRetired)
	g := h.triggerNav()
	if !h.expire(g) {
		t.Error("expire with the current generation should hide and return true")
	}
	if h.visible {
		t.Error("HUD should be hidden after expiring the current generation")
	}
	if len(h.changes) != 0 {
		t.Errorf("changes should be cleared on hide, got %v", h.changes)
	}
}

func TestTreeHud_TriggerNavPreservesPendingChange(t *testing.T) {
	// A spawn flash followed immediately by a Ctrl+N must NOT wipe the flash
	// highlight mid-display (oracle pitfall #3 — nav extends the timer only).
	var h treeHud
	h.triggerChange("baby", hudChangeSpawned)
	h.triggerNav()
	if h.changes["baby"] != hudChangeSpawned {
		t.Errorf("triggerNav must preserve pending change flash, got %v", h.changes["baby"])
	}
}

// --- hudRowStyle ---------------------------------------------------------

func TestHudRowStyle_DistinctPerKind(t *testing.T) {
	th := NewTheme("colour212")
	const sample = "x"
	neutral := hudRowStyle(&th, hudChangeNone, false).Render(sample)
	spawned := hudRowStyle(&th, hudChangeSpawned, false).Render(sample)
	retired := hudRowStyle(&th, hudChangeRetired, false).Render(sample)
	observed := hudRowStyle(&th, hudChangeNone, true).Render(sample)

	if spawned == neutral {
		t.Error("spawned row style should differ from neutral")
	}
	if retired == neutral {
		t.Error("retired row style should differ from neutral")
	}
	if observed == neutral {
		t.Error("observed row style should differ from neutral")
	}
	// Retired carries a strikethrough SGR (9), possibly combined with faint.
	if !strikeRe.MatchString(retired) {
		t.Errorf("retired style should be strikethrough, got %q", retired)
	}
}

// --- renderTreeHud -------------------------------------------------------

func TestRenderTreeHud_EmptyNodes(t *testing.T) {
	th := NewTheme("colour212")
	if got := renderTreeHud(treeHud{visible: true}, nil, "weave", &th, 30, 10); got != "" {
		t.Errorf("renderTreeHud with no nodes should be empty, got %q", got)
	}
}

func TestRenderTreeHud_ContainsNamesAndBoundedSize(t *testing.T) {
	th := NewTheme("colour212")
	nodes := []TreeNode{
		{Name: "weave", Type: "weave"},
		{Name: "tower", Type: "manager", Depth: 1},
		{Name: "finn", Type: "engineer", Depth: 2},
	}
	out := renderTreeHud(treeHud{visible: true}, nodes, "finn", &th, 40, 12)
	if out == "" {
		t.Fatal("renderTreeHud should produce output for non-empty nodes")
	}
	plain := stripANSI(out)
	for _, want := range []string{"weave", "tower", "finn"} {
		if !strings.Contains(plain, want) {
			t.Errorf("HUD render missing %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("HUD line width %d exceeds maxW 40: %q", w, stripANSI(line))
		}
	}
	if h := len(strings.Split(out, "\n")); h > 12 {
		t.Errorf("HUD height %d exceeds maxH 12", h)
	}
}

func TestRenderTreeHud_FlashHighlightsChangedNode(t *testing.T) {
	th := NewTheme("colour212")
	nodes := []TreeNode{{Name: "weave", Type: "weave"}, {Name: "ghost", Type: "engineer", Depth: 1}}
	h := treeHud{visible: true, changes: map[string]hudChangeKind{"ghost": hudChangeRetired}}
	out := renderTreeHud(h, nodes, "weave", &th, 40, 12)
	// Retired flash should strike through the changed node's row.
	if !strikeRe.MatchString(out) {
		t.Errorf("retired flash should render a strikethrough row:\n%q", out)
	}
}

func TestRenderTreeHud_RetiredGhostRowWhenAbsentFromNodes(t *testing.T) {
	th := NewTheme("colour212")
	// "ghost" is NOT in nodes (it just retired and rebuildTree dropped it) but
	// is flagged retired — it must still render as a struck-through ghost row
	// so the dim+strikethrough is actually visible in the live path.
	nodes := []TreeNode{{Name: "weave", Type: "weave"}}
	h := treeHud{visible: true, changes: map[string]hudChangeKind{"ghost": hudChangeRetired}}
	out := renderTreeHud(h, nodes, "weave", &th, 40, 12)
	if !strings.Contains(stripANSI(out), "ghost") {
		t.Errorf("retired agent absent from nodes should still appear as a ghost row:\n%s", stripANSI(out))
	}
	if !strikeRe.MatchString(out) {
		t.Errorf("ghost row should be struck through:\n%q", out)
	}
}

// --- overlayTopRight -----------------------------------------------------

func baseGrid(rows, width int) string {
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = strings.Repeat(".", width)
	}
	return strings.Join(lines, "\n")
}

func TestOverlayTopRight_PreservesLineWidths(t *testing.T) {
	base := baseGrid(20, 60)
	panel := "┌──┐\n│hi│\n└──┘"
	out := overlayTopRight(base, panel, 3)
	baseLines := strings.Split(base, "\n")
	outLines := strings.Split(out, "\n")
	if len(outLines) != len(baseLines) {
		t.Fatalf("line count changed: got %d want %d", len(outLines), len(baseLines))
	}
	for i := range baseLines {
		if bw, ow := ansi.StringWidth(baseLines[i]), ansi.StringWidth(outLines[i]); bw != ow {
			t.Errorf("row %d width changed: base=%d out=%d", i, bw, ow)
		}
	}
}

func TestOverlayTopRight_PanelFlushRightAtAnchor(t *testing.T) {
	base := baseGrid(20, 60)
	panel := "ABCD"
	out := overlayTopRight(base, panel, 5)
	row := strings.Split(out, "\n")[5]
	if !strings.HasSuffix(row, "ABCD") {
		t.Errorf("panel should be flush-right at the anchor row, got %q", row)
	}
	// Rows other than the anchor are untouched.
	if strings.Split(out, "\n")[4] != strings.Repeat(".", 60) {
		t.Errorf("non-anchor row was modified")
	}
}

func TestOverlayTopRight_NeverWritesBottomTwoRows(t *testing.T) {
	base := baseGrid(8, 40)
	// A tall panel anchored low would spill into the bottom-2 reserve.
	panel := "X\nX\nX\nX\nX\nX"
	out := overlayTopRight(base, panel, 4)
	outLines := strings.Split(out, "\n")
	baseLines := strings.Split(base, "\n")
	n := len(baseLines)
	// Bottom two rows must be byte-identical to base.
	if outLines[n-1] != baseLines[n-1] || outLines[n-2] != baseLines[n-2] {
		t.Errorf("bottom two rows were modified:\nout[-2]=%q out[-1]=%q", outLines[n-2], outLines[n-1])
	}
}
