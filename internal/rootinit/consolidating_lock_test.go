package rootinit

// QUM-522: tests for the JSON-bodied .consolidating lockfile, heartbeat
// updates, and stale-lock janitor. Production code lives in
// consolidating_lock.go (to be written by the implementer step). These
// tests are deliberately RED until that file exists.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow returns a closure that always reports the same instant. Used so
// janitor tests don't depend on wall-clock comparisons.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// writeRaw writes raw bytes to the lockfile path, creating the parent dir.
func writeRaw(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// lockPathInTempRoot mirrors consolidatingLockPath(sprawlRoot) so the tests
// exercise the same path the production code uses.
func lockPathInTempRoot(t *testing.T) (root, path string) {
	t.Helper()
	root = t.TempDir()
	path = consolidatingLockPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root, path
}

func TestLockState_RoundTrip(t *testing.T) {
	_, path := lockPathInTempRoot(t)
	started := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	hb := started.Add(30 * time.Second)
	want := &lockState{
		PID:           os.Getpid(),
		Phase:         "Consolidating timeline...",
		StartedAt:     started,
		LastHeartbeat: hb,
	}
	if err := writeLockState(path, want); err != nil {
		t.Fatalf("writeLockState: %v", err)
	}
	got, err := readLockState(path)
	if err != nil {
		t.Fatalf("readLockState: %v", err)
	}
	if got.PID != want.PID {
		t.Errorf("PID: got %d, want %d", got.PID, want.PID)
	}
	if got.Phase != want.Phase {
		t.Errorf("Phase: got %q, want %q", got.Phase, want.Phase)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt: got %s, want %s", got.StartedAt, want.StartedAt)
	}
	if !got.LastHeartbeat.Equal(want.LastHeartbeat) {
		t.Errorf("LastHeartbeat: got %s, want %s", got.LastHeartbeat, want.LastHeartbeat)
	}

	// Bytes on disk must be valid JSON with snake_case keys.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("on-disk body must be JSON, got %q (err: %v)", string(raw), err)
	}
	for _, key := range []string{"pid", "phase", "started_at", "last_heartbeat"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("expected JSON key %q on disk, got: %v", key, generic)
		}
	}
}

func TestReadLockState_LegacyPIDBody(t *testing.T) {
	_, path := lockPathInTempRoot(t)
	writeRaw(t, path, []byte("12345\n"))

	got, err := readLockState(path)
	if err != nil {
		t.Fatalf("readLockState should accept legacy plain-PID body, got err: %v", err)
	}
	if got.PID != 12345 {
		t.Errorf("PID: got %d, want 12345", got.PID)
	}
	if !got.LastHeartbeat.IsZero() {
		t.Errorf("LastHeartbeat must be zero for legacy body; got %s", got.LastHeartbeat)
	}
}

func TestReadLockState_GarbageBody(t *testing.T) {
	_, path := lockPathInTempRoot(t)
	writeRaw(t, path, []byte("not-json-not-pid\n"))

	if _, err := readLockState(path); err == nil {
		t.Fatal("expected error for non-JSON, non-numeric body")
	}
}

func TestJanitorStaleLock_RemovesDeadStale(t *testing.T) {
	root, path := lockPathInTempRoot(t)
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	state := &lockState{
		PID:           999999, // almost-certainly-dead PID
		Phase:         "Updating persistent knowledge...",
		StartedAt:     now.Add(-15 * time.Minute),
		LastHeartbeat: now.Add(-10 * time.Minute),
	}
	if err := writeLockState(path, state); err != nil {
		t.Fatalf("writeLockState: %v", err)
	}

	var stderr strings.Builder
	removed, err := JanitorStaleLock(root, &stderr, fixedNow(now))
	if err != nil {
		t.Fatalf("JanitorStaleLock: %v", err)
	}
	if !removed {
		t.Error("expected removed=true for stale dead-PID lock")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected lockfile removed; stat returned %v", err)
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "stale") &&
		!strings.Contains(strings.ToLower(stderr.String()), "remov") {
		t.Errorf("expected stderr warning about stale removal; got %q", stderr.String())
	}
}

func TestJanitorStaleLock_LeavesLiveLock(t *testing.T) {
	root, path := lockPathInTempRoot(t)
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	state := &lockState{
		PID:           os.Getpid(), // alive — we're running
		Phase:         "Consolidating timeline...",
		StartedAt:     now.Add(-15 * time.Minute),
		LastHeartbeat: now.Add(-10 * time.Minute),
	}
	if err := writeLockState(path, state); err != nil {
		t.Fatalf("writeLockState: %v", err)
	}

	removed, err := JanitorStaleLock(root, nil, fixedNow(now))
	if err != nil {
		t.Fatalf("JanitorStaleLock: %v", err)
	}
	if removed {
		t.Error("expected removed=false when PID is alive")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected lockfile to remain; stat err %v", err)
	}
}

