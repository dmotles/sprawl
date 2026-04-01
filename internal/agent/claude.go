package agent

import "os/exec"

// Launcher finds the claude CLI binary.
type Launcher interface {
	FindBinary() (string, error)
}

// RealLauncher implements Launcher using the real claude CLI.
type RealLauncher struct{}

// FindBinary locates the claude binary in PATH.
func (r *RealLauncher) FindBinary() (string, error) {
	return exec.LookPath("claude")
}
