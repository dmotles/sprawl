//go:build linux

package procutil

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestSetPdeathsig_SetsSigkillAndPgid asserts that SetPdeathsig wires
// PR_SET_PDEATHSIG=SIGKILL and Setpgid=true onto the cmd's SysProcAttr.
// QUM-458 layer 2.
func TestSetPdeathsig_SetsSigkillAndPgid(t *testing.T) {
	cmd := exec.Command("/bin/true")
	SetPdeathsig(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatalf("SetPdeathsig did not initialize SysProcAttr")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Errorf("Setpgid = false, want true (process group ownership required for KillProcessGroup)")
	}
}

// TestSetPdeathsig_OverwritesIsAcceptable: callers should not set
// SysProcAttr before calling SetPdeathsig. Document that behavior with a
// test asserting that calling it on a fresh cmd yields a populated struct.
func TestSetPdeathsig_OnFreshCmdYieldsPopulatedSysProcAttr(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if cmd.SysProcAttr != nil {
		t.Fatalf("precondition: fresh exec.Command should have nil SysProcAttr")
	}
	SetPdeathsig(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatalf("SetPdeathsig must allocate SysProcAttr")
	}
}

// TestKillProcessGroup_KillsWholeGroup spawns a bash that backgrounds two
// sleeps in its own process group, then calls KillProcessGroup on the bash
// leader and asserts ALL three pids (leader + two sleeps) are gone within 2s.
func TestKillProcessGroup_KillsWholeGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses; skipped in -short mode")
	}

	// Print leader pid + two child pids on stdout, then wait. The children
	// are NOT shell builtins — they must inherit the same pgid as bash, which
	// they do because we set Setpgid=true on the bash exec.
	script := `echo $$; sleep 30 & echo $!; sleep 30 & echo $!; wait`
	cmd := exec.Command("bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Read the three pids.
	buf := make([]byte, 256)
	n, _ := readWithTimeout(stdout, buf, 3*time.Second)
	if n == 0 {
		_ = cmd.Process.Kill()
		t.Fatalf("no pids read from bash subprocess")
	}
	pids, err := parsePids(string(buf[:n]))
	if err != nil || len(pids) < 3 {
		_ = cmd.Process.Kill()
		t.Fatalf("expected 3 pids, got %v (err %v)", pids, err)
	}

	if err := KillProcessGroup(cmd.Process); err != nil {
		t.Fatalf("KillProcessGroup: %v", err)
	}

	// KillProcessGroup no longer reaps the leader — that's the caller's job
	// (in production, agentloop's waitFn / cmd.Wait()). Reap it here so the
	// "still alive" check below doesn't trip on a zombie /proc entry.
	go func() { _ = cmd.Wait() }()

	// Wait up to 2s for all three pids to disappear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive := 0
		for _, pid := range pids {
			if procExists(pid) {
				alive++
			}
		}
		if alive == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, pid := range pids {
		if procExists(pid) {
			t.Errorf("pid %d still alive after KillProcessGroup", pid)
		}
	}
}

func procExists(pid int) bool {
	if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err == nil {
		// Also check it's not a zombie that's already been reaped logically.
		// /proc/<pid> existence is a sufficient proxy for "kernel still tracks it".
		return true
	}
	return false
}

func parsePids(s string) ([]int, error) {
	var pids []int
	curr := ""
	for _, r := range s {
		if r >= '0' && r <= '9' {
			curr += string(r)
		} else if curr != "" {
			n, err := strconv.Atoi(curr)
			if err != nil {
				return nil, err
			}
			pids = append(pids, n)
			curr = ""
		}
	}
	if curr != "" {
		n, err := strconv.Atoi(curr)
		if err != nil {
			return nil, err
		}
		pids = append(pids, n)
	}
	return pids, nil
}

// readWithTimeout is a small helper to read from a pipe with a deadline,
// avoiding the test hanging if the bash never prints.
func readWithTimeout(r interface{ Read([]byte) (int, error) }, buf []byte, d time.Duration) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		// Loop reading until we have at least 3 newlines or the deadline hits.
		total := 0
		for total < len(buf) {
			n, err := r.Read(buf[total:])
			total += n
			if err != nil {
				ch <- result{total, err}
				return
			}
			if countNewlines(buf[:total]) >= 3 {
				ch <- result{total, nil}
				return
			}
		}
		ch <- result{total, nil}
	}()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-time.After(d):
		return 0, nil
	}
}

func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
