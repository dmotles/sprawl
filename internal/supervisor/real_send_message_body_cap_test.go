// QUM-1186 lane 2 / D7: Real.SendMessage caps `body` at 300 RUNES and rejects an
// over-cap send with a HARD ERROR. Never truncation — truncation silently loses
// content, which is the exact failure class QUM-1185 exists to eliminate.
//
// The cap lives in Real.SendMessage rather than the MCP handler so it covers every
// caller, and it fires before ANY I/O so a rejected send has zero side effects.
// That placement is what the "nothing persisted" assertions below pin.
//
// Spawn prompts are deliberately NOT capped; nothing in Spawn reads this limit.
//
// RECORDED CONTROLS. Probed against the pre-cap tree, so the discriminating
// power of the "nothing persisted" and "not woken" assertions is observed rather
// than assumed — both t.Fatalf before reaching them on the red run, which is
// exactly how an assertion goes unwatched:
//
//	400-rune send to a started alice:   err=<nil>, queue pending=1, maildir inbox=1
//	400-rune send to a `complete` alice: status after = "active" (want "complete")
//
// So both arms of assertNothingPersisted really do discriminate, and the wake
// arm really does flip complete→active synchronously.
//
// MUTATION LOG — each assertion below has been watched fail.
//
//	C1  count BYTES (len(body)) instead of runes.
//	    → BodyCapIsRuneCounted_NotBytes FAILED both ways: the 300-rune "é" body
//	      was REJECTED with "body is 600 characters, the limit is 300", and the
//	      301-rune body reported 602. Every ASCII test in this file stayed
//	      green, which is exactly why this arm exists.
//	C2  off-by-one: `n >= max` instead of `n > max`.
//	    → BodyExactly300Runes_Accepted FAILED: "body is 300 characters, the
//	      limit is 300 ... want accepted — the cap is inclusive".
//	C3  move the cap BELOW inboxprompt.WrapForDeadTarget.
//	    → DeadTargetWrapperMayExceedCap FAILED: "body is 393 characters, the
//	      limit is 300" — a 300-rune message rejected because of 93 characters
//	      of prefix the caller never wrote and cannot shorten. That is the
//	      defect D7 names. OverCapDoesNotWakeCompleteAgent FAILED in the same
//	      run with 'alice status = "active" ... want "complete" unchanged',
//	      i.e. the rejected send had already spun the recipient up.
package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

// startedAlice brings up a live runtime for "alice" so that an accepted send
// takes the full persistence path (maildir + queue). Without a started runtime a
// send can short-circuit, which would let a "nothing persisted" assertion pass
// for the wrong reason.
func startedAlice(t *testing.T) (*Real, string) {
	t.Helper()
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	// Stop the runtime rather than leaking it: a started runtime goes on writing
	// under t.TempDir() after the test body returns, which races TempDir's
	// RemoveAll cleanup and shows up as a "directory not empty" flake attributed
	// to whatever ran next.
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })
	return r, tmpDir
}

// truncateForMsg shortens a body for a failure message so a 300-rune mismatch
// does not dump the whole payload into the test log.
func truncateForMsg(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40])
	}
	return string(r)
}

// assertNothingPersisted is the AC assertion for the cap: an over-cap body must
// leave BOTH the recipient's agentloop queue AND their maildir inbox empty. Two
// stores, two separate writes in SendMessage (messages.Send then
// agentloop.Enqueue) — checking only one would miss a cap placed between them.
func assertNothingPersisted(t *testing.T, tmpDir, agent string) {
	t.Helper()
	entries, err := agentloop.ListPending(tmpDir, agent)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("queue pending entries = %d, want 0 — an over-cap body must not be queued in any form", len(entries))
	}
	inbox, err := messages.Inbox(tmpDir, agent)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 0 {
		t.Errorf("maildir inbox entries = %d, want 0 — an over-cap body must not reach the inbox in any form", len(inbox))
	}
}

