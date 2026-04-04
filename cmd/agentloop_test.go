package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
	mu              sync.Mutex
	startErr        error
	sendResults     []*protocol.ResultMessage
	sendErrors      []error
	sendIndex       int
	stopErr         error
	running         bool
	startCalled     bool
	stopCalled      bool
	interruptCalled bool
	interruptErr    error
	prompts         []string
	configs         []agentloop.ProcessConfig
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

func (m *mockProcessManager) InterruptTurn(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interruptCalled = true
	return m.interruptErr
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
		loadAgent:    state.LoadAgent,
		nextTask:     state.NextTask,
		updateTask:   state.UpdateTask,
		listMessages: messages.List,
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
		mkdirAll:   os.MkdirAll,
		createFile: os.Create,
		stdout:     &bytes.Buffer{},
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
		newWorkLock: func(lockDir, agentName string) (*workLock, error) {
			return &workLock{
				Acquire: func() error { return nil },
				Release: func() error { return nil },
			}, nil
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

	// Queue a task (EnqueueTask now writes a prompt file)
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

	// Verify the task prompt was sent to Claude via @file reference
	if len(mockProc.prompts) < 1 {
		t.Fatal("expected at least one prompt sent to process")
	}
	// Should contain @/ file reference (since EnqueueTask sets PromptFile)
	if !strings.Contains(mockProc.prompts[0], "@/") {
		t.Errorf("first prompt should contain @/path reference, got: %q", mockProc.prompts[0])
	}
	if !strings.Contains(mockProc.prompts[0], ".md") {
		t.Errorf("first prompt should reference a .md file, got: %q", mockProc.prompts[0])
	}
}

func TestRunAgentLoop_TaskDelivery_UsesPromptFileRef(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Create a task with an explicit PromptFile set (simulating new-style task)
	promptContent := "do important work"
	promptFilePath := filepath.Join(tmpDir, ".dendra", "agents", "ash", "prompts", "explicit-task.md")
	os.MkdirAll(filepath.Dir(promptFilePath), 0o755)
	os.WriteFile(promptFilePath, []byte(promptContent), 0o644)

	task := &state.Task{
		ID:         "explicit-task-id",
		Prompt:     promptContent,
		PromptFile: promptFilePath,
		Status:     "queued",
		CreatedAt:  "2026-03-31T12:00:00Z",
	}
	tasksDir := state.TasksDir(tmpDir, "ash")
	os.MkdirAll(tasksDir, 0o755)
	taskData, _ := json.Marshal(task)
	os.WriteFile(filepath.Join(tasksDir, "20260331T120000.000000000Z-explicit-task-id.json"), taskData, 0o644)

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	if len(mockProc.prompts) < 1 {
		t.Fatal("expected at least one prompt sent to process")
	}
	// Should deliver @file reference, not raw prompt text
	if !strings.Contains(mockProc.prompts[0], "@"+promptFilePath) {
		t.Errorf("expected prompt to contain @%s, got: %q", promptFilePath, mockProc.prompts[0])
	}
}

func TestRunAgentLoop_TaskDelivery_FallbackRawPrompt(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Create a task WITHOUT a PromptFile (simulating legacy/backward compat)
	task := &state.Task{
		ID:        "legacy-task-id",
		Prompt:    "do legacy work",
		Status:    "queued",
		CreatedAt: "2026-03-31T12:00:00Z",
	}
	// Write task JSON directly (bypassing EnqueueTask which now sets PromptFile)
	tasksDir := state.TasksDir(tmpDir, "ash")
	os.MkdirAll(tasksDir, 0o755)
	taskData, _ := json.Marshal(task)
	os.WriteFile(filepath.Join(tasksDir, "20260331T120000.000000000Z-legacy-task-id.json"), taskData, 0o644)

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	if len(mockProc.prompts) < 1 {
		t.Fatal("expected at least one prompt sent to process")
	}
	// Should fall back to raw prompt text since PromptFile is empty
	if mockProc.prompts[0] != "do legacy work" {
		t.Errorf("expected raw prompt fallback, got: %q", mockProc.prompts[0])
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
	// With prompt files, tasks are delivered via @/path references to .md files
	for i, p := range mockProc.prompts[:2] {
		if !strings.Contains(p, "@/") || !strings.Contains(p, ".md") {
			t.Errorf("prompt[%d] should contain @/path reference to .md file, got: %q", i, p)
		}
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
	// The prompt should tell the agent to run dendra messages read, not contain inline content.
	prompt := mockProc.prompts[0]
	if strings.Contains(prompt, "check this out") {
		t.Errorf("prompt should NOT contain message body inline, got: %q", prompt)
	}
	if !strings.Contains(prompt, "dendra messages read") {
		t.Errorf("prompt should contain 'dendra messages read' command, got: %q", prompt)
	}
	if !strings.Contains(prompt, `subject: "hey"`) {
		t.Errorf("prompt should contain message subject, got: %q", prompt)
	}
	if !strings.Contains(prompt, "root") {
		t.Errorf("prompt should contain sender name, got: %q", prompt)
	}
	if !strings.Contains(prompt, "Read them with the commands below") {
		t.Errorf("prompt should contain directive text, got: %q", prompt)
	}

	// After delivery, messages should remain UNREAD (agent reads them itself).
	unread, _ := messages.List(tmpDir, "ash", "unread")
	if len(unread) != 1 {
		t.Errorf("expected 1 unread message after delivery (agent reads it), got %d", len(unread))
	}
}

func TestRunAgentLoop_InboxRedeliveryUntilRead(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Send a message.
	if err := messages.Send(tmpDir, "root", "ash", "hey", "check this"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
		{Type: "result", Result: "done"},
	}

	// Run 2 loop iterations: both should deliver because message stays unread.
	ctx, cancel := context.WithCancel(context.Background())
	iterCount := 0
	deps.sleepFunc = func(d time.Duration) {
		iterCount++
		if iterCount >= 2 {
			cancel()
		}
	}

	_ = runAgentLoop(ctx, deps, "ash")

	// Both iterations should prompt the agent since the message remains unread.
	if len(mockProc.prompts) != 2 {
		t.Errorf("expected 2 prompts (redelivery until agent reads), got %d: %v", len(mockProc.prompts), mockProc.prompts)
	}
	// Both should contain the read command format.
	for i, p := range mockProc.prompts {
		if !strings.Contains(p, "dendra messages read") {
			t.Errorf("prompt %d should contain 'dendra messages read', got: %q", i, p)
		}
	}
}

func TestRunAgentLoop_WakeFile(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)
	_ = tmpDir

	wakeContent := "wake up and do something"
	var removedFiles []string

	wakeReadCount := 0
	deps.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ash.wake") {
			wakeReadCount++
			if wakeReadCount == 1 {
				return []byte(wakeContent), nil
			}
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
	if createdConfigs[1].Args.Resume != true {
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

func TestTmuxObserver_ToolUse(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	contentBlock := []map[string]interface{}{
		{
			"type":  "tool_use",
			"name":  "Bash",
			"input": map[string]interface{}{"command": "go test ./..."},
		},
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
	if !strings.Contains(output, "[tool] Bash:") {
		t.Errorf("expected output to contain [tool] Bash:, got: %q", output)
	}
	if !strings.Contains(output, "go test") {
		t.Errorf("expected output to contain tool input, got: %q", output)
	}
}

func TestTmuxObserver_ToolUseTruncation(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	// Create a very long input string (>200 chars)
	longCommand := strings.Repeat("x", 300)
	contentBlock := []map[string]interface{}{
		{
			"type":  "tool_use",
			"name":  "Bash",
			"input": map[string]interface{}{"command": longCommand},
		},
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
	if !strings.Contains(output, "...") {
		t.Errorf("expected truncated output with ..., got: %q", output)
	}
	// The output line should be reasonably short (prefix + 200 chars + ...)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "[tool]") && len(line) > 250 {
			t.Errorf("expected truncated line under 250 chars, got %d chars", len(line))
		}
	}
}

func TestTmuxObserver_MixedContent(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	contentBlock := []map[string]interface{}{
		{"type": "text", "text": "Let me run the tests"},
		{
			"type":  "tool_use",
			"name":  "Bash",
			"input": map[string]interface{}{"command": "go test ./..."},
		},
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
	if !strings.Contains(output, "[tool] Bash:") {
		t.Errorf("expected output to contain [tool] Bash:, got: %q", output)
	}
}

func TestTmuxObserver_SystemMessage(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	sysMsg := &protocol.Message{
		Type:    "system",
		Subtype: "session_state_changed",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":    "system",
			"subtype": "session_state_changed",
			"state":   "idle",
		}),
	}

	obs.OnMessage(sysMsg)

	output := buf.String()
	if !strings.Contains(output, "[system]") {
		t.Errorf("expected output to contain [system], got: %q", output)
	}
	if !strings.Contains(output, "session_state_changed") {
		t.Errorf("expected output to contain subtype, got: %q", output)
	}
}

func TestTmuxObserver_SystemInit(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	sysMsg := &protocol.Message{
		Type:    "system",
		Subtype: "init",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":    "system",
			"subtype": "init",
		}),
	}

	obs.OnMessage(sysMsg)

	output := buf.String()
	if !strings.Contains(output, "[system] init") {
		t.Errorf("expected output to contain [system] init, got: %q", output)
	}
}

func TestTmuxObserver_ResultMessage(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	resultMsg := &protocol.Message{
		Type: "result",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":        "result",
			"is_error":    false,
			"stop_reason": "end_turn",
			"num_turns":   3,
		}),
	}

	obs.OnMessage(resultMsg)

	output := buf.String()
	if !strings.Contains(output, "[result]") {
		t.Errorf("expected output to contain [result], got: %q", output)
	}
	if !strings.Contains(output, "success") {
		t.Errorf("expected output to contain success, got: %q", output)
	}
}

