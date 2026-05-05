//go:build linux

// Package procutil provides cross-platform helpers for managing subprocess
// lifetimes — in particular Linux PR_SET_PDEATHSIG and process-group kills.
//
// QUM-458: claude subprocesses spawned by sprawl enter were leaking when their
// parent host died via SIGKILL because Pdeathsig was never set. This package
// centralizes that wiring so every long-lived subprocess gets the same
// parent-death contract.
package procutil

import (
	"os"
	"os/exec"
	"syscall"
)

// SetPdeathsig configures cmd to receive SIGKILL when its parent process
// dies and to start in its own process group. Linux-only; the !linux build
// tag stub is a no-op.
//
// This must be called BEFORE cmd.Start(). If cmd.SysProcAttr is already
// non-nil, the existing fields are preserved and Pdeathsig/Setpgid merged in.
func SetPdeathsig(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	cmd.SysProcAttr.Setpgid = true
}

// KillProcessGroup sends SIGKILL to the entire process group led by p,
// falling back to a single-process kill if the group kill fails with an
// error other than ESRCH.
// Returns nil on success or if the process is already gone.
func KillProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if err != nil && err != syscall.ESRCH {
		// Fall back to single-process kill.
		_ = p.Kill()
	}
	// Reaping is the responsibility of whoever holds *os.Process for
	// cmd.Wait() (e.g. agentloop's waitFn). Descendants are reaped by their
	// subreaper / init. Doing a Wait4 here would race with that caller.
	return nil
}
