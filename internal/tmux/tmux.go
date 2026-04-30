package tmux

import (
	"fmt"
	"os/exec"
	"strings"
)

// Runner is the interface for tmux operations. All methods respect
// the configured socket label, ensuring session isolation.
type Runner interface {
	NewSession(name string, width, height int, cmd string) error
	KillSession(name string) error
	HasSession(name string) bool
	SendKeys(target string, keys ...string) error
	CapturePane(target string) (string, error)
	ListSessions() ([]string, error)
	SetOption(target, option, value string) error
	ResizeWindow(target string, width, height int) error
}

// RealRunner implements Runner by shelling out to the tmux binary.
// When SocketLabel is non-empty, all commands are issued with -L <label>,
// isolating sessions onto a dedicated tmux server socket.
type RealRunner struct {
	TmuxBin     string
	SocketLabel string
}

// NewRealRunner constructs a RealRunner, resolving the tmux binary via PATH.
func NewRealRunner(socketLabel string) (*RealRunner, error) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("tmux not found: %w", err)
	}
	return &RealRunner{TmuxBin: bin, SocketLabel: socketLabel}, nil
}

// baseArgs returns the socket flag prefix for all tmux commands.
func (r *RealRunner) baseArgs() []string {
	if r.SocketLabel != "" {
		return []string{"-L", r.SocketLabel}
	}
	return nil
}

func (r *RealRunner) run(args ...string) error {
	cmd := exec.Command(r.TmuxBin, append(r.baseArgs(), args...)...) //nolint:gosec // args are constructed from trusted caller inputs
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func (r *RealRunner) output(args ...string) (string, error) {
	cmd := exec.Command(r.TmuxBin, append(r.baseArgs(), args...)...) //nolint:gosec // args are constructed from trusted caller inputs
	out, err := cmd.Output()
	return string(out), err
}

func (r *RealRunner) NewSession(name string, width, height int, cmdStr string) error {
	return r.run("new-session", "-d",
		"-s", name,
		"-x", fmt.Sprintf("%d", width),
		"-y", fmt.Sprintf("%d", height),
		cmdStr,
	)
}

func (r *RealRunner) KillSession(name string) error {
	return r.run("kill-session", "-t", name)
}

func (r *RealRunner) HasSession(name string) bool {
	return r.run("has-session", "-t", name) == nil
}

func (r *RealRunner) SendKeys(target string, keys ...string) error {
	args := append([]string{"send-keys", "-t", target}, keys...)
	return r.run(args...)
}

func (r *RealRunner) CapturePane(target string) (string, error) {
	return r.output("capture-pane", "-t", target, "-p")
}

func (r *RealRunner) ListSessions() ([]string, error) {
	out, err := r.output("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (r *RealRunner) SetOption(target, option, value string) error {
	return r.run("set-option", "-t", target, option, value)
}

func (r *RealRunner) ResizeWindow(target string, width, height int) error {
	return r.run("resize-window", "-t", target,
		"-x", fmt.Sprintf("%d", width),
		"-y", fmt.Sprintf("%d", height),
	)
}
