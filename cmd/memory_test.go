package cmd

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/memory"
)

// fakeRegenerateRun captures calls into memory.RegenerateTimeline so cmd
// tests can assert on the exact RegenerateOptions assembled by the cobra
// layer.
type fakeRegenerateRun struct {
	called bool
	opts   memory.RegenerateOptions
	err    error
}

func (f *fakeRegenerateRun) Run(_ context.Context, opts memory.RegenerateOptions) error {
	f.called = true
	f.opts = opts
	return f.err
}

// sentinelInvoker is a stand-in for memory.ClaudeInvoker in cmd tests; cmd-layer
// tests never actually invoke it. Carries a tag field so tests can assert
// the *exact* invoker pointer survives the cobra → RegenerateOptions plumbing.
type sentinelInvoker struct {
	tag string
}

func (*sentinelInvoker) Invoke(_ context.Context, _ string, _ ...memory.InvokeOption) (string, error) {
	return "", nil
}

// resetMemoryFlags clears package-level flag vars between tests so flag
// state from one test doesn't leak into the next.
func resetMemoryFlags() {
	regenerateOutPath = ""
	regenerateDryRun = false
	regenerateForce = false
	regenerateModel = ""
	regenerateTimeout = 0
}

func newTestRegenerateDeps(t *testing.T, root string) (*regenerateTimelineDeps, *fakeRegenerateRun, *sentinelInvoker) {
	t.Helper()
	fake := &fakeRegenerateRun{}
	inv := &sentinelInvoker{tag: "cmd-test-sentinel"}
	deps := &regenerateTimelineDeps{
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

func TestMemoryCmdRegistered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"memory", "regenerate-timeline"})
	if err != nil {
		t.Fatalf("rootCmd.Find: %v", err)
	}
	if c == nil || c.Name() != "regenerate-timeline" {
		t.Fatalf("memory regenerate-timeline command not registered; found: %v", c)
	}
	// Public, not hidden.
	if c.Hidden {
		t.Error("regenerate-timeline command should not be hidden")
	}
	// Help text mentions non-destructive .next path.
	long := c.Long + " " + c.Short
	if !strings.Contains(long, ".next") {
		t.Errorf("help text should mention the .next path, got: %q", long)
	}
}

func TestRegenerateTimelineCmd_DefaultOutPath(t *testing.T) {
	resetMemoryFlags()
	defer resetMemoryFlags()

	root := t.TempDir()
	deps, fake, _ := newTestRegenerateDeps(t, root)
	defaultRegenerateTimelineDeps = deps
	defer func() { defaultRegenerateTimelineDeps = nil }()

	regenerateTimelineCmd.SetArgs(nil)
	if err := regenerateTimelineCmd.RunE(regenerateTimelineCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if !fake.called {
		t.Fatal("expected deps.Run to be called")
	}
	wantOut := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	if fake.opts.OutPath != wantOut {
		t.Errorf("OutPath = %q, want %q", fake.opts.OutPath, wantOut)
	}
	if fake.opts.SprawlRoot != root {
		t.Errorf("SprawlRoot = %q, want %q", fake.opts.SprawlRoot, root)
	}
}

func TestRegenerateTimelineCmd_FlagsForwarded(t *testing.T) {
	resetMemoryFlags()
	defer resetMemoryFlags()

	root := t.TempDir()
	deps, fake, sentinel := newTestRegenerateDeps(t, root)
	defaultRegenerateTimelineDeps = deps
	defer func() { defaultRegenerateTimelineDeps = nil }()

	customOut := filepath.Join(root, "custom-output.md")
	regenerateOutPath = customOut
	regenerateDryRun = true
	regenerateForce = true
	regenerateModel = "sonnet"

	if err := regenerateTimelineCmd.RunE(regenerateTimelineCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if !fake.called {
		t.Fatal("expected deps.Run to be called")
	}
	if fake.opts.OutPath != customOut {
		t.Errorf("--out not forwarded: got %q, want %q", fake.opts.OutPath, customOut)
	}
	if !fake.opts.DryRun {
		t.Error("--dry-run not forwarded as DryRun=true")
	}
	if !fake.opts.Force {
		t.Error("--force not forwarded as Force=true")
	}
	if fake.opts.Cfg.Model != "sonnet" {
		t.Errorf("--model not forwarded: got %q, want %q", fake.opts.Cfg.Model, "sonnet")
	}
	// The deps' Invoker must be propagated verbatim — same pointer — into
	// the captured RegenerateOptions. This proves the cobra layer is
	// wiring the dep through and not silently substituting nil or a
	// fresh invoker.
	gotInv, ok := fake.opts.Invoker.(*sentinelInvoker)
	if !ok {
		t.Fatalf("Invoker not propagated as *sentinelInvoker; got %T (%v)", fake.opts.Invoker, fake.opts.Invoker)
	}
	if gotInv != sentinel {
		t.Errorf("Invoker pointer mismatch: got %p (%+v), want %p (%+v)", gotInv, gotInv, sentinel, sentinel)
	}
}
