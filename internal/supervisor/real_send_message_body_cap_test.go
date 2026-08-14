// QUM-1186 lane 2 / D7: Real.SendMessage caps `body` in RUNES and rejects an
// over-cap send with a HARD ERROR. Never truncation — truncation silently loses
// content, which is the exact failure class QUM-1185 exists to eliminate.
//
// QUM-1216 split the single cap into TWO numbers that differ ON PURPOSE:
// sendMessageBodyHardMaxRunes (500) is what rejects; sendMessageBodyDocumentedMaxRunes
// (300) is the only number that ever reaches a string an agent reads. The tests
// below pin BOTH halves — the boundary now lives at the hard limit, while the
// denominator in every error stays the documented one. See real.go's definition
// site for why the mismatch is deliberate.
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
//	over-cap send to a started alice:   err=<nil>, queue pending=1, maildir inbox=1
//	over-cap send to a `complete` alice: status after = "active" (want "complete")
//
// (Those were probed with a 400-rune body against the pre-cap tree. 400 is
// UNDER the QUM-1216 hard limit and no longer describes a defect, so the
// over-cap tests below now use 560; C3 in the mutation log re-observes both
// arms at the new counts against the current tree.)
//
// So both arms of assertNothingPersisted really do discriminate, and the wake
// arm really does flip complete→active synchronously.
//
// MUTATION LOG — each assertion below has been watched fail, with what it
// printed. C1-C3 were re-run at the QUM-1216 counts because the old ones no
// longer describe a defect; C4-C10 are new with that change. Test names in
// C1-C3 are the CURRENT ones (BodyExactly300Runes_Accepted became
// BodyAtDocumentedLimit_Accepted; the boundary claim moved to BodyAtHardLimit).
//
//	C1  count BYTES (len(body)) instead of runes.
//	    → BodyCapIsRuneCounted_NotBytes FAILED both ways: the 500-rune "é" body
//	      was REJECTED reporting "body is 1000 characters", and the 501-rune
//	      body reported 1002. Every ASCII test in this file stayed green, which
//	      is exactly why this arm exists.
//	C2  off-by-one: `n >= hard` instead of `n > hard`.
//	    → BodyAtHardLimit_Accepted FAILED: "body is 500 characters, the limit
//	      is 300 ... want accepted — the cap is inclusive".
//	C3  move the cap BELOW inboxprompt.WrapForDeadTarget (moved to just above
//	    messages.Send).
//	    → DeadTargetWrapperMayExceedCap FAILED: "body is 593 characters" — a
//	      500-rune message rejected because of 93 characters of prefix the
//	      caller never wrote and cannot shorten. That is the defect D7 names.
//	      OverCapToNonexistentAgent FAILED in the same run with the "not found"
//	      lookup error instead of the cap error, and
//	      OverCapDoesNotWakeCompleteAgent FAILED with 'alice status = "active"
//	      ... want "complete" unchanged', i.e. the rejected send had already
//	      spun the recipient up.
//	C4  swap the error's format arg to the HARD limit (the tidy-up that makes
//	    the error "honest").
//	    → OverCapErrorCitesDocumentedLimit_NotHardLimit FAILED on all three
//	      arms: 'reports the limit as "500", want the DOCUMENTED "300"', the
//	      equals-hard arm, and the whole-string sweep 'shows the caller the
//	      number "500"'. This is the control the QUM-1216 ACs require.
//	C5  equalise UPWARD: documented = 500.
//	    → OverCapErrorCitesDocumentedLimit_NotHardLimit FAILED on the
//	      equals-hard arm alone (the want-arm slides with the constant, which
//	      is why that second arm exists), AreADeliberatelyUnequalPair FAILED
//	      "documented=500 hard=500, want documented < hard", and
//	      AgentVisibleSurfaces FAILED on all 13 statements at once.
//	C6  equalise DOWNWARD: hard = 300, i.e. QUM-1216 reverted.
//	    → BodyInSlackBand_Delivers FAILED "SendMessage(350 runes) = ... the
//	      limit is 300 ... want accepted", AreADeliberatelyUnequalPair FAILED
//	      "documented=300 hard=300". EXACTLY those two: AgentVisibleSurfaces
//	      stays quiet because the hard-limit arm is guarded on hard != want.
//	      Unguarded it also fired on all four correct sources, telling the
//	      reader the DOCUMENTED cap must not be visible to agents — an exactly
//	      inverted diagnostic on the one mutation most likely to be someone
//	      mid-revert. That is why the guard is there.
//	C7  plant "max 500 characters" in tools.go's send_message description.
//	    → AgentVisibleSurfaces FAILED twice: the cap-figure arm and the
//	      contains-hard-limit arm.
//	    NEGATIVE CONTROL, same site: reword to "max 300 chars" — a clean
//	      subject with different wording. The probe stayed QUIET (ok). So it
//	      keys on the NUMBER, not on the phrasing it happened to be written
//	      against.
//	C8  delete the cap sentence from every prompt source (s/300 characters/an
//	    appropriate length/).
//	    → AgentVisibleSurfaces FAILED "only 5 cap statements found ... want at
//	      least 8". The floor is what stops this scan from passing vacuously
//	      once there is nothing left to match.
//	C9  write an EMPTY body to the maildir while still queueing the real one.
//	    → BodyInSlackBand_Delivers and BodyAtDocumentedLimit_Accepted both
//	      FAILED 'inbox body = 0 runes ("") ... want the original 350/300 runes
//	      stored verbatim'. assertDelivered's maildir arm is therefore watched,
//	      not assumed — the queue arm alone would have stayed green.
package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// truncateForMsg shortens a body for a failure message so a long-body mismatch
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

