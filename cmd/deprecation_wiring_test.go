package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// These tests assert that every deprecated command's runX entry point
// emits the deprecation warning. They use the package-level
// deprecationStderr buffer hooked up via withDeprecationCapture so the
// warning text is observable without reaching real os.Stderr.
//
// The tests pass the minimum args needed for the warning line to fire —
// the warning is the FIRST statement in each runX, so downstream errors
// (missing env, missing agent, etc.) are fine and we don't assert on
// them. The deps.stdout/deps.stderr buffers are also asserted to NOT
// contain the warning, validating that the helper writes only to the
// dedicated channel and leaves command stdout/stderr untouched.

func assertDeprecation(t *testing.T, buf *bytes.Buffer, cmdName, replacement string) {
	t.Helper()
	out := buf.String()
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected deprecation warning, got: %q", out)
	}
	if !strings.Contains(out, "`sprawl "+cmdName+"`") {
		t.Errorf("expected warning to mention `sprawl %s`, got: %q", cmdName, out)
	}
	if replacement != "" && !strings.Contains(out, replacement) {
		t.Errorf("expected warning to mention %q, got: %q", replacement, out)
	}
}

func TestDeprecation_Spawn_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	// Pass nil deps — runSpawn fires the warning before calling Spawn.
	defer func() { _ = recover() }()
	_ = runSpawn(nil, "engineering", "engineer", "task", "branch")
	assertDeprecation(t, buf, "spawn", "spawn")
}

func TestDeprecation_Spawn_QuietSuppresses(t *testing.T) {
	buf := withDeprecationCapture(t, "1")
	defer func() { _ = recover() }()
	_ = runSpawn(nil, "engineering", "engineer", "task", "branch")
	if buf.Len() != 0 {
		t.Errorf("expected no warning under quiet env, got: %q", buf.String())
	}
}

func TestDeprecation_Retire_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	defer func() { _ = recover() }()
	_ = runRetire(nil, "alice", false, false, false, false, false)
	assertDeprecation(t, buf, "retire", "retire")
}

func TestDeprecation_Kill_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	defer func() { _ = recover() }()
	_ = runKill(nil, "alice", false)
	assertDeprecation(t, buf, "kill", "kill")
}

func TestDeprecation_Delegate_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &delegateDeps{getenv: func(string) string { return "" }}
	_ = runDelegate(deps, "alice", "task")
	assertDeprecation(t, buf, "delegate", "delegate")
}

func TestDeprecation_MessagesSend_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &messagesDeps{
		getenv: func(string) string { return "" },
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	_ = runMessagesSend(deps, "alice", "subj", "body")
	assertDeprecation(t, buf, "messages send", "send_async")

	// Verify the warning didn't leak into deps.stderr / deps.stdout.
	if strings.Contains(deps.stderr.(*bytes.Buffer).String(), "warning:") {
		t.Errorf("deprecation warning leaked into deps.stderr: %q", deps.stderr)
	}
	if strings.Contains(deps.stdout.(*bytes.Buffer).String(), "warning:") {
		t.Errorf("deprecation warning leaked into deps.stdout: %q", deps.stdout)
	}
}

func TestDeprecation_MessagesRead_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &messagesDeps{
		getenv: func(string) string { return "" },
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	_, _ = runMessagesRead(deps, "abc")
	assertDeprecation(t, buf, "messages read", "messages_read")
}

func TestDeprecation_MessagesList_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &messagesDeps{
		getenv: func(string) string { return "" },
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	_ = runMessagesListDisplay(deps, "")
	assertDeprecation(t, buf, "messages list", "messages_list")
}

func TestDeprecation_MessagesArchive_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	// Invoke the cobra RunE directly — archive's warning lives there.
	_ = messagesArchiveCmd.RunE(messagesArchiveCmd, []string{"abc"})
	assertDeprecation(t, buf, "messages archive", "messages_archive")
}

func TestDeprecation_Report_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &reportDeps{getenv: func(string) string { return "" }}
	_ = runReport(deps, "status", "msg")
	assertDeprecation(t, buf, "report status", "report_status")
}

func TestDeprecation_Status_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	// Inject a deps with empty getenv so the warning fires and runStatus
	// then errors out on missing SPRAWL_ROOT — keeps the test hermetic
	// regardless of the real env.
	prev := defaultStatusDeps
	defaultStatusDeps = &statusDeps{
		getenv: func(string) string { return "" },
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	t.Cleanup(func() { defaultStatusDeps = prev })
	_ = statusCmd.RunE(statusCmd, []string{})
	assertDeprecation(t, buf, "status", "status")
}

func TestDeprecation_Tree_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &treeDeps{getenv: func(string) string { return "" }}
	_ = runTree(deps, &bytes.Buffer{}, false, "")
	assertDeprecation(t, buf, "tree", "status")
}

func TestDeprecation_Handoff_Wired(t *testing.T) {
	buf := withDeprecationCapture(t, "")
	deps := &handoffDeps{getenv: func(string) string { return "" }}
	_ = runHandoff(deps)
	assertDeprecation(t, buf, "handoff", "handoff")
}

func TestDeprecation_Color_Wired(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"show", func() error { return runColorShow(&colorDeps{getenv: func(string) string { return "" }}) }},
		{"list", func() error { return runColorList(&colorDeps{getenv: func(string) string { return "" }}) }},
		{"rotate", func() error { return runColorRotate(&colorDeps{getenv: func(string) string { return "" }}) }},
		{"set", func() error {
			return runColorSet(&colorDeps{getenv: func(string) string { return "" }}, "blue")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := withDeprecationCapture(t, "")
			_ = tc.fn()
			out := buf.String()
			if !strings.Contains(out, "warning:") || !strings.Contains(out, "`sprawl color`") {
				t.Errorf("expected deprecation warning for color %s, got: %q", tc.name, out)
			}
		})
	}
}

// Smoke: archive RunE branches that hit the warning before any flag
// validation. Calling without args + no flags is an error path; we still
// expect the warning to have fired first.
func TestDeprecation_MessagesArchive_FiresBeforeArgValidation(t *testing.T) {
	// reset flag globals to default so the test is order-independent.
	prevAll, prevRead := archiveAll, archiveRead
	archiveAll, archiveRead = false, false
	t.Cleanup(func() { archiveAll, archiveRead = prevAll, prevRead })

	buf := withDeprecationCapture(t, "")
	err := messagesArchiveCmd.RunE(messagesArchiveCmd, []string{})
	if err == nil {
		t.Fatal("expected error from messages archive with no args/flags")
	}
	assertDeprecation(t, buf, "messages archive", "messages_archive")
}

// Make sure we don't break a runX caller chain: state.AgentState is
// imported here only because go-vet may otherwise flag the unused import
// chain — referencing it via a type assertion costs nothing at runtime.
var _ = (*state.AgentState)(nil)
