package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/host"
)

// dummyMCPServer satisfies host.MCPServer for tests that need an MCP bridge.
type dummyMCPServer struct{}

func (d *dummyMCPServer) HandleMessage(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestRunnerHandle_StopStopsRunnerBeforeCancel(t *testing.T) {
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn
	defer func() {
		startRunnerFn = oldStart
		runRunnerFn = oldRun
		stopRunnerFn = oldStop
	}()

	var runCtx context.Context
	runCanceled := make(chan struct{}, 1)
	stopCalled := make(chan struct{}, 1)

	startRunnerFn = func(ctx context.Context, _ *agentloop.RunnerDeps, _ string) (*agentloop.Runner, error) {
		runCtx = ctx
		return nil, nil
	}
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		<-ctx.Done()
		runCanceled <- struct{}{}
		return nil
	}
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		if runCtx == nil {
			t.Fatal("run context was not captured")
		}
		if runCtx.Err() != nil {
			t.Fatal("runner context was canceled before stop executed")
		}
		stopCalled <- struct{}{}
		return nil
	}

	handle, err := newInProcessRuntimeStarter(backend.InitSpec{}, nil).Start(context.Background(), RuntimeStartSpec{
		Name:       "alice",
		Worktree:   "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo",
		SessionID:  "sess-alice",
		TreePath:   "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	select {
	case <-stopCalled:
	default:
		t.Fatal("stopRunnerFn was not called")
	}
	select {
	case <-runCanceled:
	default:
		t.Fatal("runner context was not canceled after stop")
	}
}

// makeExitError returns a real *exec.ExitError by running a command that fails.
func makeExitError() *exec.ExitError {
	err := exec.Command("false").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		panic(fmt.Sprintf("expected *exec.ExitError from 'false', got %T", err))
	}
	return exitErr
}

func TestRunnerHandle_StopSuppressesExitErrorFromStop(t *testing.T) {
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn

	exitErr := makeExitError()
	runDone := make(chan struct{})

	startRunnerFn = func(_ context.Context, _ *agentloop.RunnerDeps, _ string) (*agentloop.Runner, error) {
		return nil, nil
	}
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		defer close(runDone)
		<-ctx.Done()
		return nil
	}
	// Simulate Process.Stop returning a wrapped *exec.ExitError
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		return fmt.Errorf("waiting for process: %w", exitErr)
	}

	handle, err := newInProcessRuntimeStarter(backend.InitSpec{}, nil).Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo", SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	stopErr := handle.Stop(context.Background())
	// Wait for the run goroutine to finish before restoring package-level vars.
	<-runDone
	startRunnerFn = oldStart
	runRunnerFn = oldRun
	stopRunnerFn = oldStop

	if stopErr != nil {
		t.Fatalf("Stop() should suppress *exec.ExitError during intentional stop, got: %v", stopErr)
	}
}

func TestRunnerHandle_StopSuppressesExitErrorFromWaitErr(t *testing.T) {
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn

	exitErr := makeExitError()

	startRunnerFn = func(_ context.Context, _ *agentloop.RunnerDeps, _ string) (*agentloop.Runner, error) {
		return nil, nil
	}
	// Simulate Run() returning an *exec.ExitError (process crash during teardown)
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		<-ctx.Done()
		return fmt.Errorf("child crashed: %w", exitErr)
	}
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		return nil
	}

	handle, err := newInProcessRuntimeStarter(backend.InitSpec{}, nil).Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo", SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	stopErr := handle.Stop(context.Background())
	// Restore package-level vars after Stop completes (goroutine finishes via done channel).
	startRunnerFn = oldStart
	runRunnerFn = oldRun
	stopRunnerFn = oldStop

	if stopErr != nil {
		t.Fatalf("Stop() should suppress *exec.ExitError from waitErr, got: %v", stopErr)
	}
}

func TestRunnerHandle_StopPropagatesNonExitError(t *testing.T) {
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn

	runDone := make(chan struct{})
	startRunnerFn = func(_ context.Context, _ *agentloop.RunnerDeps, _ string) (*agentloop.Runner, error) {
		return nil, nil
	}
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		defer close(runDone)
		<-ctx.Done()
		return nil
	}
	// Non-ExitError: must propagate
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		return fmt.Errorf("closing writer: broken pipe")
	}

	handle, err := newInProcessRuntimeStarter(backend.InitSpec{}, nil).Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo", SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	err = handle.Stop(context.Background())
	// Wait for the run goroutine to finish before restoring package-level vars.
	<-runDone
	startRunnerFn = oldStart
	runRunnerFn = oldRun
	stopRunnerFn = oldStop

	if err == nil {
		t.Fatal("Stop() should propagate non-ExitError, got nil")
	}
	want := "closing writer: broken pipe"
	if err.Error() != want {
		t.Fatalf("Stop() error = %q, want %q", err.Error(), want)
	}
}

