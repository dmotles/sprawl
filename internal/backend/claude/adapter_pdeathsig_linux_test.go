//go:build linux

// QUM-896: regression guard for the QUM-458 Layer-2 parent-death protection
// that was lost in refactor commit 6683edf. The claude subprocess launcher
// MUST set Pdeathsig=SIGKILL (child dies if its sprawl host dies) and
// Setpgid=true (child leads its own process group so teardown can reap
// claude's descendants via KillProcessGroup). Asserts the SysProcAttr fields
// directly on the built command so no real subprocess is spawned. Pdeathsig
// is a Linux-only field, hence the build tag.

package claude

import (
	"syscall"
	"testing"
)

func TestNewStartCommand_SetsPdeathsig(t *testing.T) {
	cmd := newStartCommand(ExecSpec{Path: "claude", Args: []string{"claude"}})
	if cmd.SysProcAttr == nil {
		t.Fatal("newStartCommand: SysProcAttr is nil, want Pdeathsig/Setpgid configured")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("SysProcAttr.Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("SysProcAttr.Setpgid = false, want true")
	}
}
