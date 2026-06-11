package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// QUM-806: gentle "breathing" brightness pulse on header orbital pills whose
// derived state is "working". A single shared phase counter (treePulseFrame)
// drives all working pills in unison; the pulse tick (treePulseTickMsg) is
// armed only while ≥1 working pill exists and goes silent when none.

// renderPhase renders a fixed working-pill label at the given pulse phase.
// Comparing rendered output (rather than just the foreground color) captures
// every style attribute — foreground AND any Faint/Bold toggles — so two
// visually distinct phases can never compare equal by accident.
func renderPhase(phase int) string {
	return treeWorkPulseStyle(phase).Render("kid ⚙")
}

// fgLuminance returns the Rec.601 luma of a pulse phase's foreground color,
// used to pin the dim→normal→bright direction of the breathe ramp.
func fgLuminance(phase int) float64 {
	r, g, b, _ := treeWorkPulseStyle(phase).GetForeground().RGBA()
	// RGBA() returns 16-bit premultiplied values in [0,65535].
	return 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
}

// --- Pure helper: anyWorkingPill --------------------------------------------

func TestAnyWorkingPill(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		nodes []TreeNode
		want  bool
	}{
		{
			name:  "one in-turn child",
			nodes: []TreeNode{{Name: "weave", Type: "weave"}, {Name: "kid", Type: "engineer", InTurn: true}},
			want:  true,
		},
		{
			name:  "all idle",
			nodes: []TreeNode{{Name: "weave", Type: "weave"}, {Name: "kid", Type: "engineer"}},
			want:  false,
		},
		{
			name:  "empty",
			nodes: nil,
			want:  false,
		},
		{
			// weave root is StateRoot, never StateWorking, even with a working
			// status string.
			name:  "only weave root",
			nodes: []TreeNode{{Name: "weave", Type: "weave", Status: "working"}},
			want:  false,
		},
		{
			name:  "recent activity path",
			nodes: []TreeNode{{Name: "kid", Type: "engineer", LastActivityAt: now.Add(-time.Second)}},
			want:  true,
		},
		{
			// A paused agent must not register as working even if it was
			// recently active (Liveness wins in DeriveIconState).
			name:  "paused not working",
			nodes: []TreeNode{{Name: "kid", Type: "engineer", Liveness: "paused", InTurn: true}},
			want:  false,
		},
		{
			// A dormant-revivable agent (disk Status == complete) is not working.
			name:  "dormant not working",
			nodes: []TreeNode{{Name: "kid", Type: "engineer", Status: state.StatusComplete}},
			want:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyWorkingPill(tc.nodes, now); got != tc.want {
				t.Errorf("anyWorkingPill = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- Pure helper: treeWorkPulseStyle ramp -----------------------------------

// TestTreeWorkPulseStyle_RampStructure pins the breathe shape: four phases
// where 0/1/2 are visually distinct and 1==3 (the "normal" anchor), so the
// cycle reads dim→normal→bright→normal rather than a hard two-state blink.
func TestTreeWorkPulseStyle_RampStructure(t *testing.T) {
	dim := renderPhase(0)
	normal := renderPhase(1)
	bright := renderPhase(2)
	normal2 := renderPhase(3)

	if dim == normal {
		t.Error("phase 0 (dim) and phase 1 (normal) must render differently")
	}
	if bright == normal {
		t.Error("phase 2 (bright) and phase 1 (normal) must render differently")
	}
	if dim == bright {
		t.Error("phase 0 (dim) and phase 2 (bright) must render differently")
	}
	if normal != normal2 {
		t.Error("phase 1 and phase 3 are both the breathe anchor and must render identically")
	}
}

// TestTreeWorkPulseStyle_RampDirection pins dim < normal < bright by luma so
// the cycle is a genuine fade-in/fade-out and not an arbitrary permutation.
func TestTreeWorkPulseStyle_RampDirection(t *testing.T) {
	if !(fgLuminance(0) < fgLuminance(1) && fgLuminance(1) < fgLuminance(2)) {
		t.Errorf("expected dim < normal < bright luminance, got %.0f, %.0f, %.0f",
			fgLuminance(0), fgLuminance(1), fgLuminance(2))
	}
}

func TestTreeWorkPulseStyle_WrapsModulo(t *testing.T) {
	if renderPhase(4) != renderPhase(0) {
		t.Error("phase 4 should wrap to phase 0")
	}
	if renderPhase(5) != renderPhase(1) {
		t.Error("phase 5 should wrap to phase 1")
	}
}

// --- RenderTreeOrbital phase behavior ---------------------------------------

func TestRenderTreeOrbital_PulsePhaseAffectsWorkingPill(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave"},
		{Name: "kid", Type: "engineer", InTurn: true},
		{Name: "rest", Type: "engineer"},
	}
	p0 := strings.Join(RenderTreeOrbital(nodes, "", 200, 0), "\n")
	p1 := strings.Join(RenderTreeOrbital(nodes, "", 200, 1), "\n")
	p2 := strings.Join(RenderTreeOrbital(nodes, "", 200, 2), "\n")
	p3 := strings.Join(RenderTreeOrbital(nodes, "", 200, 3), "\n")

	// Raw (ANSI-included) output differs between dim/bright phases: the
	// working pill's foreground escape changes.
	if p0 == p2 {
		t.Error("expected raw output to differ between pulse phase 0 and 2 (color change)")
	}
	// ANSI-stripped output is identical: only color changes, never the glyph
	// or layout (guards against hard blink / spinner glyph).
	if stripAnsi(p0) != stripAnsi(p2) {
		t.Errorf("ANSI-stripped output must be phase-invariant (no glyph/layout change):\n%q\nvs\n%q", stripAnsi(p0), stripAnsi(p2))
	}
	// Breathe anchor frames render identically.
	if p1 != p3 {
		t.Error("expected pulse phase 1 and 3 (both 'normal' anchor) to render identically")
	}
}

// TestRenderTreeOrbital_OnlyWorkingPillPulses verifies that, while a working
// pill drives the shared phase, a non-working sibling's rendered bytes stay
// stable across phases — only the working pill animates.
func TestRenderTreeOrbital_OnlyWorkingPillPulses(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave"},
		{Name: "kid", Type: "engineer", InTurn: true},
		{Name: "done", Type: "engineer", LastReportState: "complete"},
	}
	p0 := strings.Join(RenderTreeOrbital(nodes, "", 200, 0), "\n")
	p2 := strings.Join(RenderTreeOrbital(nodes, "", 200, 2), "\n")

	// The whole row changes (working pill pulsed)...
	if p0 == p2 {
		t.Fatal("expected the row to change across phases while a working pill is present")
	}
	// ...but the non-working sibling's exact styled bytes are present in both.
	doneStyled := treeDoneStyle.Render("done ✓")
	if !strings.Contains(p0, doneStyled) || !strings.Contains(p2, doneStyled) {
		t.Errorf("non-working 'done' pill must render statically across phases; expected %q in both frames", doneStyled)
	}
}

func TestRenderTreeOrbital_SelectedWorkingPillStatic(t *testing.T) {
	nodes := []TreeNode{
		{Name: "weave", Type: "weave"},
		{Name: "kid", Type: "engineer", InTurn: true},
	}
	p0 := strings.Join(RenderTreeOrbital(nodes, "kid", 200, 0), "\n")
	p2 := strings.Join(RenderTreeOrbital(nodes, "kid", 200, 2), "\n")
	if p0 != p2 {
		t.Error("a selected working pill keeps the static reverse-video style and must not pulse across phases")
	}
}

// --- AppModel gating: arm / disarm ------------------------------------------

func TestTreePulse_ArmsOnWorkingAgent(t *testing.T) {
	app := resizedApp(t, 200, 60)
	if app.treePulseTicking {
		t.Fatal("treePulseTicking should start false")
	}
	next, cmd := app.Update(AgentTreeMsg{Nodes: []TreeNode{{Name: "kid", Type: "engineer", InTurn: true}}})
	app = next.(AppModel)
	if !app.treePulseTicking {
		t.Error("expected treePulseTicking=true after a working AgentTreeMsg")
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd (the armed pulse tick) after a working AgentTreeMsg")
	}
}

func TestTreePulse_NoArmWhenNoWorkingAgent(t *testing.T) {
	app := resizedApp(t, 200, 60)
	next, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{{Name: "kid", Type: "engineer"}}})
	app = next.(AppModel)
	if app.treePulseTicking {
		t.Error("expected treePulseTicking to stay false with no working agent")
	}
}

