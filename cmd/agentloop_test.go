package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/agentloop"
	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/protocol"
	"github.com/dmotles/dendra/internal/state"
)

// mockProcessManager implements the processManager interface for testing.
type mockProcessManager struct {
	mu          sync.Mutex
	startErr    error
	sendResults []*protocol.ResultMessage
	sendErrors  []error
	sendIndex   int
	stopErr     error
	running     bool
	startCalled bool
	stopCalled  bool
	prompts     []string
	configs     []agentloop.ProcessConfig
}

func (m *mockProcessManager) Start(ctx context.Context, initialPrompt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startCalled = true
	if m.startErr != nil {
		return m.startErr
	}
	m.running = true
	return nil
}

func (m *mockProcessManager) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt)
	idx := m.sendIndex
	m.sendIndex++
	if idx < len(m.sendErrors) && m.sendErrors[idx] != nil {
		m.running = false
		return nil, m.sendErrors[idx]
	}
	if idx < len(m.sendResults) {
		return m.sendResults[idx], nil
	}
	return &protocol.ResultMessage{Type: "result", Result: "ok"}, nil
}

func (m *mockProcessManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalled = true
	m.running = false
	return m.stopErr
}

func (m *mockProcessManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// newTestAgentLoopDeps creates a standard test deps struct with sensible defaults.
// Returns the deps, the temp dir (used as DENDRA_ROOT), and the mock process manager.
func newTestAgentLoopDeps(t *testing.T) (*agentLoopDeps, string, *mockProcessManager) {
	t.Helper()
	tmpDir := t.TempDir()

	mockProc := &mockProcessManager{
		sendResults: []*protocol.ResultMessage{
			{Type: "result", Result: "done"},
		},
	}

	agentState := &state.AgentState{
		Name:      "ash",
		Type:      "engineer",
		Family:    "engineering",
		Parent:    "root",
		Prompt:    "do stuff",
		Branch:    "dendra/ash",
		Worktree:  tmpDir,
		Status:    "active",
		SessionID: "dendra-ash",
	}
	if err := state.SaveAgent(tmpDir, agentState); err != nil {
		t.Fatalf("saving test agent state: %v", err)
	}

	var exitCode int
	var exitCalled bool
	var sleepCalls int
	var sleepMu sync.Mutex

	deps := &agentLoopDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		loadAgent: func(root, name string) (*state.AgentState, error) {
			return state.LoadAgent(root, name)
		},
		nextTask: func(root, name string) (*state.Task, error) {
			return state.NextTask(root, name)
		},
		updateTask: func(root, name string, task *state.Task) error {
			return state.UpdateTask(root, name, task)
		},
		listMessages: func(root, agent, filter string) ([]*messages.Message, error) {
			return messages.List(root, agent, filter)
		},
		sendMessage: func(root, from, to, subject, body string) error {
			return messages.Send(root, from, to, subject, body)
		},
		findClaude: func() (string, error) {
			return "/usr/bin/claude", nil
		},
		readFile: func(path string) ([]byte, error) {
			return nil, errors.New("file not found")
		},
		removeFile: func(path string) error {
			return nil
		},
		buildPrompt: func(a *state.AgentState) string {
			return "system prompt for " + a.Name
		},
		sleepFunc: func(d time.Duration) {
			sleepMu.Lock()
			sleepCalls++
			sleepMu.Unlock()
		},
		stdout: &bytes.Buffer{},
		exit: func(code int) {
			exitCode = code
			exitCalled = true
		},
		newProcess: func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
			mockProc.mu.Lock()
			mockProc.configs = append(mockProc.configs, config)
			mockProc.mu.Unlock()
			return mockProc
		},
	}

	// Suppress unused variable warnings by using them in a closure.
	_ = exitCode
	_ = exitCalled
	_ = sleepCalls

	return deps, tmpDir, mockProc
}

func TestAgentLoopCmd_Hidden(t *testing.T) {
	if !agentLoopCmd.Hidden {
		t.Error("agent-loop command should be hidden")
	}
}

