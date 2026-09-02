package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/store"
)

// fakeOperability stands in for *store.Ledger. enabled and degraded are separate
// fields rather than derived, because the three states these commands must tell
// apart — off, unreachable, and empty — are exactly what one bool collapses.
type fakeOperability struct {
	enabled  bool
	degraded error
	goals    []store.OpenGoal
	flows    []store.WorkflowSummary
	events   []store.DispatchedEvent
	err      error
	// Recorded so the paging contract can be asserted rather than assumed.
	gotWorkflow uuid.UUID
	gotAfterSeq int64
	gotLimit    int
	logCalls    int
}

func (f *fakeOperability) Enabled() bool        { return f.enabled }
func (f *fakeOperability) DegradedError() error { return f.degraded }

func (f *fakeOperability) AllOpenGoals(context.Context) ([]store.OpenGoal, error) {
	return f.goals, f.err
}

func (f *fakeOperability) OpenWorkflows(context.Context) ([]store.WorkflowSummary, error) {
	return f.flows, f.err
}

func (f *fakeOperability) EventsByWorkflowInstance(_ context.Context, wf uuid.UUID, afterSeq int64, limit int) ([]store.DispatchedEvent, error) {
	f.logCalls++
	f.gotWorkflow, f.gotAfterSeq, f.gotLimit = wf, afterSeq, limit
	return f.events, f.err
}

func newTestOperabilityDeps(t *testing.T, fake *fakeOperability) (*operabilityDeps, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	return &operabilityDeps{
		SprawlRoot: t.TempDir(),
		Open:       func(context.Context, string) (operabilityView, error) { return fake, nil },
		Stdout:     out,
		Stderr:     &bytes.Buffer{},
	}, out
}

// TestOperability_UnusableStoreIsNeverReportedAsQuiet.
//
// The failure this guards is silent and reads as good news: an operator told
// "no goals are outstanding" on a host where the log is switched off has been
// told the fleet is idle when nothing is even being recorded. Both commands,
// all three unusable states — plus the paired control at the bottom, without
// which a gate that refused everything would satisfy the whole table.
func TestOperability_UnusableStoreIsNeverReportedAsQuiet(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		fake *fakeOperability
		root string
		want string
	}{
		{"no sprawl root", &fakeOperability{enabled: true}, "-", "SPRAWL_ROOT"},
		{"disabled", &fakeOperability{}, "", "disabled"},
		{"degraded", &fakeOperability{enabled: true, degraded: errors.New("dial tcp: refused")}, "", "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, out := newTestOperabilityDeps(t, tc.fake)
			if tc.root == "-" {
				deps.SprawlRoot = ""
			}
			for name, run := range map[string]func() error{
				"goals":     func() error { return runGoals(ctx, deps) },
				"workflows": func() error { return runWorkflowsList(ctx, deps) },
			} {
				out.Reset()
				err := run()
				if err == nil {
					t.Errorf("%s listed work from an unusable store; printed: %q", name, out.String())
					continue
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s error should mention %q; got: %v", name, tc.want, err)
				}
				if out.Len() != 0 {
					t.Errorf("%s printed a listing as well as refusing: %q", name, out.String())
				}
			}
		})
	}

	// Control: the same code path on a healthy, EMPTY store must succeed and say
	// so, or the refusals above prove only that the command never works.
	deps, out := newTestOperabilityDeps(t, &fakeOperability{enabled: true})
	if err := runGoals(ctx, deps); err != nil {
		t.Fatalf("a healthy empty store was refused: %v", err)
	}
	if !strings.Contains(out.String(), "no goals are outstanding") {
		t.Errorf("an empty healthy store should say so; got: %q", out.String())
	}
	// The caveat belongs on the empty listing specifically: that is where it is
	// load-bearing, because "no goals" reads as "no work".
	if !strings.Contains(out.String(), "sprawl status") {
		t.Errorf("the empty listing should point at prose-spawned agents; got: %q", out.String())
	}
}

