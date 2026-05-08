package agent

import (
	"fmt"
	"os"
	"os/exec"
)

// Launcher finds the claude CLI binary.
type Launcher interface {
	FindBinary() (string, error)
}

// RealLauncher implements Launcher using the real claude CLI.
type RealLauncher struct{}

// FindBinary locates the claude binary.
//
// If $SPRAWL_CLAUDE is set, it is used as the absolute path to the binary
// (typically a shim like scripts/run-claude that injects auth env vars
// before exec'ing the real claude — see CLAUDE.md and QUM-518). The path
// must exist; otherwise an error is returned. When $SPRAWL_CLAUDE is unset
// or empty, falls back to PATH lookup.
func (r *RealLauncher) FindBinary() (string, error) {
	if override := os.Getenv("SPRAWL_CLAUDE"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("SPRAWL_CLAUDE=%q: %w", override, err)
		}
		return override, nil
	}
	return exec.LookPath("claude")
}
