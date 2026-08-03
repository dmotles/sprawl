package tui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// QUM-832 — a pending (in-zone) user prompt renders DIM to signal "queued / not
// yet echoed"; it brightens to normal styling when it settles into the committed
// transcript on its consume ack. These tests pin the styling state flip at the
// UserItem layer (pending flag) and the ChatList zone→settle integration
// (including the render-cache staleness guard the oracle flagged).

// A UserItem defaults to bright (committed) styling; SetZonePending(true) renders it
// dim. The two differ only in styling, never in text/layout.
func TestUserItem_PendingRendersDimNotBright(t *testing.T) {
	ctx := newTestCtx()

	bright := NewUserItem(ctx, "hello world")
	if bright.pending {
		t.Fatal("NewUserItem must default to pending=false (committed bubbles render bright)")
	}
	brightOut := bright.Render(80)

	dim := NewUserItem(ctx, "hello world")
	dim.SetZonePending(true)
	dimOut := dim.Render(80)

	if dimOut == brightOut {
		t.Errorf("pending render must differ from bright render (dim styling); both were:\n%q", brightOut)
	}
	if stripAnsi(dimOut) != stripAnsi(brightOut) {
		t.Errorf("pending vs bright must differ ONLY in styling, not text:\ndim=%q\nbright=%q",
			stripAnsi(dimOut), stripAnsi(brightOut))
	}
}

// A zone (pending) user bubble renders dim, distinct from a committed bright
// bubble; after ZoneSettle the relocated bubble brightens to the exact bright
// rendering — proving (a) the styling flips and (b) the per-envelope render
// cache does not serve a stale dim string after settle.
func TestChatList_ZoneUserBubble_DimThenBrightOnSettle(t *testing.T) {
	// Bright reference: a committed AppendUser bubble at the same width.
	ref := newTestChatList()
	ref.SetSize(80)
	ref.AppendUser("pending prompt")
	bright := ref.Render(80)

	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddUser("u1", "pending prompt")
	dim := cl.Render(80)

	if dim == bright {
		t.Errorf("zone (pending) user bubble must render DIM, distinct from a committed bright bubble")
	}
	if stripAnsi(dim) != stripAnsi(bright) {
		t.Errorf("dim vs bright must differ ONLY in styling, not text:\ndim=%q\nbright=%q",
			stripAnsi(dim), stripAnsi(bright))
	}

	cl.ZoneSettle("u1")
	settled := cl.Render(80)
	if settled != bright {
		t.Errorf("settled bubble must brighten to normal styling (no stale dim cache):\nsettled=%q\nbright=%q",
			settled, bright)
	}
}

// QUM-925 — the same dim→bright treatment for SYSTEM notification entries held
// in the pending zone. Un-consumed system frames must be visually distinct from
// consumed ones (LOCKED requirement).
//
// QUM-925 amendment (F3, dmotles): the distinction must NOT be SGR-only. Faint
// (SGR 2) is advisory — a terminal that ignores it would render pending and
// consumed identically, silently voiding a LOCKED requirement. So the delta is
// faint PLUS a structural gutter marker (`┊` pending / `│` consumed), and the
// two assertions below are in tension by design: assertDimIsFaintDelta demands
// faint be added, assertPendingSurvivesSGRStrip demands the distinction not
// DEPEND on it. Both must hold.

// fgSGRPattern extracts a 256-color foreground SGR parameter (e.g. "38;5;245")
// from a rendered string, so a test can assert the dim variant preserved the
// bright variant's foreground and differs only by an added attribute.
var fgSGRPattern = regexp.MustCompile(`38;5;\d+`)

// sgrSeqPattern matches a whole SGR escape sequence so its parameters can be
// collected.
var sgrSeqPattern = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// sgrParams returns the set of SGR parameters present anywhere in a rendered
// string. Splitting a composite foreground ("38;5;245") into its parts is fine
// here: the caller only compares two renders of the SAME content, so a changed
// foreground shows up as a set difference either way.
func sgrParams(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range sgrSeqPattern.FindAllStringSubmatch(s, -1) {
		for _, p := range strings.Split(m[1], ";") {
			if p != "" {
				out[p] = true
			}
		}
	}
	return out
}