func TestAgentLoopCmd_ExactArgs(t *testing.T) {
	cmd := agentLoopCmd
	// Cobra ExactArgs(1) validator should reject 0 args
	err := cmd.Args(cmd, []string{})
	if err == nil {
		t.Error("expected error when no args provided")
	}

	// Should reject 2 args
	err = cmd.Args(cmd, []string{"one", "two"})
	if err == nil {
		t.Error("expected error when 2 args provided")
	}

	// Should accept exactly 1 arg
	err = cmd.Args(cmd, []string{"ash"})
	if err != nil {
		t.Errorf("expected no error for 1 arg, got: %v", err)
	}
}

func TestRunAgentLoop_MissingDendraRoot(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)
	deps.getenv = func(key string) string { return "" }

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "ash")
	if err == nil {
		t.Fatal("expected error when DENDRA_ROOT is empty")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestRunAgentLoop_AgentNotFound(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)
	deps.loadAgent = func(root, name string) (*state.AgentState, error) {
		return nil, errors.New("agent not found")
	}

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "nonexistent")
	if err == nil {
		t.Fatal("expected error when agent not found")
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("error should mention agent, got: %v", err)
	}
}

func TestRunAgentLoop_FindClaudeFails(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)
	deps.findClaude = func() (string, error) {
		return "", errors.New("claude not found")
	}

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "ash")
	if err == nil {
		t.Fatal("expected error when findClaude fails")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should mention claude, got: %v", err)
	}
}

func TestRunAgentLoop_ProcessStartFails(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)
	mockProc.startErr = errors.New("process start failed")

	var exitCode int
	deps.exit = func(code int) { exitCode = code }

	var sentMessages []string
	deps.sendMessage = func(root, from, to, subject, body string) error {
		sentMessages = append(sentMessages, to+":"+subject)
		return nil
	}

	ctx := context.Background()
	_ = runAgentLoop(ctx, deps, "ash")
	_ = tmpDir

	// Should have reported failure to parent "root"
	parentNotified := false
	for _, msg := range sentMessages {
		if strings.HasPrefix(msg, "root:") {
			parentNotified = true
			break
		}
	}
	if !parentNotified {
		t.Error("expected failure message sent to parent agent 'root'")
	}

	if exitCode != 1 {
		t.Errorf("expected exit(1) on start failure, got exit(%d)", exitCode)
	}
}

func TestRunAgentLoop_ProcessesTask(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task
	if _, err := state.EnqueueTask(tmpDir, "ash", "implement feature X"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "completed feature X"},
	}

	// Cancel context after first SendPrompt call
	ctx, cancel := context.WithCancel(context.Background())
	origNewProcess := deps.newProcess
	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		pm := origNewProcess(config, observer)
		return pm
	}
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")

	// Verify the task prompt was sent to Claude
	if len(mockProc.prompts) < 1 {
		t.Fatal("expected at least one prompt sent to process")
	}
	if !strings.Contains(mockProc.prompts[0], "implement feature X") {
		t.Errorf("first prompt should contain task prompt, got: %q", mockProc.prompts[0])
	}
}

func TestRunAgentLoop_TaskFIFO(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue two tasks
	if _, err := state.EnqueueTask(tmpDir, "ash", "first task"); err != nil {
		t.Fatalf("creating task1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := state.EnqueueTask(tmpDir, "ash", "second task"); err != nil {
		t.Fatalf("creating task2: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done 1"},
		{Type: "result", Result: "done 2"},
	}

	// Cancel after both tasks processed
	ctx, cancel := context.WithCancel(context.Background())
	promptCount := 0
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}
	origNewProcess := deps.newProcess
	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		pm := origNewProcess(config, observer)
		return &promptCountingManager{
			processManager: pm,
			onPrompt: func() {
				promptCount++
				if promptCount >= 2 {
					cancel()
				}
			},
		}
	}

	_ = runAgentLoop(ctx, deps, "ash")

	if len(mockProc.prompts) < 2 {
		t.Fatalf("expected at least 2 prompts, got %d", len(mockProc.prompts))
	}
	if !strings.Contains(mockProc.prompts[0], "first task") {
		t.Errorf("first prompt should be 'first task', got: %q", mockProc.prompts[0])
	}
	if !strings.Contains(mockProc.prompts[1], "second task") {
		t.Errorf("second prompt should be 'second task', got: %q", mockProc.prompts[1])
	}
}

