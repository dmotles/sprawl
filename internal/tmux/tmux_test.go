package tmux

import (
	"os"
	"testing"
)

func TestBuildShellCmd_Simple(t *testing.T) {
	got := BuildShellCmd("/usr/bin/claude", []string{"--bare", "--name", "test"})
	want := "'/usr/bin/claude' '--bare' '--name' 'test'"
	if got != want {
		t.Errorf("BuildShellCmd = %q, want %q", got, want)
	}
}

func TestBuildShellCmd_QuotesSpecialChars(t *testing.T) {
	got := BuildShellCmd("/usr/bin/claude", []string{"--system-prompt", "you're the root"})
	want := "'/usr/bin/claude' '--system-prompt' 'you'\\''re the root'"
	if got != want {
		t.Errorf("BuildShellCmd = %q, want %q", got, want)
	}
}

func TestBuildShellCmd_EmptyArgs(t *testing.T) {
	got := BuildShellCmd("/usr/bin/claude", nil)
	want := "'/usr/bin/claude'"
	if got != want {
		t.Errorf("BuildShellCmd = %q, want %q", got, want)
	}
}

func TestBuildShellCmd_EmptyString(t *testing.T) {
	got := BuildShellCmd("/usr/bin/claude", []string{""})
	want := "'/usr/bin/claude' ''"
	if got != want {
		t.Errorf("BuildShellCmd = %q, want %q", got, want)
	}
}

func TestIsInsideTmux(t *testing.T) {
	// Save and restore TMUX env var
	orig := os.Getenv("TMUX")
	defer os.Setenv("TMUX", orig)

	os.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	if !IsInsideTmux() {
		t.Error("expected IsInsideTmux() = true when TMUX is set")
	}

	os.Unsetenv("TMUX")
	if IsInsideTmux() {
		t.Error("expected IsInsideTmux() = false when TMUX is unset")
	}
}

// testableRunner is a RealRunner that captures the command args instead of executing.
// We test the arg-building logic by inspecting what would be passed to tmux.
// Note: RealRunner methods use exec.Command which we can't easily mock,
// so we test the interface contract through the mock used in cmd tests.

func TestDefaultNamespace(t *testing.T) {
	if DefaultNamespace != "⚡" {
		t.Errorf("DefaultNamespace = %q, want %q", DefaultNamespace, "⚡")
	}
}

func TestDefaultRootName(t *testing.T) {
	if DefaultRootName != "neo" {
		t.Errorf("DefaultRootName = %q, want %q", DefaultRootName, "neo")
	}
}

func TestRootWindowNameMatchesDefaultRootName(t *testing.T) {
	if RootWindowName != DefaultRootName {
		t.Errorf("RootWindowName = %q, want it to match DefaultRootName = %q", RootWindowName, DefaultRootName)
	}
}

func TestBranchSeparator(t *testing.T) {
	if BranchSeparator != "├" {
		t.Errorf("BranchSeparator = %q, want %q", BranchSeparator, "├")
	}
}

func TestRootSessionName(t *testing.T) {
	tests := []struct {
		namespace string
		rootName  string
		want      string
	}{
		{"⚡", "neo", "⚡neo"},
		{"🔮", "test", "🔮test"},
		{"💠", "kai", "💠kai"},
	}

	for _, tt := range tests {
		got := RootSessionName(tt.namespace, tt.rootName)
		if got != tt.want {
			t.Errorf("RootSessionName(%q, %q) = %q, want %q", tt.namespace, tt.rootName, got, tt.want)
		}
	}
}

func TestChildrenSessionName(t *testing.T) {
	tests := []struct {
		namespace string
		treePath  string
		want      string
	}{
		{"⚡", "neo", "⚡neo├"},
		{"⚡", "neo├ash", "⚡neo├ash├"},
		{"⚡", "neo├ash├oak", "⚡neo├ash├oak├"},
		{"🔮", "test", "🔮test├"},
	}

	for _, tt := range tests {
		got := ChildrenSessionName(tt.namespace, tt.treePath)
		if got != tt.want {
			t.Errorf("ChildrenSessionName(%q, %q) = %q, want %q", tt.namespace, tt.treePath, got, tt.want)
		}
	}
}

// mockPickRunner implements Runner for PickNamespace testing.
type mockPickRunner struct {
	sessions []string
	err      error
}