// TestReal_SendMessage_BodyOverHardLimit_HardErrors_NothingPersisted is the
// primary AC: a 560-char body errors, and the recipient's inbox and queue are
// both empty afterward.
//
// The error content assertions are not decoration — /cli-ux-best-practices
// requires an error to tell the calling agent what to do next, and the issue
// names the limit, the actual length and a next action specifically. The limit
// it names is the DOCUMENTED one (QUM-1216); the actual length is the real
// count, because that is what tells the caller how much to cut.
func TestReal_SendMessage_BodyOverHardLimit_HardErrors_NothingPersisted(t *testing.T) {
	r, tmpDir := startedAlice(t)

	body := strings.Repeat("a", overCapRunes)
	res, err := r.SendMessage(context.Background(), "alice", body, false, false)
	if err == nil {
		t.Fatalf("SendMessage(560-rune body) returned nil error, want a hard error — truncating or silently accepting an over-cap body is the defect QUM-1186 removes")
	}
	if res != nil {
		t.Errorf("SendMessage result = %+v, want nil alongside the error", res)
	}
	for _, want := range []string{strconv.Itoa(sendMessageBodyDocumentedMaxRunes), "560"} {
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

// assertDelivered is the mirror of assertNothingPersisted: an accepted body must
// reach BOTH stores, verbatim. The accepted-path tests predating QUM-1216 only
// ever checked the queue, so "the recipient actually received it" was never
// pinned on the maildir side.
func assertDelivered(t *testing.T, tmpDir, agent, wantBody string) {
	t.Helper()
	entries, err := agentloop.ListPending(tmpDir, agent)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("queue pending entries = %d, want 1 — the message never reached the recipient's queue", len(entries))
	}
	if entries[0].Body != wantBody {
		t.Errorf("queued body = %d runes (%q...), want the original %d runes stored verbatim, never truncated", len([]rune(entries[0].Body)), truncateForMsg(entries[0].Body), len([]rune(wantBody)))
	}
	inbox, err := messages.Inbox(tmpDir, agent)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("maildir inbox entries = %d, want 1 — a delivered message must be retrievable via messages_read, not only enqueued", len(inbox))
	}
	if inbox[0].Body != wantBody {
		t.Errorf("inbox body = %d runes (%q...), want the original %d runes stored verbatim", len([]rune(inbox[0].Body)), truncateForMsg(inbox[0].Body), len([]rune(wantBody)))
	}
}

