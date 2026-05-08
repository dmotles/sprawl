package merge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// heartbeatInterval is how often a "validate still running" heartbeat is
// pushed to the sink while a streaming run is in progress. Package-level
// var so tests can override it.
var heartbeatInterval = 30 * time.Second

// killGracePeriod is how long we wait between SIGTERM and SIGKILL when
// cancelling the subprocess tree, and also how long we wait for scanners
// to drain after the leader exits before force-closing the pipe read ends
// to unstick scanners pinned by a backgrounded grandchild.
var killGracePeriod = 5 * time.Second

// RealRunTestsStreaming runs the validation command in dir, streaming
// stdout+stderr line-by-line to sink. It honors ctx (cancel sends SIGTERM
// to the entire subprocess group, then SIGKILL after killGracePeriod), sets
// Setpgid so backgrounded grandchildren cannot pin the parent on stdio, and
// emits a "[heartbeat] T+Ns last: …" line every heartbeatInterval.
//
// We own the pipes (os.Pipe), not cmd.StdoutPipe(), so cmd.Wait does not
// race with scanners on read-end closure. Scanners drain to EOF naturally
// when the leader (and any grandchildren holding the writer fds) close
// them. For the QUM-496 backgrounded-grandchild case, a watcher goroutine
// force-closes our read ends after the leader exits + a grace period.
//
// QUM-496.
func RealRunTestsStreaming(ctx context.Context, dir, command string, sink func(line string)) (string, error) {
	cmd := exec.Command("bash", "-c", command) //nolint:gosec // G204: command from project-level .sprawl/config.yaml, trusted like committed config
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rOut, wOut, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		_ = rOut.Close()
		_ = wOut.Close()
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout = wOut
	cmd.Stderr = wErr

	if err := cmd.Start(); err != nil {
		_ = rOut.Close()
		_ = wOut.Close()
		_ = rErr.Close()
		_ = wErr.Close()
		return "", fmt.Errorf("starting command: %w", err)
	}
	// Close parent's copies of the write ends so scanners observe EOF as
	// soon as the last subprocess fd is closed.
	_ = wOut.Close()
	_ = wErr.Close()

	var (
		mu       sync.Mutex
		output   strings.Builder
		lastLine string
	)
	pushLine := func(line string) {
		mu.Lock()
		output.WriteString(line)
		output.WriteByte('\n')
		lastLine = line
		mu.Unlock()
		if sink != nil {
			sink(line)
		}
	}
	getLast := func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastLine
	}

	var scanWG sync.WaitGroup
	scan := func(r io.Reader) {
		defer scanWG.Done()
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 64*1024), 1024*1024)
		for s.Scan() {
			pushLine(s.Text())
		}
	}
	scanWG.Add(2)
	go scan(rOut)
	go scan(rErr)

	scannersDone := make(chan struct{})
	go func() { scanWG.Wait(); close(scannersDone) }()

	// Heartbeat goroutine.
	hbDone := make(chan struct{})
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		start := time.Now()
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hbDone:
				return
			case <-t.C:
				if sink != nil {
					sink(fmt.Sprintf("[heartbeat] validate still running, T+%.0fs, last line: %s",
						time.Since(start).Seconds(), getLast()))
				}
			}
		}
	}()

	// Reap the leader. cmd.Wait does NOT block on our pipes (we own them);
	// it returns as soon as the process exits.
	procDone := make(chan struct{})
	procExitErr := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		close(procDone)
		procExitErr <- err
	}()

	var closeOnce sync.Once
	closePipes := func() {
		closeOnce.Do(func() {
			_ = rOut.Close()
			_ = rErr.Close()
		})
	}

	// Watcher: forwards ctx cancellation to the process group, and after
	// the leader exits, force-closes our read ends if scanners are still
	// pinned by a grandchild holding the writer fds.
	var watcherWG sync.WaitGroup
	var (
		ctxErrMu       sync.Mutex
		capturedCtxErr error
	)
	watcherWG.Add(1)
	go func() {
		defer watcherWG.Done()
		var ctxCh <-chan struct{}
		if ctx != nil {
			ctxCh = ctx.Done()
		}
		killed := false
		for {
			select {
			case <-procDone:
				select {
				case <-scannersDone:
				case <-time.After(killGracePeriod):
					closePipes()
				}
				return
			case <-ctxCh:
				if !killed && cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
					killed = true
					go func() {
						timer := time.NewTimer(killGracePeriod)
						defer timer.Stop()
						select {
						case <-procDone:
						case <-timer.C:
							if cmd.Process != nil {
								_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
							}
						}
					}()
				}
				ctxErrMu.Lock()
				if capturedCtxErr == nil {
					capturedCtxErr = ctx.Err()
				}
				ctxErrMu.Unlock()
				ctxCh = nil // disarm; don't busy-loop on a closed Done channel.
			}
		}
	}()

	// Block until both leader exit (procExitErr) and scanners drain.
	<-scannersDone
	waitErr := <-procExitErr
	close(hbDone)
	hbWG.Wait()
	watcherWG.Wait()
	closePipes()

	combined := output.String()

	ctxErrMu.Lock()
	ctxErr := capturedCtxErr
	ctxErrMu.Unlock()
	if ctxErr != nil {
		if waitErr != nil {
			return combined, fmt.Errorf("validate cancelled: %w (wait error: %s)", ctxErr, waitErr.Error())
		}
		return combined, ctxErr
	}
	if waitErr != nil {
		return combined, waitErr
	}
	return combined, nil
}