func (m *mockPickRunner) HasSession(name string) bool                        { return false }
func (m *mockPickRunner) HasWindow(string, string) bool                      { return false }
func (m *mockPickRunner) NewSession(string, map[string]string, string) error { return nil }
func (m *mockPickRunner) NewSessionWithWindow(string, string, map[string]string, string) error {
	return nil
}
func (m *mockPickRunner) NewWindow(string, string, map[string]string, string) error { return nil }
func (m *mockPickRunner) KillWindow(string, string) error                           { return nil }
func (m *mockPickRunner) ListWindowPIDs(string, string) ([]int, error)              { return nil, nil }
func (m *mockPickRunner) SendKeys(string, string, string) error                     { return nil }
func (m *mockPickRunner) Attach(string) error                                       { return nil }
func (m *mockPickRunner) ListSessionNames() ([]string, error)                       { return m.sessions, m.err }

func TestPickNamespace_NoServer(t *testing.T) {
	runner := &mockPickRunner{err: errNoServer}
	got := PickNamespace(runner)
	if got != DefaultNamespace {
		t.Errorf("PickNamespace with no server = %q, want %q", got, DefaultNamespace)
	}
}

func TestPickNamespace_NoSessions(t *testing.T) {
	runner := &mockPickRunner{sessions: nil}
	got := PickNamespace(runner)
	if got != "⚡" {
		t.Errorf("PickNamespace with no sessions = %q, want %q", got, "⚡")
	}
}

func TestPickNamespace_FirstTaken(t *testing.T) {
	runner := &mockPickRunner{sessions: []string{"⚡neo"}}
	got := PickNamespace(runner)
	if got != "🔮" {
		t.Errorf("PickNamespace = %q, want %q (should skip ⚡)", got, "🔮")
	}
}

func TestPickNamespace_MultipleTaken(t *testing.T) {
	runner := &mockPickRunner{sessions: []string{"⚡neo", "🔮neo", "💠neo"}}
	got := PickNamespace(runner)
	if got != "🌃" {
		t.Errorf("PickNamespace = %q, want %q (should skip first three)", got, "🌃")
	}
}

func TestPickNamespace_ChildSessionDoesNotMarkTaken(t *testing.T) {
	// A children session like ⚡neo├ should NOT mark ⚡ as taken.
	// If neo was killed but children are still running, user should be able
	// to re-init into the same namespace.
	runner := &mockPickRunner{sessions: []string{"⚡neo├"}}
	got := PickNamespace(runner)
	if got != "⚡" {
		t.Errorf("PickNamespace = %q, want %q (child session should not count)", got, "⚡")
	}
}

func TestPickNamespace_RootSessionMarksTaken(t *testing.T) {
	// Only a root session (emoji+DefaultRootName) marks the namespace as taken.
	runner := &mockPickRunner{sessions: []string{"⚡neo"}}
	got := PickNamespace(runner)
	if got != "🔮" {
		t.Errorf("PickNamespace = %q, want %q (root session should mark taken)", got, "🔮")
	}
}

func TestNamespacePool_NotEmpty(t *testing.T) {
	if len(NamespacePool) == 0 {
		t.Error("NamespacePool should not be empty")
	}
	if NamespacePool[0] != "⚡" {
		t.Errorf("NamespacePool[0] = %q, want %q", NamespacePool[0], "⚡")
	}
}

func TestExactTarget(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"⚡neo", "=⚡neo"},
		{"⚡neo├", "=⚡neo├"},
		{"", "="},
	}

	for _, tt := range tests {
		got := exactTarget(tt.input)
		if got != tt.want {
			t.Errorf("exactTarget(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's", "'it'\\''s'"},
		{"spaces here", "'spaces here'"},
		{"$VAR", "'$VAR'"},
	}

	for _, tt := range tests {
		got := ShellQuote(tt.input)
		if got != tt.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// mockHasWindowRunner implements Runner for HasWindow testing.
type mockHasWindowRunner struct {
	mockPickRunner
	hasWindow bool
}

func (m *mockHasWindowRunner) HasWindow(sessionName, windowName string) bool {
	return m.hasWindow
}

func TestHasWindow_MockReturnsTrue(t *testing.T) {
	var runner Runner = &mockHasWindowRunner{hasWindow: true}
	if !runner.HasWindow("session", "window") {
		t.Error("expected HasWindow to return true")
	}
}

func TestHasWindow_MockReturnsFalse(t *testing.T) {
	var runner Runner = &mockHasWindowRunner{hasWindow: false}
	if runner.HasWindow("session", "window") {
		t.Error("expected HasWindow to return false")
	}
}

func TestHasWindow_RealRunnerBadPath(t *testing.T) {
	// RealRunner with a non-existent tmux binary should return false gracefully.
	r := &RealRunner{TmuxPath: "/nonexistent/tmux"}
	if r.HasWindow("session", "window") {
		t.Error("expected HasWindow to return false with invalid tmux path")
	}
}

var errNoServer = errorf("no server running")

func errorf(msg string) error {
	return &simpleError{msg}
}

type simpleError struct {
	msg string
}

func (e *simpleError) Error() string { return e.msg }
