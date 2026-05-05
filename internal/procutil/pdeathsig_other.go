//go:build !linux

package procutil

import (
	"os"
	"os/exec"
)

// SetPdeathsig is a no-op on non-Linux platforms. Pdeathsig is a Linux-only
// kernel feature (PR_SET_PDEATHSIG); other platforms have no equivalent.
func SetPdeathsig(cmd *exec.Cmd) {
	_ = cmd
}

// KillProcessGroup falls back to a single-process kill on non-Linux.
func KillProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
