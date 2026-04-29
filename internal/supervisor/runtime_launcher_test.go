package supervisor

import (
	"context"
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