// TestGoals_ShowsEveryOwnerAndMarksLegacyWork.
//
// The AC is that `sprawl goals` shows a legacy prose-spawned agent's work
// ALONGSIDE engine goals, so both must be present and they must be
// distinguishable: a list that showed both without the marker would be equally
// green here and would tell an operator nothing about which half the engine
// drives.
func TestGoals_ShowsEveryOwnerAndMarksLegacyWork(t *testing.T) {
	wf := uuid.New()
	engine := store.OpenGoal{
		GoalEventID: uuid.New(), WorkflowID: wf, GoalType: "research",
		Owner: "finn", OpenedAt: time.Now().Add(-3 * time.Hour),
	}
	legacy := store.OpenGoal{
		GoalEventID: uuid.New(), WorkflowID: uuid.New(), GoalType: "legacy_spawn",
		Owner: "ratz", Legacy: true, OpenedAt: time.Now().Add(-90 * time.Minute),
	}
	deps, out := newTestOperabilityDeps(t, &fakeOperability{
		enabled: true, goals: []store.OpenGoal{engine, legacy},
	})
	if err := runGoals(context.Background(), deps); err != nil {
		t.Fatalf("runGoals: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		engine.GoalEventID.String(), "research", "finn", wf.String(),
		legacy.GoalEventID.String(), "legacy_spawn", "ratz",
		"2 goal(s) outstanding",
		"sprawl workflows",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing should contain %q; got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "ratz (legacy spawn)") {
		t.Errorf("the legacy goal is not marked, so it is indistinguishable from engine work; got:\n%s", got)
	}
	if strings.Contains(got, "finn (legacy spawn)") {
		t.Errorf("an engine goal was marked legacy; got:\n%s", got)
	}
	// The caveat is for the EMPTY listing only: repeated over a populated one it
	// is noise, and it would contradict the legacy row printed right above it.
	if strings.Contains(got, "sprawl status") {
		t.Errorf("the prose-spawn caveat should not appear on a populated listing; got:\n%s", got)
	}
}

// TestGoals_NamesAnUnownedGoalRatherThanPrintingABlank.
func TestGoals_NamesAnUnownedGoalRatherThanPrintingABlank(t *testing.T) {
	deps, out := newTestOperabilityDeps(t, &fakeOperability{
		enabled: true,
		goals: []store.OpenGoal{{
			GoalEventID: uuid.New(), WorkflowID: uuid.New(),
			GoalType: "research", Owner: "", OpenedAt: time.Now(),
		}},
	})
	if err := runGoals(context.Background(), deps); err != nil {
		t.Fatalf("runGoals: %v", err)
	}
	if !strings.Contains(out.String(), "(unowned)") {
		t.Errorf("an unowned goal should say so — nobody can be poked about it; got:\n%s", out.String())
	}
}

// TestWorkflows_ListReportsEventsAndOpenContractsSeparately.
//
// The two counts are over different sets, and an implementation that printed
// one number twice would satisfy any fixture where they happen to agree — so
// the fixture makes them disagree.
func TestWorkflows_ListReportsEventsAndOpenContractsSeparately(t *testing.T) {
	wf := uuid.New()
	deps, out := newTestOperabilityDeps(t, &fakeOperability{
		enabled: true,
		flows: []store.WorkflowSummary{{
			WorkflowID: wf, Events: 7, OpenContracts: 2,
			StartedAt: time.Now().Add(-5 * time.Hour), LastEventAt: time.Now().Add(-2 * time.Minute),
		}},
	})
	if err := runWorkflowsList(context.Background(), deps); err != nil {
		t.Fatalf("runWorkflowsList: %v", err)
	}
	got := out.String()
	for _, want := range []string{wf.String(), "1 workflow instance(s)", "events:   7", "open:     2 contract(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing should contain %q; got:\n%s", want, got)
		}
	}
}

// TestWorkflowLog_RejectsABadIDBeforeOpeningTheStore.
func TestWorkflowLog_RejectsABadIDBeforeOpeningTheStore(t *testing.T) {
	fake := &fakeOperability{enabled: true}
	deps, out := newTestOperabilityDeps(t, fake)
	err := runWorkflowLog(context.Background(), deps, "not-a-uuid", 200)
	if err == nil {
		t.Fatal("a malformed workflow id was accepted")
	}
	if !strings.Contains(err.Error(), "not a uuid") {
		t.Errorf("the error should say what is wrong with it; got: %v", err)
	}
	if fake.logCalls != 0 {
		t.Errorf("the store was read %d time(s) despite a malformed id", fake.logCalls)
	}
	if out.Len() != 0 {
		t.Errorf("a refusal printed a log: %q", out.String())
	}
}

// TestWorkflowLog_PrintsTheInstanceInOrderAndWarnsOnAFullPage.
func TestWorkflowLog_PrintsTheInstanceInOrderAndWarnsOnAFullPage(t *testing.T) {
	wf := uuid.New()
	closes := uuid.New()
	fake := &fakeOperability{
		enabled: true,
		events: []store.DispatchedEvent{
			{Seq: 1, ID: uuid.New(), SchemaName: "goal_opened", SchemaVersion: 1, At: time.Now(), Payload: []byte(`{"goal_type":"research"}`)},
			{Seq: 2, ID: uuid.New(), SchemaName: "goal_closed", SchemaVersion: 1, At: time.Now(), ClosesEventID: &closes, Payload: []byte(`{"outcome":"success"}`)},
		},
	}
	deps, out := newTestOperabilityDeps(t, fake)
	if err := runWorkflowLog(context.Background(), deps, wf.String(), 2); err != nil {
		t.Fatalf("runWorkflowLog: %v", err)
	}
	got := out.String()

	if fake.gotWorkflow != wf {
		t.Errorf("read instance %s, want %s", fake.gotWorkflow, wf)
	}
	if fake.gotAfterSeq != 0 || fake.gotLimit != 2 {
		t.Errorf("paged with afterSeq=%d limit=%d, want 0 and 2", fake.gotAfterSeq, fake.gotLimit)
	}
	for _, want := range []string{"goal_opened", "goal_closed", `"outcome":"success"`, "closes:  " + closes.String()} {
		if !strings.Contains(got, want) {
			t.Errorf("the log should contain %q; got:\n%s", want, got)
		}
	}
	if strings.Index(got, "goal_opened") > strings.Index(got, "goal_closed") {
		t.Errorf("the log is out of order; got:\n%s", got)
	}
	// A full page is the only state in which there might be more, and a
	// truncated log presented as a whole one is how an operator concludes a goal
	// never progressed.
	if !strings.Contains(got, "--limit") {
		t.Errorf("a full page should warn that there may be more; got:\n%s", got)
	}
}

