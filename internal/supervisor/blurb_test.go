package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/blurb"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeBlurbInvoker records prompts and returns a canned response. No subprocess.
type fakeBlurbInvoker struct {
	resp       string
	err        error
	calls      int
	lastPrompt string
}

func (f *fakeBlurbInvoker) Invoke(_ context.Context, prompt string, _ ...memory.InvokeOption) (string, error) {
	f.calls++
	f.lastPrompt = prompt
	return f.resp, f.err
}

var _ memory.ClaudeInvoker = (*fakeBlurbInvoker)(nil)

func TestStatus_IncludesBlurb(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	saveTestAgent(t, tmp, &state.AgentState{
		Name:   "kit",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: state.StatusComplete,
		Blurb:  "Knows the merge queue; last wired QUM-899.",
	})

	infos, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var found bool
	for _, i := range infos {
		if i.Name == "kit" {
			found = true
			if i.Blurb != "Knows the merge queue; last wired QUM-899." {
				t.Errorf("Blurb = %q", i.Blurb)
			}
		}
	}
	if !found {
		t.Fatal("kit not in status")
	}
}

func TestPeek_IncludesBlurb(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	saveTestAgent(t, tmp, &state.AgentState{
		Name:   "kit",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: state.StatusComplete,
		Blurb:  "Resting expert on the blurb generator.",
	})

	pr, err := sup.Peek(context.Background(), "kit", 10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if pr.Blurb != "Resting expert on the blurb generator." {
		t.Errorf("Peek Blurb = %q", pr.Blurb)
	}
}

func TestGenerateAndPersistBlurb_AssemblesAndPersists(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	saveTestAgent(t, tmp, &state.AgentState{
		Name:   "kit",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: state.StatusRunning,
		Prompt: "Implement QUM-899 capability blurb",
	})
	writeActivity(t, tmp, "kit", []agentloop.ActivityEntry{
		{TS: time.Now().Add(-2 * time.Minute), Kind: "tool_use", Tool: "Edit", Summary: "Edit state.go"},
	})

	fake := &fakeBlurbInvoker{resp: "Wired the QUM-899 blurb pipeline. Knows state migration and the heartbeat refresh path."}
	sup.blurbInvoker = fake
	sup.gitDiffStat = func(string) (string, error) { return "internal/blurb/blurb.go | 40 +", nil }

	if err := sup.generateAndPersistBlurb(context.Background(), "kit", blurb.TriggerInitial); err != nil {
		t.Fatalf("generateAndPersistBlurb: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("invoker calls = %d, want 1", fake.calls)
	}
	// The assembled prompt must carry the role, prompt, and referenced issue key.
	for _, want := range []string{"engineer / engineering", "Implement QUM-899", "QUM-899"} {
		if !strings.Contains(fake.lastPrompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	got, err := state.LoadAgent(tmp, "kit")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Blurb != fake.resp {
		t.Errorf("persisted Blurb = %q, want %q", got.Blurb, fake.resp)
	}
	if got.BlurbAt.IsZero() {
		t.Error("BlurbAt not stamped")
	}
}

func TestGenerateAndPersistBlurb_SkipsRoot(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	saveTestAgent(t, tmp, &state.AgentState{
		Name:   "weave",
		Type:   "manager",
		Parent: "", // root
		Status: state.StatusRunning,
	})
	fake := &fakeBlurbInvoker{resp: "should not be produced"}
	sup.blurbInvoker = fake

	if err := sup.generateAndPersistBlurb(context.Background(), "weave", blurb.TriggerInitial); err != nil {
		t.Fatalf("generateAndPersistBlurb: %v", err)
	}
	if fake.calls != 0 {
		t.Errorf("invoker called %d times for root, want 0", fake.calls)
	}
	got, _ := state.LoadAgent(tmp, "weave")
	if got.Blurb != "" {
		t.Errorf("root got a blurb: %q", got.Blurb)
	}
}

func TestGenerateAndPersistBlurb_EmptyResultKeepsPrevious(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	prevAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	saveTestAgent(t, tmp, &state.AgentState{
		Name:    "kit",
		Type:    "engineer",
		Family:  "engineering",
		Parent:  "weave",
		Status:  state.StatusRunning,
		Blurb:   "Previous, still-good blurb.",
		BlurbAt: prevAt,
	})
	sup.blurbInvoker = &fakeBlurbInvoker{resp: "   "} // empty after trim

	if err := sup.generateAndPersistBlurb(context.Background(), "kit", blurb.TriggerRefresh); err != nil {
		t.Fatalf("generateAndPersistBlurb: %v", err)
	}
	got, _ := state.LoadAgent(tmp, "kit")
	if got.Blurb != "Previous, still-good blurb." {
		t.Errorf("Blurb clobbered: %q", got.Blurb)
	}
	if !got.BlurbAt.Equal(prevAt) {
		t.Errorf("BlurbAt moved on empty result: %v, want %v", got.BlurbAt, prevAt)
	}
}

// QUM-1186: TestReportStatus_CompleteDispatchesCompletionBlurb and
// TestReportStatus_WorkingDoesNotDispatchBlurb were removed here. They drove
// blurb dispatch through Real.ReportStatus, which is deleted.
//
// FLAGGED, because it is more than a test deletion: Real.ReportStatus was the
// ONLY production caller of dispatchBlurb with blurb.TriggerCompletion. That
// trigger is now unreachable in production — the completion one-shot blurb
// regeneration never fires. blurb.TriggerInitial (real.go, on spawn) and
// TriggerRefresh (maybeRefreshBlurb) are unaffected and still covered below.
//
// RESOLVED by lane 3: TriggerCompletion is NOT re-homed onto the idle reaper,
// deliberately. An idle reclaim is not a completion — see the decision record
// on blurb.TriggerCompletion itself (internal/blurb/blurb.go). This is a
// closed decision, not an open action item.

func TestMaybeRefreshBlurb_DispatchesWhenDirtyAndFloorElapsed(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	sup.blurbNow = func() time.Time { return now }
	saveTestAgent(t, tmp, &state.AgentState{
		Name: "kit", Type: "engineer", Family: "engineering", Parent: "weave",
		Status: state.StatusRunning, Blurb: "b", BlurbAt: now.Add(-30 * time.Minute),
	})
	var gotKind blurb.TriggerKind
	var calls int
	sup.dispatchBlurb = func(_ string, kind blurb.TriggerKind) { calls++; gotKind = kind }

	sup.maybeRefreshBlurb("kit", now.Add(-1*time.Minute)) // dirty: activity after blurb

	if calls != 1 || gotKind != blurb.TriggerRefresh {
		t.Errorf("dispatch = (%d, %v), want (1, refresh)", calls, gotKind)
	}
}

func TestMaybeRefreshBlurb_SkipsIdleAgent(t *testing.T) {
	sup, tmp := newTestSupervisor(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	sup.blurbNow = func() time.Time { return now }
	saveTestAgent(t, tmp, &state.AgentState{
		Name: "kit", Type: "engineer", Family: "engineering", Parent: "weave",
		Status: state.StatusRunning, Blurb: "b", BlurbAt: now.Add(-30 * time.Minute),
	})
	var calls int
	sup.dispatchBlurb = func(string, blurb.TriggerKind) { calls++ }

	sup.maybeRefreshBlurb("kit", now.Add(-40*time.Minute)) // activity predates blurb → idle

	if calls != 0 {
		t.Errorf("idle agent dispatched %d times, want 0", calls)
	}
}