// promptCountingManager wraps processManager to count SendPrompt calls.
type promptCountingManager struct {
	processManager
	onPrompt func()
}

func (p *promptCountingManager) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	result, err := p.processManager.SendPrompt(ctx, prompt)
	if p.onPrompt != nil {
		p.onPrompt()
	}
	return result, err
}

func TestRunAgentLoop_InboxTriggers(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// No tasks queued, but send a message to the agent's inbox
	if err := messages.Send(tmpDir, "root", "ash", "hey", "check this out"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "checked inbox"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")

	if len(mockProc.prompts) < 1 {
		t.Fatal("expected at least one prompt when inbox has messages")
	}
	// The prompt should instruct the agent to check its inbox
	prompt := mockProc.prompts[0]
	if !strings.Contains(strings.ToLower(prompt), "inbox") {
		t.Errorf("prompt should mention inbox, got: %q", prompt)
	}
}

func TestRunAgentLoop_WakeFile(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)
	_ = tmpDir

	wakeContent := "wake up and do something"
	var removedFiles []string

	deps.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ash.wake") {
			return []byte(wakeContent), nil
		}
		return nil, errors.New("file not found")
	}
	deps.removeFile = func(path string) error {
		removedFiles = append(removedFiles, path)
		return nil
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "woke up"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")

	if len(mockProc.prompts) < 1 {
		t.Fatal("expected prompt from wake file")
	}
	if !strings.Contains(mockProc.prompts[0], wakeContent) {
		t.Errorf("prompt should contain wake file contents, got: %q", mockProc.prompts[0])
	}

	// Wake file should be removed after reading
	foundRemove := false
	for _, f := range removedFiles {
		if strings.Contains(f, "ash.wake") {
			foundRemove = true
			break
		}
	}
	if !foundRemove {
		t.Error("expected wake file to be removed after reading")
	}
}

func TestRunAgentLoop_IdleSleep(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)

	// No tasks, no messages, no wake file
	deps.readFile = func(path string) ([]byte, error) {
		return nil, errors.New("file not found")
	}
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) {
		return nil, nil
	}
	deps.nextTask = func(root, name string) (*state.Task, error) {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	sleepCalled := false
	deps.sleepFunc = func(d time.Duration) {
		sleepCalled = true
		cancel() // Exit the loop after sleep
	}

	_ = runAgentLoop(ctx, deps, "ash")

	if !sleepCalled {
		t.Error("expected sleepFunc to be called when idle")
	}
}

func TestRunAgentLoop_ProcessCrash_Restart(t *testing.T) {
	deps, tmpDir, _ := newTestAgentLoopDeps(t)

	// Queue a task so the loop has work to do
	if _, err := state.EnqueueTask(tmpDir, "ash", "do work"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	callCount := 0
	var createdConfigs []agentloop.ProcessConfig
	ctx, cancel := context.WithCancel(context.Background())

	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		createdConfigs = append(createdConfigs, config)
		callCount++
		if callCount == 1 {
			// First process: starts fine, but SendPrompt crashes
			return &mockProcessManager{
				sendErrors: []error{errors.New("process crashed")},
			}
		}
		// Second process: should have Resume=true, succeeds
		pm := &mockProcessManager{
			sendResults: []*protocol.ResultMessage{
				{Type: "result", Result: "recovered"},
			},
		}
		// Cancel after the second process completes
		deps.sleepFunc = func(d time.Duration) { cancel() }
		return pm
	}

	_ = runAgentLoop(ctx, deps, "ash")

	if len(createdConfigs) < 2 {
		t.Fatalf("expected at least 2 process creations (original + restart), got %d", len(createdConfigs))
	}
	if createdConfigs[1].Resume != true {
		t.Error("restarted process should have Resume=true")
	}
}

