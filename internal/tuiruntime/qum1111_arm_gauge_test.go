// QUM-1111 — pump arm accounting for UserMessageSentMsg.
//
// `SessionBackend.WaitForEvent()` is ONE-SHOT: each returned tea.Cmd consumes
// exactly one pump event and must be replaced by a reducer that received a
// pump-delivered msg (QUM-826), or the bubbletea event pump parks and live
// render freezes. The inverse hazard is the one this file measures: a reducer
// that re-arms on a msg the pump did NOT deliver net-CREATES an arm. Every
// such step leaves one more goroutine reading the single `tui-viewport`
// channel, and with N readers the EventBus Seq order stops surviving to the
// reducer — which is how QUM-1111's Ctrl+G bubble stranded (Sent(B) and
// Consumed(B) inverted, ZoneSettle(B) no-op'd, ZoneAddUser(B) re-created it,
// and QUM-1068's idempotency gate guarantees no second consume ack).
//
// `UserMessageSentMsg` is the only user-message msg with MIXED provenance:
// cmd-returned from TUIAdapter.SendMessage / SendPassthrough / SendAttachment,
// pump-delivered from event_translate.go's EventUserMessageSent case.
//
// THE ASSERTION PAIR. "Arm gauge never exceeds 1" (A1) is ALSO satisfied by a
// frozen pump at zero — the exact QUM-826 freeze — so a fix that simply
// deletes the re-arm passes A1 while re-breaking Ctrl+G and system
// notifications. So every step asserts BOTH:
//
//	A1 — the gauge never exceeds 1 (peak <= 1).
//	A2 — the gauge is EXACTLY 1 at the step boundary (outstanding == 1).
//
// A2 is the mutation guard. See the per-test doc comments for the two
// mutations these tests must fail under.
//
// EVIDENCE QUALITY. These drive the real tui.AppModel reducer, the real
// TUIAdapter (every method is the genuine one — gaugedBridge only counts arm
// construction and resolution around it), and a real EventBus + UnifiedRuntime.
// internal/tui's fakeSessionBackend reproduces only the cmd-returned leg and
// has no channel or Seq, so it cannot exhibit any of this.

package tuiruntime

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	tui "github.com/dmotles/sprawl/internal/tui"
)

// gaugedBridge wraps a real *TUIAdapter and counts pump arms. Every other
// SessionBackend method is promoted from the embedded adapter, so the model
// under test talks to the genuine implementation.
//
// The gauge counts arm CONSTRUCTION, not OS-level goroutine parking:
// WaitForEvent() is a side-effect-free closure factory, so counting there
// happens synchronously inside AppModel.Update and makes the assertions
// deterministic (no settle windows, no blocked goroutines). Resolution is
// counted when the closure returns a msg — a closure that internally skips
// several nil-translating events is still exactly one arm.
type gaugedBridge struct {
	*TUIAdapter
	created  atomic.Int64
	resolved atomic.Int64
	peak     atomic.Int64
	// live holds constructed-but-unresolved arm cmds in creation order so the
	// harness can run the outstanding arm independently of whatever cmd tree
	// the reducer returned. Unsynchronised on purpose: only the test goroutine
	// appends and pops it. The counters are atomics because runCmd's goroutine
	// resolves an arm — and if runCmd's deadline ever fires, that orphaned
	// goroutine keeps resolving behind an already-failing test.
	live []tea.Cmd
}