func TestTmuxObserver_ResultError(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	resultMsg := &protocol.Message{
		Type: "result",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":        "result",
			"is_error":    true,
			"stop_reason": "error",
			"num_turns":   1,
		}),
	}

	obs.OnMessage(resultMsg)

	output := buf.String()
	if !strings.Contains(output, "[result]") {
		t.Errorf("expected output to contain [result], got: %q", output)
	}
	if !strings.Contains(output, "error") {
		t.Errorf("expected output to contain error, got: %q", output)
	}
}

func TestTmuxObserver_UserMessageSuppressed(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	userMsg := &protocol.Message{
		Type: "user",
		Raw: mustMarshal(t, map[string]interface{}{
			"type": "user",
		}),
	}

	obs.OnMessage(userMsg)

	output := buf.String()
	if output != "" {
		t.Errorf("expected no output for type=user message, got: %q", output)
	}
}

func TestTmuxObserver_UserMessageWithSubtypeSuppressed(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	userMsg := &protocol.Message{
		Type:    "user",
		Subtype: "some_subtype",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":    "user",
			"subtype": "some_subtype",
		}),
	}

	obs.OnMessage(userMsg)

	output := buf.String()
	if output != "" {
		t.Errorf("expected no output for type=user message with subtype, got: %q", output)
	}
}