// assertDimIsFaintDelta pins the LOCKED requirement precisely: the pending
// render must add SGR 2 (faint) and change NOTHING else. Asserting "the renders
// differ" is not enough — Underline(true) or Reverse(true) also differ, and both
// make the pending row MORE prominent than the committed one, the exact inverse
// of the requirement.
func assertDimIsFaintDelta(t *testing.T, dim, bright string) {
	t.Helper()
	const faint = "2"
	dimP, brightP := sgrParams(dim), sgrParams(bright)
	// The faint leg reads ATTRIBUTE params only, never the flattened union: under a
	// truecolor profile lipgloss emits `38;2;R;G;B`, whose "2" is a colour-space
	// selector. Flattened, that "2" satisfies "faint is set" in BOTH renders and
	// the bidirectional diff below is then satisfied by a no-op with no faint at
	// all — the whole assertion would go quiet on a palette change alone.
	if !sgrAttrs(dim)[faint] {
		t.Errorf("dim render does not set SGR %q (faint) as an ATTRIBUTE — it is not dimmed: %q", faint, dim)
	}
	for p := range dimP {
		if !brightP[p] && p != faint {
			t.Errorf("dim render added SGR %q; the only permitted addition is %q (faint):\ndim=%q\nbright=%q",
				p, faint, dim, bright)
		}
	}
	for p := range brightP {
		if !dimP[p] {
			t.Errorf("dim render dropped SGR %q present in the bright render:\ndim=%q\nbright=%q",
				p, dim, bright)
		}
	}
}

// sgrAttrs returns the ATTRIBUTE params present in a rendered string, skipping the
// arguments of extended colour selectors: `38`/`48` consume either `5;<idx>` or
// `2;<r>;<g>;<b>`, so those digits are colour data and must not be mistaken for
// attributes (SGR 2 is faint; SGR 5 is blink).
func sgrAttrs(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range sgrSeqPattern.FindAllStringSubmatch(s, -1) {
		params := strings.Split(m[1], ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "38", "48", "58":
				if i+1 < len(params) && params[i+1] == "5" {
					i += 2 // 5;<idx>
				} else if i+1 < len(params) && params[i+1] == "2" {
					i += 4 // 2;<r>;<g>;<b>
				}
			case "":
			default:
				out[params[i]] = true
			}
		}
	}
	return out
}

// assertPendingSurvivesSGRStrip pins the F3 amendment mechanically: with EVERY
// SGR sequence removed from both renders, the remaining plain text must still
// differ. That is what "distinguishable on a terminal that ignores faint" means,
// and no amount of styling can satisfy it — only a structural differentiator can.
// Complement to assertDimIsFaintDelta, which by construction cannot detect that
// faint is the ONLY differentiator.
func assertPendingSurvivesSGRStrip(t *testing.T, dim, bright string) {
	t.Helper()
	dimPlain, brightPlain := sgrSeqPattern.ReplaceAllString(dim, ""), sgrSeqPattern.ReplaceAllString(bright, "")
	if dimPlain == brightPlain {
		t.Errorf("with all SGR stripped the pending and consumed renders are identical — the distinction is SGR-only and vanishes on a terminal that ignores faint:\n%q", dimPlain)
	}
}

// assertOnlyTheGutterDiffers pins that the structural differentiator is exactly
// the gutter marker and nothing else: substituting the pending gutter back to the
// consumed one must reproduce the consumed plain text byte for byte. That bounds
// the change — it does NOT establish distinctness (it passes vacuously when the
// two renders are equal), which is assertPendingSurvivesSGRStrip's job, and it
// cannot see whether the marker is VISIBLE, which is
// TestGutterConstants_AreVisiblyDistinctSingleCells' job. All three are needed.
//
// Substitution is anchored to the START OF EACH LINE, not global: a notification
// body is agent-authored and can legitimately contain box-drawing characters
// (pasted `tree` or table output), and an unanchored ReplaceAll would rewrite
// those too and fail a correct implementation.
func assertOnlyTheGutterDiffers(t *testing.T, dim, bright string) {
	t.Helper()
	dimPlain := sgrSeqPattern.ReplaceAllString(dim, "")
	brightPlain := sgrSeqPattern.ReplaceAllString(bright, "")
	if got := normalizeLeadingGutter(dimPlain); got != brightPlain {
		t.Errorf("pending render differs from consumed beyond the gutter marker (text/wrapping must be untouched):\npending(gutter-normalized)=%q\nconsumed=%q", got, brightPlain)
	}
}

