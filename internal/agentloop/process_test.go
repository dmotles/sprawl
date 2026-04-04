package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/claude"
	"github.com/dmotles/dendra/internal/protocol"
)

// --- Mock infrastructure ---

// mockReader returns pre-configured messages in order, then io.EOF.
type mockReader struct {
	mu       sync.Mutex
	messages []*protocol.Message
	errors   []error // parallel to messages; if non-nil, return error instead
	index    int
}

func newMockReader(msgs []*protocol.Message, errs []error) *mockReader {
	if errs == nil {
		errs = make([]error, len(msgs))
	}
	return &mockReader{messages: msgs, errors: errs}
}

func (r *mockReader) Next() (*protocol.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.index >= len(r.messages) {
		return nil, io.EOF
	}
	i := r.index
	r.index++
	if r.errors[i] != nil {
		return nil, r.errors[i]
	}
	return r.messages[i], nil
}

// blockingMockReader delivers messages via a channel, allowing precise
// concurrency control in tests. Close the channel to signal EOF.
type readerResult struct {
	msg *protocol.Message
	err error
}

type blockingMockReader struct {
	ch chan readerResult
}

func (r *blockingMockReader) Next() (*protocol.Message, error) {
	res, ok := <-r.ch
	if !ok {
		return nil, io.EOF
	}
	return res.msg, res.err
}

// mockWriter records all method calls for verification.
type mockWriter struct {
	mu             sync.Mutex
	promptsSent    []string
	toolsApproved  []string
	interruptsSent []string
	closed         bool
	sendErr        error
	approveErr     error
	interruptErr   error
	closeErr       error
}

func (w *mockWriter) SendUserMessage(prompt string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.promptsSent = append(w.promptsSent, prompt)
	return w.sendErr
}

func (w *mockWriter) ApproveToolUse(requestID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.toolsApproved = append(w.toolsApproved, requestID)
	return w.approveErr
}

func (w *mockWriter) SendInterrupt(requestID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interruptsSent = append(w.interruptsSent, requestID)
	return w.interruptErr
}

func (w *mockWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeErr
}

// mockCommandStarter returns pre-configured mocks from Start().
type mockCommandStarter struct {
	reader   MessageReader
	writer   *mockWriter
	waitFn   WaitFunc
	cancelFn CancelFunc
	startErr error
}

func (s *mockCommandStarter) Start(ctx context.Context, config ProcessConfig) (MessageReader, MessageWriter, WaitFunc, CancelFunc, error) {
	if s.startErr != nil {
		return nil, nil, nil, nil, s.startErr
	}
	return s.reader, s.writer, s.waitFn, s.cancelFn, nil
}

// mockObserver records all messages received via OnMessage.
type mockObserver struct {
	mu       sync.Mutex
	messages []*protocol.Message
}

func (o *mockObserver) OnMessage(msg *protocol.Message) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.messages = append(o.messages, msg)
}

func (o *mockObserver) getMessages() []*protocol.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := make([]*protocol.Message, len(o.messages))
	copy(cp, o.messages)
	return cp
}

// --- Helpers ---

// makeMessage builds a *protocol.Message by marshaling v to JSON and then
// unmarshaling the envelope fields, populating Raw with the full JSON.
func makeMessage(v any) *protocol.Message {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("makeMessage: marshal error: %v", err))
	}
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		panic(fmt.Sprintf("makeMessage: unmarshal error: %v", err))
	}
	msg.Raw = data
	return &msg
}

func makeInitMessage(sessionID string) *protocol.Message {
	return makeMessage(protocol.SystemInit{
		Type:           "system",
		Subtype:        "init",
		SessionID:      sessionID,
		CWD:            "/tmp",
		Tools:          []string{"Bash"},
		Model:          "claude-sonnet-4-6",
		PermissionMode: "bypassPermissions",
		ClaudeVersion:  "2.1.87",
		APIKeySource:   "user",
	})
}

func makeResultMessage(result string, isError bool) *protocol.Message {
	return makeMessage(protocol.ResultMessage{
		Type:         "result",
		Subtype:      "success",
		Result:       result,
		IsError:      isError,
		DurationMs:   100,
		NumTurns:     1,
		TotalCostUsd: 0.01,
		StopReason:   "end_turn",
	})
}

func makeControlRequest(requestID string) *protocol.Message {
	return makeMessage(protocol.ControlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   json.RawMessage(`{"subtype":"can_use_tool","tool_name":"Bash"}`),
	})
}

