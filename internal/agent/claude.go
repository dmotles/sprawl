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
		// QUM-1223: G703 flags os.Getenv -> os.Stat as tainted-path traversal.
		// There is no confinement boundary to escape: SPRAWL_CLAUDE IS the
		// whole path, by design and by the doc comment above, and it is never
		// joined onto a root.
		//
		// Stated plainly rather than understated: this stat is a PRE-FLIGHT
		// EXISTENCE CHECK on a path that the caller then EXECUTES (the return
		// value becomes argv[0]). Executing an operator-named binary is a
		// larger thing than statting one, and it is the accepted, documented
		// design of the QUM-518 auth shim — not something this suppression
		// waves through. That is exec-argument territory (G204), not G703, and
		// it is unaffected by this directive.
		//
		// The taint source is at the process's own privilege: anyone who can
		// set SPRAWL_CLAUDE in sprawl's environment can already run any binary
		// directly, so there is no privilege boundary for a "../" to cross.
		//#nosec G703 -- SPRAWL_CLAUDE is the operator-supplied path to the binary sprawl will exec (QUM-518 shim); the variable IS the path, so there is no base dir to confine it to
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("SPRAWL_CLAUDE=%q: %w", override, err)
		}
		return override, nil
	}
	return exec.LookPath("claude")
}