func TestRunnerHandle_StopPropagatesNonExitErrorFromWaitErr(t *testing.T) {
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn

	startRunnerFn = func(_ context.Context, _ *agentloop.RunnerDeps, _ string) (*agentloop.Runner, error) {
		return nil, nil
	}
	// Non-ExitError from Run: must propagate
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		<-ctx.Done()
		return fmt.Errorf("unexpected internal failure")
	}
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		return nil
	}

	handle, err := newInProcessRuntimeStarter(backend.InitSpec{}, nil).Start(context.Background(), RuntimeStartSpec{
		Name: "alice", Worktree: "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo", SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	err = handle.Stop(context.Background())
	// Restore package-level vars after Stop completes (goroutine finishes via done channel).
	startRunnerFn = oldStart
	runRunnerFn = oldRun
	stopRunnerFn = oldStop

	if err == nil {
		t.Fatal("Stop() should propagate non-ExitError from waitErr, got nil")
	}
	want := "unexpected internal failure"
	if err.Error() != want {
		t.Fatalf("Stop() error = %q, want %q", err.Error(), want)
	}
}

func TestBuildRunnerDeps_IncludesInitSpecWithMCPBridge(t *testing.T) {
	// When inProcessRuntimeStarter has a non-nil supervisor, Start() wires
	// the MCP bridge into RunnerDeps.InitSpec. This test captures deps via
	// startRunnerFn to verify the wiring.
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn
	defer func() {
		startRunnerFn = oldStart
		runRunnerFn = oldRun
		stopRunnerFn = oldStop
	}()

	var capturedDeps *agentloop.RunnerDeps
	startRunnerFn = func(_ context.Context, deps *agentloop.RunnerDeps, _ string) (*agentloop.Runner, error) {
		capturedDeps = deps
		return nil, nil
	}
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		<-ctx.Done()
		return nil
	}
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		return nil
	}

	mcpBridge := host.NewMCPBridge()
	mcpBridge.Register("sprawl", &dummyMCPServer{})
	initSpec := backend.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     mcpBridge,
	}
	allowedTools := []string{
		"mcp__sprawl__spawn",
		"mcp__sprawl__status",
		"mcp__sprawl__report_status",
	}

	starter := newInProcessRuntimeStarter(initSpec, allowedTools)
	handle, err := starter.Start(context.Background(), RuntimeStartSpec{
		Name:       "alice",
		Worktree:   "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo",
		SessionID:  "sess-alice",
		TreePath:   "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer handle.Stop(context.Background()) //nolint:errcheck

	if capturedDeps == nil {
		t.Fatal("startRunnerFn was not called")
	}

	// InitSpec should carry the sprawl MCP server name.
	if len(capturedDeps.InitSpec.MCPServerNames) == 0 {
		t.Fatal("RunnerDeps.InitSpec.MCPServerNames is empty; expected [\"sprawl\"]")
	}
	found := false
	for _, name := range capturedDeps.InitSpec.MCPServerNames {
		if name == "sprawl" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RunnerDeps.InitSpec.MCPServerNames = %v, want to contain \"sprawl\"", capturedDeps.InitSpec.MCPServerNames)
	}

	// InitSpec.ToolBridge should be non-nil.
	if capturedDeps.InitSpec.ToolBridge == nil {
		t.Fatal("RunnerDeps.InitSpec.ToolBridge is nil; expected MCP bridge")
	}

	// AllowedTools should be passed through.
	if len(capturedDeps.AllowedTools) != len(allowedTools) {
		t.Fatalf("RunnerDeps.AllowedTools length = %d, want %d", len(capturedDeps.AllowedTools), len(allowedTools))
	}
	toolSet := make(map[string]bool, len(capturedDeps.AllowedTools))
	for _, tool := range capturedDeps.AllowedTools {
		toolSet[tool] = true
	}
	for _, tool := range allowedTools {
		if !toolSet[tool] {
			t.Errorf("RunnerDeps.AllowedTools missing %q", tool)
		}
	}
}

func TestInProcessRuntimeStarter_ChildCapabilitiesIncludeToolBridge(t *testing.T) {
	// After implementation, a child started via inProcessRuntimeStarter should
	// report SupportsToolBridge: true in its Capabilities.
	oldStart := startRunnerFn
	oldRun := runRunnerFn
	oldStop := stopRunnerFn
	defer func() {
		startRunnerFn = oldStart
		runRunnerFn = oldRun
		stopRunnerFn = oldStop
	}()

	var capturedDeps *agentloop.RunnerDeps
	startRunnerFn = func(ctx context.Context, deps *agentloop.RunnerDeps, name string) (*agentloop.Runner, error) {
		capturedDeps = deps
		return nil, nil
	}
	runRunnerFn = func(_ *agentloop.Runner, ctx context.Context) error {
		<-ctx.Done()
		return nil
	}
	stopRunnerFn = func(_ *agentloop.Runner, _ context.Context) error {
		return nil
	}

	mcpBridge := host.NewMCPBridge()
	mcpBridge.Register("sprawl", &dummyMCPServer{})
	initSpec := backend.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     mcpBridge,
	}
	handle, err := newInProcessRuntimeStarter(initSpec, []string{"mcp__sprawl__spawn"}).Start(context.Background(), RuntimeStartSpec{
		Name:       "alice",
		Worktree:   "/repo/.sprawl/worktrees/alice",
		SprawlRoot: "/repo",
		SessionID:  "sess-alice",
		TreePath:   "weave/alice",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer handle.Stop(context.Background()) //nolint:errcheck

	// The captured deps should have InitSpec with MCP bridge configured.
	if capturedDeps == nil {
		t.Fatal("startRunnerFn was not called with deps")
	}
	if capturedDeps.InitSpec.ToolBridge == nil {
		t.Error("RunnerDeps.InitSpec.ToolBridge is nil; child MCP bridge not wired")
	}
	if len(capturedDeps.InitSpec.MCPServerNames) == 0 {
		t.Error("RunnerDeps.InitSpec.MCPServerNames is empty; expected sprawl")
	}
	if len(capturedDeps.AllowedTools) == 0 {
		t.Error("RunnerDeps.AllowedTools is empty; expected MCP tool names")
	}
}

// Ensure the backend import is used (compile guard).
var _ backend.InitSpec