// countLeadingGutters counts lines whose first cell (after SGR removal) is the
// given gutter. Line-anchored, so a gutter character inside a body cannot inflate
// the count.
func countLeadingGutters(rendered, gutter string) int {
	n := 0
	for _, ln := range strings.Split(sgrSeqPattern.ReplaceAllString(rendered, ""), "\n") {
		if strings.HasPrefix(ln, gutter) {
			n++
		}
	}
	return n
}

// normalizeLeadingGutter rewrites a leading pending gutter to the committed one
// on every line, leaving any occurrence inside body text alone.
func normalizeLeadingGutter(plain string) string {
	lines := strings.Split(plain, "\n")
	for i, ln := range lines {
		if rest, ok := strings.CutPrefix(ln, pendingGutter); ok {
			lines[i] = committedGutter + rest
		}
	}
	return strings.Join(lines, "\n")
}

// The gutter constants carry the whole non-SGR half of the requirement, and the
// other assertions in this file are structurally unable to audit them:
// assertOnlyTheGutterDiffers NORMALIZES pendingGutter away (so it cannot fail for
// any value of it), and assertPendingSurvivesSGRStrip only demands the bytes
// differ (so a zero-width space, a variation selector, or a doubled glyph all
// satisfy it while being invisible or misaligned in a real terminal — the exact
// silent-degradation class F3 exists to prevent). So assert the constants
// directly, against the requirement rather than against each other.
func TestGutterConstants_AreVisiblyDistinctSingleCells(t *testing.T) {
	for name, g := range map[string]string{"committedGutter": committedGutter, "pendingGutter": pendingGutter} {
		r := []rune(g)
		// Exactly one rune: rejects "​│" (ZWSP prefix), "│︎"
		// (variation selector) and "┊┊" (shifts the row by a cell).
		if len(r) != 1 {
			t.Errorf("%s = %q is %d runes; the gutter must be exactly one rune so it cannot hide an invisible codepoint or shift the row", name, g, len(r))
			continue
		}
		if !unicode.IsGraphic(r[0]) || unicode.IsSpace(r[0]) {
			t.Errorf("%s = %q is not a visible graphic character — an invisible marker is not a differentiator", name, g)
		}
		if w := ansi.StringWidth(g); w != 1 {
			t.Errorf("%s = %q has display width %d, want 1 (both gutters must occupy the same single cell or the body misaligns)", name, g, w)
		}
	}
	if pendingGutter == committedGutter {
		t.Fatalf("pendingGutter == committedGutter (%q) — there is no structural differentiator at all", pendingGutter)
	}
	// Direction, not just difference: a COMMITTED notification row is ordinary
	// transcript chrome and must use the same solid gutter as the rest of the
	// committed transcript (see the `"│ "` prefixes in items.go / render_helpers.go),
	// so the PENDING row is the deviation. Swapping the two constants leaves every
	// other assertion in this file green while inverting the affordance — pending
	// would render as the settled state and vice versa.
	if committedGutter != "│" {
		t.Errorf("committedGutter = %q, want %q — a committed row must match the transcript's solid gutter, leaving the pending row as the deviation", committedGutter, "│")
	}
}

// sgrAttrs must not mistake colour data for an attribute. This carries its own
// control: the truecolor sequence contains the digit 2 (as the colour-space
// selector) and the digit 5, and neither may register as faint or blink, while a
// genuine leading 2 must.
func TestSgrAttrs_IgnoresExtendedColourArguments(t *testing.T) {
	cases := []struct {
		name  string
		seq   string
		faint bool
	}{
		{"truecolor fg, no faint", "\x1b[38;2;34;211;238mx\x1b[m", false},
		{"256-colour fg, no faint", "\x1b[38;5;245mx\x1b[m", false},
		{"truecolor fg WITH faint", "\x1b[2;38;2;34;211;238mx\x1b[m", true},
		{"256-colour fg WITH faint", "\x1b[2;38;5;245mx\x1b[m", true},
		{"bold only", "\x1b[1;38;5;245mx\x1b[m", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sgrAttrs(tc.seq)["2"]; got != tc.faint {
				t.Errorf("sgrAttrs(%q) reports faint=%v, want %v", tc.seq, got, tc.faint)
			}
			// The flattened view is what this replaces — shown failing on the very
			// case that motivated it, so the improvement is not merely asserted.
			if !tc.faint && tc.name == "truecolor fg, no faint" && !sgrParams(tc.seq)["2"] {
				t.Error("setup: sgrParams was expected to be fooled by the truecolor 2 — if it no longer is, sgrAttrs' rationale needs revisiting")
			}
		})
	}
}

