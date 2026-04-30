package tmux

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBaseArgs_WithSocket(t *testing.T) {
	r := &RealRunner{TmuxBin: "tmux", SocketLabel: "test-sock"}
	args := r.baseArgs()
	if len(args) != 2 || args[0] != "-L" || args[1] != "test-sock" {
		t.Errorf("baseArgs() = %v, want [-L test-sock]", args)
	}
}

func TestBaseArgs_NoSocket(t *testing.T) {
	r := &RealRunner{TmuxBin: "tmux", SocketLabel: ""}
	args := r.baseArgs()
	if len(args) != 0 {
		t.Errorf("baseArgs() = %v, want []", args)
	}
}

func TestNewRealRunner(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	r, err := NewRealRunner("test-label")
	if err != nil {
		t.Fatalf("NewRealRunner() error = %v", err)
	}
	if r.SocketLabel != "test-label" {
		t.Errorf("SocketLabel = %q, want %q", r.SocketLabel, "test-label")
	}
	if r.TmuxBin == "" {
		t.Error("TmuxBin is empty")
	}
}

func TestNewRealRunner_EmptyLabel(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	r, err := NewRealRunner("")
	if err != nil {
		t.Fatalf("NewRealRunner() error = %v", err)
	}
	if r.SocketLabel != "" {
		t.Errorf("SocketLabel = %q, want empty", r.SocketLabel)
	}
}

// Integration tests that require a real tmux binary.
// They use a unique socket label so they are fully isolated from the user's
// default tmux server and any production sprawl sessions.

func testSocketLabel(t *testing.T) string {
	t.Helper()
	return "sprawl-test-" + strings.ReplaceAll(t.Name(), "/", "-")
}

func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
}

func TestIntegration_NewSession_HasSession_KillSession(t *testing.T) {
	skipIfNoTmux(t)

	label := testSocketLabel(t)
	r, err := NewRealRunner(label)
	if err != nil {
		t.Fatal(err)
	}

	sessionName := "int-test-sess"

	// Ensure clean state
	_ = r.KillSession(sessionName)

	if r.HasSession(sessionName) {
		t.Fatal("session should not exist before NewSession")
	}

	if err := r.NewSession(sessionName, 80, 24, "sleep 300"); err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	t.Cleanup(func() { _ = r.KillSession(sessionName) })

	if !r.HasSession(sessionName) {
		t.Error("HasSession() = false after NewSession, want true")
	}

	if err := r.KillSession(sessionName); err != nil {
		t.Fatalf("KillSession() error = %v", err)
	}

	if r.HasSession(sessionName) {
		t.Error("HasSession() = true after KillSession, want false")
	}
}

func TestIntegration_ListSessions(t *testing.T) {
	skipIfNoTmux(t)

	label := testSocketLabel(t)
	r, err := NewRealRunner(label)
	if err != nil {
		t.Fatal(err)
	}

	s1, s2 := "list-test-a", "list-test-b"
	_ = r.KillSession(s1)
	_ = r.KillSession(s2)

	if err := r.NewSession(s1, 80, 24, "sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.KillSession(s1) })

	if err := r.NewSession(s2, 80, 24, "sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.KillSession(s2) })

	sessions, err := r.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}

	found := map[string]bool{}
	for _, s := range sessions {
		found[s] = true
	}
	if !found[s1] || !found[s2] {
		t.Errorf("ListSessions() = %v, want both %q and %q", sessions, s1, s2)
	}
}

func TestIntegration_CapturePane(t *testing.T) {
	skipIfNoTmux(t)

	label := testSocketLabel(t)
	r, err := NewRealRunner(label)
	if err != nil {
		t.Fatal(err)
	}

	sessionName := "cap-test"
	_ = r.KillSession(sessionName)

	// Use a shell that echoes something recognizable
	if err := r.NewSession(sessionName, 80, 24, "echo CAPTURE_TEST_MARKER; sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.KillSession(sessionName) })

	// Give tmux a moment to render the echo output
	time.Sleep(500 * time.Millisecond)

	output, err := r.CapturePane(sessionName)
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}

	if !strings.Contains(output, "CAPTURE_TEST_MARKER") {
		t.Errorf("CapturePane() output = %q, want to contain CAPTURE_TEST_MARKER", output)
	}
}

func TestIntegration_SendKeys(t *testing.T) {
	skipIfNoTmux(t)

	label := testSocketLabel(t)
	r, err := NewRealRunner(label)
	if err != nil {
		t.Fatal(err)
	}

	sessionName := "sendkeys-test"
	_ = r.KillSession(sessionName)

	if err := r.NewSession(sessionName, 80, 24, "cat"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.KillSession(sessionName) })

	if err := r.SendKeys(sessionName, "hello-from-test", "Enter"); err != nil {
		t.Fatalf("SendKeys() error = %v", err)
	}
}

func TestIntegration_SetOption_ResizeWindow(t *testing.T) {
	skipIfNoTmux(t)

	label := testSocketLabel(t)
	r, err := NewRealRunner(label)
	if err != nil {
		t.Fatal(err)
	}

	sessionName := "resize-test"
	_ = r.KillSession(sessionName)

	if err := r.NewSession(sessionName, 80, 24, "sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.KillSession(sessionName) })

	if err := r.SetOption(sessionName, "window-size", "manual"); err != nil {
		t.Fatalf("SetOption() error = %v", err)
	}

	if err := r.ResizeWindow(sessionName, 200, 50); err != nil {
		t.Fatalf("ResizeWindow() error = %v", err)
	}
}

func TestIntegration_IsolatedFromDefaultSocket(t *testing.T) {
	skipIfNoTmux(t)

	label := testSocketLabel(t)
	r, err := NewRealRunner(label)
	if err != nil {
		t.Fatal(err)
	}

	sessionName := "isolation-test"
	if err := r.NewSession(sessionName, 80, 24, "sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.KillSession(sessionName) })

	// A runner with no socket label (default socket) should NOT see this session
	defaultRunner := &RealRunner{TmuxBin: r.TmuxBin, SocketLabel: ""}
	if defaultRunner.HasSession(sessionName) {
		t.Error("session on dedicated socket visible from default socket — isolation broken")
	}
}
