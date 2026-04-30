package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tui"
)

// TestEnter_HandoffChannelDispatch_EndToEnd is the QUM-329 integration-level
// guard: a call to sup.Handoff() (the code path that the handoff MCP
// tool hits via sprawlmcp.New(sup)) MUST cause a tui.HandoffRequestedMsg to
// be dispatched into the Bubble Tea program via the onStart-registered send
// function. Before the QUM-329 fix this silently failed because the MCP
// server's supervisor and the TUI listener's supervisor were two separate
// instances with independent handoffCh channels.
//
// This test drives runEnter with:
//   - a real supervisor.Real (so Handoff() executes its real channel send),
//   - a stub runProgram that captures the onStart send function,
//   - newSession=nil (no claude subprocess; we're asserting the channel
//     wiring, not the whole subprocess lifecycle).
//
// Requires tmux on PATH (supervisor.NewReal looks it up); skipped otherwise.
func TestEnter_HandoffChannelDispatch_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH — supervisor.NewReal requires it")
	}

	tmpDir := t.TempDir()

	// .sprawl/state — runEnter reads accent-color from here; missing is fine
	// but the dir must exist for AcquireWeaveLock to write the lock file.
	if err := os.MkdirAll(filepath.Join(tmpDir, ".sprawl", "state"), 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// .sprawl/memory/last-session-id — supervisor.Real.Handoff() reads this
	// to decide where to persist the session summary.
	memDir := filepath.Join(tmpDir, ".sprawl", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	sessionID := "00000000-1111-2222-3333-444444444444"
	if err := os.WriteFile(filepath.Join(memDir, "last-session-id"), []byte(sessionID+"\n"), 0o644); err != nil {
		t.Fatalf("write last-session-id: %v", err)
	}

	sup, err := supervisor.NewReal(supervisor.Config{
		SprawlRoot: tmpDir,
		CallerName: "weave",
	})
	if err != nil {
		t.Fatalf("supervisor.NewReal: %v", err)
	}

	// Stub runProgram: capture the send function from onStart, then block
	// until the test signals shutdown via doneCh.
	received := make(chan tea.Msg, 16)
	doneCh := make(chan struct{})

	deps := &enterDeps{
		getenv: func(k string) string {
			switch k {
			case "SPRAWL_ROOT":
				return tmpDir
			case "SPRAWL_TUI_NO_STDERR_REDIRECT":
				return "1"
			}
			return ""
		},
		getwd: func() (string, error) { return tmpDir, nil },
		runProgram: func(_ tea.Model, onStart func(func(tea.Msg))) error {
			if onStart != nil {
				onStart(func(msg tea.Msg) { received <- msg })
			}
			<-doneCh
			return nil
		},
		newSession:    nil, // skip subprocess; we're testing channel wiring.
		newSupervisor: func(_ string) supervisor.Supervisor { return sup },
	}

	runErr := make(chan error, 1)
	go func() { runErr <- runEnter(deps) }()

	// Give the listener goroutine a moment to subscribe to HandoffRequested.
	time.Sleep(100 * time.Millisecond)

	// Fire the handoff from the same channel the MCP tool would use.
	if hErr := sup.Handoff(context.Background(), "integration-test summary"); hErr != nil {
		close(doneCh)
		t.Fatalf("sup.Handoff: %v", hErr)
	}

	// Assert the TUI receives HandoffRequestedMsg within a generous 2s.
	timeout := time.After(2 * time.Second)
	gotHandoff := false
	for !gotHandoff {
		select {
		case msg := <-received:
			if _, ok := msg.(tui.HandoffRequestedMsg); ok {
				gotHandoff = true
			}
		case <-timeout:
			close(doneCh)
			<-runErr
			t.Fatal("HandoffRequestedMsg never dispatched within 2s — QUM-329 regression: MCP supervisor and TUI listener are on different handoff channels")
		}
	}

	close(doneCh)
	if err := <-runErr; err != nil {
		t.Fatalf("runEnter returned error: %v", err)
	}

	// Verify the on-disk side-effects of Handoff ran under the shared sup.
	if _, statErr := os.Stat(filepath.Join(memDir, "handoff-signal")); statErr != nil {
		t.Errorf("handoff-signal file not written: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(memDir, "sessions", sessionID+".md")); statErr != nil {
		t.Errorf("session summary file not written: %v", statErr)
	}
}