// A SystemNotificationItem defaults to bright (committed) styling; SetZonePending
// (true) renders it dim. Table-driven over every style class the
// notificationGlyphAndStyle branch selects, so the faint delta is total over the
// branch rather than wired for one class — and each class keeps its own
// foreground, so dimming cannot collapse the classes into one flat gray.
func TestSystemNotificationItem_PendingRendersDimNotBright(t *testing.T) {
	ctx := newTestCtx()
	cases := []struct {
		name      string
		notifType string
		interrupt bool
	}{
		{"status_change", NotificationKindStatusChange, false},
		// The selector ignores Interrupt on the status_change arm; pin that the
		// pending treatment is total over that arm too rather than assuming it.
		{"status_change_interrupt", NotificationKindStatusChange, true},
		{"liveness_check", NotificationKindLivenessCheck, false},
		{"message_interrupt", NotificationKindMessage, true},
		{"message_async", NotificationKindMessage, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bright := NewSystemNotificationItem(ctx, "alpha → working", tc.notifType, tc.interrupt)
			if bright.pending {
				t.Fatal("NewSystemNotificationItem must default to pending=false (committed entries render bright)")
			}
			brightOut := bright.Render(80)
			if !strings.Contains(stripAnsi(brightOut), "alpha → working") {
				t.Fatalf("setup: bright render lost the body — instrument is dead: %q", brightOut)
			}

			dim := NewSystemNotificationItem(ctx, "alpha → working", tc.notifType, tc.interrupt)
			dim.SetZonePending(true)
			dimOut := dim.Render(80)

			if dimOut == brightOut {
				t.Errorf("pending render must differ from bright render (dim styling); both were:\n%q", brightOut)
			}
			// F3: the distinction must survive a terminal that ignores SGR 2…
			assertPendingSurvivesSGRStrip(t, dimOut, brightOut)
			// …and the structural part must be ONLY the gutter marker, so the
			// body text, wrapping and alignment are untouched.
			assertOnlyTheGutterDiffers(t, dimOut, brightOut)
			// The dim variant keeps the class's own foreground, so dimming cannot
			// flatten the classes into one gray…
			brightFg := fgSGRPattern.FindString(brightOut)
			if brightFg == "" {
				t.Fatalf("setup: no 256-color foreground in the bright render: %q", brightOut)
			}
			if !strings.Contains(dimOut, brightFg) {
				t.Errorf("dim render dropped the class foreground %q (the styling delta must be additive): %q",
					brightFg, dimOut)
			}
			// …and the delta is FAINT specifically, not merely "some other SGR".
			assertDimIsFaintDelta(t, dimOut, brightOut)
		})
	}
}

// The structural marker must hold at narrow widths too, where
// formatSystemMessage's wrap budget (width-4) actually wraps.
//
// Honest scope: today the gutter is concatenated AHEAD of formatSystemMessage's
// output, so the wrap input is byte-identical between the two branches and this
// test cannot fail for a wrap reason — it is a REGRESSION GUARD for the refactor
// that moves the marker into the wrapped text (e.g. per-line gutters), which is
// exactly when a shifted wrap point becomes possible. It does assert that the
// body genuinely wrapped, so it cannot silently degrade into a width-independent
// check if formatSystemMessage stops wrapping.
func TestSystemNotificationItem_PendingGutter_SurvivesNarrowWidths(t *testing.T) {
	ctx := newTestCtx()
	const body = "alpha → working on a considerably longer status line that must wrap"
	for _, width := range []int{12, 20, 40, 80} {
		bright := NewSystemNotificationItem(ctx, body, NotificationKindStatusChange, false)
		brightOut := bright.Render(width)
		if !strings.Contains(stripAnsi(brightOut), "alpha") {
			t.Fatalf("width %d: setup: consumed render lost the body — instrument is dead: %q", width, brightOut)
		}
		dim := NewSystemNotificationItem(ctx, body, NotificationKindStatusChange, false)
		dim.SetZonePending(true)
		dimOut := dim.Render(width)

		t.Run("w"+strconv.Itoa(width), func(t *testing.T) {
			assertPendingSurvivesSGRStrip(t, dimOut, brightOut)
			assertOnlyTheGutterDiffers(t, dimOut, brightOut)
		})
	}
}