func (g *gaugedBridge) WaitForEvent() tea.Cmd {
	inner := g.TUIAdapter.WaitForEvent()
	n := g.created.Add(1) - g.resolved.Load()
	for {
		peak := g.peak.Load()
		if n <= peak || g.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	cmd := func() tea.Msg {
		defer g.resolved.Add(1)
		return inner()
	}
	g.live = append(g.live, cmd)
	return cmd
}

func (g *gaugedBridge) outstanding() int64 { return g.created.Load() - g.resolved.Load() }

// notifFrame is a real system-notification frame body, so the kind:system
// tests traverse the reducer's ZoneAddSystem leg rather than falling through
// to ZoneAddUser (classifyInboundFrame keys on the literal open tag).
const notifFrame = `<system-notification type="status_change">alpha -> working</system-notification>`

// gaugedApp is the harness: a real AppModel over a gauged real adapter.
type gaugedApp struct {
	t   *testing.T
	app tui.AppModel
	g   *gaugedBridge
}

func newGaugedApp(t *testing.T, mock *adapterMockSession) (*gaugedApp, *sprawlrt.UnifiedRuntime, *TUIAdapter) {
	t.Helper()
	rt, a := buildAdapter(t, mock)
	g := &gaugedBridge{TUIAdapter: a}
	app := tui.NewAppModel("colour212", "testrepo", "v0.1.0", g, nil, "", nil)
	updated, cmd := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Fatalf("WindowSizeMsg returned a cmd (%T); harness assumes it never arms", cmd)
	}
	return &gaugedApp{t: t, app: updated.(tui.AppModel), g: g}, rt, a
}

// step feeds msg to the real reducer and asserts A1 + A2 at the resulting step
// boundary. It returns the reducer's cmd so the caller can run a non-arm cmd
// (e.g. the SendMessage write).
//
// The pair is enforced HERE, once, for every test — so no individual test can
// be accidentally A1-only. Several test names below are A1-shaped
// ("DoesNotDoubleArm"); the check they make is always both, including the
// exactly-1 half.
func (h *gaugedApp) step(label string, msg tea.Msg) tea.Cmd {
	h.t.Helper()
	updated, cmd := h.app.Update(msg)
	h.app = updated.(tui.AppModel)
	if peak := h.g.peak.Load(); peak > 1 {
		h.t.Errorf("A1 after %s: pump arm gauge peaked at %d, want <= 1 — each surplus arm is a permanently parked WaitForEvent goroutine reading tui-viewport, which destroys EventBus Seq ordering at the reducer", label, peak)
	}
	if out := h.g.outstanding(); out != 1 {
		h.t.Errorf("A2 after %s: pump arm gauge = %d, want exactly 1 (0 = parked pump / frozen live render; >1 = leaked reader goroutine)", label, out)
	}
	return cmd
}

// runArm executes the oldest outstanding arm and returns the msg it yielded.
// It requires an event to already be on the bus.
func (h *gaugedApp) runArm() tea.Msg {
	h.t.Helper()
	if len(h.g.live) == 0 {
		h.t.Fatal("no outstanding pump arm to run; the pump is parked")
	}
	cmd := h.g.live[0]
	h.g.live = h.g.live[1:]
	return runCmd(h.t, cmd)
}

// init arms the pump exactly once, the way cmd/enter.go's continuous bridge
// does. This controls the CREATION half of the instrument: if this step does
// not move the gauge 0 -> 1, every later "== 1" assertion is vacuous. The
// RESOLUTION half is controlled by TestArmGauge_Instrument_ResolvesOnRun.
func (h *gaugedApp) init() {
	h.t.Helper()
	if got := h.g.created.Load(); got != 0 {
		h.t.Fatalf("gauge created = %d before init, want 0", got)
	}
	h.step("SessionInitializedMsg", tui.SessionInitializedMsg{})
	if got := h.g.created.Load(); got != 1 {
		h.t.Fatalf("gauge created = %d after init, want 1 — the instrument never moved, so the A2 assertions below prove nothing", got)
	}
}

// TestArmGauge_SessionInit_ArmsExactlyOnce controls the creation half.
func TestArmGauge_SessionInit_ArmsExactlyOnce(t *testing.T) {
	h, _, _ := newGaugedApp(t, &adapterMockSession{})
	h.init()
}