func TestRunAgentLoop_RestartFailure_ReportsParent(t *testing.T) {
	deps, tmpDir, _ := newTestAgentLoopDeps(t)

	// Queue a task
	if _, err := state.EnqueueTask(tmpDir, "ash", "do work"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	callCount := 0
	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		callCount++
		if callCount == 1 {
			// First process: starts fine, but SendPrompt crashes
			return &mockProcessManager{
				sendErrors: []error{errors.New("process crashed")},
			}
		}
		// Second process (restart): fails to start
		return &mockProcessManager{
			startErr: errors.New("cannot start"),
		}
	}

	var sentTo []string
	deps.sendMessage = func(root, from, to, subject, body string) error {
		sentTo = append(sentTo, to)
		return nil
	}

	var exitCode int
	deps.exit = func(code int) { exitCode = code }

	ctx := context.Background()
	_ = runAgentLoop(ctx, deps, "ash")

	// Should have sent a message to parent ("root") about the failure
	parentNotified := false
	for _, to := range sentTo {
		if to == "root" {
			parentNotified = true
			break
		}
	}
	if !parentNotified {
		t.Error("expected failure message sent to parent agent 'root'")
	}

	if exitCode != 1 {
		t.Errorf("expected exit(1) on restart failure, got exit(%d)", exitCode)
	}
}

func TestRunAgentLoop_GracefulShutdown(t *testing.T) {
	deps, _, mockProc := newTestAgentLoopDeps(t)

	// No tasks, cancel context immediately
	deps.nextTask = func(root, name string) (*state.Task, error) {
		return nil, nil
	}
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) {
		return nil, nil
	}
	deps.readFile = func(path string) ([]byte, error) {
		return nil, errors.New("file not found")
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")

	if !mockProc.stopCalled {
		t.Error("expected proc.Stop to be called on graceful shutdown")
	}
}

func TestTmuxObserver_AssistantMessage(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	// Build an assistant message with text content
	contentBlock := []map[string]interface{}{
		{"type": "text", "text": "Hello from Claude"},
	}
	msgContent := map[string]interface{}{
		"role":    "assistant",
		"content": contentBlock,
	}
	raw, _ := json.Marshal(msgContent)

	assistantMsg := &protocol.Message{
		Type: "assistant",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":    "assistant",
			"message": json.RawMessage(raw),
		}),
	}

	obs.OnMessage(assistantMsg)

	output := buf.String()
	if !strings.Contains(output, "[claude]") {
		t.Errorf("expected output to contain [claude], got: %q", output)
	}
}

func TestTmuxObserver_RateLimitBlocked(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	rateMsg := &protocol.Message{
		Type: "rate_limit_event",
		Raw: mustMarshal(t, map[string]interface{}{
			"type": "rate_limit_event",
			"rate_limit_info": map[string]interface{}{
				"status":        "blocked",
				"resetsAt":      time.Now().Add(60 * time.Second).Unix(),
				"rateLimitType": "token",
			},
		}),
	}

	obs.OnMessage(rateMsg)

	output := buf.String()
	if output == "" {
		t.Error("expected output for blocked rate limit event")
	}
	if !strings.Contains(strings.ToLower(output), "rate") {
		t.Errorf("expected output to mention rate limit, got: %q", output)
	}
}

func TestTmuxObserver_IgnoresNonBlocked(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	rateMsg := &protocol.Message{
		Type: "rate_limit_event",
		Raw: mustMarshal(t, map[string]interface{}{
			"type": "rate_limit_event",
			"rate_limit_info": map[string]interface{}{
				"status":        "ok",
				"resetsAt":      0,
				"rateLimitType": "token",
			},
		}),
	}

	obs.OnMessage(rateMsg)

	output := buf.String()
	if output != "" {
		t.Errorf("expected no output for non-blocked rate limit event, got: %q", output)
	}
}

