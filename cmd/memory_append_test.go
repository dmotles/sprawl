// cmd/memory_append_test.go — failing red-phase tests for QUM-515 cobra
// wiring of `sprawl memory append-session`. The implementation is in
// cmd/memory_append.go (not yet written).
package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
)

// fakeAppendRun captures calls into memory.AppendSessionWithOptions.
type fakeAppendRun struct {
	called bool
	opts   memory.AppendOptions
	res    memory.AppendResult
	err    error
}

func (f *fakeAppendRun) Run(_ context.Context, opts memory.AppendOptions) (memory.AppendResult, error) {
	f.called = true
	f.opts = opts
	return f.res, f.err
}

func resetAppendFlags() {
	appendSessionDryRun = false
	appendSessionModel = "haiku"
	appendSessionTimeout = memory.DefaultInvokeTimeout
	appendSessionLockTO = memory.DefaultAppendLockTimeout
}

func newTestAppendDeps(t *testing.T, root string) (*appendSessionDeps, *fakeAppendRun, *sentinelInvoker) {
	t.Helper()
	fake := &fakeAppendRun{}
	inv := &sentinelInvoker{tag: "append-cmd-test-sentinel"}
	deps := &appendSessionDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		Stdout:  io.Discard,
		Invoker: inv,
		Run:     fake.Run,
	}
	return deps, fake, inv
}

func TestAppendSessionCmdRegistered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"memory", "append-session"})
	if err != nil {
		t.Fatalf("rootCmd.Find: %v", err)
	}
	if c == nil || c.Name() != "append-session" {
		t.Fatalf("memory append-session not registered; got %v", c)
	}
	if !c.Hidden {
		t.Error("append-session should be Hidden: true")
	}
}

func TestAppendSessionCmd_RequiresSessionIDArg(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	root := t.TempDir()
	deps, _, _ := newTestAppendDeps(t, root)
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	// Drive the parent so cobra performs Args validation.
	rootCmd.SetArgs([]string{"memory", "append-session"})
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(io.Discard)
	defer func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetOut(nil)
	}()
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when invoked with no args; got nil")
	}
}

func TestAppendSessionCmd_PassesArgsToRun(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	root := t.TempDir()
	deps, fake, _ := newTestAppendDeps(t, root)
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	const sid = "abc12345-abc1-abc1-abc1-abcabcabcabc"
	if err := appendSessionCmd.RunE(appendSessionCmd, []string{sid}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !fake.called {
		t.Fatal("Run was not called")
	}
	if fake.opts.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", fake.opts.SessionID, sid)
	}
	if fake.opts.SprawlRoot != root {
		t.Errorf("SprawlRoot = %q, want %q", fake.opts.SprawlRoot, root)
	}
	if fake.opts.Cfg.Model != "haiku" {
		t.Errorf("default model = %q, want haiku", fake.opts.Cfg.Model)
	}
	if fake.opts.LockTimeout != memory.DefaultAppendLockTimeout {
		t.Errorf("default LockTimeout = %v, want %v", fake.opts.LockTimeout, memory.DefaultAppendLockTimeout)
	}
}

func TestAppendSessionCmd_FlagsForwarded(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	root := t.TempDir()
	deps, fake, sentinel := newTestAppendDeps(t, root)
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	appendSessionDryRun = true
	appendSessionModel = "sonnet"
	appendSessionTimeout = 30 * time.Second
	appendSessionLockTO = 2 * time.Second

	const sid = "abc12345-abc1-abc1-abc1-abcabcabcabc"
	if err := appendSessionCmd.RunE(appendSessionCmd, []string{sid}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !fake.opts.DryRun {
		t.Error("DryRun not forwarded")
	}
	if fake.opts.Cfg.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", fake.opts.Cfg.Model)
	}
	if fake.opts.Cfg.InvokeTimeout != 30*time.Second {
		t.Errorf("InvokeTimeout = %v, want 30s", fake.opts.Cfg.InvokeTimeout)
	}
	if fake.opts.LockTimeout != 2*time.Second {
		t.Errorf("LockTimeout = %v, want 2s", fake.opts.LockTimeout)
	}
	gotInv, ok := fake.opts.Invoker.(*sentinelInvoker)
	if !ok || gotInv != sentinel {
		t.Errorf("Invoker pointer not propagated; got %T %p, want %p", fake.opts.Invoker, gotInv, sentinel)
	}
}

func TestAppendSessionCmd_RequiresSprawlRoot(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	deps := &appendSessionDeps{
		Getenv:  func(string) string { return "" },
		Stdout:  io.Discard,
		Invoker: &sentinelInvoker{},
		Run: func(context.Context, memory.AppendOptions) (memory.AppendResult, error) {
			return memory.AppendResult{}, nil
		},
	}
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	err := appendSessionCmd.RunE(appendSessionCmd, []string{"abc12345-abc1-abc1-abc1-abcabcabcabc"})
	if err == nil {
		t.Fatal("expected error when SPRAWL_ROOT is unset; got nil")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT; got %v", err)
	}
}

func TestAppendSessionCmd_NextActionHint_NewRow(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	root := t.TempDir()
	deps, fake, _ := newTestAppendDeps(t, root)
	fake.res = memory.AppendResult{NoOp: false, Row: "2026-01-01 11111111-1111-1111-1111-111111111111 | foo"}
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	var stderr bytes.Buffer
	appendSessionCmd.SetErr(&stderr)
	defer appendSessionCmd.SetErr(nil)

	if err := appendSessionCmd.RunE(appendSessionCmd, []string{"11111111-1111-1111-1111-111111111111"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	out := stderr.String()
	if !strings.Contains(out, "timeline.md") {
		t.Errorf("hint should mention timeline path; got %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "append") && !strings.Contains(strings.ToLower(out), "diff") {
		t.Errorf("hint should mention 'append' or 'diff'; got %q", out)
	}
}

func TestAppendSessionCmd_NextActionHint_NoOp(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	root := t.TempDir()
	deps, fake, _ := newTestAppendDeps(t, root)
	fake.res = memory.AppendResult{NoOp: true}
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	var stderr bytes.Buffer
	appendSessionCmd.SetErr(&stderr)
	defer appendSessionCmd.SetErr(nil)

	if err := appendSessionCmd.RunE(appendSessionCmd, []string{"11111111-1111-1111-1111-111111111111"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	out := strings.ToLower(stderr.String())
	if !strings.Contains(out, "already") && !strings.Contains(out, "no-op") {
		t.Errorf("hint should mention 'already present' or 'no-op'; got %q", stderr.String())
	}
}

func TestAppendSessionCmd_NextActionHint_DryRun(t *testing.T) {
	resetAppendFlags()
	defer resetAppendFlags()

	root := t.TempDir()
	deps, fake, _ := newTestAppendDeps(t, root)
	fake.res = memory.AppendResult{Row: "2026-01-01 11111111-1111-1111-1111-111111111111 | foo"}
	defaultAppendSessionDeps = deps
	defer func() { defaultAppendSessionDeps = nil }()

	appendSessionDryRun = true

	var stderr bytes.Buffer
	appendSessionCmd.SetErr(&stderr)
	defer appendSessionCmd.SetErr(nil)

	if err := appendSessionCmd.RunE(appendSessionCmd, []string{"11111111-1111-1111-1111-111111111111"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	out := stderr.String()
	if !strings.Contains(strings.ToLower(out), "dry-run") {
		t.Errorf("hint should mention 'Dry-run'; got %q", out)
	}
	if !strings.Contains(out, "without --dry-run") {
		t.Errorf("hint should suggest re-run without --dry-run; got %q", out)
	}
}