func TestTreePulse_DoesNotDoubleArm(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.childNodes = []TreeNode{{Name: "kid", Type: "engineer", InTurn: true}}
	app.rebuildTree()
	app.treePulseTicking = true
	if cmd := app.armTreePulseCmd(); cmd != nil {
		t.Error("armTreePulseCmd must return nil when already ticking (no double-arm)")
	}
}

func TestTreePulseTick_AdvancesFrameWhileWorking(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.childNodes = []TreeNode{{Name: "kid", Type: "engineer", InTurn: true}}
	app.rebuildTree()
	app.treePulseTicking = true
	before := app.treePulseFrame
	next, cmd := app.Update(treePulseTickMsg{})
	app = next.(AppModel)
	if app.treePulseFrame != before+1 {
		t.Errorf("treePulseFrame = %d, want %d", app.treePulseFrame, before+1)
	}
	if cmd == nil {
		t.Error("expected the pulse tick to re-arm (non-nil cmd) while a working pill exists")
	}
	if !app.treePulseTicking {
		t.Error("expected treePulseTicking to stay true while working")
	}
}

func TestTreePulseTick_GoesSilentWhenNoWorkingPill(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.childNodes = []TreeNode{{Name: "kid", Type: "engineer"}}
	app.rebuildTree()
	app.treePulseTicking = true
	next, cmd := app.Update(treePulseTickMsg{})
	app = next.(AppModel)
	if app.treePulseTicking {
		t.Error("expected treePulseTicking=false once no working pill remains")
	}
	if cmd != nil {
		t.Error("expected nil cmd (tick goes silent) once no working pill remains")
	}
}