// TestReal_SendMessage_BodyOver300Runes_HardErrors_NothingPersisted is the
// primary AC: a 400-char body errors, and the recipient's inbox and queue are
// both empty afterward.
//
// The error content assertions are not decoration — /cli-ux-best-practices
// requires an error to tell the calling agent what to do next, and the issue
// names the limit, the actual length and a next action specifically.
func TestReal_SendMessage_BodyOver300Runes_HardErrors_NothingPersisted(t *testing.T) {
	r, tmpDir := startedAlice(t)

	body := strings.Repeat("a", 400)
	res, err := r.SendMessage(context.Background(), "alice", body, false, false)
	if err == nil {
		t.Fatalf("SendMessage(400-rune body) returned nil error, want a hard error — truncating or silently accepting an over-cap body is the defect QUM-1186 removes")
	}
	if res != nil {
		t.Errorf("SendMessage result = %+v, want nil alongside the error", res)
	}
	for _, want := range []string{"300", "400"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q — the caller must be told the limit AND the actual length", err.Error(), want)
		}
	}
	// Next-action hint. Matched on the imperative, not on a full sentence, so
	// rewording the advice does not break the test but deleting it does.
	if !strings.Contains(err.Error(), "Next action:") {
		t.Errorf("error %q carries no %q hint (/cli-ux-best-practices)", err.Error(), "Next action:")
	}

	assertNothingPersisted(t, tmpDir, "alice")
}

// TestReal_SendMessage_BodyExactly300Runes_Accepted pins the boundary as
// inclusive (`>` not `>=`). Without it, an off-by-one that rejects exactly-300
// bodies passes every other test in this file.
func TestReal_SendMessage_BodyExactly300Runes_Accepted(t *testing.T) {
	r, tmpDir := startedAlice(t)

	body := strings.Repeat("a", 300)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
		t.Fatalf("SendMessage(exactly 300 runes) = %v, want accepted — the cap is inclusive", err)
	}
	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(entries))
	}
	// Pins "never truncation" on the accepted side: the body must arrive whole.
	if entries[0].Body != body {
		t.Errorf("queued body = %d runes (%q...), want the original %d runes stored verbatim, never truncated", len([]rune(entries[0].Body)), truncateForMsg(entries[0].Body), len([]rune(body)))
	}
}

// TestReal_SendMessage_BodyCapIsRuneCounted_NotBytes is the assertion that
// distinguishes a rune cap from a byte cap. "é" is 2 bytes / 1 rune, so 300 of
// them are 600 bytes: a len(body) implementation rejects a body that is exactly
// at the limit, while passing the ASCII-only tests above.
//
// D7 specifies rune counting, following the toastTextMaxRunes precedent.
func TestReal_SendMessage_BodyCapIsRuneCounted_NotBytes(t *testing.T) {
	t.Run("300 multibyte runes accepted", func(t *testing.T) {
		r, _ := startedAlice(t)
		body := strings.Repeat("é", 300) // 300 runes, 600 bytes
		if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
			t.Fatalf("SendMessage(300 multibyte runes) = %v, want accepted — the cap counts runes, not bytes", err)
		}
	})
	t.Run("301 multibyte runes rejected", func(t *testing.T) {
		r, tmpDir := startedAlice(t)
		body := strings.Repeat("é", 301)
		_, err := r.SendMessage(context.Background(), "alice", body, false, false)
		if err == nil {
			t.Fatalf("SendMessage(301 multibyte runes) = nil, want a hard error")
		}
		if !strings.Contains(err.Error(), "301") {
			t.Errorf("error %q should report the length in RUNES (301), not bytes (602)", err.Error())
		}
		assertNothingPersisted(t, tmpDir, "alice")
	})
}

