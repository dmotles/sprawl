package agentloop

import (
	"context"
	"io"
	"os"
	"sync/atomic"
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

type blockingRunnerProcess struct {
	sendStarted chan struct{}
	sendRelease chan struct{}
	promptCh    chan string
	interrupts  atomic.Int32
	sendCalls   atomic.Int32
}

func (p *blockingRunnerProcess) Launch(context.Context) error { return nil }

func (p *blockingRunnerProcess) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	p.sendCalls.Add(1)
	if p.promptCh != nil {
		select {
		case p.promptCh <- prompt:
		default:
		}
	}
	if p.sendStarted != nil {
		select {
		case p.sendStarted <- struct{}{}:
		default:
		}
	}
	if p.sendRelease != nil {
		select {
		case <-p.sendRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &protocol.ResultMessage{Type: "result", Result: "ok"}, nil
}

func (p *blockingRunnerProcess) InterruptTurn(context.Context) error {
	p.interrupts.Add(1)
	return nil
}

func (p *blockingRunnerProcess) Stop(context.Context) error { return nil }
func (p *blockingRunnerProcess) IsRunning() bool            { return true }

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
		NewBackendProcess: func(backend.SessionSpec, backend.InitSpec, Observer) ProcessManager {
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

func TestSendPromptWithInterrupt_ControlInterruptSignalsBusyTurn(t *testing.T) {
	controlCh := make(chan ControlSignal, 1)
	proc := &blockingRunnerProcess{
		sendStarted: make(chan struct{}, 1),
		sendRelease: make(chan struct{}),
	}
	deps := &RunnerDeps{
		ReadFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		RemoveFile: func(string) error { return nil },
		ListMessages: func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		},
		Stdout:    io.Discard,
		ControlCh: controlCh,
	}

	done := make(chan struct {
		result *protocol.ResultMessage
		err    error
	}, 1)
	go func() {
		result, _, err := SendPromptWithInterrupt(context.Background(), proc, deps, "/tmp/finn.poke", "keep working", 10*time.Millisecond, "/tmp/root", "finn")
		done <- struct {
			result *protocol.ResultMessage
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-proc.sendStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SendPrompt did not start")
	}

	controlCh <- ControlSignalInterrupt

	deadline := time.After(500 * time.Millisecond)
	for proc.interrupts.Load() == 0 {
		select {
		case <-deadline:
			close(proc.sendRelease)
			<-done
			t.Fatal("control interrupt did not interrupt the busy turn")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(proc.sendRelease)
	res := <-done
	if res.err != nil {
		t.Fatalf("SendPromptWithInterrupt() error: %v", res.err)
	}
}

func TestSendPromptWithInterrupt_ControlWakeDoesNotInterruptBusyTurn(t *testing.T) {
	controlCh := make(chan ControlSignal, 1)
	proc := &blockingRunnerProcess{
		sendStarted: make(chan struct{}, 1),
		sendRelease: make(chan struct{}),
	}
	deps := &RunnerDeps{
		ReadFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		RemoveFile: func(string) error { return nil },
		ListMessages: func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		},
		Stdout:    io.Discard,
		ControlCh: controlCh,
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := SendPromptWithInterrupt(context.Background(), proc, deps, "/tmp/finn.poke", "keep working", 10*time.Millisecond, "/tmp/root", "finn")
		done <- err
	}()

	select {
	case <-proc.sendStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SendPrompt did not start")
	}

	controlCh <- ControlSignalWake

	select {
	case err := <-done:
		t.Fatalf("SendPromptWithInterrupt returned early after wake-only control: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if got := proc.interrupts.Load(); got != 0 {
		close(proc.sendRelease)
		<-done
		t.Fatalf("wake-only control interrupted busy turn %d time(s), want 0", got)
	}

	close(proc.sendRelease)
	if err := <-done; err != nil {
		t.Fatalf("SendPromptWithInterrupt() error: %v", err)
	}
}

func TestRunnerRun_WakeSignalPreemptsIdleSleepAndDeliversTask(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentState := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "weave",
		Prompt:    "",
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

	controlCh := make(chan ControlSignal, 1)
	sleepEntered := make(chan struct{}, 1)
	sleepRelease := make(chan struct{})
	proc := &blockingRunnerProcess{
		promptCh: make(chan string, 1),
	}
	var taskReady atomic.Bool
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
		LoadAgent: state.LoadAgent,
		NextTask: func(string, string) (*state.Task, error) {
			if !taskReady.Load() {
				return nil, nil
			}
			taskReady.Store(false)
			return &state.Task{
				ID:        "task-1",
				Prompt:    "deliver queued work",
				Status:    "queued",
				CreatedAt: "2026-04-28T01:00:00Z",
			}, nil
		},
		UpdateTask: func(string, string, *state.Task) error { return nil },
		ListMessages: func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		},
		SendMessage: func(string, string, string, string, string) error { return nil },
		ReadFile:    os.ReadFile,
		RemoveFile:  os.Remove,
		BuildPrompt: func(*state.AgentState) string { return "system prompt" },
		SleepFunc: func(time.Duration) {
			select {
			case sleepEntered <- struct{}{}:
			default:
			}
			<-sleepRelease
		},
		MkdirAll:   os.MkdirAll,
		CreateFile: os.Create,
		Stdout:     io.Discard,
		ControlCh:  controlCh,
		NewBackendProcess: func(backend.SessionSpec, backend.InitSpec, Observer) ProcessManager {
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

	runner, err := StartRunner(context.Background(), deps, "finn")
	if err != nil {
		t.Fatalf("StartRunner() error: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(runCtx)
	}()

	select {
	case <-sleepEntered:
	case <-time.After(500 * time.Millisecond):
		cancel()
		close(sleepRelease)
		<-done
		t.Fatal("runner never reached idle sleep")
	}

	taskReady.Store(true)
	controlCh <- ControlSignalWake

	select {
	case prompt := <-proc.promptCh:
		if prompt != "deliver queued work" {
			cancel()
			close(sleepRelease)
			<-done
			t.Fatalf("runner prompt = %q, want queued task prompt", prompt)
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		close(sleepRelease)
		<-done
		t.Fatal("wake signal did not preempt idle sleep and deliver queued work")
	}

	cancel()
	close(sleepRelease)
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestRunnerCapabilities_SupportsToolBridgeWhenInitSpecSet(t *testing.T) {
	// After implementation, Runner.Capabilities() should report
	// SupportsToolBridge: true when the runner's InitSpec has a ToolBridge.
	// Currently it hardcodes false — this test will fail until the
	// implementation wires InitSpec through RunnerDeps into the Runner.
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
		NewBackendProcess: func(backend.SessionSpec, backend.InitSpec, Observer) ProcessManager {
			return proc
		},
		NewWorkLock: func(string, string) (*WorkLock, error) {
			return &WorkLock{
				Acquire: func() error { return nil },
				Release: func() error { return nil },
			}, nil
		},
		Getpid:   func() int { return 1234 },
		InitSpec: backend.InitSpec{MCPServerNames: []string{"sprawl-ops"}},
	}

	runner, err := StartRunner(context.Background(), deps, "finn")
	if err != nil {
		t.Fatalf("StartRunner() error: %v", err)
	}

	caps := runner.Capabilities()
	if !caps.SupportsToolBridge {
		t.Fatal("Capabilities().SupportsToolBridge = false, want true when InitSpec has MCP servers")
	}

	// Cleanup
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = runner.Run(runCtx)
}

func TestRunnerRun_InterruptSignalAlsoPreemptsIdleSleepAndDeliversTask(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentState := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "weave",
		Prompt:    "",
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

	controlCh := make(chan ControlSignal, 1)
	sleepEntered := make(chan struct{}, 1)
	sleepRelease := make(chan struct{})
	proc := &blockingRunnerProcess{
		promptCh: make(chan string, 1),
	}
	var taskReady atomic.Bool
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
		LoadAgent: state.LoadAgent,
		NextTask: func(string, string) (*state.Task, error) {
			if !taskReady.Load() {
				return nil, nil
			}
			taskReady.Store(false)
			return &state.Task{
				ID:        "task-1",
				Prompt:    "deliver queued work",
				Status:    "queued",
				CreatedAt: "2026-04-28T01:00:00Z",
			}, nil
		},
		UpdateTask: func(string, string, *state.Task) error { return nil },
		ListMessages: func(string, string, string) ([]*messages.Message, error) {
			return nil, nil
		},
		SendMessage: func(string, string, string, string, string) error { return nil },
		ReadFile:    os.ReadFile,
		RemoveFile:  os.Remove,
		BuildPrompt: func(*state.AgentState) string { return "system prompt" },
		SleepFunc: func(time.Duration) {
			select {
			case sleepEntered <- struct{}{}:
			default:
			}
			<-sleepRelease
		},
		MkdirAll:   os.MkdirAll,
		CreateFile: os.Create,
		Stdout:     io.Discard,
		ControlCh:  controlCh,
		NewBackendProcess: func(backend.SessionSpec, backend.InitSpec, Observer) ProcessManager {
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

	runner, err := StartRunner(context.Background(), deps, "finn")
	if err != nil {
		t.Fatalf("StartRunner() error: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(runCtx)
	}()

	select {
	case <-sleepEntered:
	case <-time.After(500 * time.Millisecond):
		cancel()
		close(sleepRelease)
		<-done
		t.Fatal("runner never reached idle sleep")
	}

	taskReady.Store(true)
	controlCh <- ControlSignalInterrupt

	select {
	case prompt := <-proc.promptCh:
		if prompt != "deliver queued work" {
			cancel()
			close(sleepRelease)
			<-done
			t.Fatalf("runner prompt = %q, want queued task prompt", prompt)
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		close(sleepRelease)
		<-done
		t.Fatal("interrupt-capable signal did not wake idle runner and deliver queued work")
	}

	cancel()
	close(sleepRelease)
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}
