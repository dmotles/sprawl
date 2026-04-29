package agentloop

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

type testRunnerProcess struct {
	sendCalls int
}

func (p *testRunnerProcess) Launch(context.Context) error { return nil }

func (p *testRunnerProcess) SendPrompt(context.Context, string) (*protocol.ResultMessage, error) {
	p.sendCalls++
	return &protocol.ResultMessage{Type: "result", Result: "ok"}, nil
}

func (p *testRunnerProcess) InterruptTurn(context.Context) error { return nil }
func (p *testRunnerProcess) Stop(context.Context) error          { return nil }
func (p *testRunnerProcess) IsRunning() bool                     { return true }

func TestStartRunner_DefersInitialPromptUntilRun(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentState := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "weave",
		Prompt:    "implement the feature",
		Branch:    "dmotles/finn",
		Worktree:  sprawlRoot,
		Status:    "active",
		CreatedAt: "2026-04-28T00:00:00Z",
		SessionID: "sess-finn",
		TreePath:  "weave/finn",
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	proc := &testRunnerProcess{}
	deps := &RunnerDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return sprawlRoot
			case "SPRAWL_AGENT_IDENTITY":
				return "finn"
			default:
				return ""
			}
		},
		LoadAgent:    state.LoadAgent,
		NextTask:     func(string, string) (*state.Task, error) { return nil, nil },
		UpdateTask:   func(string, string, *state.Task) error { return nil },
		ListMessages: func(string, string, string) ([]*messages.Message, error) { return nil, nil },
		SendMessage:  func(string, string, string, string, string) error { return nil },
		ReadFile:     os.ReadFile,
		RemoveFile:   os.Remove,
		BuildPrompt:  func(*state.AgentState) string { return "system prompt" },
		SleepFunc:    func(time.Duration) {},
		MkdirAll:     os.MkdirAll,
		CreateFile:   os.Create,
		Stdout:       io.Discard,
		NewBackendProcess: func(backend.SessionSpec, Observer) ProcessManager {
			return proc
		},
		NewWorkLock: func(string, string) (*WorkLock, error) {
			return &WorkLock{
				Acquire: func() error { return nil },
				Release: func() error { return nil },
			}, nil
		},
		Getpid: func() int { return 1234 },
	}

	done := make(chan struct {
		runner *Runner
		err    error
	}, 1)
	go func() {
		runner, err := StartRunner(context.Background(), deps, "finn")
		done <- struct {
			runner *Runner
			err    error
		}{runner: runner, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("StartRunner() error: %v", res.err)
		}
		if proc.sendCalls != 0 {
			t.Fatalf("StartRunner() sent %d prompt(s), want 0 before Run()", proc.sendCalls)
		}
		runCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := res.runner.Run(runCtx); err != nil {
			t.Fatalf("Run() cleanup error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("StartRunner() blocked waiting on the initial prompt")
	}
}