// TestReal_SendMessage_BodyInSlackBand_Delivers is the QUM-1216 headline AC: a
// body between the documented limit and the hard limit DELIVERS.
//
// 350 is the measured near-miss case — an agent aiming at 300 and overshooting,
// which it cannot avoid because it cannot count characters. Before QUM-1216 this
// cost a round trip. Delivery is asserted on both stores rather than on the
// absence of an error, per the AC.
func TestReal_SendMessage_BodyInSlackBand_Delivers(t *testing.T) {
	r, tmpDir := startedAlice(t)

	body := strings.Repeat("a", 350)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
		t.Fatalf("SendMessage(350 runes) = %v, want accepted — 350 is inside the deliberate slack band (%d < 350 <= %d) and must not cost the caller a round trip", err, sendMessageBodyDocumentedMaxRunes, sendMessageBodyHardMaxRunes)
	}
	assertDelivered(t, tmpDir, "alice", body)
}

// TestReal_SendMessage_BodyAtHardLimit_Accepted and its +1 sibling hold the
// inclusive-boundary claim (`>` not `>=`) that BodyAtDocumentedLimit_Accepted
// used to hold. The boundary moved with the enforcing constant.
func TestReal_SendMessage_BodyAtHardLimit_Accepted(t *testing.T) {
	r, tmpDir := startedAlice(t)

	body := strings.Repeat("a", sendMessageBodyHardMaxRunes)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
		t.Fatalf("SendMessage(exactly %d runes) = %v, want accepted — the cap is inclusive", sendMessageBodyHardMaxRunes, err)
	}
	assertDelivered(t, tmpDir, "alice", body)
}

func TestReal_SendMessage_BodyOneOverHardLimit_Rejected(t *testing.T) {
	r, tmpDir := startedAlice(t)

	n := sendMessageBodyHardMaxRunes + 1
	body := strings.Repeat("a", n)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err == nil {
		t.Fatalf("SendMessage(%d runes) = nil error, want a hard error — one rune past the hard limit must reject", n)
	}
	assertNothingPersisted(t, tmpDir, "alice")
}

// overCapRunes is the over-cap body length every rejection test uses. Named
// because the positive control's whole-integer sweep must stay in sync with the
// body it sends — a literal in both places is a silent coupling. 560 is the
// length the QUM-1216 AC names, and its digits contain neither "300" nor "500",
// so a substring check cannot pass on it by accident.
const overCapRunes = 560

// capDenominatorRe extracts the limit an over-cap error reports to the caller.
// Matched on the canonical sentence rather than on a bare substring: a
// strings.Contains(err, "300") check is satisfied by "the limit is 500
// (documented 300)", which is exactly the leak this test exists to catch.
var capDenominatorRe = regexp.MustCompile(`the limit is (\d+)`)

// capIntegerRe finds every number the caller is shown.
var capIntegerRe = regexp.MustCompile(`\d+`)

// TestReal_SendMessage_OverCapErrorCitesDocumentedLimit_NotHardLimit is the
// POSITIVE CONTROL the QUM-1216 ACs require, and the reason this file is worth
// more than the change it guards.
//
// The design deliberately tells agents 300 while rejecting at 500. That mismatch
// looks like a bug to every future reader, so the realistic failure mode is not
// a wrong number — it is a well-meaning tidy-up that "aligns" the error with
// what actually rejects. This test fires when that happens, in either of the two
// ways it can happen:
//
//  1. the format arg is swapped to the hard limit (denominator becomes 500)
//  2. the constants are equalised (denominator is still "documented", but the
//     documented value is no longer 300)
//
// Both arms below, plus a whole-string sweep so a leak in prose the regexp does
// not cover is caught too.
func TestReal_SendMessage_OverCapErrorCitesDocumentedLimit_NotHardLimit(t *testing.T) {
	r, _ := newFakeReal(t)

	body := strings.Repeat("a", overCapRunes)
	_, err := r.SendMessage(context.Background(), "alice", body, false, false)
	if err == nil {
		t.Fatalf("SendMessage(560 runes) = nil error, want a hard error")
	}
	msg := err.Error()

	got := capDenominatorRe.FindAllStringSubmatch(msg, -1)
	if len(got) != 1 {
		t.Fatalf("error %q states the limit %d times, want exactly 1 — the caller must be given ONE unambiguous number to aim at", msg, len(got))
	}
	if want := strconv.Itoa(sendMessageBodyDocumentedMaxRunes); got[0][1] != want {
		t.Errorf("error reports the limit as %q, want the DOCUMENTED %q — the enforcing limit must never be quoted to a caller (QUM-1216); if you are here because you made the error cite what actually rejects, read the constants' comment in real.go before changing this test", got[0][1], want)
	}
	if hard := strconv.Itoa(sendMessageBodyHardMaxRunes); got[0][1] == hard {
		t.Errorf("error reports the limit as %q, which equals the hard limit — the slack has been erased, either by swapping the format arg or by equalising the two constants", hard)
	}

	// Whole-string sweep. The regexp above only inspects one sentence; this
	// catches a hard-limit leak anywhere else in the message, including in the
	// next-action hint.
	wantNums := map[string]bool{strconv.Itoa(overCapRunes): true, strconv.Itoa(sendMessageBodyDocumentedMaxRunes): true}
	for _, n := range capIntegerRe.FindAllString(msg, -1) {
		if !wantNums[n] {
			t.Errorf("error %q shows the caller the number %q; only the actual length (%d) and the documented limit (%d) may appear", msg, n, overCapRunes, sendMessageBodyDocumentedMaxRunes)
		}
	}
}

