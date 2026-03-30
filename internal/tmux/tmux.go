package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const RootSessionName = "dendra-root"

// Runner abstracts tmux operations for testability.
type Runner interface {
	HasSession(name string) bool
	NewSession(name string, env map[string]string, shellCmd string) error
	Attach(name string) error
}

// RealRunner implements Runner using the real tmux binary.
type RealRunner struct {
	TmuxPath string
}

// FindTmux locates the tmux binary in PATH.
func FindTmux() (string, error) {
	return exec.LookPath("tmux")
}

// HasSession returns true if a tmux session with the given name exists.
func (r *RealRunner) HasSession(name string) bool {
	cmd := exec.Command(r.TmuxPath, "has-session", "-t", name)
	return cmd.Run() == nil
}

// NewSession creates a new detached tmux session running the given shell command.
func (r *RealRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	args := []string{"new-session", "-d", "-s", name}

	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, shellCmd)

	cmd := exec.Command(r.TmuxPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Attach connects to the named tmux session. If called from inside an
// existing tmux session (TMUX env var is set), it uses switch-client to
// avoid nesting. Otherwise it replaces the current process with
// tmux attach-session via syscall.Exec.
func (r *RealRunner) Attach(name string) error {
	if IsInsideTmux() {
		args := []string{"tmux", "switch-client", "-t", name}
		return syscall.Exec(r.TmuxPath, args, os.Environ())
	}
	args := []string{"tmux", "attach-session", "-t", name}
	return syscall.Exec(r.TmuxPath, args, os.Environ())
}

// IsInsideTmux returns true if the current process is running inside a tmux session.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// BuildShellCmd joins a command and its arguments into a single shell command string
// suitable for passing to tmux new-session.
func BuildShellCmd(binary string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, shellQuote(binary))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}
