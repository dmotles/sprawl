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
	if DefaultNamespace != "dendra" {
		t.Errorf("DefaultNamespace = %q, want %q", DefaultNamespace, "dendra")
	}
}

func TestRootSessionName(t *testing.T) {
	tests := []struct {
		namespace string
		want      string
	}{
		{"dendra", "dendra-root"},
		{"test-123", "test-123-root"},
		{"my-ns", "my-ns-root"},
	}

	for _, tt := range tests {
		got := RootSessionName(tt.namespace)
		if got != tt.want {
			t.Errorf("RootSessionName(%q) = %q, want %q", tt.namespace, got, tt.want)
		}
	}
}

func TestChildrenSessionName(t *testing.T) {
	tests := []struct {
		namespace string
		parent    string
		want      string
	}{
		{"dendra", "root", "dendra-root-children"},
		{"dendra", "alice", "dendra-alice-children"},
		{"test-123", "root", "test-123-root-children"},
		{"my-ns", "bob", "my-ns-bob-children"},
	}

	for _, tt := range tests {
		got := ChildrenSessionName(tt.namespace, tt.parent)
		if got != tt.want {
			t.Errorf("ChildrenSessionName(%q, %q) = %q, want %q", tt.namespace, tt.parent, got, tt.want)
		}
	}
}

func TestExactTarget(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"dendra-root", "=dendra-root"},
		{"dendra-root-children", "=dendra-root-children"},
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
