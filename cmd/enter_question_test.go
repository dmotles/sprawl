package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/sprawlmcp"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tui"
)

// writeMinimalState writes the minimum filesystem prerequisites for runEnter
// to drive a stubbed runProgram path.
func writeMinimalState(t *testing.T, dir string) {
	t.Helper()
	stateDir := filepath.Join(dir, ".sprawl", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "accent-color"), []byte("colour212"), 0o644); err != nil {
		t.Fatalf("setup write accent-color: %v", err)
	}
}

// TestEnter_RegistersAndUnregistersQuestionConsumer asserts that runEnter
// registers a TUI question consumer on start and unregisters it on shutdown.
// (QUM-527 slice 2c)
func TestEnter_RegistersAndUnregistersQuestionConsumer(t *testing.T) {
	tmpDir := t.TempDir()
	writeMinimalState(t, tmpDir)

	mockSup := &shutdownMockSupervisor{}

	deps := &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(_ tea.Model, onStart func(func(tea.Msg))) error {
			if onStart != nil {
				onStart(func(tea.Msg) {})
			}
			return nil
		},
		newSession:    nil,
		newSupervisor: func(_ string, _ *calllog.Logger) (supervisor.Supervisor, *sprawlmcp.Server) { return mockSup, nil },
	}

	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}

	mockSup.qmu.Lock()
	defer mockSup.qmu.Unlock()
	if len(mockSup.questionRegistered) == 0 {
		t.Fatal("expected RegisterQuestionConsumer to be called")
	}
	if got := mockSup.questionRegistered[0].Name(); got != "tui" {
		t.Errorf("registered consumer Name() = %q, want %q", got, "tui")
	}
	foundTUI := false
	for _, n := range mockSup.questionUnregistered {
		if n == "tui" {
			foundTUI = true
			break
		}
	}
	if !foundTUI {
		t.Errorf("expected UnregisterQuestionConsumer(\"tui\") on shutdown, got %v", mockSup.questionUnregistered)
	}
}

// TestEnter_QuestionsForwarder_SendsMsgOnSignal asserts that signaling the
// supervisor's QuestionsChanged channel causes the onStart-installed sender
// to receive a QuestionsAvailableMsg derived from PeekQuestions. (QUM-527)
func TestEnter_QuestionsForwarder_SendsMsgOnSignal(t *testing.T) {
	tmpDir := t.TempDir()
	writeMinimalState(t, tmpDir)

	ch := make(chan struct{}, 1)
	subscribed := make(chan struct{})
	head := &supervisor.PendingQuestion{
		Req: supervisor.QuestionRequest{RequestID: "r1", From: "weave"},
		Seq: 1,
	}
	mockSup := &shutdownMockSupervisor{
		questionsChangedCh: ch,
		peekDepth:          1,
		peekHead:           head,
		subscribed:         subscribed,
	}

	received := make(chan tea.Msg, 4)
	doneCh := make(chan struct{})

	deps := &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(_ tea.Model, onStart func(func(tea.Msg))) error {
			if onStart != nil {
				onStart(func(m tea.Msg) { received <- m })
			}
			<-doneCh
			return nil
		},
		newSession:    nil,
		newSupervisor: func(_ string, _ *calllog.Logger) (supervisor.Supervisor, *sprawlmcp.Server) { return mockSup, nil },
	}

	runErr := make(chan error, 1)
	go func() { runErr <- runEnter(deps) }()

	// Deterministically wait until the forwarder goroutine has subscribed
	// (called QuestionsChanged()) before signaling.
	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		close(doneCh)
		<-runErr
		t.Fatal("forwarder did not subscribe within 2s")
	}
	ch <- struct{}{}

	deadline := time.After(2 * time.Second)
	got := false
	for !got {
		select {
		case msg := <-received:
			if qa, ok := msg.(tui.QuestionsAvailableMsg); ok {
				if qa.Depth != 1 {
					t.Errorf("QuestionsAvailableMsg.Depth = %d, want 1", qa.Depth)
				}
				if qa.Head != head {
					t.Errorf("QuestionsAvailableMsg.Head = %p, want %p", qa.Head, head)
				}
				got = true
			}
		case <-deadline:
			close(doneCh)
			<-runErr
			t.Fatal("QuestionsAvailableMsg never dispatched within 2s")
		}
	}

	close(doneCh)
	if err := <-runErr; err != nil {
		t.Fatalf("runEnter: %v", err)
	}
}

// TestEnter_QuestionsForwarder_TerminatesOnHandoffDone asserts the forwarder
// goroutine exits cleanly when runProgram returns (handoffDone is closed),
// such that a subsequent signal on the supervisor's channel does not cause
// a panic (writing to a closed sender) or hang. (QUM-527)
func TestEnter_QuestionsForwarder_TerminatesOnHandoffDone(t *testing.T) {
	tmpDir := t.TempDir()
	writeMinimalState(t, tmpDir)

	ch := make(chan struct{}, 4)
	subscribed := make(chan struct{})
	head := &supervisor.PendingQuestion{
		Req: supervisor.QuestionRequest{RequestID: "post-shutdown", From: "weave"},
		Seq: 1,
	}
	mockSup := &shutdownMockSupervisor{
		questionsChangedCh: ch,
		subscribed:         subscribed,
		peekDepth:          1,
		peekHead:           head,
	}

	// program-sender stub: records every msg the forwarder dispatches.
	received := make(chan tea.Msg, 8)

	deps := &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		runProgram: func(_ tea.Model, onStart func(func(tea.Msg))) error {
			if onStart != nil {
				onStart(func(m tea.Msg) { received <- m })
			}
			// Wait until the forwarder has subscribed; otherwise the
			// post-shutdown probe is meaningless (no goroutine was ever
			// alive).
			select {
			case <-subscribed:
			case <-time.After(2 * time.Second):
				t.Error("forwarder never subscribed; cannot prove it exited")
			}
			// Return immediately so cmd/enter.go closes handoffDone.
			return nil
		},
		newSession:    nil,
		newSupervisor: func(_ string, _ *calllog.Logger) (supervisor.Supervisor, *sprawlmcp.Server) { return mockSup, nil },
	}

	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}

	// Probe sentinel: after runEnter returned, signal on the supervisor's
	// changed channel. A correctly-shut-down forwarder will NOT pick this
	// up, so no QuestionsAvailableMsg should reach the program sender. A
	// leaked goroutine WILL pick it up and dispatch a msg — that's the
	// failure mode we want to catch.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("post-shutdown signal panicked: %v", r)
		}
	}()
	select {
	case ch <- struct{}{}:
	default:
		// Buffer full means the forwarder never drained — also a leak,
		// but caught by the read below.
	}

	select {
	case msg := <-received:
		if _, ok := msg.(tui.QuestionsAvailableMsg); ok {
			t.Fatalf("forwarder goroutine leaked: received %T after shutdown", msg)
		}
	case <-time.After(200 * time.Millisecond):
		// No msg dispatched within 200ms — goroutine exited cleanly.
	}
}
