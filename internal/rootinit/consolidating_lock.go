package rootinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// lockfileMu serializes read-modify-write access to the consolidation
// lockfile body across the heartbeat ticker and pipeline phase setters
// running in the same process. Without it, concurrent updateHeartbeat
// calls can lose phase transitions (heartbeat clobbering setPhase) or
// regress the phase label.
var lockfileMu sync.Mutex

// QUM-522: structured JSON-bodied .consolidating lockfile. The body records
// the holder PID, a human-readable phase label, the start time, and the most
// recent heartbeat. A janitor on weave startup uses these fields to recover
// from crashed consolidations whose flock was orphaned without removing the
// lockfile.

// Tunable bounds. The constants are public so other packages / tests can
// document the contract; the `var` shadows are internal so tests can shrink
// the heartbeat / per-phase timeout for fast unit tests.
const (
	HeartbeatInterval            = 10 * time.Second
	StaleLockThreshold           = 5 * time.Minute
	PerPhaseConsolidationTimeout = 5 * time.Minute
)

// Package-private seam vars: tests override these to exercise heartbeat and
// per-phase-timeout behaviour quickly.
var (
	heartbeatInterval = HeartbeatInterval
	perPhaseTimeout   = PerPhaseConsolidationTimeout
)

// lockState is the JSON body of the consolidation lockfile.
type lockState struct {
	PID           int       `json:"pid"`
	Phase         string    `json:"phase"`
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// readLockState reads and decodes the lockfile body. To preserve backward
// compatibility with the legacy plain-PID body (e.g. lockfiles written by
// previous sprawl versions before QUM-522), an integer-only body parses to a
// lockState with PID set and zero values for all other fields.
func readLockState(path string) (*lockState, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from sprawl-internal config
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	// Empty body — treat as garbage so callers can warn / leave alone.
	if trimmed == "" {
		return nil, errors.New("empty lockfile body")
	}
	// Try JSON first.
	if trimmed[0] == '{' {
		var s lockState
		if err := json.Unmarshal([]byte(trimmed), &s); err != nil {
			return nil, fmt.Errorf("decoding lockfile JSON: %w", err)
		}
		return &s, nil
	}
	// Legacy plain-PID body.
	if pid, perr := strconv.Atoi(trimmed); perr == nil {
		return &lockState{PID: pid}, nil
	}
	return nil, fmt.Errorf("lockfile body is neither JSON nor a decimal PID: %q", trimmed)
}

// writeLockState serializes s as JSON and writes it to path with 0o644.
func writeLockState(path string, s *lockState) error {
	body, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshalling lockfile state: %w", err)
	}
	return os.WriteFile(path, body, 0o644) //nolint:gosec // matches adjacent memory files
}

// updateHeartbeat reads the current lockfile state, sets LastHeartbeat to
// `now`, optionally updates Phase (if non-empty), and writes the result back.
// PID and StartedAt are preserved.
func updateHeartbeat(path string, phase string, now time.Time) error {
	lockfileMu.Lock()
	defer lockfileMu.Unlock()
	s, err := readLockState(path)
	if err != nil {
		return err
	}
	if phase != "" {
		s.Phase = phase
	}
	s.LastHeartbeat = now
	return writeLockState(path, s)
}

// pidAlive returns true if a process with the given PID is currently alive.
// On Linux/Unix we send signal 0; ESRCH means "no such process" (dead),
// EPERM means "exists but we lack permission" (alive).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}

// JanitorStaleLock inspects the consolidation lockfile and removes it iff:
//   - the body parses as a structured (post-QUM-522) JSON lockState,
//   - the recorded LastHeartbeat is older than StaleLockThreshold, AND
//   - the recorded PID is no longer alive.
//
// Legacy plain-PID lockfiles have no heartbeat to evaluate; the janitor
// leaves them alone (with a warning). Non-existent lockfiles are a no-op.
// Errors reading/parsing the lockfile are warned to stderr but never
// removed — better to leave a confusing-but-recoverable lockfile than
// to silently drop a live one.
func JanitorStaleLock(sprawlRoot string, stderr io.Writer, now func() time.Time) (bool, error) {
	path := consolidatingLockPath(sprawlRoot)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "[weave] warning: stat consolidation lockfile: %v\n", err)
		}
		return false, nil
	}

	s, err := readLockState(path)
	if err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "[weave] warning: cannot parse consolidation lockfile %s; leaving alone: %v\n", path, err)
		}
		return false, nil
	}

	// Legacy plain-PID body: no heartbeat → cannot judge staleness.
	if s.LastHeartbeat.IsZero() {
		if stderr != nil {
			fmt.Fprintf(stderr, "[weave] warning: legacy consolidation lockfile (no heartbeat) at %s; leaving alone\n", path)
		}
		return false, nil
	}

	age := now().Sub(s.LastHeartbeat)
	if age <= StaleLockThreshold {
		return false, nil
	}
	if pidAlive(s.PID) {
		return false, nil
	}

	if err := os.Remove(path); err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "[weave] warning: removing stale consolidation lockfile %s: %v\n", path, err)
		}
		return false, nil
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "[weave] removed stale consolidation lockfile %s (pid %d dead, heartbeat age %s)\n",
			path, s.PID, age.Round(time.Second))
	}
	return true, nil
}