func TestTmuxObserver_UnknownType(t *testing.T) {
	var buf bytes.Buffer
	obs := &tmuxObserver{w: &buf}

	unknownMsg := &protocol.Message{
		Type:    "unknown_thing",
		Subtype: "foo",
		Raw: mustMarshal(t, map[string]interface{}{
			"type":    "unknown_thing",
			"subtype": "foo",
		}),
	}

	obs.OnMessage(unknownMsg)

	output := buf.String()
	if !strings.Contains(output, "[agent-loop] message:") {
		t.Errorf("expected output to contain [agent-loop] message:, got: %q", output)
	}
	if !strings.Contains(output, "type=unknown_thing") {
		t.Errorf("expected output to contain type=unknown_thing, got: %q", output)
	}
	if !strings.Contains(output, "subtype=foo") {
		t.Errorf("expected output to contain subtype=foo, got: %q", output)
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
	if capturedConfig.Args.SessionID == "" {
		t.Error("SessionID should be set from agent state")
	}
	if capturedConfig.ClaudePath == "" {
		t.Error("ClaudePath should be set from findClaude")
	}
	if capturedConfig.Args.SystemPromptFile == "" {
		t.Error("SystemPromptFile should be set")
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

func TestRunAgentLoop_DebugConfigOutput(t *testing.T) {
	deps, tmpDir, _ := newTestAgentLoopDeps(t)

	var out bytes.Buffer
	deps.stdout = &out

	// Set up env vars to be printed
	deps.getenv = func(key string) string {
		switch key {
		case "DENDRA_ROOT":
			return tmpDir
		case "DENDRA_AGENT_IDENTITY":
			return "ash"
		default:
			return ""
		}
	}

	// Cancel after first sleep
	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }
	deps.nextTask = func(root, name string) (*state.Task, error) { return nil, nil }
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) { return nil, nil }
	deps.readFile = func(path string) ([]byte, error) { return nil, errors.New("not found") }

	_ = runAgentLoop(ctx, deps, "ash")

	output := out.String()

	// (1) System prompt section with full prompt content
	if !strings.Contains(output, "=== SYSTEM PROMPT ===") {
		t.Error("expected system prompt section header in debug output")
	}
	if !strings.Contains(output, "system prompt for ash") {
		t.Error("expected full system prompt content in debug output")
	}

	// (2) Initial prompt/task
	if !strings.Contains(output, "=== INITIAL PROMPT/TASK ===") {
		t.Error("expected initial prompt section header in debug output")
	}
	if !strings.Contains(output, "do stuff") {
		t.Error("expected task prompt 'do stuff' in debug output")
	}

	// (3) ProcessConfig fields
	if !strings.Contains(output, "=== PROCESS CONFIG ===") {
		t.Error("expected process config section header in debug output")
	}
	if !strings.Contains(output, "session-id") {
		t.Error("expected session-id field in process config debug output")
	}
	if !strings.Contains(output, "work-dir") {
		t.Error("expected work-dir field in process config debug output")
	}

	// (4) Key env vars
	if !strings.Contains(output, "=== KEY ENV VARS ===") {
		t.Error("expected env vars section header in debug output")
	}
	if !strings.Contains(output, "DENDRA_AGENT_IDENTITY") {
		t.Error("expected DENDRA_AGENT_IDENTITY in env vars debug output")
	}
	if !strings.Contains(output, "DENDRA_ROOT") {
		t.Error("expected DENDRA_ROOT in env vars debug output")
	}

	// All lines should have [agent-loop] prefix (after timestamp)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, "[agent-loop]") {
			t.Errorf("expected all output lines to contain [agent-loop], got: %q", line)
		}
	}
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
	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("buildPrompt for researcher type should NOT contain YOUR TASK section")
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
	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("buildPrompt for engineer type should NOT contain YOUR TASK section")
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
	if strings.Contains(prompt, "YOUR TASK:") {
		t.Error("buildPrompt for unknown type should NOT contain YOUR TASK section")
	}
}

// TestDefaultBuildPrompt_ManagerType verifies that defaultAgentLoopDeps().buildPrompt
// dispatches to the manager prompt when the agent type is "manager".
func TestDefaultBuildPrompt_ManagerType(t *testing.T) {
	deps := defaultAgentLoopDeps()

	agentState := &state.AgentState{
		Name:     "ash",
		Type:     "manager",
		Family:   "engineering",
		Parent:   "sensei",
		Branch:   "dmotles/feature-x",
		Worktree: "/tmp/test-worktree",
		Prompt:   "coordinate feature work",
	}

	prompt := deps.buildPrompt(agentState)

	if !strings.Contains(prompt, "Manager agent") {
		t.Error("buildPrompt for manager type should contain 'Manager agent'")
	}
	if !strings.Contains(prompt, "engineering manager") {
		t.Error("buildPrompt for manager type should contain 'engineering manager'")
	}
	if !strings.Contains(prompt, "integration branch") {
		t.Error("buildPrompt for manager type should contain 'integration branch'")
	}
	if strings.Contains(prompt, "TDD WORKFLOW") {
		t.Error("buildPrompt for manager type should NOT contain 'TDD WORKFLOW' (that is engineer prompt text)")
	}
	if strings.Contains(prompt, "hands-on builder") {
		t.Error("buildPrompt for manager type should NOT contain 'hands-on builder' (that is engineer prompt text)")
	}
}

// --- timestampWriter tests ---

