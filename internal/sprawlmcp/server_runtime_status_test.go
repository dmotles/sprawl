package sprawlmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/buildinfo"
	"github.com/dmotles/sprawl/internal/supervisor"
)

func fixedNow() time.Time { return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC) }

func cleanImage() buildinfo.ImageStatus {
	return buildinfo.ImageStatus{
		ExePath:       "/home/coder/.local/bin/sprawl",
		ExeCheck:      "ok",
		RunningCommit: "abc123",
		OnDiskCommit:  "abc123",
		CommitCheck:   "match",
	}
}

func staleImage() buildinfo.ImageStatus {
	return buildinfo.ImageStatus{
		ExePath:       "/home/coder/.local/bin/sprawl",
		ExeCheck:      "deleted",
		RunningCommit: "abc123",
		OnDiskCommit:  "def456",
		CommitCheck:   "differ",
		Stale:         true,
		Detail:        "running image was replaced on disk; restart sprawl",
	}
}

func statusPayloadOf(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("unmarshal status: %v\n%s", err, out)
	}
	return m
}

func TestToolStatus_ReturnsObjectWithRuntimeAndAgents(t *testing.T) {
	mock := &mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "ratz", Type: "engineer"}}}
	srv := New(mock).withImageFn(cleanImage).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	m := statusPayloadOf(t, out)
	if _, ok := m["runtime"]; !ok {
		t.Errorf("status payload has no runtime block\n%s", out)
	}
	agents, ok := m["agents"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("agents = %v, want 1 entry\n%s", m["agents"], out)
	}
}

// The zero-agent case is exactly when nobody is watching. The runtime verdict
// must still be emitted rather than collapsing to a bare sentence.
func TestToolStatus_NoAgentsStillEmitsRuntime(t *testing.T) {
	srv := New(&mockSupervisor{}).withImageFn(staleImage).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	m := statusPayloadOf(t, out)
	rt, ok := m["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("no runtime block with zero agents\n%s", out)
	}
	if rt["stale"] != true {
		t.Errorf("runtime.stale = %v, want true\n%s", rt["stale"], out)
	}
	if m["note"] == nil || !strings.Contains(m["note"].(string), "No agents") {
		t.Errorf("note = %v, want the no-agents sentence retained\n%s", m["note"], out)
	}
}

// Wiring test, not a control: it establishes that a clean verdict reaches the
// wire without acquiring a warning. The real negative control (subject known
// clean, probe must stay quiet) lives in internal/buildinfo, on classifyImage
// and on Image() against this process.
func TestToolStatus_CleanRuntimeStaysQuiet(t *testing.T) {
	srv := New(&mockSupervisor{}).withImageFn(cleanImage).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	rt := statusPayloadOf(t, out)["runtime"].(map[string]any)
	if rt["stale"] != false {
		t.Errorf("runtime.stale = %v, want false\n%s", rt["stale"], out)
	}
	if _, ok := rt["detail"]; ok {
		t.Errorf("runtime.detail present on a clean image: %v\n%s", rt["detail"], out)
	}
}

// Wiring test, not a control: it establishes that a stale verdict survives to
// the wire with both commits and the detail intact. The real positive control
// (a genuinely deleted running image) lives in internal/buildinfo.
func TestToolStatus_StaleRuntimeIsLoud(t *testing.T) {
	srv := New(&mockSupervisor{}).withImageFn(staleImage).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	rt := statusPayloadOf(t, out)["runtime"].(map[string]any)
	if rt["stale"] != true {
		t.Errorf("runtime.stale = %v, want true\n%s", rt["stale"], out)
	}
	if rt["exe_check"] != "deleted" {
		t.Errorf("runtime.exe_check = %v, want deleted\n%s", rt["exe_check"], out)
	}
	if rt["detail"] == "" || rt["detail"] == nil {
		t.Errorf("runtime.detail empty on a stale image\n%s", out)
	}
	if rt["running_commit"] != "abc123" || rt["on_disk_commit"] != "def456" {
		t.Errorf("both commits must be visible: running=%v on_disk=%v\n%s",
			rt["running_commit"], rt["on_disk_commit"], out)
	}
}

// Degrading loudly is the whole thesis: a check that could not run must say so
// on the wire, not vanish. An absent runtime block reads as "fine".
func TestToolStatus_UnavailableCheckIsStillReported(t *testing.T) {
	unavailable := func() buildinfo.ImageStatus {
		return buildinfo.ImageStatus{
			ExeCheck:      "unavailable",
			RunningCommit: "abc123",
			CommitCheck:   "unknown",
			Detail:        "cannot read /proc/self/exe: permission denied",
		}
	}
	srv := New(&mockSupervisor{}).withImageFn(unavailable).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	rt, ok := statusPayloadOf(t, out)["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime block omitted when the check could not run\n%s", out)
	}
	if rt["exe_check"] != "unavailable" {
		t.Errorf("runtime.exe_check = %v, want unavailable\n%s", rt["exe_check"], out)
	}
	if _, has := rt["stale"]; !has {
		t.Errorf("runtime.stale key absent; absence reads as fine\n%s", out)
	}
	if d, _ := rt["detail"].(string); !strings.Contains(d, "permission denied") {
		t.Errorf("runtime.detail = %q, want the reason named\n%s", d, out)
	}
}

