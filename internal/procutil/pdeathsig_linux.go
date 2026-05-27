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
	"strconv"
	"strings"
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

// KillChildProcessGroups SIGKILLs the process group of every direct child of
// the current process. QUM-636: a shutdown watchdog force-exit (os.Exit) does
// NOT kill child processes, so the claude subprocess would orphan to init
// (ppid=1) and leak. Calling this before os.Exit reaps those subprocesses.
// Each child started via SetPdeathsig is its own process-group leader, so the
// negative-pid group kill reaps it together with any of its descendants; the
// single-pid kill is a fallback for children not started in their own group.
// Best-effort: errors (already-gone, no-such-group) are ignored.
func KillChildProcessGroups() {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if readProcPPID(pid) != self {
			continue
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// readProcPPID returns the parent PID of pid from /proc/<pid>/stat, or 0 if it
// cannot be read. The comm field (2nd) is parenthesized and may contain spaces
// or ')', so we parse the fields after the LAST ')': they are
// `state ppid pgrp ...`, making ppid the 2nd whitespace-separated field.
func readProcPPID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(data)
	rp := strings.LastIndexByte(s, ')')
	if rp < 0 || rp+1 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[rp+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