func TestTimestampWriter_SingleLine(t *testing.T) {
	var buf bytes.Buffer
	fixedTime := time.Date(2026, 4, 1, 14, 30, 45, 0, time.UTC)
	tw := &timestampWriter{
		w:       &buf,
		nowFunc: func() time.Time { return fixedTime },
	}

	_, err := tw.Write([]byte("[agent-loop] starting task abc123\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	expected := "[14:30:45] [agent-loop] starting task abc123\n"
	if output != expected {
		t.Errorf("got %q, want %q", output, expected)
	}
}

func TestTimestampWriter_MultiLine(t *testing.T) {
	var buf bytes.Buffer
	callCount := 0
	times := []time.Time{
		time.Date(2026, 4, 1, 14, 30, 45, 0, time.UTC),
		time.Date(2026, 4, 1, 14, 30, 46, 0, time.UTC),
	}
	tw := &timestampWriter{
		w: &buf,
		nowFunc: func() time.Time {
			idx := callCount
			if idx >= len(times) {
				idx = len(times) - 1
			}
			callCount++
			return times[idx]
		},
	}

	_, err := tw.Write([]byte("[claude] line one\n[claude] line two\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), output)
	}
	if !strings.HasPrefix(lines[0], "[14:30:45] ") {
		t.Errorf("line 0 missing timestamp prefix: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "[14:30:46] ") {
		t.Errorf("line 1 missing timestamp prefix: %q", lines[1])
	}
}

func TestTimestampWriter_EmptyWrite(t *testing.T) {
	var buf bytes.Buffer
	tw := &timestampWriter{
		w:       &buf,
		nowFunc: time.Now,
	}

	n, err := tw.Write([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes reported, got %d", n)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty write, got %q", buf.String())
	}
}

func TestTimestampWriter_Format(t *testing.T) {
	var buf bytes.Buffer
	fixedTime := time.Date(2026, 4, 1, 4, 5, 7, 0, time.UTC)
	tw := &timestampWriter{
		w:       &buf,
		nowFunc: func() time.Time { return fixedTime },
	}

	tw.Write([]byte("hello\n"))

	output := buf.String()
	// Should have zero-padded HH:MM:SS format
	if !strings.HasPrefix(output, "[04:05:07] ") {
		t.Errorf("expected [04:05:07] prefix, got %q", output)
	}
}

func TestTimestampWriter_ReturnsOriginalByteCount(t *testing.T) {
	var buf bytes.Buffer
	tw := &timestampWriter{
		w:       &buf,
		nowFunc: time.Now,
	}

	input := []byte("[agent-loop] test\n")
	n, err := tw.Write(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(input) {
		t.Errorf("expected Write to return %d, got %d", len(input), n)
	}
}

func TestRunAgentLoop_OutputHasTimestamps(t *testing.T) {
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
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// Every non-empty line should start with a timestamp like [HH:MM:SS]
	tsPattern := regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2}\] `)
	for _, line := range lines {
		if line == "" {
			continue
		}
		if !tsPattern.MatchString(line) {
			t.Errorf("expected line to start with [HH:MM:SS] timestamp, got: %q", line)
		}
	}
}

// =====================================================================
// Tests for sendPromptWithInterrupt and poke file handling
// =====================================================================

func TestSendPromptWithInterrupt_NoPokeFile(t *testing.T) {
	mockProc := &mockProcessManager{
		sendResults: []*protocol.ResultMessage{
			{Type: "result", Result: "done"},
		},
		running: true,
	}

	deps := &agentLoopDeps{
		readFile:   func(string) ([]byte, error) { return nil, errors.New("not found") },
		removeFile: func(string) error { return nil },
	}

	result, pokeContent, err := sendPromptWithInterrupt(
		context.Background(), mockProc, deps, "/tmp/test.poke", "hello",
		10*time.Millisecond, "", "",
	)
	if err != nil {
		t.Fatalf("sendPromptWithInterrupt error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Result != "done" {
		t.Errorf("result.Result = %q, want %q", result.Result, "done")
	}
	if pokeContent != "" {
		t.Errorf("pokeContent = %q, want empty", pokeContent)
	}
	if mockProc.interruptCalled {
		t.Error("InterruptTurn should not be called when no poke file exists")
	}
}

func TestSendPromptWithInterrupt_PokeFileFound(t *testing.T) {
	// This mock blocks until we signal it, simulating a long-running turn.
	sendCh := make(chan struct{})
	mockProc := &mockProcessManager{
		running: true,
	}
	// Override SendPrompt to block until channel is closed.
	originalSend := mockProc.SendPrompt
	_ = originalSend
	blockingProc := &blockingSendProcessManager{
		mockProcessManager: mockProc,
		sendCh:             sendCh,
		result:             &protocol.ResultMessage{Type: "result", Result: "interrupted", IsError: true},
	}

	var pokeReturned sync.Once
	deps := &agentLoopDeps{
		readFile: func(path string) ([]byte, error) {
			var result []byte
			var found bool
			if strings.HasSuffix(path, ".poke") {
				pokeReturned.Do(func() {
					result = []byte("urgent: check status")
					found = true
				})
				if found {
					return result, nil
				}
			}
			return nil, errors.New("not found")
		},
		removeFile: func(string) error { return nil },
	}

	var result *protocol.ResultMessage
	var pokeContent string
	var sendErr error
	done := make(chan struct{})

	go func() {
		defer close(done)
		result, pokeContent, sendErr = sendPromptWithInterrupt(
			context.Background(), blockingProc, deps, "/tmp/test.poke", "hello",
			10*time.Millisecond, "", "",
		)
	}()

	// Wait for interrupt to be called, then unblock SendPrompt.
	time.Sleep(100 * time.Millisecond)
	close(sendCh)
	<-done

	if sendErr != nil {
		t.Fatalf("sendPromptWithInterrupt error: %v", sendErr)
	}
	if pokeContent != "urgent: check status" {
		t.Errorf("pokeContent = %q, want %q", pokeContent, "urgent: check status")
	}
	mockProc.mu.Lock()
	interrupted := mockProc.interruptCalled
	mockProc.mu.Unlock()
	if !interrupted {
		t.Error("InterruptTurn should have been called")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// blockingSendProcessManager wraps mockProcessManager with a blocking SendPrompt.
type blockingSendProcessManager struct {
	*mockProcessManager
	sendCh chan struct{}
	result *protocol.ResultMessage
}

func (b *blockingSendProcessManager) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	b.mu.Lock()
	b.prompts = append(b.prompts, prompt)
	b.mu.Unlock()
	<-b.sendCh
	return b.result, nil
}

func TestRunAgentLoop_PokeFileBetweenTurns(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)
	_ = tmpDir

	pokeDelivered := false
	deps.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ash.poke") && !pokeDelivered {
			pokeDelivered = true
			return []byte("hey, you okay?"), nil
		}
		return nil, errors.New("file not found")
	}

	var removedFiles []string
	deps.removeFile = func(path string) error {
		removedFiles = append(removedFiles, path)
		return nil
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "poke handled"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	// The poke content should have been delivered as a prompt.
	found := false
	for _, p := range mockProc.prompts {
		if strings.Contains(p, "hey, you okay?") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected poke content in prompts, got: %v", mockProc.prompts)
	}

	// Poke file should have been removed.
	pokeRemoved := false
	for _, f := range removedFiles {
		if strings.Contains(f, "ash.poke") {
			pokeRemoved = true
			break
		}
	}
	if !pokeRemoved {
		t.Error("expected poke file to be removed after reading")
	}
}

func TestRunAgentLoop_InboxMessagesLoggedDuringTurn(t *testing.T) {
	// When a task is being processed (SendPrompt is blocking) and inbox messages
	// arrive, the log output should contain a "queued, waiting for current turn"
	// line, AND the messages should still be delivered normally on the next iteration.
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task so the loop enters a sendWithInterrupt call.
	if _, err := state.EnqueueTask(tmpDir, "ash", "long running task"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Use a blocking process manager so SendPrompt blocks while we send messages.
	sendCh := make(chan struct{})
	blockingProc := &blockingSendProcessManager{
		mockProcessManager: mockProc,
		sendCh:             sendCh,
		result:             &protocol.ResultMessage{Type: "result", Result: "done"},
	}

	// Capture stdout
	var outBuf bytes.Buffer
	deps.stdout = &outBuf

	ctx, cancel := context.WithCancel(context.Background())

	// Track how many prompts have been sent to decide when to cancel.
	promptCount := 0
	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		return &promptInterceptor{
			processManager: blockingProc,
			onSend: func(prompt string) {
				promptCount++
				if promptCount >= 2 {
					// After second prompt (inbox delivery), cancel.
					cancel()
				}
			},
		}
	}

	deps.sleepFunc = func(d time.Duration) {
		if promptCount >= 2 {
			cancel()
		}
	}

	// Start the loop in a goroutine.
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = runAgentLoop(ctx, deps, "ash")
	}()

	// Wait a bit for the loop to start processing the task (SendPrompt blocks).
	time.Sleep(50 * time.Millisecond)

	// Send a message to ash's inbox while the turn is in progress.
	if err := messages.Send(tmpDir, "root", "ash", "Fix the bug", "details here"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	// Wait for the poller to notice the message (poll interval is 500ms default,
	// but we need it to see our message).
	time.Sleep(700 * time.Millisecond)

	// Unblock the first SendPrompt so the turn finishes and loop continues.
	close(sendCh)

	// Wait for the loop to finish.
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		cancel()
		<-loopDone
	}

	output := outBuf.String()

	// The output should contain a queued message log line during the turn.
	expectedLog := `message received from root (subject: "Fix the bug")`
	if !strings.Contains(output, expectedLog) {
		t.Errorf("expected output to contain queued message log line %q\ngot:\n%s", expectedLog, output)
	}

	// The message should ALSO be delivered normally (as a read command) on the next iteration.
	found := false
	for _, p := range mockProc.prompts {
		if strings.Contains(p, "dendra messages read") && strings.Contains(p, `subject: "Fix the bug"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected inbox message to be delivered as a read command on next iteration, prompts: %v", mockProc.prompts)
	}
}

func TestRunAgentLoop_InboxQueuedLogNoDuplicates(t *testing.T) {
	// When the same message is polled multiple times during a single turn,
	// only one log line should be emitted for it.
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task so the loop enters a sendWithInterrupt call.
	if _, err := state.EnqueueTask(tmpDir, "ash", "slow task"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Use a blocking process manager.
	sendCh := make(chan struct{})
	blockingProc := &blockingSendProcessManager{
		mockProcessManager: mockProc,
		sendCh:             sendCh,
		result:             &protocol.ResultMessage{Type: "result", Result: "done"},
	}

	var outBuf bytes.Buffer
	deps.stdout = &outBuf

	ctx, cancel := context.WithCancel(context.Background())

	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		return blockingProc
	}
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = runAgentLoop(ctx, deps, "ash")
	}()

	// Wait for the blocking SendPrompt to be reached.
	time.Sleep(50 * time.Millisecond)

	// Send a message to ash's inbox.
	if err := messages.Send(tmpDir, "root", "ash", "Same message", "body"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	// Wait long enough for multiple poll intervals to fire (2+ polls should see it).
	time.Sleep(1500 * time.Millisecond)

	// Unblock SendPrompt and let loop finish.
	close(sendCh)

	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		cancel()
		<-loopDone
	}

	output := outBuf.String()

	// Count how many times the queued log line appears.
	queuedLine := `message received from root (subject: "Same message")`
	count := strings.Count(output, queuedLine)

	if count == 0 {
		t.Errorf("expected at least one queued message log line, got none.\noutput:\n%s", output)
	}
	if count > 1 {
		t.Errorf("expected exactly 1 queued message log line, got %d.\noutput:\n%s", count, output)
	}
}

func TestRunAgentLoop_InboxQueuedLogFormat(t *testing.T) {
	// Verify the exact format of the queued message log line.
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task to trigger a sendWithInterrupt call.
	if _, err := state.EnqueueTask(tmpDir, "ash", "working on stuff"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	sendCh := make(chan struct{})
	blockingProc := &blockingSendProcessManager{
		mockProcessManager: mockProc,
		sendCh:             sendCh,
		result:             &protocol.ResultMessage{Type: "result", Result: "done"},
	}

	var outBuf bytes.Buffer
	deps.stdout = &outBuf

	ctx, cancel := context.WithCancel(context.Background())

	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		return blockingProc
	}
	deps.sleepFunc = func(d time.Duration) {
		cancel()
	}

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		_ = runAgentLoop(ctx, deps, "ash")
	}()

	time.Sleep(50 * time.Millisecond)

	if err := messages.Send(tmpDir, "root", "ash", "Fix the bug", "please fix it"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	time.Sleep(700 * time.Millisecond)

	close(sendCh)

	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		cancel()
		<-loopDone
	}

	output := outBuf.String()

	// Check the exact log line format including the [agent-loop] prefix and trailing text.
	expectedPattern := regexp.MustCompile(
		`\[agent-loop\] message received from root \(subject: "Fix the bug"\) — queued, waiting for current turn to finish`,
	)
	if !expectedPattern.MatchString(output) {
		t.Errorf("expected log line matching pattern:\n  %s\ngot output:\n%s", expectedPattern.String(), output)
	}
}

