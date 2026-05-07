package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
)

// fakeContextBuilder captures calls into the BuildContextBlob seam.
type fakeContextBuilder struct {
	called     bool
	sprawlRoot string
	rootName   string
	out        string
	err        error
}

func (f *fakeContextBuilder) Build(sprawlRoot, rootName string) (string, error) {
	f.called = true
	f.sprawlRoot = sprawlRoot
	f.rootName = rootName
	return f.out, f.err
}

// fakeArcRun captures calls into memory.SummarizeProjectArc.
type fakeArcRun struct {
	called bool
	opts   memory.ArcOptions
	out    string
	err    error
}

func (f *fakeArcRun) Run(_ context.Context, opts memory.ArcOptions) (string, error) {
	f.called = true
	f.opts = opts
	return f.out, f.err
}

// resetShowFlags clears flag state between tests.
func resetShowFlags() {
	showArcTimelinePath = ""
	showArcModel = ""
	showArcTimeout = 0
}

func newTestShowContextBlobDeps(t *testing.T, root string) (*showContextBlobDeps, *fakeContextBuilder, *bytes.Buffer) {
	t.Helper()
	fake := &fakeContextBuilder{out: "BLOB CONTENT"}
	buf := &bytes.Buffer{}
	deps := &showContextBlobDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		Stdout: buf,
		Build:  fake.Build,
	}
	return deps, fake, buf
}

func newTestShowArcSummaryDeps(t *testing.T, root string) (*showArcSummaryDeps, *fakeArcRun, *sentinelInvoker, *bytes.Buffer) {
	t.Helper()
	fake := &fakeArcRun{out: "ARC SUMMARY"}
	inv := &sentinelInvoker{tag: "show-arc-sentinel"}
	buf := &bytes.Buffer{}
	deps := &showArcSummaryDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		Stdout:  buf,
		Invoker: inv,
		Run:     fake.Run,
	}
	return deps, fake, inv, buf
}

func TestShowContextBlobCmd_Registered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"memory", "show-context-blob"})
	if err != nil {
		t.Fatalf("rootCmd.Find: %v", err)
	}
	if c == nil || c.Name() != "show-context-blob" {
		t.Fatalf("show-context-blob command not registered; found: %v", c)
	}
	if !c.Hidden {
		t.Error("show-context-blob should be hidden")
	}
}

func TestShowArcSummaryCmd_Registered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"memory", "show-arc-summary"})
	if err != nil {
		t.Fatalf("rootCmd.Find: %v", err)
	}
	if c == nil || c.Name() != "show-arc-summary" {
		t.Fatalf("show-arc-summary command not registered; found: %v", c)
	}
	if !c.Hidden {
		t.Error("show-arc-summary should be hidden")
	}
}

func TestShowContextBlob_CallsBuilder(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, buf := newTestShowContextBlobDeps(t, root)
	defaultShowContextBlobDeps = deps
	defer func() { defaultShowContextBlobDeps = nil }()

	if err := showContextBlobCmd.RunE(showContextBlobCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !fake.called {
		t.Fatal("expected Build to be called")
	}
	if fake.sprawlRoot != root {
		t.Errorf("sprawlRoot = %q, want %q", fake.sprawlRoot, root)
	}
	if !strings.Contains(buf.String(), "BLOB CONTENT") {
		t.Errorf("stdout should contain blob; got: %q", buf.String())
	}
}

func TestShowContextBlob_MissingSprawlRoot(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	deps := &showContextBlobDeps{
		Getenv: func(string) string { return "" },
		Stdout: &bytes.Buffer{},
		Build:  func(string, string) (string, error) { return "", nil },
	}
	defaultShowContextBlobDeps = deps
	defer func() { defaultShowContextBlobDeps = nil }()

	err := showContextBlobCmd.RunE(showContextBlobCmd, nil)
	if err == nil {
		t.Fatal("expected error when SPRAWL_ROOT unset, got nil")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT; got: %v", err)
	}
}

func TestShowContextBlob_BuilderError(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	wantErr := errors.New("builder boom")
	deps := &showContextBlobDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		Stdout: &bytes.Buffer{},
		Build:  func(string, string) (string, error) { return "", wantErr },
	}
	defaultShowContextBlobDeps = deps
	defer func() { defaultShowContextBlobDeps = nil }()

	err := showContextBlobCmd.RunE(showContextBlobCmd, nil)
	if err == nil {
		t.Fatal("expected error from builder, got nil")
	}
	if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), "builder boom") {
		t.Errorf("error should propagate builder error; got: %v", err)
	}
}