func TestJanitorStaleLock_LeavesFreshLock(t *testing.T) {
	root, path := lockPathInTempRoot(t)
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	state := &lockState{
		PID:           999999, // dead
		Phase:         "Consolidating timeline...",
		StartedAt:     now.Add(-1 * time.Minute),
		LastHeartbeat: now.Add(-30 * time.Second), // fresh
	}
	if err := writeLockState(path, state); err != nil {
		t.Fatalf("writeLockState: %v", err)
	}

	removed, err := JanitorStaleLock(root, nil, fixedNow(now))
	if err != nil {
		t.Fatalf("JanitorStaleLock: %v", err)
	}
	if removed {
		t.Error("expected removed=false when heartbeat is fresh, even with dead PID")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected lockfile to remain; stat err %v", err)
	}
}

func TestJanitorStaleLock_NoLockfile(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	removed, err := JanitorStaleLock(root, nil, fixedNow(now))
	if err != nil {
		t.Fatalf("JanitorStaleLock: %v", err)
	}
	if removed {
		t.Error("expected removed=false when lockfile does not exist")
	}
}

func TestJanitorStaleLock_LegacyBody_LeavesAlone(t *testing.T) {
	root, path := lockPathInTempRoot(t)
	// Legacy plain-PID body — heartbeat is unknown so the janitor cannot
	// safely judge staleness. Leave it alone, but warn.
	writeRaw(t, path, []byte("999999\n"))
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	var stderr strings.Builder
	removed, err := JanitorStaleLock(root, &stderr, fixedNow(now))
	if err != nil {
		t.Fatalf("JanitorStaleLock: %v", err)
	}
	if removed {
		t.Error("expected removed=false for legacy body (heartbeat unknown)")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected lockfile to remain; stat err %v", err)
	}
	if stderr.Len() == 0 {
		t.Error("expected a warning to stderr for legacy body")
	}
	warn := strings.ToLower(stderr.String())
	if !strings.Contains(warn, "legacy") && !strings.Contains(warn, "heartbeat") {
		t.Errorf("expected warning to mention 'legacy' or 'heartbeat' (case-insensitive); got %q", stderr.String())
	}
}

func TestUpdateHeartbeat_PreservesOtherFields(t *testing.T) {
	_, path := lockPathInTempRoot(t)
	started := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	original := &lockState{
		PID:           os.Getpid(),
		Phase:         "starting",
		StartedAt:     started,
		LastHeartbeat: started,
	}
	if err := writeLockState(path, original); err != nil {
		t.Fatalf("writeLockState: %v", err)
	}

	hbTime := started.Add(45 * time.Second)
	if err := updateHeartbeat(path, "Consolidating timeline...", hbTime); err != nil {
		t.Fatalf("updateHeartbeat: %v", err)
	}

	got, err := readLockState(path)
	if err != nil {
		t.Fatalf("readLockState: %v", err)
	}
	if got.PID != original.PID {
		t.Errorf("PID changed: got %d, want %d", got.PID, original.PID)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt changed: got %s, want %s", got.StartedAt, started)
	}
	if got.Phase != "Consolidating timeline..." {
		t.Errorf("Phase: got %q, want \"Consolidating timeline...\"", got.Phase)
	}
	if !got.LastHeartbeat.Equal(hbTime) {
		t.Errorf("LastHeartbeat: got %s, want %s", got.LastHeartbeat, hbTime)
	}
}

// pidAlive sanity check: the current process must register as alive.
func TestPIDAlive_SelfReturnsTrue(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("pidAlive(self) must return true")
	}
}

// And a definitely-dead PID must be reported as dead. PID 999999 is well
// above any normal Linux pid_max but we tolerate the very rare flake by
// only asserting the call returns (not panics) and produces a bool — the
// stale-lock tests rely on the dead-PID semantics covered there.
func TestPIDAlive_LikelyDeadDoesNotPanic(t *testing.T) {
	_ = pidAlive(999999)
}

// TestStaleLockThresholdConstants documents the public bounds. If the
// implementer changes them, these assertions force re-considering the
// downstream stale-detection tests.
func TestStaleLockThresholdConstants(t *testing.T) {
	if HeartbeatInterval <= 0 {
		t.Errorf("HeartbeatInterval must be > 0; got %s", HeartbeatInterval)
	}
	if StaleLockThreshold <= HeartbeatInterval {
		t.Errorf("StaleLockThreshold (%s) must exceed HeartbeatInterval (%s)",
			StaleLockThreshold, HeartbeatInterval)
	}
	if PerPhaseConsolidationTimeout <= 0 {
		t.Errorf("PerPhaseConsolidationTimeout must be > 0; got %s", PerPhaseConsolidationTimeout)
	}
}