// promptInterceptor wraps a processManager and calls onSend after each SendPrompt.
type promptInterceptor struct {
	processManager
	onSend func(prompt string)
}

func (p *promptInterceptor) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	result, err := p.processManager.SendPrompt(ctx, prompt)
	if p.onSend != nil {
		p.onSend(prompt)
	}
	return result, err
}

// --- Tests for injected prompt logging (QUM-62) ---

func TestRunAgentLoop_PokeDelivery_LogsInjectedPrompt(t *testing.T) {
	deps, _, mockProc := newTestAgentLoopDeps(t)
	buf := &bytes.Buffer{}
	deps.stdout = buf

	pokeContent := "hey agent, look at this"
	pokeRead := false
	deps.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ash.poke") && !pokeRead {
			pokeRead = true
			return []byte(pokeContent), nil
		}
		return nil, errors.New("file not found")
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	output := buf.String()
	if !strings.Contains(output, "=== INJECTED PROMPT ===") {
		t.Error("expected log output to contain '=== INJECTED PROMPT ===' for poke delivery")
	}
	if !strings.Contains(output, pokeContent) {
		t.Errorf("expected log output to contain poke content %q, got:\n%s", pokeContent, output)
	}
}

func TestRunAgentLoop_InboxDelivery_LogsInjectedPrompt(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)
	buf := &bytes.Buffer{}
	deps.stdout = buf

	// Send a message to inbox
	if err := messages.Send(tmpDir, "root", "ash", "urgent", "please fix the bug"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	output := buf.String()
	if !strings.Contains(output, "=== INJECTED PROMPT ===") {
		t.Error("expected log output to contain '=== INJECTED PROMPT ===' for inbox delivery")
	}
	if !strings.Contains(output, "dendra messages read") {
		t.Errorf("expected log output to contain 'dendra messages read' command, got:\n%s", output)
	}
	if !strings.Contains(output, `subject: "urgent"`) {
		t.Errorf("expected log output to contain message subject, got:\n%s", output)
	}
	if strings.Contains(output, "please fix the bug") {
		t.Errorf("log output should NOT contain message body inline, got:\n%s", output)
	}
}