// Degenerate bodies must not collapse the distinction: an empty body still yields
// a marked row, and a body that itself contains box-drawing characters (agents
// paste `tree` / table output) must not be mistaken for the gutter by either the
// implementation or the assertions.
func TestSystemNotificationItem_PendingGutter_DegenerateBodies(t *testing.T) {
	ctx := newTestCtx()
	for _, body := range []string{"", "   ", "tree: a┊b", "pipe │ inside", "a\nb"} {
		bright := NewSystemNotificationItem(ctx, body, NotificationKindStatusChange, false)
		dim := NewSystemNotificationItem(ctx, body, NotificationKindStatusChange, false)
		dim.SetZonePending(true)
		brightOut, dimOut := bright.Render(80), dim.Render(80)

		t.Run(strconv.Quote(body), func(t *testing.T) {
			assertPendingSurvivesSGRStrip(t, dimOut, brightOut)
			assertOnlyTheGutterDiffers(t, dimOut, brightOut)
			assertDimIsFaintDelta(t, dimOut, brightOut)
		})
	}
}

// A zone (pending) system notification renders dim, distinct from a committed
// bright one; after ZoneSettle the relocated entry brightens to the exact
// committed rendering — proving both the styling flip and that the per-envelope
// render cache (keyed on width/expanded only) is not served stale.
func TestChatList_ZoneSystemNotification_DimThenBrightOnSettle(t *testing.T) {
	ref := newTestChatList()
	ref.SetSize(80)
	ref.AppendSystemNotification(notifFrameA)
	bright := ref.Render(80)
	if !strings.Contains(stripAnsi(bright), "alpha → working") {
		t.Fatalf("setup: committed reference render lost the body — instrument is dead: %q", bright)
	}

	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("s1", notifFrameA)
	dim := cl.Render(80)

	if dim == bright {
		t.Errorf("zone (pending) system notification must render DIM, distinct from a committed bright one")
	}
	assertPendingSurvivesSGRStrip(t, dim, bright)
	assertOnlyTheGutterDiffers(t, dim, bright)

	cl.ZoneSettle("s1")
	settled := cl.Render(80)
	if settled != bright {
		t.Errorf("settled notification must brighten to committed styling (no stale dim cache):\nsettled=%q\nbright=%q",
			settled, bright)
	}
}