func TestRunAgentLoop_LogPrefix(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)

	var out bytes.Buffer
	deps.stdout = &out

	// No tasks, cancel after first sleep
	deps.nextTask = func(root, name string) (*state.Task, error) { return nil, nil }
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) { return nil, nil }
	deps.readFile = func(path string) ([]byte, error) { return nil, errors.New("not found") }

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	output := out.String()
	if !strings.Contains(output, "[agent-loop]") {
		t.Errorf("expected [agent-loop] prefix in stdout, got: %q", output)
	}
}

func TestRunAgentLoop_ProcessConfigFromAgentState(t *testing.T) {
	deps, tmpDir, _ := newTestAgentLoopDeps(t)

	var capturedConfig agentloop.ProcessConfig
	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		capturedConfig = config
		return &mockProcessManager{}
	}

	// Cancel immediately after process start
	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }
	deps.nextTask = func(root, name string) (*state.Task, error) { return nil, nil }
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) { return nil, nil }
	deps.readFile = func(path string) ([]byte, error) { return nil, errors.New("not found") }

	_ = runAgentLoop(ctx, deps, "ash")
	_ = tmpDir

	// Verify ProcessConfig was populated from agent state
	if capturedConfig.AgentName != "ash" {
		t.Errorf("AgentName = %q, want %q", capturedConfig.AgentName, "ash")
	}
	if capturedConfig.WorkDir == "" {
		t.Error("WorkDir should be set from agent state Worktree")
	}
	if capturedConfig.SessionID == "" {
		t.Error("SessionID should be set from agent state")
	}
	if capturedConfig.ClaudePath == "" {
		t.Error("ClaudePath should be set from findClaude")
	}
	if capturedConfig.SystemPrompt == "" {
		t.Error("SystemPrompt should be set from buildPrompt")
	}
	if capturedConfig.DendraRoot == "" {
		t.Error("DendraRoot should be set from DENDRA_ROOT env var")
	}
}

func TestRunAgentLoop_KillSentinel(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// No tasks, no inbox messages.
	deps.nextTask = func(root, name string) (*state.Task, error) { return nil, nil }
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) { return nil, nil }

	// Kill sentinel file exists when readFile is called for it.
	expectedKillPath := filepath.Join(tmpDir, ".dendra", "agents", "ash.kill")
	var removedFiles []string
	deps.readFile = func(path string) ([]byte, error) {
		if path == expectedKillPath {
			return []byte(""), nil // sentinel file exists
		}
		return nil, errors.New("file not found")
	}
	deps.removeFile = func(path string) error {
		removedFiles = append(removedFiles, path)
		return nil
	}

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "ash")
	if err != nil {
		t.Fatalf("expected clean exit on kill sentinel, got error: %v", err)
	}

	// Verify the kill sentinel file was removed.
	killSentinelRemoved := false
	for _, f := range removedFiles {
		if f == expectedKillPath {
			killSentinelRemoved = true
			break
		}
	}
	if !killSentinelRemoved {
		t.Errorf("expected kill sentinel to be removed at %s, removed files: %v", expectedKillPath, removedFiles)
	}

	// Verify proc.Stop was called (via deferred cleanup).
	if !mockProc.stopCalled {
		t.Error("expected proc.Stop to be called when exiting via kill sentinel")
	}
}