// TestTreePulse_DoesNotInvalidateBodyCache pins the QUM-769 property: the
// orbital header is composed OUTSIDE the body render cache, so advancing the
// pulse frame must re-render the (uncached) header without busting the cached
// body. Catches a future regression that folds the header into the cache and
// would silently freeze the pulse.
func TestTreePulse_DoesNotInvalidateBodyCache(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.childNodes = []TreeNode{{Name: "kid", Type: "engineer", InTurn: true}}
	app.rebuildTree()
	app.treePulseTicking = true
	_ = app.View()

	vpBefore := app.cache.viewport
	mainRowBefore := app.cache.mainRow
	composedKeyBefore := app.cache.composedKey
	contentBefore := app.View().Content

	next, _ := app.Update(treePulseTickMsg{})
	app = next.(AppModel)
	contentAfter := app.View().Content

	if app.cache.viewport != vpBefore {
		t.Error("viewport body cache must not change across a pulse tick")
	}
	if app.cache.mainRow != mainRowBefore {
		t.Error("mainRow body cache must not change across a pulse tick")
	}
	if app.cache.composedKey != composedKeyBefore {
		t.Error("composed body cache key must not change across a pulse tick (header lives outside the cache)")
	}
	if contentBefore == contentAfter {
		t.Error("expected the rendered content to change across a pulse tick (header pulse animated)")
	}
}