// TestReal_SendMessage_DeadTargetWrapperMayExceedCap pins the cap's position
// relative to inboxprompt.WrapForDeadTarget (D7). The wrapper prepends a
// routed-up notice to the body; if the cap were enforced AFTER it, a legitimate
// at-the-limit message to a dead agent would be rejected by a prefix the caller
// never wrote and cannot shorten.
//
// The caller's body is what is capped. The wrapper is sprawl's own addition and
// is allowed to push the stored body past 300 runes.
func TestReal_SendMessage_DeadTargetWrapperMayExceedCap(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	// weave (root, alive by construction) -> alice (DIED). Route-up needs a
	// GENUINELY-known-live ancestor; without weave's own state record the walk
	// takes the deadFallback arm instead and the wrapper never applies, which
	// would make this test pass for the wrong reason.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: state.StatusDied,
	})

	body := strings.Repeat("a", 300)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
		t.Fatalf("SendMessage(300 runes, dead target) = %v, want accepted", err)
	}

	entries, err := agentloop.ListPending(tmpDir, "weave")
	if err != nil {
		t.Fatalf("ListPending(weave): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("weave pending entries = %d, want 1 — the send must have routed up, or this fixture is not exercising the wrapper at all", len(entries))
	}

	// Pin the wrapper exactly. A length-only check would stay green if the
	// wrapper were replaced by any other body-lengthening behaviour.
	wantBody := inboxprompt.WrapForDeadTarget("weave", "alice", []string{"alice"}, body)
	if entries[0].Body != wantBody {
		t.Errorf("routed body mismatch\n got: %q\nwant: %q", entries[0].Body, wantBody)
	}
	// The stored body carries the wrapper and is therefore LONGER than the cap.
	// This is the assertion that fails if the cap moves below WrapForDeadTarget.
	if got := len([]rune(entries[0].Body)); got <= 300 {
		t.Errorf("stored body = %d runes, want > 300 — the caller's 300-rune body plus sprawl's own routed-up prefix must exceed the cap; if it does not, this test cannot detect a cap enforced after WrapForDeadTarget", got)
	}
}

// TestReal_SendMessage_OverCapToNonexistentAgent_ReportsTheCap pins the ordering
// claim the cap's own comment makes: it fires before ValidateName/LoadAgent
// resolve anything.
//
// Every other test here uses a recipient that exists, so a future reorder that
// moved the cap below LoadAgent would break the claim while the whole file
// stayed green. The discriminator is WHICH error comes back, not that one does.
func TestReal_SendMessage_OverCapToNonexistentAgent_ReportsTheCap(t *testing.T) {
	r, _ := newFakeReal(t)

	body := strings.Repeat("a", 400)
	_, err := r.SendMessage(context.Background(), "nosuchagent", body, false, false)
	if err == nil {
		t.Fatalf("SendMessage(400 runes, unknown recipient) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "300") {
		t.Errorf("error %q is not the cap error — the cap must be evaluated before the recipient is resolved, so a caller learns the real problem with its payload rather than a downstream symptom", err.Error())
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q reports the recipient lookup instead of the cap; the cap is checked first by design", err.Error())
	}
}

// TestReal_SendMessage_OverCapToDeadTarget_StillRejected closes the other half
// of the cap-ordering question.
//
// DeadTargetWrapperMayExceedCap proves an ACCEPTED body survives route-up. This
// proves the cap is reached at all on that path: a check placed inside the
// non-dead branch, or below the deadFallback block, rejects nothing when the
// target is Died — and every other test in this file uses a live target, so
// nothing else would notice.
func TestReal_SendMessage_OverCapToDeadTarget_StillRejected(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: state.StatusDied,
	})

	body := strings.Repeat("a", 301)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err == nil {
		t.Fatalf("SendMessage(301 runes, dead target) = nil error, want a hard error — the cap must fire before route-up, not only on the live-target path")
	}
	// The routed-up recipient must be untouched too.
	assertNothingPersisted(t, tmpDir, "weave")
	assertNothingPersisted(t, tmpDir, "alice")
}

// TestReal_SendMessage_OverCapDoesNotWakeCompleteAgent pins the cap ABOVE the
// auto-wake arm. A `complete` recipient is auto-revived by SendMessage; if the
// cap were checked after that, an over-cap send would spin up an agent process
// and then reject the message — an expensive side effect from a rejected call.
func TestReal_SendMessage_OverCapDoesNotWakeCompleteAgent(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusComplete
	saveTestAgent(t, tmpDir, agentState)

	body := strings.Repeat("a", 400)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err == nil {
		t.Fatalf("SendMessage(400 runes) = nil error, want a hard error")
	}

	after, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if after.Status != state.StatusComplete {
		t.Errorf("alice status = %q after a rejected over-cap send, want %q unchanged — an over-cap body must not wake the recipient", after.Status, state.StatusComplete)
	}
	assertNothingPersisted(t, tmpDir, "alice")
}