// A stacked (multi-envelope) system entry brightens EVERY item on settle, not
// just the head, and settles exactly once: the second ZoneSettle is a no-op and
// neither body is rendered twice.
func TestChatList_ZoneSystemNotification_SettleBrightensExactlyOnce(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("s1", notifFrameA+notifFrameB)

	// EVERY peeled item of a stacked entry is born dim, not just the head.
	entry := cl.zone.byUUID["s1"]
	if entry == nil || len(entry.items) != 2 {
		t.Fatalf("setup: zone entry = %+v, want 2 peeled items", entry)
	}
	for idx, env := range entry.items {
		sn, ok := env.item.(*SystemNotificationItem)
		if !ok {
			t.Fatalf("zone item %d is %T, want *SystemNotificationItem", idx, env.item)
		}
		if !sn.pending {
			t.Errorf("zone item %d must be born pending=true (dim) — the flip must cover every peeled item", idx)
		}
	}
	pendingRender := cl.Render(80)
	// The flag checks above are state; this is the rendered consequence — EVERY
	// stacked row must carry the pending marker, not just the head, and none may
	// keep it after settle. Counted, because "the render changed" is satisfied by
	// one row out of two flipping.
	if n := countLeadingGutters(pendingRender, pendingGutter); n != 2 {
		t.Errorf("pending stacked render carries %d pending gutters, want 2 (one per envelope):\n%q", n, stripAnsi(pendingRender))
	}

	if !cl.ZoneSettle("s1") {
		t.Fatal("ZoneSettle(s1) = false, want true")
	}
	if cl.Len() != 2 {
		t.Fatalf("committed Len = %d, want 2 (both stacked envelopes relocated)", cl.Len())
	}
	for idx, env := range cl.items {
		sn, ok := env.item.(*SystemNotificationItem)
		if !ok {
			t.Fatalf("committed item %d is %T, want *SystemNotificationItem", idx, env.item)
		}
		if sn.pending {
			t.Errorf("committed item %d still pending=true — settle must brighten every relocated item", idx)
		}
	}

	first := cl.Render(80)
	if first == pendingRender {
		t.Error("settled render is byte-identical to the pending render — the stacked entry never brightened")
	}
	plain := stripAnsi(first)
	if n := strings.Count(plain, "alpha → working"); n != 1 {
		t.Errorf("first envelope body rendered %d times, want 1", n)
	}
	if n := strings.Count(plain, "beta heads up"); n != 1 {
		t.Errorf("second envelope body rendered %d times, want 1", n)
	}
	if n := countLeadingGutters(first, pendingGutter); n != 0 {
		t.Errorf("settled stacked render still carries %d pending gutters, want 0:\n%q", n, plain)
	}

	if cl.ZoneSettle("s1") {
		t.Error("second ZoneSettle(s1) = true, want false (entry already settled)")
	}
	if cl.Len() != 2 {
		t.Errorf("committed Len = %d after a repeat settle, want 2", cl.Len())
	}
	if again := cl.Render(80); again != first {
		t.Errorf("repeat settle changed the render:\nafter=%q\nbefore=%q", again, first)
	}
}

// LOCKED invariant 5 companion — a refused ZoneDrop of a system entry must not
// brighten it as a side effect. It stays pending, dim, and in the zone.
func TestChatList_ZoneDrop_SystemEntry_RefusedAndStaysDim(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("sys1", notifFrameA)

	// Setup, not the claim: the entry must be dim before the drop for the
	// post-drop check below to mean anything.
	if !zoneSystemItem(t, cl, "sys1").pending {
		t.Fatal("setup: ZoneAddSystem must produce a pending (dim) entry")
	}
	before := cl.Render(80)

	if cl.ZoneDrop("sys1") {
		t.Error("ZoneDrop(system uuid) = true, want false")
	}
	// Re-fetch rather than reusing sn, so a future zone that REPLACES the entry
	// instead of mutating it in place can't satisfy this vacuously.
	if cl.zone.byUUID["sys1"] == nil {
		t.Fatal("system entry vanished from the zone after a refused drop")
	}
	if !zoneSystemItem(t, cl, "sys1").pending {
		t.Error("refused drop must leave the system entry pending (dim), not brighten it")
	}
	if cl.Len() != 0 {
		t.Errorf("committed Len = %d, want 0 (a refused drop commits nothing)", cl.Len())
	}
	after := cl.Render(80)
	if after != before {
		t.Errorf("refused drop changed the render:\nafter=%q\nbefore=%q", after, before)
	}
	// …and the surviving render is still the DIM one, not the committed styling.
	ref := newTestChatList()
	ref.SetSize(80)
	ref.AppendSystemNotification(notifFrameA)
	if after == ref.Render(80) {
		t.Error("post-drop render matches the committed bright styling — the entry brightened")
	}
}

// ZoneSettle must brighten ONLY the settled entry. A user prompt settling while
// a system notification is still queued must leave that notification dim and in
// the zone (the settle loop is entry-scoped, not zone-wide).
func TestChatList_ZoneSettle_UserEntry_LeavesPendingSystemEntryDim(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddUser("u1", "typed prompt")
	cl.ZoneAddSystem("s1", notifFrameA)

	if !zoneSystemItem(t, cl, "s1").pending {
		t.Fatal("setup: ZoneAddSystem must produce a pending (dim) entry")
	}

	if !cl.ZoneSettle("u1") {
		t.Fatal("ZoneSettle(u1) = false, want true")
	}
	if cl.zone.byUUID["s1"] == nil {
		t.Fatal("settling the user entry removed the system entry from the zone")
	}
	if !zoneSystemItem(t, cl, "s1").pending {
		t.Error("settling a user entry must not brighten an unrelated pending system entry")
	}
	// Behavioral leg: the still-queued notification renders dim, so its row is
	// not byte-identical to a committed bright one.
	ref := newTestChatList()
	ref.SetSize(80)
	ref.AppendSystemNotification(notifFrameA)
	refPlain := stripAnsi(ref.Render(80))
	if !strings.Contains(refPlain, "alpha → working") {
		t.Fatalf("setup: committed reference render lost the body: %q", refPlain)
	}
	if strings.Contains(cl.Render(80), ref.Render(80)) {
		t.Error("the still-queued notification renders with committed bright styling — it brightened")
	}
}