// TestWorkflowLog_ShortPageDoesNotWarn is the negative half of the pair above:
// without it, a command that printed the warning unconditionally would pass.
func TestWorkflowLog_ShortPageDoesNotWarn(t *testing.T) {
	fake := &fakeOperability{
		enabled: true,
		events: []store.DispatchedEvent{
			{Seq: 1, SchemaName: "goal_opened", SchemaVersion: 1, At: time.Now(), Payload: []byte(`{}`)},
		},
	}
	deps, out := newTestOperabilityDeps(t, fake)
	if err := runWorkflowLog(context.Background(), deps, uuid.New().String(), 200); err != nil {
		t.Fatalf("runWorkflowLog: %v", err)
	}
	if strings.Contains(out.String(), "--limit") {
		t.Errorf("a short page warned about truncation; got:\n%s", out.String())
	}
}

// TestWorkflowLog_EmptyInstanceIsNotAnError.
//
// An id that names nothing and an instance with no events yet are the same
// observation from here; claiming either specifically would be a guess.
func TestWorkflowLog_EmptyInstanceIsNotAnError(t *testing.T) {
	deps, out := newTestOperabilityDeps(t, &fakeOperability{enabled: true})
	if err := runWorkflowLog(context.Background(), deps, uuid.New().String(), 200); err != nil {
		t.Fatalf("an empty instance was reported as an error: %v", err)
	}
	if !strings.Contains(out.String(), "no events") {
		t.Errorf("an empty instance should say so; got: %q", out.String())
	}
}

// TestOperabilityCommands_AreRegisteredAndReadOnly.
func TestOperabilityCommands_AreRegisteredAndReadOnly(t *testing.T) {
	found := map[string]*cobraCmdShape{}
	for _, c := range rootCmd.Commands() {
		if c.Name() == "goals" || c.Name() == "workflows" {
			found[c.Name()] = &cobraCmdShape{use: c.Use, short: c.Short, long: c.Long}
		}
	}
	if len(found) != 2 {
		t.Fatalf("got %d of the two operability commands registered on rootCmd", len(found))
	}
	// Both must say they need the event log: the refusal is correct but arrives
	// only at runtime, and `--help` is where an operator looks first.
	for name, c := range found {
		if !strings.Contains(c.long, "event_log.enabled") {
			t.Errorf("%s's help should name the config it requires; got: %q", name, c.long)
		}
	}
	if !strings.Contains(found["goals"].long, "sprawl status") {
		t.Error("goals' help should point prose-spawn users at `sprawl status`, since their work is absent from it")
	}
}

type cobraCmdShape struct{ use, short, long string }

// TestOperabilityCommands_DoNotPrintUsageOverAStoreRefusal.
//
// Every error these commands raise past arg validation is about the store, and
// a flag reference stacked under "the event log is unreachable" pushes the
// remedy off the top of the operator's screen. Asserted through RunE rather
// than the run* functions, because the suppression lives in the cobra wiring
// and a test of the inner function cannot see it.
func TestOperabilityCommands_DoNotPrintUsageOverAStoreRefusal(t *testing.T) {
	cmds := []*cobra.Command{goalsCmd, workflowsCmd}
	for _, c := range cmds {
		c.SilenceUsage = false
		t.Cleanup(func() { c.SilenceUsage = false })
	}
	prev := defaultOperabilityDeps
	t.Cleanup(func() { defaultOperabilityDeps = prev })
	// A disabled store: the refusal arrives from inside RunE, which is exactly
	// the class of error the usage block must not accompany.
	defaultOperabilityDeps, _ = newTestOperabilityDeps(t, &fakeOperability{})

	for _, c := range cmds {
		if err := c.RunE(c, nil); err == nil {
			t.Errorf("%s did not refuse a disabled store", c.Name())
		}
		if !c.SilenceUsage {
			t.Errorf("%s prints its usage block over a store refusal", c.Name())
		}
	}
}