// TestSendMessageBodyLimits_AreADeliberatelyUnequalPair is the tripwire against
// the tidy-up that equalises the constants instead of the error string.
//
// It looks tautological — it asserts two literals. It is not: the literals are
// an operator decision (QUM-1216), and the whole design is that they DIFFER. An
// equality here is not a simplification, it is the defect. Do not "simplify"
// this test by deleting it; if you are changing the numbers, change them here
// deliberately and say why in the commit.
func TestSendMessageBodyLimits_AreADeliberatelyUnequalPair(t *testing.T) {
	if sendMessageBodyDocumentedMaxRunes >= sendMessageBodyHardMaxRunes {
		t.Fatalf("documented=%d hard=%d, want documented < hard — the gap IS the feature: it absorbs the overshoot of a caller that cannot count characters", sendMessageBodyDocumentedMaxRunes, sendMessageBodyHardMaxRunes)
	}
	if sendMessageBodyDocumentedMaxRunes != 300 {
		t.Errorf("documented limit = %d, want 300 — this is the number stated in every prompt and tool description; changing it here alone desynchronises the agent-visible surface", sendMessageBodyDocumentedMaxRunes)
	}
	if sendMessageBodyHardMaxRunes != 500 {
		t.Errorf("hard limit = %d, want 500 (QUM-1216 operator decision)", sendMessageBodyHardMaxRunes)
	}
}

