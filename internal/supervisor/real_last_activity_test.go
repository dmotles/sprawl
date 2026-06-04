package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeLastActivityHandle is a RuntimeHandle that additionally exposes
// LastActivityAt() time.Time. Mirrors fakeInAutonomousTurnHandle. Used to
// exercise the QUM-665 runtime-wins path in Real.Status.
type fakeLastActivityHandle struct {
	caps         backendpkg.Capabilities
	sessionID    string
	autonomy     bool
	lastActivity time.Time
	doneCh       chan struct{}
}

func (h *fakeLastActivityHandle) Interrupt(context.Context) error       { return nil }
func (h *fakeLastActivityHandle) Wake() error                           { return nil }
func (h *fakeLastActivityHandle) WakeForDelivery() error                { return nil }
func (h *fakeLastActivityHandle) ForceInterruptDelivery() error         { return nil }
func (h *fakeLastActivityHandle) Stop(context.Context) error            { return nil }
func (h *fakeLastActivityHandle) StopAbandon(context.Context) error     { return nil }
func (h *fakeLastActivityHandle) SessionID() string                     { return h.sessionID }
func (h *fakeLastActivityHandle) Capabilities() backendpkg.Capabilities { return h.caps }
func (h *fakeLastActivityHandle) Done() <-chan struct{}                 { return h.doneCh }
func (h *fakeLastActivityHandle) InAutonomousTurn() bool                { return h.autonomy }
func (h *fakeLastActivityHandle) LastActivityAt() time.Time             { return h.lastActivity }

// QUM-665: when no runtime is registered for an agent, Real.Status must
// populate LastActivityAt by reading the tail of the agent's activity.ndjson
// from disk. This is the path that lets a recovered/replayed view still see
// "this agent was recently active" without an in-process runtime.
func TestReal_Status_LastActivityAt_FromDisk_NoRuntime(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "diskonly",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})

	// Write a single ndjson activity entry on disk.
	activityDir := filepath.Join(tmpDir, ".sprawl", "agents", "diskonly")
	if err := os.MkdirAll(activityDir, 0o755); err != nil {
		t.Fatalf("mkdir activity dir: %v", err)
	}
	wantTS := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	entry := map[string]any{
		"ts":      wantTS.Format(time.RFC3339Nano),
		"kind":    "tool_use",
		"tool":    "Bash",
		"summary": "test",
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	activityFile := filepath.Join(activityDir, "activity.ndjson")
	if err := os.WriteFile(activityFile, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write activity file: %v", err)
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got *AgentInfo
	for i := range agents {
		if agents[i].Name == "diskonly" {
			got = &agents[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("diskonly not in Status() output")
	}
	if got.LastActivityAt.IsZero() {
		t.Fatalf("LastActivityAt = zero, want %v (read from disk activity.ndjson)", wantTS)
	}
	if !got.LastActivityAt.Equal(wantTS) {
		t.Errorf("LastActivityAt = %v, want %v", got.LastActivityAt, wantTS)
	}
}

// QUM-665: when a runtime IS registered for an agent and its handle exposes
// LastActivityAt(), that value (not the disk file) feeds AgentInfo. Locks
// the "runtime wins over disk" precedence.
func TestReal_Status_LastActivityAt_FromRuntime(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})

	wantTS := time.Date(2026, 6, 3, 12, 30, 0, 0, time.UTC)
	h := &fakeLastActivityHandle{
		caps:         backendpkg.Capabilities{SupportsInterrupt: true},
		sessionID:    "sess-ratz",
		autonomy:     false,
		lastActivity: wantTS,
	}
	if _, err := sup.RegisterRootRuntime("ratz", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got *AgentInfo
	for i := range agents {
		if agents[i].Name == "ratz" {
			got = &agents[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("ratz not in Status() output")
	}
	if !got.LastActivityAt.Equal(wantTS) {
		t.Errorf("LastActivityAt = %v, want %v (runtime should win)", got.LastActivityAt, wantTS)
	}
}

// QUM-665: AgentInfo.InAutonomousTurn must be populated from the runtime's
// InAutonomousTurn() probe in Real.Status. Without this, the tree icon
// derivation has no way to see autonomy.
func TestReal_Status_InAutonomousTurn_FromRuntime(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "auto",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})

	h := &fakeInAutonomousTurnHandle{
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		sessionID: "sess-auto",
		autonomy:  true,
	}
	if _, err := sup.RegisterRootRuntime("auto", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got *AgentInfo
	for i := range agents {
		if agents[i].Name == "auto" {
			got = &agents[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("auto not in Status() output")
	}
	if !got.InAutonomousTurn {
		t.Errorf("AgentInfo.InAutonomousTurn = false, want true (handle reports autonomy)")
	}
}