// TestArmGauge_Instrument_ResolvesOnRun controls the resolution half: running
// an arm against a real bus event must take the gauge back to 0. Without this,
// `outstanding` could be a monotone counter and "== 1" would mean nothing.
func TestArmGauge_Instrument_ResolvesOnRun(t *testing.T) {
	h, rt, _ := newGaugedApp(t, &adapterMockSession{})
	h.init()

	if _, err := rt.WriteSystemMessage(context.Background(), notifFrame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}
	if msg := h.runArm(); msg == nil {
		t.Fatal("arm yielded no msg")
	}
	if got := h.g.outstanding(); got != 0 {
		t.Fatalf("gauge = %d after running the only arm, want 0 — the resolution half of the instrument is dead", got)
	}
}

// TestArmGauge_TypedPrompt_DoesNotDoubleArm — the QUM-1111 instance.
//
// The UserMessageSentMsg fed to the reducer is the one the REAL
// TUIAdapter.SendMessage closure produced (a hand-built literal would prove
// nothing about provenance wiring). That cmd read no pump event, so the
// reducer must not replace one.
func TestArmGauge_TypedPrompt_DoesNotDoubleArm(t *testing.T) {
	h, _, _ := newGaugedApp(t, &adapterMockSession{})
	h.init()

	sendCmd := h.step("SubmitMsg", tui.SubmitMsg{Text: "hello"})
	if sendCmd == nil {
		t.Fatal("SubmitMsg returned no cmd; expected the bridge SendMessage write")
	}
	sent, ok := runCmd(t, sendCmd).(tui.UserMessageSentMsg)
	if !ok {
		t.Fatalf("SubmitMsg cmd did not yield tui.UserMessageSentMsg")
	}
	if sent.UUID == "" {
		t.Fatal("cmd-returned UserMessageSentMsg has no UUID; the pending zone would not track it")
	}
	h.step("cmd-returned UserMessageSentMsg (SendMessage)", sent)
}

// TestArmGauge_Passthrough_DoesNotDoubleArm covers the SendPassthrough site,
// whose reducer leg is a separate early return. There is NO pump-delivered
// path with Passthrough set, so that leg's re-arm is unconditionally surplus.
func TestArmGauge_Passthrough_DoesNotDoubleArm(t *testing.T) {
	h, _, _ := newGaugedApp(t, &adapterMockSession{})
	h.init()

	sendCmd := h.step("PassthroughMsg", tui.PassthroughMsg{Text: "/compact"})
	if sendCmd == nil {
		t.Fatal("PassthroughMsg returned no cmd; expected the bridge SendPassthrough write")
	}
	sent, ok := runCmd(t, sendCmd).(tui.UserMessageSentMsg)
	if !ok {
		t.Fatalf("PassthroughMsg cmd did not yield tui.UserMessageSentMsg")
	}
	if !sent.Passthrough {
		t.Fatal("SendPassthrough msg is not flagged Passthrough; the reducer would take the wrong leg")
	}
	h.step("cmd-returned UserMessageSentMsg (SendPassthrough)", sent)
}