func TestRunAgentLoop_TaskDelivery_LogsInjectedPrompt(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)
	buf := &bytes.Buffer{}
	deps.stdout = buf

	if _, err := state.EnqueueTask(tmpDir, "ash", "implement feature Y"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	output := buf.String()
	if !strings.Contains(output, "=== INJECTED PROMPT ===") {
		t.Error("expected log output to contain '=== INJECTED PROMPT ===' for task delivery")
	}
}

func TestRunAgentLoop_WakeFile_LogsInjectedPrompt(t *testing.T) {
	deps, _, mockProc := newTestAgentLoopDeps(t)
	buf := &bytes.Buffer{}
	deps.stdout = buf

	wakeContent := "time to wake up and work"
	wakeReadCount := 0
	deps.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ash.wake") {
			wakeReadCount++
			if wakeReadCount == 1 {
				return []byte(wakeContent), nil
			}
		}
		return nil, errors.New("file not found")
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	output := buf.String()
	if !strings.Contains(output, "=== INJECTED PROMPT ===") {
		t.Error("expected log output to contain '=== INJECTED PROMPT ===' for wake file delivery")
	}
	if !strings.Contains(output, wakeContent) {
		t.Errorf("expected log output to contain wake content %q, got:\n%s", wakeContent, output)
	}
}

// TestRunAgentLoop_StateMissing_ExitsCleanly verifies that the agent loop exits
// cleanly when the agent state file has been deleted (e.g., after merge retires
// the agent). Currently this test will FAIL because the agent loop does not
// check for state file deletion — it hangs in its loop until the context times
// out. The fix: add a state-file-missing check in the loop body.
func TestRunAgentLoop_StateMissing_ExitsCleanly(t *testing.T) {
	deps, _, mockProc := newTestAgentLoopDeps(t)

	deps.nextTask = func(root, name string) (*state.Task, error) { return nil, nil }
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) { return nil, nil }

	// After startup, delete the state file so loadAgent fails in the loop
	startupDone := false
	originalLoad := deps.loadAgent
	deps.loadAgent = func(root, name string) (*state.AgentState, error) {
		if !startupDone {
			startupDone = true
			return originalLoad(root, name)
		}
		return nil, errors.New("agent state not found")
	}

	deps.readFile = func(path string) ([]byte, error) {
		return nil, errors.New("file not found")
	}

	// Use a short timeout context: if the loop doesn't exit on its own due to
	// the missing state file, the context will cancel after 5 seconds. The test
	// asserts the loop exits cleanly (nil error) rather than via timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := runAgentLoop(ctx, deps, "ash")

	// If the loop exited due to context timeout, it means it didn't detect the
	// missing state file — that's the bug.
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("agent loop did not exit when state file was deleted — it hung until context timeout (bug: no state-missing check)")
	}

	if err != nil {
		t.Fatalf("expected clean exit, got error: %v", err)
	}

	if !mockProc.stopCalled {
		t.Error("expected proc.Stop to be called on state-missing exit")
	}
}

