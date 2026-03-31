package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const RootSessionName = "dendra-root"
const RootWindowName = "root"

// Runner abstracts tmux operations for testability.
type Runner interface {
	HasSession(name string) bool
	NewSession(name string, env map[string]string, shellCmd string) error
	NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error
	NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error
	KillWindow(sessionName, windowName string) error
	ListWindowPIDs(sessionName, windowName string) ([]int, error)
	SendKeys(sessionName, windowName string, keys string) error
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

// exactTarget returns a tmux target string that forces exact session name matching.
// Without this, tmux performs prefix matching on -t arguments.
func exactTarget(name string) string {
	return "=" + name
}

// HasSession returns true if a tmux session with the given name exists.
func (r *RealRunner) HasSession(name string) bool {
	cmd := exec.Command(r.TmuxPath, "has-session", "-t", exactTarget(name))
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

// NewSessionWithWindow creates a new detached tmux session with a named first window.
func (r *RealRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	args := []string{"new-session", "-d", "-s", sessionName, "-n", windowName}

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

// NewWindow adds a new named window to an existing tmux session.
func (r *RealRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	args := []string{"new-window", "-t", exactTarget(sessionName), "-n", windowName}

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

// KillWindow closes a tmux window by name.
func (r *RealRunner) KillWindow(sessionName, windowName string) error {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "kill-window", "-t", target)
	return cmd.Run()
}

// ListWindowPIDs returns the PIDs of processes running in the given tmux window.
func (r *RealRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "list-panes", "-t", target, "-F", "#{pane_pid}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// SendKeys sends text to a specific tmux window, followed by Enter.
func (r *RealRunner) SendKeys(sessionName, windowName string, keys string) error {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "send-keys", "-t", target, keys, "Enter")
	return cmd.Run()
}

// Attach connects to the named tmux session. If called from inside an
// existing tmux session (TMUX env var is set), it uses switch-client to
// avoid nesting. Otherwise it replaces the current process with
// tmux attach-session via syscall.Exec.
func (r *RealRunner) Attach(name string) error {
	if IsInsideTmux() {
		args := []string{"tmux", "switch-client", "-t", exactTarget(name)}
		return syscall.Exec(r.TmuxPath, args, os.Environ())
	}
	args := []string{"tmux", "attach-session", "-t", exactTarget(name)}
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
	parts = append(parts, ShellQuote(binary))
	for _, a := range args {
		parts = append(parts, ShellQuote(a))
	}
	return strings.Join(parts, " ")
}

// ShellQuote wraps a string in single quotes, escaping any embedded single quotes.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}