// agentVisibleCapSources returns the files whose cap statements an agent
// actually reads: every prompt source, plus the two tracked documents CLAUDE.md
// tells agents to read. Read from source rather than from a built prompt so the
// assertion covers the literal text a future editor will touch. Precedent:
// internal/sprawlmcp/tool_description_sync_test.go.
//
// The prompt sources are GLOBBED, not listed: a hand-maintained list silently
// exempts the next prompt_*.go someone adds, which is where a "500" would land.
//
// The two markdown files ARE hand-picked, and that is a deliberate narrowing,
// not an oversight: they are the docs that state this cap today. CLAUDE.md, the
// other skills and CHANGELOG.md are also agent-read and are NOT scanned — none
// states the cap now, and CHANGELOG.md carries historical entries that a
// present-tense check should not police. So this set is a floor on coverage,
// not a proof that no agent-visible file anywhere states the hard limit.
func agentVisibleCapSources(t *testing.T) []string {
	t.Helper()
	prompts, err := filepath.Glob("../agent/prompt*.go")
	if err != nil {
		t.Fatalf("glob prompt sources: %v", err)
	}
	var out []string
	for _, p := range prompts {
		if strings.HasSuffix(p, "_test.go") {
			continue // tests are not an agent-visible surface
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		t.Fatalf("no prompt sources matched ../agent/prompt*.go — this scan is not looking at what it claims to")
	}
	return append(out, "../../DESCRIPTION.md", "../../.claude/skills/linear-issues/SKILL.md")
}

// capStatementRe matches a cap figure as an agent would read it ("300
// characters", "300-char"). Requiring the unit noun is what keeps unrelated
// bare numbers from masquerading as cap statements.
var capStatementRe = regexp.MustCompile(`(\d+)[- ](?:char|character|rune)`)

// sendMessageToolEntry slices tools.go down to the send_message tool definition.
//
// The slice is load-bearing for BOTH arms of check(), for two DIFFERENT
// unrelated-but-correct numbers: unsliced, capStatementRe matches toast's
// legitimate "120 characters", and the hard-limit arm matches messages_list's
// "clamped to 500". Neither is a leak. Do not remove the slice on the strength
// of only one of those two facts.
func sendMessageToolEntry(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../sprawlmcp/tools.go")
	if err != nil {
		t.Fatalf("read tools.go: %v", err)
	}
	src := string(b)
	start := strings.Index(src, `"name":        "send_message"`)
	if start < 0 {
		t.Fatalf("send_message tool entry not found in tools.go — this scan is not looking at what it claims to")
	}
	rest := src[start+len(`"name":        "send_message"`):]
	if end := strings.Index(rest, `"name":`); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestAgentVisibleSurfaces_StateOnlyTheDocumentedLimit is the enforced form of
// the "hardLimit is not stated anywhere an agent reads" AC.
//
// It asserts the positive property rather than the absence of a string: EVERY
// cap figure on an agent-visible surface must be the documented one. That is
// immune to unrelated 500s and still fires the moment a prompt or tool
// description starts quoting what actually rejects.
//
// The scan carries an assertion-count floor. A regexp that matches nothing
// passes forever, which is the non-asserting fallback /testing-practices names —
// the floor turns "the cap statements were deleted" from silence into a failure.
// It is a FLOOR, not a census: prompt.go states no cap today and is allowed not
// to, so the floor is aggregate and sits below the current count.
const minAgentVisibleCapStatements = 8

func TestAgentVisibleSurfaces_StateOnlyTheDocumentedLimit(t *testing.T) {
	want := strconv.Itoa(sendMessageBodyDocumentedMaxRunes)
	hard := strconv.Itoa(sendMessageBodyHardMaxRunes)
	hardWordRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(hard) + `\b`)

	// check returns how many cap statements it saw so the caller can enforce
	// the floor. A cap figure that is not the documented one fails HERE
	// regardless of which limit it happens to be: any second number on an
	// agent-visible surface makes the budget ambiguous.
	check := func(t *testing.T, label, src string) int {
		t.Helper()
		matches := capStatementRe.FindAllStringSubmatch(src, -1)
		for _, m := range matches {
			if m[1] != want {
				t.Errorf("%s states a cap of %q (in %q), want the documented %q — the enforcing limit must never appear where an agent reads it (QUM-1216). If %q is an unrelated limit, scope it out of capStatementRe rather than loosening this check", label, m[1], m[0], want, m[1])
			}
		}
		// Belt and braces: the hard limit must not appear in ANY form,
		// including shapes capStatementRe would miss ("capped at 500.").
		//
		// Guarded on hard != want, and word-bounded. Unguarded, equalising the
		// two constants makes this arm assert that the documented cap must not
		// be documented — it fired on all four correct sources with an exactly
		// inverted diagnostic. The unequal-pair invariant is owned by
		// AreADeliberatelyUnequalPair; this arm presumes it rather than
		// re-litigating it. The word boundary keeps "1500" or "500ms" from
		// failing here with a message that would misdescribe them.
		if hard != want && hardWordRe.MatchString(src) {
			t.Errorf("%s contains %q — the hard limit must not be visible to agents in any form", label, hard)
		}
		return len(matches)
	}

	total := 0
	for _, path := range agentVisibleCapSources(t) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		total += check(t, path, string(b))
	}

	entry := sendMessageToolEntry(t)
	n := check(t, "tools.go send_message entry", entry)
	if n == 0 {
		t.Errorf("the send_message tool description states no cap at all — either it stopped telling agents the limit, or the slice is pointed at the wrong text; both are failures")
	}
	total += n

	if total < minAgentVisibleCapStatements {
		t.Errorf("only %d cap statements found across agent-visible surfaces, want at least %d — a scan that matches nothing cannot fail, so too few matches is itself the failure", total, minAgentVisibleCapStatements)
	}
}