func TestShowArcSummary_CallsRunWithDefaults(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, sentinel, _ := newTestShowArcSummaryDeps(t, root)
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	if err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !fake.called {
		t.Fatal("expected Run to be called")
	}
	if fake.opts.SprawlRoot != root {
		t.Errorf("SprawlRoot = %q, want %q", fake.opts.SprawlRoot, root)
	}
	if fake.opts.TimelinePath != "" {
		t.Errorf("TimelinePath should default to empty (resolved by SummarizeProjectArc); got %q", fake.opts.TimelinePath)
	}
	if fake.opts.Cfg.Model != "haiku" {
		t.Errorf("default Model = %q, want %q", fake.opts.Cfg.Model, "haiku")
	}
	gotInv, ok := fake.opts.Invoker.(*sentinelInvoker)
	if !ok {
		t.Fatalf("Invoker not *sentinelInvoker; got %T", fake.opts.Invoker)
	}
	if gotInv != sentinel {
		t.Errorf("Invoker pointer mismatch")
	}
}

func TestShowArcSummary_TimelineFlagForwarded(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, _, _ := newTestShowArcSummaryDeps(t, root)
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	showArcTimelinePath = "/custom/path.md"

	if err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if fake.opts.TimelinePath != "/custom/path.md" {
		t.Errorf("TimelinePath = %q, want %q", fake.opts.TimelinePath, "/custom/path.md")
	}
}

func TestShowArcSummary_ModelFlagForwarded(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, _, _ := newTestShowArcSummaryDeps(t, root)
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	showArcModel = "sonnet"

	if err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if fake.opts.Cfg.Model != "sonnet" {
		t.Errorf("Cfg.Model = %q, want %q", fake.opts.Cfg.Model, "sonnet")
	}
}

func TestShowArcSummary_TimeoutFlagForwarded(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, _, _ := newTestShowArcSummaryDeps(t, root)
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	showArcTimeout = 30 * time.Second

	if err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if fake.opts.Cfg.InvokeTimeout != 30*time.Second {
		t.Errorf("Cfg.InvokeTimeout = %v, want %v", fake.opts.Cfg.InvokeTimeout, 30*time.Second)
	}
}

func TestShowArcSummary_PrintsToStdout(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, _, buf := newTestShowArcSummaryDeps(t, root)
	fake.out = "ARC-SUMMARY-OUTPUT"
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	if err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(buf.String(), "ARC-SUMMARY-OUTPUT") {
		t.Errorf("stdout should contain summary; got: %q", buf.String())
	}
}

func TestShowArcSummary_RunError(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	root := t.TempDir()
	deps, fake, _, _ := newTestShowArcSummaryDeps(t, root)
	wantErr := errors.New("run boom")
	fake.err = wantErr
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil)
	if err == nil {
		t.Fatal("expected error from Run, got nil")
	}
	if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), "run boom") {
		t.Errorf("error should propagate Run error; got: %v", err)
	}
}

func TestShowArcSummary_MissingSprawlRoot(t *testing.T) {
	resetShowFlags()
	defer resetShowFlags()

	deps := &showArcSummaryDeps{
		Getenv:  func(string) string { return "" },
		Stdout:  &bytes.Buffer{},
		Invoker: &sentinelInvoker{},
		Run:     func(context.Context, memory.ArcOptions) (string, error) { return "", nil },
	}
	defaultShowArcSummaryDeps = deps
	defer func() { defaultShowArcSummaryDeps = nil }()

	err := showArcSummaryCmd.RunE(showArcSummaryCmd, nil)
	if err == nil {
		t.Fatal("expected error when SPRAWL_ROOT unset, got nil")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT; got: %v", err)
	}
}
