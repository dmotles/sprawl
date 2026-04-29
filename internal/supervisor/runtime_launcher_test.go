package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
)

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

	handle, err := newInProcessRuntimeStarter().Start(context.Background(), RuntimeStartSpec{
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

	handle, err := newInProcessRuntimeStarter().Start(context.Background(), RuntimeStartSpec{
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

	handle, err := newInProcessRuntimeStarter().Start(context.Background(), RuntimeStartSpec{
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

	handle, err := newInProcessRuntimeStarter().Start(context.Background(), RuntimeStartSpec{
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

	handle, err := newInProcessRuntimeStarter().Start(context.Background(), RuntimeStartSpec{
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