func TestRunAgentLoop_KillSentinel_PriorityOverTasks(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task so there is work available.
	if _, err := state.EnqueueTask(tmpDir, "ash", "should not be processed"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Also have inbox messages available.
	if err := messages.Send(tmpDir, "root", "ash", "hey", "check this out"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	// Kill sentinel file exists.
	expectedKillPath := filepath.Join(tmpDir, ".dendra", "agents", "ash.kill")
	deps.readFile = func(path string) ([]byte, error) {
		if path == expectedKillPath {
			return []byte(""), nil // sentinel file exists
		}
		return nil, errors.New("file not found")
	}

	var removedFiles []string
	deps.removeFile = func(path string) error {
		removedFiles = append(removedFiles, path)
		return nil
	}

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "ash")
	if err != nil {
		t.Fatalf("expected clean exit on kill sentinel, got error: %v", err)
	}

	// No prompts should have been sent -- the kill sentinel takes priority.
	if len(mockProc.prompts) > 0 {
		t.Errorf("expected no prompts sent when kill sentinel is present, got %d: %v", len(mockProc.prompts), mockProc.prompts)
	}

	// Verify the loop exited without processing the task.
	if mockProc.stopCalled != true {
		t.Error("expected proc.Stop to be called on kill sentinel exit")
	}
}

// mustMarshal marshals v to JSON or fails the test.
func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	return json.RawMessage(data)
}

// Ensure the interfaces are used so the imports compile even without the implementation.
var _ io.Writer = (*bytes.Buffer)(nil)

// TestDefaultBuildPrompt_ResearcherType verifies that defaultAgentLoopDeps().buildPrompt
// dispatches to the researcher prompt when the agent type is "researcher".
func TestDefaultBuildPrompt_ResearcherType(t *testing.T) {
	deps := defaultAgentLoopDeps()

	agentState := &state.AgentState{
		Name:   "ash",
		Type:   "researcher",
		Parent: "root",
		Branch: "dendra/ash",
		Prompt: "investigate auth libraries",
	}

	prompt := deps.buildPrompt(agentState)

	if !strings.Contains(prompt, "Researcher agent") {
		t.Error("buildPrompt for researcher type should contain 'Researcher agent'")
	}
	if strings.Contains(prompt, "hands-on builder") {
		t.Error("buildPrompt for researcher type should NOT contain 'hands-on builder' (that is engineer prompt text)")
	}
	if !strings.Contains(prompt, "deep investigator") {
		t.Error("buildPrompt for researcher type should contain 'deep investigator'")
	}
	if !strings.Contains(prompt, "investigate auth libraries") {
		t.Error("buildPrompt for researcher type should contain the task prompt")
	}
}

// TestDefaultBuildPrompt_EngineerType verifies that defaultAgentLoopDeps().buildPrompt
// returns the engineer prompt content when agent type is "engineer".
// NOTE: This test passes against the current code (already green) — it serves as
// a regression test for the new dispatch logic.
func TestDefaultBuildPrompt_EngineerType(t *testing.T) {
	deps := defaultAgentLoopDeps()

	agentState := &state.AgentState{
		Name:   "ash",
		Type:   "engineer",
		Parent: "root",
		Branch: "dendra/ash",
		Prompt: "build login page",
	}

	prompt := deps.buildPrompt(agentState)

	if !strings.Contains(prompt, "Engineer agent") {
		t.Error("buildPrompt for engineer type should contain 'Engineer agent'")
	}
	if !strings.Contains(prompt, "hands-on builder") {
		t.Error("buildPrompt for engineer type should contain 'hands-on builder'")
	}
	if !strings.Contains(prompt, "build login page") {
		t.Error("buildPrompt for engineer type should contain the task prompt")
	}
}

// TestDefaultBuildPrompt_UnknownType verifies that an unknown agent type
// defaults to the engineer prompt (safe fallback).
func TestDefaultBuildPrompt_UnknownType(t *testing.T) {
	deps := defaultAgentLoopDeps()

	agentState := &state.AgentState{
		Name:   "ash",
		Type:   "tester",
		Parent: "root",
		Branch: "dendra/ash",
		Prompt: "test something",
	}

	prompt := deps.buildPrompt(agentState)

	// Unknown types should default to engineer prompt
	if !strings.Contains(prompt, "Engineer agent") {
		t.Error("buildPrompt for unknown type should default to engineer prompt containing 'Engineer agent'")
	}
	if !strings.Contains(prompt, "test something") {
		t.Error("buildPrompt for unknown type should contain the task prompt")
	}
}