// zoneSystemItem fetches the (single) SystemNotificationItem held by the zone
// entry for uuid. Callers re-fetch after every mutation rather than caching the
// pointer, so a zone that ever REPLACES an entry instead of mutating it in place
// cannot satisfy a pending-state assertion vacuously.
func zoneSystemItem(t *testing.T, cl *ChatList, uuid string) *SystemNotificationItem {
	t.Helper()
	entry := cl.zone.byUUID[uuid]
	if entry == nil || len(entry.items) == 0 {
		t.Fatalf("no zone entry with items for uuid %q", uuid)
	}
	sn, ok := entry.items[0].item.(*SystemNotificationItem)
	if !ok {
		t.Fatalf("zone item for %q is %T, want *SystemNotificationItem", uuid, entry.items[0].item)
	}
	return sn
}

// QUM-924 guard — the zero-peel fallback (a frame classified as system by the
// prefix check but with no peelable envelope) still yields a DIM, recallable
// user bubble. Slice B's pending flip lives inside the peel loop, so this path
// must be untouched.
func TestChatList_ZoneAddSystem_UnpeelableFallback_StillDimUserBubble(t *testing.T) {
	const malformed = "<system-notificationX>not a real envelope"

	ref := newTestChatList()
	ref.SetSize(80)
	ref.AppendUser(malformed)
	bright := ref.Render(80)

	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddSystem("u1", malformed)

	entry := cl.zone.byUUID["u1"]
	if entry == nil || entry.kind != pendingUser {
		t.Fatalf("zone entry = %+v, want a pendingUser entry (fallback)", entry)
	}
	u, ok := entry.items[0].item.(*UserItem)
	if !ok {
		t.Fatalf("fallback item is %T, want *UserItem", entry.items[0].item)
	}
	if !u.pending {
		t.Error("fallback user bubble must render dim (pending=true)")
	}
	dim := cl.Render(80)
	if dim == bright {
		t.Error("fallback bubble must render DIM, distinct from a committed bright user bubble")
	}
	if stripAnsi(dim) != stripAnsi(bright) {
		t.Errorf("dim vs bright must differ ONLY in styling:\ndim=%q\nbright=%q",
			stripAnsi(dim), stripAnsi(bright))
	}
	if !cl.ZoneDrop("u1") {
		t.Error("ZoneDrop(fallback uuid) = false, want true (the fallback entry stays recallable)")
	}
}

// The two committed construction paths — AppendSystemNotification (live) and
// Reset (replay backfill) — must both produce bright items, so an existing
// transcript never renders as if it were still queued.
func TestSystemNotificationItem_CommittedPathsAreBright(t *testing.T) {
	live := newTestChatList()
	live.SetSize(80)
	live.AppendSystemNotification(notifFrameA)

	replay := newTestChatList()
	replay.SetSize(80)
	replay.Reset([]MessageEntry{{
		Type:             MessageSystemNotification,
		Content:          "alpha → working",
		NotificationType: NotificationKindStatusChange,
		Complete:         true,
	}})

	for name, cl := range map[string]*ChatList{"live": live, "replay": replay} {
		if cl.Len() != 1 {
			t.Fatalf("%s: committed Len = %d, want 1", name, cl.Len())
		}
		sn, ok := cl.items[0].item.(*SystemNotificationItem)
		if !ok {
			t.Fatalf("%s: committed item is %T, want *SystemNotificationItem", name, cl.items[0].item)
		}
		if sn.pending {
			t.Errorf("%s: committed notification must default to pending=false (bright)", name)
		}
	}
	if live.Render(80) != replay.Render(80) {
		t.Errorf("live and replay committed renders differ:\nlive=%q\nreplay=%q",
			live.Render(80), replay.Render(80))
	}
}
