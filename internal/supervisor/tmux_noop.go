package supervisor

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/tmux"
)

type noopTmuxRunner struct{}

var errTmuxUnavailable = fmt.Errorf("tmux is unavailable")

func (n *noopTmuxRunner) HasSession(string) bool        { return false }
func (n *noopTmuxRunner) HasWindow(string, string) bool { return false }
func (n *noopTmuxRunner) NewSession(string, map[string]string, string) error {
	return errTmuxUnavailable
}

func (n *noopTmuxRunner) NewSessionWithWindow(string, string, map[string]string, string) error {
	return errTmuxUnavailable
}

func (n *noopTmuxRunner) NewWindow(string, string, map[string]string, string) error {
	return errTmuxUnavailable
}
func (n *noopTmuxRunner) KillWindow(string, string) error { return errTmuxUnavailable }
func (n *noopTmuxRunner) ListWindowPIDs(string, string) ([]int, error) {
	return nil, errTmuxUnavailable
}
func (n *noopTmuxRunner) ListSessionNames() ([]string, error)         { return nil, errTmuxUnavailable }
func (n *noopTmuxRunner) SendKeys(string, string, string) error       { return errTmuxUnavailable }
func (n *noopTmuxRunner) Attach(string) error                         { return errTmuxUnavailable }
func (n *noopTmuxRunner) SourceFile(string, string) error             { return errTmuxUnavailable }
func (n *noopTmuxRunner) SetEnvironment(string, string, string) error { return errTmuxUnavailable }

var _ tmux.Runner = (*noopTmuxRunner)(nil)