// TestReal_SendMessage_BodyAtDocumentedLimit_Accepted keeps the documented
// budget honest: a body of exactly the length agents are TOLD they may send must
// deliver. Since QUM-1216 it no longer pins the boundary — any cap >= 300
// satisfies it — so the inclusive-boundary claim lives in
// TestReal_SendMessage_BodyAtHardLimit_Accepted below.
func TestReal_SendMessage_BodyAtDocumentedLimit_Accepted(t *testing.T) {
	r, tmpDir := startedAlice(t)

	body := strings.Repeat("a", sendMessageBodyDocumentedMaxRunes)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
		t.Fatalf("SendMessage(exactly the documented %d runes) = %v, want accepted — an agent that hits the number it was given must never be refused", sendMessageBodyDocumentedMaxRunes, err)
	}
	// assertDelivered also pins "never truncation" on the accepted side: the
	// body must arrive whole, in both stores.
	assertDelivered(t, tmpDir, "alice", body)
}

// TestReal_SendMessage_BodyCapIsRuneCounted_NotBytes is the assertion that
// distinguishes a rune cap from a byte cap. "é" is 2 bytes / 1 rune, so a body
// sitting exactly ON the hard limit is 1000 bytes: a len(body) implementation
// rejects it while passing every ASCII-only test above.
//
// The counts sit at the HARD limit, not the documented one (QUM-1216). The
// binding constraint is the REJECTED arm: 301 runes is now comfortably under the
// hard limit, so a correct implementation accepts it and the arm can no longer
// observe what the length is reported IN. The accepted arm moves with it to keep
// the pair on the same boundary.
//
// D7 specifies rune counting, following the toastTextMaxRunes precedent.
func TestReal_SendMessage_BodyCapIsRuneCounted_NotBytes(t *testing.T) {
	t.Run("hard-limit multibyte runes accepted", func(t *testing.T) {
		r, _ := startedAlice(t)
		body := strings.Repeat("é", sendMessageBodyHardMaxRunes) // 500 runes, 1000 bytes
		if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
			t.Fatalf("SendMessage(%d multibyte runes) = %v, want accepted — the cap counts runes, not bytes", sendMessageBodyHardMaxRunes, err)
		}
	})
	t.Run("one over the hard limit, multibyte, rejected", func(t *testing.T) {
		r, tmpDir := startedAlice(t)
		n := sendMessageBodyHardMaxRunes + 1
		body := strings.Repeat("é", n)
		_, err := r.SendMessage(context.Background(), "alice", body, false, false)
		if err == nil {
			t.Fatalf("SendMessage(%d multibyte runes) = nil, want a hard error", n)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(n)) {
			t.Errorf("error %q should report the length in RUNES (%d), not bytes (%d)", err.Error(), n, 2*n)
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
// is allowed to push the stored body past the cap. The body sits at the HARD
// limit so the stored result genuinely exceeds what rejects; at the documented
// 300 the wrapped body would still be under 500 and the C3 mutation would slip
// through green.
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

	body := strings.Repeat("a", sendMessageBodyHardMaxRunes)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err != nil {
		t.Fatalf("SendMessage(%d runes, dead target) = %v, want accepted", sendMessageBodyHardMaxRunes, err)
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
	if got := len([]rune(entries[0].Body)); got <= sendMessageBodyHardMaxRunes {
		t.Errorf("stored body = %d runes, want > %d — the caller's at-the-limit body plus sprawl's own routed-up prefix must exceed the cap; if it does not, this test cannot detect a cap enforced after WrapForDeadTarget", got, sendMessageBodyHardMaxRunes)
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

	body := strings.Repeat("a", overCapRunes)
	_, err := r.SendMessage(context.Background(), "nosuchagent", body, false, false)
	if err == nil {
		t.Fatalf("SendMessage(560 runes, unknown recipient) = nil, want an error")
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

	body := strings.Repeat("a", overCapRunes)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err == nil {
		t.Fatalf("SendMessage(560 runes, dead target) = nil error, want a hard error — the cap must fire before route-up, not only on the live-target path")
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

	body := strings.Repeat("a", overCapRunes)
	if _, err := r.SendMessage(context.Background(), "alice", body, false, false); err == nil {
		t.Fatalf("SendMessage(560 runes) = nil error, want a hard error")
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