func makeAssistantMessage(uuid string) *protocol.Message {
	return makeMessage(protocol.AssistantMessage{
		Type:    "assistant",
		UUID:    uuid,
		Content: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"Hello"}]}`),
	})
}

// --- Tests ---

// =====================================================================
// UPDATED existing tests: Start() now blocks until initial result arrives
// =====================================================================

func TestProcess_Start_HappyPath(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultMsg := makeResultMessage("ready", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	err := p.Start(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if p.State() != StateIdle {
		t.Errorf("State() = %q, want %q", p.State(), StateIdle)
	}
}

func TestProcess_Start_EOF(t *testing.T) {
	// Reader returns EOF immediately -- no init message.
	reader := newMockReader(nil, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	err := p.Start(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("Start() expected error when reader returns EOF before init, got nil")
	}
}

func TestProcess_SendPrompt_HappyPath(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	resultMsg := makeResultMessage("Done", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultForStart, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	result, err := p.SendPrompt(context.Background(), "do something")
	if err != nil {
		t.Fatalf("SendPrompt() error: %v", err)
	}
	if result == nil {
		t.Fatal("SendPrompt() returned nil result")
	}
	if result.Result != "Done" {
		t.Errorf("result.Result = %q, want %q", result.Result, "Done")
	}
	if result.IsError {
		t.Error("result.IsError = true, want false")
	}

	// Verify the writer received the initial prompt (from Start) and the SendPrompt prompt.
	if len(writer.promptsSent) != 2 {
		t.Fatalf("writer.promptsSent has %d entries, want 2", len(writer.promptsSent))
	}
	if writer.promptsSent[0] != "test prompt" {
		t.Errorf("writer.promptsSent[0] = %q, want %q", writer.promptsSent[0], "test prompt")
	}
	if writer.promptsSent[1] != "do something" {
		t.Errorf("writer.promptsSent[1] = %q, want %q", writer.promptsSent[1], "do something")
	}

	// State should be back to idle after result.
	if p.State() != StateIdle {
		t.Errorf("State() after SendPrompt = %q, want %q", p.State(), StateIdle)
	}
}

func TestProcess_SendPrompt_WithControlRequest(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	ctrlReq := makeControlRequest("req-42")
	resultMsg := makeResultMessage("Approved and done", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultForStart, ctrlReq, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	result, err := p.SendPrompt(context.Background(), "use bash")
	if err != nil {
		t.Fatalf("SendPrompt() error: %v", err)
	}
	if result == nil {
		t.Fatal("SendPrompt() returned nil result")
	}

	// Verify tool use was approved with the correct request ID.
	if len(writer.toolsApproved) != 1 {
		t.Fatalf("writer.toolsApproved has %d entries, want 1", len(writer.toolsApproved))
	}
	if writer.toolsApproved[0] != "req-42" {
		t.Errorf("writer.toolsApproved[0] = %q, want %q", writer.toolsApproved[0], "req-42")
	}
}

func TestProcess_SendPrompt_MultipleControlRequests(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	ctrl1 := makeControlRequest("req-1")
	ctrl2 := makeControlRequest("req-2")
	ctrl3 := makeControlRequest("req-3")
	resultMsg := makeResultMessage("All approved", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultForStart, ctrl1, ctrl2, ctrl3, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	result, err := p.SendPrompt(context.Background(), "do three things")
	if err != nil {
		t.Fatalf("SendPrompt() error: %v", err)
	}
	if result == nil {
		t.Fatal("SendPrompt() returned nil result")
	}

	// All three control requests should have been approved.
	if len(writer.toolsApproved) != 3 {
		t.Fatalf("writer.toolsApproved has %d entries, want 3", len(writer.toolsApproved))
	}
	expected := []string{"req-1", "req-2", "req-3"}
	for i, want := range expected {
		if writer.toolsApproved[i] != want {
			t.Errorf("writer.toolsApproved[%d] = %q, want %q", i, writer.toolsApproved[i], want)
		}
	}
}

func TestProcess_SendPrompt_Observer(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	assistMsg := makeAssistantMessage("msg-1")
	resultMsg := makeResultMessage("Done", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultForStart, assistMsg, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}
	obs := &mockObserver{}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter, WithObserver(obs))
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	_, err := p.SendPrompt(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("SendPrompt() error: %v", err)
	}

	// Observer should now receive ALL message types including init, result, and control_request.
	observed := obs.getMessages()

	// Check that observer received init message.
	foundInit := false
	for _, m := range observed {
		if m.Type == "system" && m.Subtype == "init" {
			foundInit = true
		}
	}
	if !foundInit {
		t.Error("observer did not receive the init message")
	}

	// Check that observer received assistant message.
	foundAssistant := false
	for _, m := range observed {
		if m.Type == "assistant" && m.UUID == "msg-1" {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Error("observer did not receive the assistant message")
	}

	// Check that observer received result messages (both for start and sendprompt).
	resultCount := 0
	for _, m := range observed {
		if m.Type == "result" {
			resultCount++
		}
	}
	if resultCount < 2 {
		t.Errorf("observer received %d result messages, want at least 2", resultCount)
	}
}

func TestProcess_SendPrompt_ReaderError(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	reader := newMockReader(
		[]*protocol.Message{initMsg, resultForStart, nil},
		[]error{nil, nil, fmt.Errorf("connection broken")},
	)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	_, err := p.SendPrompt(context.Background(), "break things")
	if err == nil {
		t.Fatal("SendPrompt() expected error when reader fails, got nil")
	}

	if p.State() != StateStopped {
		t.Errorf("State() after reader error = %q, want %q", p.State(), StateStopped)
	}
}

func TestProcess_SendPrompt_ErrorResult(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	errResult := makeResultMessage("max turns exceeded", true)
	reader := newMockReader([]*protocol.Message{initMsg, resultForStart, errResult}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	result, err := p.SendPrompt(context.Background(), "do too much")
	if err != nil {
		t.Fatalf("SendPrompt() returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("SendPrompt() returned nil result")
	}
	if !result.IsError {
		t.Error("result.IsError = false, want true")
	}
	if result.Result != "max turns exceeded" {
		t.Errorf("result.Result = %q, want %q", result.Result, "max turns exceeded")
	}
}

func TestProcess_MultiTurn(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	result1 := makeResultMessage("first done", false)
	result2 := makeResultMessage("second done", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultForStart, result1, result2}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// First turn
	r1, err := p.SendPrompt(context.Background(), "first prompt")
	if err != nil {
		t.Fatalf("SendPrompt #1 error: %v", err)
	}
	if r1.Result != "first done" {
		t.Errorf("first result = %q, want %q", r1.Result, "first done")
	}
	if p.State() != StateIdle {
		t.Errorf("State() after first turn = %q, want %q", p.State(), StateIdle)
	}

	// Second turn
	r2, err := p.SendPrompt(context.Background(), "second prompt")
	if err != nil {
		t.Fatalf("SendPrompt #2 error: %v", err)
	}
	if r2.Result != "second done" {
		t.Errorf("second result = %q, want %q", r2.Result, "second done")
	}
	if p.State() != StateIdle {
		t.Errorf("State() after second turn = %q, want %q", p.State(), StateIdle)
	}

	// Verify all prompts were sent: initial (from Start) + two SendPrompt calls.
	if len(writer.promptsSent) != 3 {
		t.Fatalf("writer.promptsSent has %d entries, want 3", len(writer.promptsSent))
	}
	if writer.promptsSent[0] != "test prompt" {
		t.Errorf("promptsSent[0] = %q, want %q", writer.promptsSent[0], "test prompt")
	}
	if writer.promptsSent[1] != "first prompt" {
		t.Errorf("promptsSent[1] = %q, want %q", writer.promptsSent[1], "first prompt")
	}
	if writer.promptsSent[2] != "second prompt" {
		t.Errorf("promptsSent[2] = %q, want %q", writer.promptsSent[2], "second prompt")
	}
}

func TestProcess_Stop(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultMsg := makeResultMessage("ready", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultMsg}, nil)
	writer := &mockWriter{}
	waitCalled := false
	starter := &mockCommandStarter{
		reader: reader,
		writer: writer,
		waitFn: func() error {
			waitCalled = true
			return nil
		},
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	err := p.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if !writer.closed {
		t.Error("writer.Close() was not called by Stop()")
	}
	if !waitCalled {
		t.Error("waitFn was not called by Stop()")
	}
	if p.State() != StateStopped {
		t.Errorf("State() after Stop = %q, want %q", p.State(), StateStopped)
	}
}

func TestProcess_Kill(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultMsg := makeResultMessage("ready", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultMsg}, nil)
	writer := &mockWriter{}
	cancelCalled := false
	starter := &mockCommandStarter{
		reader: reader,
		writer: writer,
		waitFn: func() error { return nil },
		cancelFn: func() error {
			cancelCalled = true
			return nil
		},
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	err := p.Kill()
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if !cancelCalled {
		t.Error("cancelFn was not called by Kill()")
	}
	if p.State() != StateStopped {
		t.Errorf("State() after Kill = %q, want %q", p.State(), StateStopped)
	}
}

func TestProcess_IsRunning(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultMsg := makeResultMessage("ready", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)

	// Before start, process should not be running.
	if p.IsRunning() {
		t.Error("IsRunning() = true before Start(), want false")
	}

	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// After start (idle), process should be running.
	if !p.IsRunning() {
		t.Error("IsRunning() = false after Start(), want true")
	}

	// After stop, process should not be running.
	_ = p.Stop(context.Background())
	if p.IsRunning() {
		t.Error("IsRunning() = true after Stop(), want false")
	}
}

func TestProcess_SessionID(t *testing.T) {
	starter := &mockCommandStarter{
		reader:   newMockReader(nil, nil),
		writer:   &mockWriter{},
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "my-session-42"}}, starter)
	if got := p.SessionID(); got != "my-session-42" {
		t.Errorf("SessionID() = %q, want %q", got, "my-session-42")
	}
}

// =====================================================================
// NEW tests for readLoop / channel-based architecture
// =====================================================================

// TestProcess_ReadLoop_ObserverSeesAllMessages verifies that the observer
// receives ALL message types including result and control_request (not just
// assistant messages as in the current implementation).
func TestProcess_ReadLoop_ObserverSeesAllMessages(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	assistMsg := makeAssistantMessage("msg-1")
	ctrlReq := makeControlRequest("req-99")
	resultMsg := makeResultMessage("all done", false)
	reader := newMockReader([]*protocol.Message{initMsg, assistMsg, ctrlReq, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}
	obs := &mockObserver{}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter, WithObserver(obs))
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// After Start completes (which now waits for result), check observer got everything.
	observed := obs.getMessages()

	// We expect observer to have seen all four message types.
	typesSeen := map[string]bool{}
	for _, m := range observed {
		key := m.Type
		if m.Type == "system" && m.Subtype == "init" {
			key = "system/init"
		}
		typesSeen[key] = true
	}

	for _, wantType := range []string{"system/init", "assistant", "control_request", "result"} {
		if !typesSeen[wantType] {
			t.Errorf("observer did not receive message type %q; types seen: %v", wantType, typesSeen)
		}
	}
}

// TestProcess_Start_WaitsForResult verifies that Start() blocks until the
// initial prompt's result message arrives, not just until system/init.
func TestProcess_Start_WaitsForResult(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	assistMsg := makeAssistantMessage("msg-1")
	resultMsg := makeResultMessage("initial result", false)
	reader := newMockReader([]*protocol.Message{initMsg, assistMsg, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	err := p.Start(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	// After Start returns, the result has been consumed and state is idle.
	if p.State() != StateIdle {
		t.Errorf("State() = %q, want %q", p.State(), StateIdle)
	}
}

// TestProcess_Start_EOFBeforeResult verifies that Start() returns an error
// if the reader hits EOF after init but before a result message.
func TestProcess_Start_EOFBeforeResult(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	// Only init, no result -- EOF follows.
	reader := newMockReader([]*protocol.Message{initMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	err := p.Start(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("Start() expected error when EOF arrives before result, got nil")
	}
}

// TestProcess_Start_ContextCancelled verifies that Start() returns a context
// error when the context is cancelled before the result arrives.
func TestProcess_Start_ContextCancelled(t *testing.T) {
	ch := make(chan readerResult, 10)
	reader := &blockingMockReader{ch: ch}
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)

	ctx, cancel := context.WithCancel(context.Background())

	// Send init message.
	ch <- readerResult{msg: makeInitMessage("sess-1")}

	// Cancel context before sending result.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := p.Start(ctx, "test prompt")
	if err == nil {
		t.Fatal("Start() expected context error, got nil")
	}
	// Should be a context-related error.
	if ctx.Err() == nil {
		t.Error("context should be cancelled")
	}
}

// TestProcess_SendPrompt_ContextCancelled verifies that SendPrompt() returns
// a context error when the context is cancelled before the result arrives.
func TestProcess_SendPrompt_ContextCancelled(t *testing.T) {
	ch := make(chan readerResult, 10)
	reader := &blockingMockReader{ch: ch}
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)

	// Feed messages for Start to succeed: init + result.
	ch <- readerResult{msg: makeInitMessage("sess-1")}
	ch <- readerResult{msg: makeResultMessage("init done", false)}

	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context before sending result for SendPrompt.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := p.SendPrompt(ctx, "do something")
	if err == nil {
		t.Fatal("SendPrompt() expected context error, got nil")
	}
	if ctx.Err() == nil {
		t.Error("context should be cancelled")
	}
}

// TestProcess_SendPrompt_ReaderErrorViaChannel verifies that a reader error
// is propagated through the channel-based architecture to SendPrompt.
func TestProcess_SendPrompt_ReaderErrorViaChannel(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultForStart := makeResultMessage("init done", false)
	resultForPrompt1 := makeResultMessage("prompt1 done", false)
	reader := newMockReader(
		[]*protocol.Message{initMsg, resultForStart, resultForPrompt1, nil},
		[]error{nil, nil, nil, fmt.Errorf("broken pipe")},
	)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// First SendPrompt should succeed.
	r1, err := p.SendPrompt(context.Background(), "first prompt")
	if err != nil {
		t.Fatalf("SendPrompt #1 error: %v", err)
	}
	if r1.Result != "prompt1 done" {
		t.Errorf("first result = %q, want %q", r1.Result, "prompt1 done")
	}

	// Second SendPrompt should receive the reader error.
	_, err = p.SendPrompt(context.Background(), "second prompt")
	if err == nil {
		t.Fatal("SendPrompt #2 expected error from broken reader, got nil")
	}
}

// =====================================================================
// Tests for InterruptTurn
// =====================================================================

func TestProcess_InterruptTurn_WhenIdle_ReturnsErrNotRunning(t *testing.T) {
	initMsg := makeInitMessage("sess-1")
	resultMsg := makeResultMessage("ready", false)
	reader := newMockReader([]*protocol.Message{initMsg, resultMsg}, nil)
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)
	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Process is idle after Start. InterruptTurn should return ErrNotRunning.
	err := p.InterruptTurn(context.Background())
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("InterruptTurn() = %v, want ErrNotRunning", err)
	}

	// Writer should not have received any interrupt.
	writer.mu.Lock()
	numInterrupts := len(writer.interruptsSent)
	writer.mu.Unlock()
	if numInterrupts != 0 {
		t.Errorf("interruptsSent = %d, want 0", numInterrupts)
	}
}

func TestProcess_InterruptTurn_WhenStopped_ReturnsErrNotRunning(t *testing.T) {
	starter := &mockCommandStarter{
		reader:   newMockReader(nil, nil),
		writer:   &mockWriter{},
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)

	// Process is stopped (never started). InterruptTurn should return ErrNotRunning.
	err := p.InterruptTurn(context.Background())
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("InterruptTurn() = %v, want ErrNotRunning", err)
	}
}

func TestProcess_InterruptTurn_WhenRunning_SendsInterrupt(t *testing.T) {
	ch := make(chan readerResult, 10)
	reader := &blockingMockReader{ch: ch}
	writer := &mockWriter{}
	starter := &mockCommandStarter{
		reader:   reader,
		writer:   writer,
		waitFn:   func() error { return nil },
		cancelFn: func() error { return nil },
	}

	p := NewProcess(ProcessConfig{Args: claude.LaunchOpts{SessionID: "sess-1"}}, starter)

	// Feed init + result for Start.
	ch <- readerResult{msg: makeInitMessage("sess-1")}
	ch <- readerResult{msg: makeResultMessage("init done", false)}

	if err := p.Start(context.Background(), "test prompt"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Start SendPrompt in a goroutine (it blocks waiting for result).
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_, _ = p.SendPrompt(context.Background(), "do something long")
	}()

	// Wait for state to transition to Running with a reasonable timeout.
	deadline := time.After(5 * time.Second)
	for p.State() != StateRunning {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for StateRunning, got %q", p.State())
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}

	// Now interrupt the turn.
	err := p.InterruptTurn(context.Background())
	if err != nil {
		t.Fatalf("InterruptTurn() error: %v", err)
	}

	// Verify the writer received an interrupt.
	writer.mu.Lock()
	numInterrupts := len(writer.interruptsSent)
	writer.mu.Unlock()
	if numInterrupts != 1 {
		t.Errorf("interruptsSent = %d, want 1", numInterrupts)
	}

	// Feed a result to unblock SendPrompt (simulating Claude responding to interrupt).
	ch <- readerResult{msg: makeResultMessage("interrupted", true)}
	<-sendDone
}