func TestRunAgentLoop_InboxDelivery_ConsumesWakeFile(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Send a real message — this creates both inbox entry AND wake file.
	if err := messages.Send(tmpDir, "root", "ash", "hey", "check this out"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	// Use real readFile/removeFile so the wake file on disk is exercised.
	deps.readFile = os.ReadFile
	deps.removeFile = os.Remove

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "handled inbox"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	// Inbox delivery should have fired (not wake file).
	if len(mockProc.prompts) < 1 {
		t.Fatal("expected at least one prompt from inbox delivery")
	}
	if !strings.Contains(mockProc.prompts[0], "dendra messages read") {
		t.Errorf("expected inbox-style prompt, got: %q", mockProc.prompts[0])
	}

	// The wake file should have been consumed by the inbox delivery branch.
	wakePath := filepath.Join(tmpDir, ".dendra", "agents", "ash.wake")
	if _, err := os.Stat(wakePath); err == nil {
		t.Error("expected wake file to be consumed after inbox delivery, but it still exists")
	}
}

func TestRunAgentLoop_WakeFile_ContinuesImmediately(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Send a real message so inbox has content for the second iteration.
	if err := messages.Send(tmpDir, "root", "ash", "hey", "check this"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	// On the first iteration, inbox check sees messages and fires (inbox comes
	// before wake in the loop). But we want to test the wake path specifically,
	// so we make inbox empty initially and only populate it after the wake fires.
	inboxCallCount := 0
	deps.listMessages = func(root, agent, filter string) ([]*messages.Message, error) {
		inboxCallCount++
		if inboxCallCount == 1 {
			// First iteration: inbox empty, so wake file fires instead.
			return nil, nil
		}
		// Second iteration: inbox has messages (should fire immediately after wake).
		return messages.List(root, agent, filter)
	}

	wakeContent := "New message from root: hey"
	wakeReadCount := 0
	deps.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ash.wake") {
			wakeReadCount++
			if wakeReadCount == 1 {
				return []byte(wakeContent), nil
			}
		}
		return nil, errors.New("file not found")
	}
	deps.removeFile = func(path string) error { return nil }
	deps.nextTask = func(root, name string) (*state.Task, error) { return nil, nil }

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "woke up"},
		{Type: "result", Result: "read inbox"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	sleepCount := 0
	deps.sleepFunc = func(d time.Duration) {
		sleepCount++
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")

	// With the fix: wake fires (no sleep) -> immediate next iteration -> inbox fires -> sleep -> cancel.
	// Without the fix: wake fires -> sleep -> cancel. Only 1 prompt delivered.
	// So we assert 2 prompts were delivered before the first sleep.
	if len(mockProc.prompts) < 2 {
		t.Fatalf("expected 2 prompts (wake + inbox) before first sleep, got %d: %v", len(mockProc.prompts), mockProc.prompts)
	}
}

func TestRunAgentLoop_MessageSend_SingleNotificationType(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Send a real message — creates both inbox entry and wake file.
	if err := messages.Send(tmpDir, "root", "ash", "deploy done", "all good"); err != nil {
		t.Fatalf("sending message: %v", err)
	}

	// Use real file operations so both inbox and wake file are on disk.
	deps.readFile = os.ReadFile
	deps.removeFile = os.Remove

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	iterCount := 0
	deps.sleepFunc = func(d time.Duration) {
		iterCount++
		if iterCount >= 2 {
			cancel()
		}
	}

	_ = runAgentLoop(ctx, deps, "ash")

	// Every prompt should be inbox-style (contains "dendra messages read").
	// No prompt should be wake-file-style (raw "New message from" content).
	for i, p := range mockProc.prompts {
		if !strings.Contains(p, "dendra messages read") {
			t.Errorf("prompt[%d] should be inbox-style, got: %q", i, p)
		}
		if strings.Contains(p, "New message from root") {
			t.Errorf("prompt[%d] should NOT contain wake file content, got: %q", i, p)
		}
	}
}

func TestRunAgentLoop_RapidMessages_SingleBatchDelivery(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Send 3 messages rapidly.
	for i, subj := range []string{"msg1", "msg2", "msg3"} {
		if err := messages.Send(tmpDir, "root", "ash", subj, fmt.Sprintf("body %d", i)); err != nil {
			t.Fatalf("sending message %d: %v", i, err)
		}
	}

	// Use real file operations.
	deps.readFile = os.ReadFile
	deps.removeFile = os.Remove

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	// Should deliver exactly 1 prompt that batches all 3 messages.
	if len(mockProc.prompts) != 1 {
		t.Fatalf("expected 1 batched prompt, got %d: %v", len(mockProc.prompts), mockProc.prompts)
	}
	prompt := mockProc.prompts[0]
	if !strings.Contains(prompt, "3 new message(s)") {
		t.Errorf("prompt should mention 3 messages, got: %q", prompt)
	}
	// Wake file should be consumed.
	wakePath := filepath.Join(tmpDir, ".dendra", "agents", "ash.wake")
	if _, err := os.Stat(wakePath); err == nil {
		t.Error("expected wake file to be consumed after inbox delivery, but it still exists")
	}
}

func TestRunAgentLoop_LockAcquiredBeforeWork(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task so the loop has work to do.
	if _, err := state.EnqueueTask(tmpDir, "ash", "implement feature X"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	mockProc.sendResults = []*protocol.ResultMessage{
		{Type: "result", Result: "done"},
	}

	// Track event ordering.
	var mu sync.Mutex
	var events []string

	deps.newWorkLock = func(lockDir, agentName string) (*workLock, error) {
		return &workLock{
			Acquire: func() error {
				mu.Lock()
				events = append(events, "acquire")
				mu.Unlock()
				return nil
			},
			Release: func() error {
				mu.Lock()
				events = append(events, "release")
				mu.Unlock()
				return nil
			},
		}, nil
	}

	// Wrap newProcess to track send events.
	origNewProcess := deps.newProcess
	deps.newProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
		pm := origNewProcess(config, observer)
		return &eventTrackingProcess{
			processManager: pm,
			onSend: func() {
				mu.Lock()
				events = append(events, "send")
				mu.Unlock()
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	mu.Lock()
	defer mu.Unlock()

	// Verify ordering: acquire must come before send, send before release.
	acquireIdx, sendIdx, releaseIdx := -1, -1, -1
	for i, e := range events {
		switch e {
		case "acquire":
			if acquireIdx == -1 {
				acquireIdx = i
			}
		case "send":
			if sendIdx == -1 {
				sendIdx = i
			}
		case "release":
			if releaseIdx == -1 {
				releaseIdx = i
			}
		}
	}

	if acquireIdx == -1 {
		t.Fatal("lock was never acquired")
	}
	if sendIdx == -1 {
		t.Fatal("SendPrompt was never called")
	}
	if releaseIdx == -1 {
		t.Fatal("lock was never released")
	}
	if acquireIdx >= sendIdx {
		t.Errorf("acquire (idx=%d) should happen before send (idx=%d)", acquireIdx, sendIdx)
	}
	if sendIdx >= releaseIdx {
		t.Errorf("send (idx=%d) should happen before release (idx=%d)", sendIdx, releaseIdx)
	}
}

// eventTrackingProcess wraps a processManager and calls onSend before each SendPrompt.
type eventTrackingProcess struct {
	processManager
	onSend func()
}

func (e *eventTrackingProcess) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	if e.onSend != nil {
		e.onSend()
	}
	return e.processManager.SendPrompt(ctx, prompt)
}

func TestRunAgentLoop_LockReleasedOnIdle(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)

	// No tasks, no messages, no wake, no poke — idle loop.
	var mu sync.Mutex
	var events []string

	deps.newWorkLock = func(lockDir, agentName string) (*workLock, error) {
		return &workLock{
			Acquire: func() error {
				mu.Lock()
				events = append(events, "acquire")
				mu.Unlock()
				return nil
			},
			Release: func() error {
				mu.Lock()
				events = append(events, "release")
				mu.Unlock()
				return nil
			},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) {
		mu.Lock()
		events = append(events, "sleep")
		mu.Unlock()
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")

	mu.Lock()
	defer mu.Unlock()

	// Lock should be acquired, then released, then sleep (lock NOT held during sleep).
	acquireIdx, releaseIdx, sleepIdx := -1, -1, -1
	for i, e := range events {
		switch e {
		case "acquire":
			if acquireIdx == -1 {
				acquireIdx = i
			}
		case "release":
			if releaseIdx == -1 {
				releaseIdx = i
			}
		case "sleep":
			if sleepIdx == -1 {
				sleepIdx = i
			}
		}
	}

	if acquireIdx == -1 {
		t.Fatal("lock was never acquired")
	}
	if releaseIdx == -1 {
		t.Fatal("lock was never released")
	}
	if sleepIdx == -1 {
		t.Fatal("sleep was never called")
	}
	if acquireIdx >= releaseIdx {
		t.Errorf("acquire (idx=%d) should happen before release (idx=%d)", acquireIdx, releaseIdx)
	}
	if releaseIdx >= sleepIdx {
		t.Errorf("release (idx=%d) should happen before sleep (idx=%d)", releaseIdx, sleepIdx)
	}
}

func TestRunAgentLoop_LockReleasedOnError(t *testing.T) {
	deps, tmpDir, mockProc := newTestAgentLoopDeps(t)

	// Queue a task so the loop has work to do.
	if _, err := state.EnqueueTask(tmpDir, "ash", "do work"); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// First send fails (crash), restart succeeds, second send succeeds.
	mockProc.sendErrors = []error{errors.New("process crashed"), nil}
	mockProc.sendResults = []*protocol.ResultMessage{
		nil,
		{Type: "result", Result: "done"},
	}

	var mu sync.Mutex
	var releaseCalled bool

	deps.newWorkLock = func(lockDir, agentName string) (*workLock, error) {
		return &workLock{
			Acquire: func() error { return nil },
			Release: func() error {
				mu.Lock()
				releaseCalled = true
				mu.Unlock()
				return nil
			},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) { cancel() }

	_ = runAgentLoop(ctx, deps, "ash")

	mu.Lock()
	defer mu.Unlock()
	if !releaseCalled {
		t.Error("lock was not released after error/crash")
	}
}

func TestRunAgentLoop_LockAcquireError(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)

	deps.newWorkLock = func(lockDir, agentName string) (*workLock, error) {
		return &workLock{
			Acquire: func() error { return errors.New("lock acquire failed") },
			Release: func() error { return nil },
		}, nil
	}

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "ash")
	if err == nil {
		t.Fatal("expected error when lock acquire fails")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("error should mention lock, got: %v", err)
	}
}

func TestRunAgentLoop_NewWorkLockError(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)

	deps.newWorkLock = func(lockDir, agentName string) (*workLock, error) {
		return nil, errors.New("cannot create lock")
	}

	ctx := context.Background()
	err := runAgentLoop(ctx, deps, "ash")
	if err == nil {
		t.Fatal("expected error when newWorkLock fails")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("error should mention lock, got: %v", err)
	}
}

func TestRunAgentLoop_LockNotHeldDuringSleep(t *testing.T) {
	deps, _, _ := newTestAgentLoopDeps(t)

	var mu sync.Mutex
	lockHeld := false

	deps.newWorkLock = func(lockDir, agentName string) (*workLock, error) {
		return &workLock{
			Acquire: func() error {
				mu.Lock()
				lockHeld = true
				mu.Unlock()
				return nil
			},
			Release: func() error {
				mu.Lock()
				lockHeld = false
				mu.Unlock()
				return nil
			},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	deps.sleepFunc = func(d time.Duration) {
		mu.Lock()
		held := lockHeld
		mu.Unlock()
		if held {
			t.Error("lock should NOT be held during sleep")
		}
		cancel()
	}

	_ = runAgentLoop(ctx, deps, "ash")
}
