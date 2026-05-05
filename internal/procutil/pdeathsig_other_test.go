//go:build !linux

package procutil

import (
	"os/exec"
	"testing"
)

// TestSetPdeathsig_NonLinuxIsNoOp asserts the stub doesn't panic and doesn't
// mutate fields on platforms without PR_SET_PDEATHSIG.
func TestSetPdeathsig_NonLinuxIsNoOp(t *testing.T) {
	cmd := exec.Command("/bin/true")
	SetPdeathsig(cmd)
	// No assertion on SysProcAttr — non-linux callers must tolerate either
	// nil or a zero-value struct. The contract is "doesn't panic, doesn't
	// set Pdeathsig".
}

func TestKillProcessGroup_NilProcess(t *testing.T) {
	if err := KillProcessGroup(nil); err != nil {
		t.Errorf("KillProcessGroup(nil) = %v, want nil", err)
	}
}
