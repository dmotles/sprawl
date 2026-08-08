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
	// The absent age must not sit next to a bogus timestamp: omitempty does
	// NOT elide a zero time.Time, so a never-active agent used to emit
	// "0001-01-01T00:00:00Z", which reads as data.
	if ts, ok := a1["last_activity_at"]; ok {
		t.Errorf("never-active agent emitted last_activity_at = %v, want the field absent\n%s", ts, out)
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

// peek must reach the same verdict as status on a zero timestamp. Sharing
// only the AGE left the two surfaces disagreeing on exactly the case the
// guard was added for: status suppressed the bogus stamp while peek still
// printed "0001-01-01T00:00:00Z" — the "reads as data" shape, surviving on
// the other surface. QUM-1154.
func TestToolPeek_ZeroReportAtDropsTheBogusTimestamp(t *testing.T) {
	mock := &mockSupervisor{peekResult: &supervisor.PeekResult{
		Status:     "active",
		LastReport: supervisor.LastReport{State: "working", At: time.Time{}.UTC().Format(time.RFC3339)},
	}}
	srv := New(mock).withNowFn(fixedNow)
	out, err := srv.toolPeek(context.Background(), json.RawMessage(`{"agent":"ratz"}`))
	if err != nil {
		t.Fatalf("toolPeek: %v", err)
	}
	lr, ok := statusPayloadOf(t, out)["last_report"].(map[string]any)
	if !ok {
		t.Fatalf("no last_report block\n%s", out)
	}
	if ts, ok := lr["at"]; ok {
		t.Errorf("peek emitted last_report.at = %v for a zero timestamp, want the key dropped\n%s", ts, out)
	}
	if lr["age"] != "unknown" {
		t.Errorf("last_report.age = %v, want %q — same verdict status reaches\n%s", lr["age"], "unknown", out)
	}
	if lr["state"] != "working" {
		t.Errorf("last_report.state = %v, want it retained\n%s", lr["state"], out)
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

// statusAgentsByName runs toolStatus over the given agents and returns a
// lookup for the emitted views, plus the raw payload for failure messages.
//
// Keyed, not indexed: an index-addressed assertion that survives a reordering
// still labels the wrong agent in its failure message. The lookup t.Fatalf's
// on a missing name rather than returning the zero value, because these tests
// assert that keys are ABSENT — and every such assertion is trivially true of
// the nil map a plain map index hands back, so a `status` that emitted no
// agents at all would pass them. Absence of a key is only evidence once the
// subject is known to exist.
func statusAgentsByName(t *testing.T, agents []supervisor.AgentInfo) (func(string) map[string]any, string) {
	t.Helper()
	srv := New(&mockSupervisor{statusResult: agents}).withImageFn(cleanImage).withNowFn(fixedNow)
	out, err := srv.toolStatus(context.Background())
	if err != nil {
		t.Fatalf("toolStatus: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, a := range statusPayloadOf(t, out)["agents"].([]any) {
		v := a.(map[string]any)
		byName[v["name"].(string)] = v
	}
	return func(name string) map[string]any {
		t.Helper()
		v, ok := byName[name]
		if !ok {
			t.Fatalf("agent %q absent from the status payload; an absence assertion against a missing agent proves nothing\n%s", name, out)
		}
		return v
	}, out
}

// AC5: `status` — not just `peek` — must render an age on the last report.
// `status` is the surface agents actually read, and it is where a
// `report_status: working` 15 hours stale was misread as live state.
//
// Positive control (defect state present, probe MUST fire): the 15h-stale
// report must render a visible age. The 30s case is a discrimination case,
// not a control — the probe must still fire there, just with a different
// answer; it defeats a hardcoded "15h ago" and exercises the sub-minute
// branch of HumanizeSince. The negative control lives in
// TestToolStatus_NoReportHasNoAge. QUM-1154.
func TestToolStatus_LastReportAge(t *testing.T) {
	view, out := statusAgentsByName(t, []supervisor.AgentInfo{
		{
			Name:              "stale",
			Type:              "engineer",
			LastReportState:   "working",
			LastReportMessage: "wiring the heartbeat",
			LastReportAt:      fixedNow().Add(-15 * time.Hour).Format(time.RFC3339),
		},
		{
			Name:            "fresh",
			Type:            "engineer",
			LastReportState: "working",
			LastReportAt:    fixedNow().Add(-30 * time.Second).Format(time.RFC3339),
		},
	})

	stale := view("stale")
	if stale["last_report_age"] != "15h ago" {
		t.Errorf("stale: last_report_age = %v, want %q\n%s", stale["last_report_age"], "15h ago", out)
	}
	if _, ok := stale["last_report_at"]; !ok {
		t.Errorf("stale: last_report_at absent; the age augments the timestamp, not replaces it\n%s", out)
	}
	// The age is worthless unless it sits next to the token it qualifies: a
	// bare `working` is precisely what was misread.
	if stale["last_report_state"] != "working" {
		t.Errorf("stale: last_report_state = %v, want %q\n%s", stale["last_report_state"], "working", out)
	}

	if fresh := view("fresh"); fresh["last_report_age"] != "30s ago" {
		t.Errorf("fresh: last_report_age = %v, want %q\n%s", fresh["last_report_age"], "30s ago", out)
	}
}

// Negative control: a known-clean subject — an agent that has never reported —
// must produce no staleness signal at all. Asserted on key PRESENCE, because
// an equality check against "" is satisfied by the wrong thing. Mirrors the
// peek sibling TestToolPeek_NoReportHasNoAge. QUM-1154.
func TestToolStatus_NoReportHasNoAge(t *testing.T) {
	view, out := statusAgentsByName(t, []supervisor.AgentInfo{
		{Name: "quiet", Type: "engineer"},
	})
	quiet := view("quiet")
	if age, ok := quiet["last_report_age"]; ok {
		t.Errorf("never-reported agent got an age: %v\n%s", age, out)
	}
	if ts, ok := quiet["last_report_at"]; ok {
		t.Errorf("never-reported agent emitted last_report_at = %v, want the field absent\n%s", ts, out)
	}
}

// The zero timestamp is the live hazard, and the string carry does NOT remove
// it: "0001-01-01T00:00:00Z" parses without error, so it reaches the renderer
// as a real time and `omitempty` will not elide the non-empty string. That is
// the same shape that made a never-active agent emit "0001-01-01T00:00:00Z"
// on last_activity_at, and that tui.Ago guards against printing as
// "106751d ago".
//
// The bogus timestamp is dropped, but the report is LABELLED rather than
// silently disguised: suppressing both fields would leave `last_report_state:
// "working"` standing with no timestamp and no age, which is byte-for-byte
// the pre-fix output shape this whole issue exists to eliminate. A botched
// write and an unparseable write are both bad writes and both say "unknown".
// QUM-1154.
func TestToolStatus_ZeroReportAtIsLabelledNotDisguised(t *testing.T) {
	view, out := statusAgentsByName(t, []supervisor.AgentInfo{
		{
			Name:            "zeroed",
			Type:            "engineer",
			LastReportState: "working",
			LastReportAt:    time.Time{}.UTC().Format(time.RFC3339),
		},
	})
	zeroed := view("zeroed")
	if ts, ok := zeroed["last_report_at"]; ok {
		t.Errorf("zero timestamp emitted last_report_at = %v, want the field absent; it reads as data\n%s", ts, out)
	}
	if zeroed["last_report_age"] != "unknown" {
		t.Errorf("last_report_age = %v, want %q — a dropped timestamp must not leave a bare state unqualified\n%s", zeroed["last_report_age"], "unknown", out)
	}
	// The regression this guards: the state must survive, or the test would
	// be satisfied by dropping the whole report.
	if zeroed["last_report_state"] != "working" {
		t.Errorf("last_report_state = %v, want it retained\n%s", zeroed["last_report_state"], out)
	}
}

// A timestamp in the future must not read as maximally fresh. HumanizeSince
// clamps a negative duration to 0, so an unguarded future stamp renders
// "0s ago" — the exact inverse of the founding defect and strictly worse: a
// stale report eventually looks stale, whereas a skewed future one reads as
// brand new forever and never ages out. QUM-1154.
func TestToolStatus_FutureReportAtDoesNotReadAsFresh(t *testing.T) {
	view, out := statusAgentsByName(t, []supervisor.AgentInfo{
		{
			Name:            "skewed",
			Type:            "engineer",
			LastReportState: "working",
			LastReportAt:    fixedNow().Add(3 * time.Hour).Format(time.RFC3339),
		},
	})
	skewed := view("skewed")
	if skewed["last_report_age"] != "unknown" {
		t.Errorf("last_report_age = %v, want %q for a future timestamp\n%s", skewed["last_report_age"], "unknown", out)
	}
	if age, _ := skewed["last_report_age"].(string); strings.HasSuffix(age, " ago") {
		t.Errorf("future timestamp rendered as an age: %q — it reads as freshness\n%s", age, out)
	}
	// Keep the raw value: it is the evidence that the clock is wrong.
	if skewed["last_report_at"] == nil {
		t.Errorf("future timestamp dropped; the raw value is the evidence of the skew\n%s", out)
	}
}

// A stored timestamp we cannot parse must say so on `status`, using the same
// token `peek` already uses for the same input — never an age derived from
// the zero time. The raw value must survive: it is the only evidence an
// operator has for diagnosing the bad write. QUM-1154.
func TestToolStatus_UnparseableReportAtDegradesLoudly(t *testing.T) {
	view, out := statusAgentsByName(t, []supervisor.AgentInfo{
		{Name: "ratz", Type: "engineer", LastReportState: "working", LastReportAt: "garbage"},
	})
	a0 := view("ratz")
	if a0["last_report_age"] != "unknown" {
		t.Errorf("last_report_age = %v, want %q for an unparseable timestamp\n%s", a0["last_report_age"], "unknown", out)
	}
	if a0["last_report_at"] != "garbage" {
		t.Errorf("last_report_at = %v, want the raw value retained as evidence\n%s", a0["last_report_at"], out)
	}
	if a0["last_report_state"] != "working" {
		t.Errorf("last_report_state = %v, want it retained alongside a bad timestamp\n%s", a0["last_report_state"], out)
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