// TestArmGauge_Attachment_DoesNotDoubleArm covers the third cmd-returned site
// rather than inferring it from the first. The msg carries chips built by the
// real attach.Build.
func TestArmGauge_Attachment_DoesNotDoubleArm(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mock.png")
	if err := os.WriteFile(p, pngHeader, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h, _, _ := newGaugedApp(t, &adapterMockSession{})
	h.init()

	sendCmd := h.step("AttachMsg", tui.AttachMsg{Paths: []string{p}, Prompt: "what is this"})
	if sendCmd == nil {
		t.Fatal("AttachMsg returned no cmd; expected the bridge SendAttachment write")
	}
	sent, ok := runCmd(t, sendCmd).(tui.UserMessageSentMsg)
	if !ok {
		t.Fatalf("AttachMsg cmd did not yield tui.UserMessageSentMsg")
	}
	if len(sent.Attachments) != 1 {
		t.Fatalf("attachment chips = %d, want 1", len(sent.Attachments))
	}
	if sent.UUID == "" {
		t.Fatal("attachment msg has no UUID; the reducer would take the legacy AppendUserWithAttachments leg, not the zone leg under test")
	}
	h.step("cmd-returned UserMessageSentMsg (SendAttachment)", sent)
}

// TestArmGauge_PumpDeliveredSent_KeepsArming_SystemFrame drives the
// kind:system leg of writeMessage (internal/runtime/unified.go), whose
// EventUserMessageSent is genuinely pump-delivered. The msg is taken OUT of
// WaitForEvent — so it consumed an arm — and the step boundary must show the
// gauge back at 1.
//
// Green today. This is one of the two tests that must FAIL under the mutation
// "delete the re-arm unconditionally", which A1 alone cannot see.
func TestArmGauge_PumpDeliveredSent_KeepsArming_SystemFrame(t *testing.T) {
	h, rt, _ := newGaugedApp(t, &adapterMockSession{})
	h.init()

	if _, err := rt.WriteSystemMessage(context.Background(), notifFrame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}
	msg := h.runArm()
	sent, ok := msg.(tui.UserMessageSentMsg)
	if !ok {
		t.Fatalf("pump yielded %T, want tui.UserMessageSentMsg for the kind:system frame", msg)
	}
	if out := h.g.outstanding(); out != 0 {
		t.Fatalf("gauge = %d after the pump delivered the msg, want 0 (the arm was consumed)", out)
	}
	h.step("pump-delivered UserMessageSentMsg (kind:system)", sent)
}

// TestArmGauge_PumpDeliveredSent_KeepsArming_SendAllNow drives the Ctrl+G
// path: SendAllNow cancels the queued prompt and republishes the coalesced
// text at priority now, publishing cancelled -> sent -> consumed synchronously
// onto the bus before returning. Each is drained one arm at a time (serial
// reads of a bus already holding all three — NOT a probabilistic ordering
// assertion) and every step boundary must show the gauge back at 1.
//
// Green today; must FAIL at the Sent step under the delete-the-re-arm
// mutation, where the pump parks and the Consumed read that drives ZoneSettle
// becomes unreachable — QUM-1111's stranded bubble, arrived at from the other
// direction.
func TestArmGauge_PumpDeliveredSent_KeepsArming_SendAllNow(t *testing.T) {
	mock := &adapterMockSession{cancelResults: map[string]bool{}}
	h, rt, a := newGaugedApp(t, mock)
	h.init()

	orig, err := rt.WriteUserPrompt(context.Background(), "queued one", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	mock.cancelResults[orig] = true

	res, ok := runCmd(t, a.SendAllNow()).(tui.SendAllNowResultMsg)
	if !ok {
		t.Fatal("SendAllNow did not yield tui.SendAllNowResultMsg")
	}
	if res.Err != nil {
		t.Fatalf("SendAllNowResultMsg.Err = %v", res.Err)
	}

	var sawSent, sawConsumed bool
	for i := 0; !sawSent || !sawConsumed; i++ {
		if i >= 8 {
			t.Fatalf("drained 8 pump msgs without seeing both sent and consumed (sent=%v consumed=%v)", sawSent, sawConsumed)
		}
		msg := h.runArm()
		switch m := msg.(type) {
		case tui.UserMessageSentMsg:
			if m.UUID == "" || m.UUID == orig {
				t.Fatalf("sent uuid = %q, want the fresh now-write uuid (not %q)", m.UUID, orig)
			}
			sawSent = true
		case tui.UserMessageConsumedMsg:
			sawConsumed = true
		case tui.UserMessageCancelledMsg:
			// The superseded `next` write; expected on this path.
		default:
			t.Fatalf("unexpected pump msg %T while draining SendAllNow events", msg)
		}
		h.step("pump-delivered "+msgLabel(msg), msg)
	}
}

func msgLabel(msg tea.Msg) string {
	switch msg.(type) {
	case tui.UserMessageSentMsg:
		return "UserMessageSentMsg (SendAllNow now-write)"
	case tui.UserMessageConsumedMsg:
		return "UserMessageConsumedMsg"
	case tui.UserMessageCancelledMsg:
		return "UserMessageCancelledMsg"
	default:
		return "msg"
	}
}
