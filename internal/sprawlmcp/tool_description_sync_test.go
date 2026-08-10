// Package sprawlmcp tests pin consistency between MCP tool descriptions
// (tools.go) and prompt mentions in internal/agent/prompt_mode.go. After
// QUM-550 slice 5, the deprecated send_async / send_interrupt tools are
// removed entirely; send_message is the sole messaging tool surfaced in
// the prompt templates.
package sprawlmcp

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// promptModeSource reads internal/agent/prompt_mode.go relative to this test
// package. We intentionally avoid adding a new export from internal/agent;
// the test asserts the source file's literal contents stay in sync with the
// MCP tool surface.
func promptModeSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../agent/prompt_mode.go")
	if err != nil {
		t.Fatalf("read prompt_mode.go: %v", err)
	}
	return string(b)
}

// canonicalMessagingTools is the subset of canonical tools we expect to be
// surfaced in the TUI-mode prompt templates.
//
// QUM-1186: `report_status` and `delegate` were REMOVED from this list. Leaving
// either here inverted the test's purpose: §1 asserts every entry appears in
// prompt_mode.go, so the list would have REQUIRED the shipped agent prompt to
// go on teaching a deleted tool — and it would have stayed green only for as
// long as that prose survived, then gone red at whoever removed it.
var canonicalMessagingTools = []string{
	"send_message",
	"peek",
	"spawn",
	"merge",
	"retire",
}

// nowParamRe matches the `now` PARAMETER, not the English adverb.
//
// The naive flip of this test's §2 probe when `interrupt` was renamed is
// `strings.Contains(window, "now")` — and that assertion CANNOT FAIL. "now" is
// an ordinary English word already present in prompt_mode.go prose, and
// strings.Contains is substring-matched, so "know"/"known" alone satisfies it.
// It would stay green with the parameter entirely absent. Requiring the
// `now:` / `now =` shape is the form only the parameter produces.
var nowParamRe = regexp.MustCompile(`now\s*[:=]`)

// TestPromptModeDescriptions_InSyncWithMCPTools verifies the canonical
// messaging surface is mentioned in prompt_mode.go and that the canonical
// `send_message(to, body, now)` argument shape is preserved.
func TestPromptModeDescriptions_InSyncWithMCPTools(t *testing.T) {
	src := promptModeSource(t)

	// 1. Every canonical messaging tool must appear by name in prompt_mode.go.
	for _, name := range canonicalMessagingTools {
		if !strings.Contains(src, name) {
			t.Errorf("prompt_mode.go missing canonical MCP tool mention: %q", name)
		}
	}

	// 2. send_message: must appear with the `now` parameter referenced nearby
	//    (within 500 chars of a `send_message(` call site), and must NOT still
	//    reference the renamed-away `interrupt`. Both arms are needed:
	//    `send_message({to, body, now: false, interrupt: false})` would satisfy
	//    the first alone.
	if idx := strings.Index(src, "send_message("); idx < 0 {
		t.Errorf("prompt_mode.go must reference `send_message(` (canonical messaging tool)")
	} else {
		start := idx
		end := idx + 500
		if end > len(src) {
			end = len(src)
		}
		window := src[start:end]
		if !nowParamRe.MatchString(window) {
			t.Errorf("prompt_mode.go mentions send_message( but not its `now` argument within 500 chars; window=%q", window)
		}
		if strings.Contains(window, "interrupt") {
			t.Errorf("prompt_mode.go still references the renamed-away `interrupt` argument near send_message(; it is `now` as of QUM-1186; window=%q", window)
		}
	}

	// 3. Removed tools must NOT appear anywhere in prompt_mode.go. A prompt
	//    that keeps teaching a deleted tool makes every spawned agent call it
	//    and collect an unknown-tool error — the deletion reaches the
	//    implementation but not the advertisement.
	//
	//    `delegate(` and not bare `delegate`: "if you delegate research to a
	//    sidechain" at prompt_mode.go's sidechain guidance is ordinary English
	//    and must survive. The bare-word ban belongs in internal/agent's
	//    prompt-render test, which can allowlist by line.
	for _, banned := range []string{"send_async", "send_interrupt", "report_status", "delegate("} {
		if strings.Contains(src, banned) {
			t.Errorf("prompt_mode.go must not reference removed tool %q", banned)
		}
	}

	// 4. Negative: send_message must NOT carry deprecated send_async
	//    argument shape (subject:/reply_to:/tags:).
	bannedNearSendMessage := []string{"subject:", "reply_to:", "tags:"}
	searchFrom := 0
	for {
		i := strings.Index(src[searchFrom:], "send_message(")
		if i < 0 {
			break
		}
		abs := searchFrom + i
		lo := abs
		hi := abs + 200
		if hi > len(src) {
			hi = len(src)
		}
		window := src[lo:hi]
		for _, banned := range bannedNearSendMessage {
			if strings.Contains(window, banned) {
				t.Errorf("prompt_mode.go has banned key %q within 200 chars of send_message(; window=%q", banned, window)
			}
		}
		searchFrom = abs + len("send_message(")
	}

	// QUM-1186: the former §5 scanned every `report_status(` mention for a
	// banned `detail:` field. §3 now bans `report_status` outright, so that
	// loop could only ever iterate zero times — a permanently vacuous check
	// that still reads as coverage. It is deleted rather than "updated":
	// there is no report_status contract left to pin.
}

// TestNowParamRe_Controls demonstrates the §2 probe both firing and staying
// quiet. The needle is a common English word, which is exactly the shape that
// produces a check that cannot fail, so it is not accepted green on trust.
func TestNowParamRe_Controls(t *testing.T) {
	// Positive control, direction = MUST fire (i.e. MUST NOT match): the word
	// "now" (and "know"/"known") is present, the PARAMETER is absent.
	vacuous := `send_message({to: "x", body: "..."}) — you now know the recipient is notified; it is known to be async.`
	if !strings.Contains(vacuous, "now") {
		t.Fatal("fixture no longer contains the English word `now`; the demonstration is broken")
	}
	if nowParamRe.MatchString(vacuous) {
		t.Errorf("nowParamRe matched a subject carrying no `now` parameter — it is as vacuous as strings.Contains(w, \"now\"); subject=%q", vacuous)
	}

	// Negative control, direction = MUST stay quiet (i.e. MUST match): the
	// parameter really is there, in both spellings a prompt might use.
	for _, subject := range []string{
		`send_message({to: "x", body: "y", now: true})`,
		`send_message(to = "x", now = false)`,
	} {
		if !nowParamRe.MatchString(subject) {
			t.Errorf("nowParamRe missed a real `now` parameter; subject=%q", subject)
		}
	}
}

// TestPromptModeDescriptions_SendMessageMentionedInTUITemplates pins the
// literal `send_message(` call-shape into prompt_mode.go.
func TestPromptModeDescriptions_SendMessageMentionedInTUITemplates(t *testing.T) {
	src := promptModeSource(t)
	if !strings.Contains(src, "send_message(") {
		t.Fatalf("prompt_mode.go must reference `send_message(` — canonical messaging tool after QUM-550.")
	}
}

// TestPromptModeDescriptions_ReportStatusHasNoDetailField was deleted in
// QUM-1186. It scanned prompt_mode.go for `report_status(` lines carrying a
// banned `detail:` field; with report_status deleted the loop body was
// unreachable, so the test passed by iterating zero times. A zero-iteration
// loop that still reads as coverage is the vacuous green this slice exists to
// remove. §3 of TestPromptModeDescriptions_InSyncWithMCPTools now bans the
// tool name outright, which is the assertion that has a subject.
