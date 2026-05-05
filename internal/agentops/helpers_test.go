package agentops

import (
	"runtime"
	"syscall"
	"testing"
)

// TestRunBashScript_SetsPdeathsig guards QUM-458 layer 2 for the bash
// subprocess used by spawn-time hook scripts: it must inherit a parent-death
// SIGKILL so that a SIGKILL'd sprawl host doesn't leave bash hooks running.
//
// Red phase: buildBashScriptCmd is a stub without Pdeathsig wiring; this test
// fails until procutil.SetPdeathsig is invoked.
func TestRunBashScript_SetsPdeathsig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig is Linux-only")
	}
	cmd := buildBashScriptCmd("true", t.TempDir(), nil)
	if cmd.SysProcAttr == nil {
		t.Fatalf("buildBashScriptCmd must set SysProcAttr (QUM-458 Pdeathsig wiring missing)")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
}