func TestToolStatus_LastActivityAge(t *testing.T) {
	mock := &mockSupervisor{statusResult: []supervisor.AgentInfo{
		{Name: "ratz", Type: "engineer", LastActivityAt: fixedNow().Add(-15 * time.Hour)},
		{Name: "quiet", Type: "engineer"},
	}}
	srv := New(mock).withImageFn(cleanImage).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	agents := statusPayloadOf(t, out)["agents"].([]any)
	a0 := agents[0].(map[string]any)
	if a0["last_activity_age"] != "15h ago" {
		t.Errorf("last_activity_age = %v, want %q\n%s", a0["last_activity_age"], "15h ago", out)
	}
	if _, ok := a0["last_activity_at"]; !ok {
		t.Errorf("last_activity_at dropped; the age augments the timestamp, not replaces it\n%s", out)
	}
	a1 := agents[1].(map[string]any)
	if _, ok := a1["last_activity_age"]; ok {
		t.Errorf("agent with no activity got an age: %v\n%s", a1["last_activity_age"], out)
	}
}

func TestToolPeek_LastReportAge(t *testing.T) {
	mock := &mockSupervisor{peekResult: &supervisor.PeekResult{
		Status:     "active",
		LastReport: supervisor.LastReport{State: "working", At: fixedNow().Add(-15 * time.Hour).Format(time.RFC3339)},
	}}
	srv := New(mock).withNowFn(fixedNow)
	out, err := srv.toolPeek(context.Background(), json.RawMessage(`{"agent":"ratz"}`))
	if err != nil {
		t.Fatalf("toolPeek: %v", err)
	}
	m := statusPayloadOf(t, out)
	lr, ok := m["last_report"].(map[string]any)
	if !ok {
		t.Fatalf("no last_report block\n%s", out)
	}
	if lr["age"] != "15h ago" {
		t.Errorf("last_report.age = %v, want %q\n%s", lr["age"], "15h ago", out)
	}
}

// The age must come from the NEWEST timestamp, computed — not from whichever
// slot the fixture happens to put last. Both orderings are asserted so an
// implementation indexing len-1 cannot pass.
func TestToolPeek_LastActivityAgeFromNewestEntry(t *testing.T) {
	older := agentloop.ActivityEntry{TS: fixedNow().Add(-9 * time.Hour), Kind: "system", Summary: "older"}
	newest := agentloop.ActivityEntry{TS: fixedNow().Add(-2 * time.Hour), Kind: "system", Summary: "newest"}
	for name, entries := range map[string][]agentloop.ActivityEntry{
		"chronological": {older, newest},
		"reversed":      {newest, older},
	} {
		mock := &mockSupervisor{peekResult: &supervisor.PeekResult{Status: "active", Activity: entries}}
		srv := New(mock).withNowFn(fixedNow)
		out, err := srv.toolPeek(context.Background(), json.RawMessage(`{"agent":"ratz"}`))
		if err != nil {
			t.Fatalf("%s: toolPeek: %v", name, err)
		}
		m := statusPayloadOf(t, out)
		if m["last_activity_age"] != "2h ago" {
			t.Errorf("%s: last_activity_age = %v, want %q (newest entry)\n%s", name, m["last_activity_age"], "2h ago", out)
		}
	}
}

// A timestamp we cannot parse must say so. Silently emitting an age derived
// from the zero time would render "489000h ago" and read as data.
func TestToolPeek_UnparseableReportAtDegradesLoudly(t *testing.T) {
	mock := &mockSupervisor{peekResult: &supervisor.PeekResult{
		Status:     "active",
		LastReport: supervisor.LastReport{State: "working", At: "garbage"},
	}}
	srv := New(mock).withNowFn(fixedNow)
	out, err := srv.toolPeek(context.Background(), json.RawMessage(`{"agent":"ratz"}`))
	if err != nil {
		t.Fatalf("toolPeek: %v", err)
	}
	lr := statusPayloadOf(t, out)["last_report"].(map[string]any)
	if lr["age"] != "unknown" {
		t.Errorf("last_report.age = %v, want %q for an unparseable timestamp\n%s", lr["age"], "unknown", out)
	}
}

// Never-reported is a real empty, not a parse failure — do not label it.
func TestToolPeek_NoReportHasNoAge(t *testing.T) {
	mock := &mockSupervisor{peekResult: &supervisor.PeekResult{Status: "active"}}
	srv := New(mock).withNowFn(fixedNow)
	out, err := srv.toolPeek(context.Background(), json.RawMessage(`{"agent":"ratz"}`))
	if err != nil {
		t.Fatalf("toolPeek: %v", err)
	}
	m := statusPayloadOf(t, out)
	if lr, ok := m["last_report"].(map[string]any); ok {
		if _, has := lr["age"]; has {
			t.Errorf("last_report.age present with no report: %v\n%s", lr["age"], out)
		}
	}
	if _, has := m["last_activity_age"]; has {
		t.Errorf("last_activity_age present with no activity\n%s", out)
	}
}

// The default (production) server must read the real process image, not a
// zero value: New() without seams still produces a verdict.
func TestNew_DefaultsToRealImageCheck(t *testing.T) {
	srv := New(&mockSupervisor{})
	got := srv.image()
	if got.ExeCheck == "" {
		t.Errorf("New() server produced no exe_check verdict: %+v", got)
	}
	if got.ExePath == "" {
		t.Errorf("New() server produced no exe_path: %+v", got)
	}
}
