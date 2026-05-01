package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// newFakeReal returns a Real with no-op Fn seams. Tests override what
// they care about.
func newFakeReal(t *testing.T) (*Real, string) {
	t.Helper()
	r, tmpDir := newTestSupervisor(t)
	r.runtimeStarter = &runtimeTestStarter{
		session: &runtimeTestSession{
			sessionID: "test-session",
			caps: backendpkg.Capabilities{
				SupportsInterrupt: true,
				SupportsResume:    true,
			},
		},
	}
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error) {
		return nil, errors.New("spawnFn not overridden")
	}
	r.mergeFn = func(*agentops.MergeDeps, string, string, bool, bool) error {
		return errors.New("mergeFn not overridden")
	}
	r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
		return errors.New("retireFn not overridden")
	}
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		return errors.New("killFn not overridden")
	}
	return r, tmpDir
}

func TestSpawn_MapsSpawnRequestAndReturnsAgentInfo(t *testing.T) {
	r, _ := newFakeReal(t)

	var gotFamily, gotType, gotPrompt, gotBranch string
	r.spawnFn = func(_ *agentops.SpawnDeps, family, agentType, prompt, branch string) (*state.AgentState, error) {
		gotFamily = family
		gotType = agentType
		gotPrompt = prompt
		gotBranch = branch
		return &state.AgentState{
			Name:   "engineer-abc123",
			Type:   "engineer",
			Family: "engineering",
			Parent: "weave",
			Status: "active",
			Branch: "dmotles/feature-x",
		}, nil
	}

	info, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "build feature X",
		Branch: "dmotles/feature-x",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if gotFamily != "engineering" || gotType != "engineer" ||
		gotPrompt != "build feature X" || gotBranch != "dmotles/feature-x" {
		t.Errorf("args not forwarded: family=%q type=%q prompt=%q branch=%q",
			gotFamily, gotType, gotPrompt, gotBranch)
	}

	want := &AgentInfo{
		Name: "engineer-abc123", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active", Branch: "dmotles/feature-x",
	}
	if *info != *want {
		t.Errorf("AgentInfo = %+v, want %+v", *info, *want)
	}
}

func TestSpawn_PropagatesError(t *testing.T) {
	r, _ := newFakeReal(t)
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error) {
		return nil, errors.New("boom")
	}

	info, err := r.Spawn(context.Background(), SpawnRequest{Family: "engineering", Type: "engineer", Prompt: "x", Branch: "b"})
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil", info)
	}
}

func TestSpawn_InjectsCallerAndRootViaGetenv(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	captured := map[string]string{}
	r.spawnFn = func(deps *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		captured["SPRAWL_AGENT_IDENTITY"] = deps.Getenv("SPRAWL_AGENT_IDENTITY")
		captured["SPRAWL_ROOT"] = deps.Getenv("SPRAWL_ROOT")
		return &state.AgentState{Name: "x"}, nil
	}

	if _, err := r.Spawn(context.Background(), SpawnRequest{Family: "engineering", Type: "engineer", Prompt: "x", Branch: "b"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if captured["SPRAWL_AGENT_IDENTITY"] != "weave" {
		t.Errorf("SPRAWL_AGENT_IDENTITY = %q, want weave", captured["SPRAWL_AGENT_IDENTITY"])
	}
	if captured["SPRAWL_ROOT"] != tmpDir {
		t.Errorf("SPRAWL_ROOT = %q, want %q", captured["SPRAWL_ROOT"], tmpDir)
	}
}

func TestMerge_ForwardsArgs(t *testing.T) {
	r, _ := newFakeReal(t)

	var gotName, gotMsg string
	var gotNoValidate, gotDryRun bool
	r.mergeFn = func(_ *agentops.MergeDeps, name, msg string, noValidate, dryRun bool) error {
		gotName, gotMsg, gotNoValidate, gotDryRun = name, msg, noValidate, dryRun
		return nil
	}

	if err := r.Merge(context.Background(), "ratz", "custom commit", true); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if gotName != "ratz" || gotMsg != "custom commit" || !gotNoValidate || gotDryRun {
		t.Errorf("args not forwarded: name=%q msg=%q noValidate=%v dryRun=%v",
			gotName, gotMsg, gotNoValidate, gotDryRun)
	}
}

func TestMerge_PropagatesError(t *testing.T) {
	r, _ := newFakeReal(t)
	r.mergeFn = func(*agentops.MergeDeps, string, string, bool, bool) error {
		return errors.New("dirty tree")
	}
	err := r.Merge(context.Background(), "ratz", "", false)
	if err == nil || err.Error() != "dirty tree" {
		t.Errorf("err = %v, want dirty tree", err)
	}
}

func TestRetire_ForwardsFlags(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "ghost", Type: "researcher", Parent: "weave", Status: "active"})

	var got struct {
		name                                                 string
		cascade, force, abandon, mergeFirst, yes, noValidate bool
	}
	r.retireFn = func(_ *agentops.RetireDeps, name string, cascade, force, abandon, mergeFirst, yes, noValidate bool) error {
		got.name = name
		got.cascade = cascade
		got.force = force
		got.abandon = abandon
		got.mergeFirst = mergeFirst
		got.yes = yes
		got.noValidate = noValidate
		return nil
	}

	if err := r.Retire(context.Background(), "ghost", true /* mergeFirst */, false /* abandon */, false /* cascade */, false /* noValidate */); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if got.name != "ghost" {
		t.Errorf("name = %q, want ghost", got.name)
	}
	if !got.mergeFirst {
		t.Error("mergeFirst should be true")
	}
	if got.abandon {
		t.Error("abandon should be false")
	}
	if !got.yes {
		t.Error("yes should default to true (non-interactive TUI supervisor)")
	}
	if got.cascade {
		t.Error("cascade should be false")
	}
	if got.force {
		t.Error("force should be false")
	}
	if got.noValidate {
		t.Error("noValidate should be false")
	}
}

func TestRetire_AbandonMode(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "ghost", Type: "researcher", Parent: "weave", Status: "active"})
	var gotAbandon, gotMergeFirst bool
	r.retireFn = func(_ *agentops.RetireDeps, _ string, _, _, abandon, mergeFirst, _, _ bool) error {
		gotAbandon, gotMergeFirst = abandon, mergeFirst
		return nil
	}
	if err := r.Retire(context.Background(), "ghost", false, true, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if !gotAbandon || gotMergeFirst {
		t.Errorf("abandon=%v mergeFirst=%v, want abandon=true mergeFirst=false", gotAbandon, gotMergeFirst)
	}
}

func TestRetire_CascadeAndNoValidate(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "ghost", Type: "researcher", Parent: "weave", Status: "active"})
	var gotCascade, gotNoValidate bool
	r.retireFn = func(_ *agentops.RetireDeps, _ string, cascade, _, _, _, _, noValidate bool) error {
		gotCascade, gotNoValidate = cascade, noValidate
		return nil
	}
	if err := r.Retire(context.Background(), "ghost", true /* merge */, false, true /* cascade */, true /* noValidate */); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if !gotCascade {
		t.Error("cascade should be true")
	}
	if !gotNoValidate {
		t.Error("noValidate should be true")
	}
}

func TestKill_IdempotentOnMissingAgent(t *testing.T) {
	r, _ := newFakeReal(t)
	called := false
	r.killFn = func(*agentops.KillDeps, string, bool) error {
		called = true
		return nil
	}

	// No agent state file was created. Kill must return nil and NOT invoke
	// killFn (which would fail on missing state anyway).
	if err := r.Kill(context.Background(), "ghost"); err != nil {
		t.Errorf("Kill on missing agent: got err %v, want nil (idempotent)", err)
	}
	if called {
		t.Error("killFn should not be invoked when agent state is absent")
	}
}

func TestKill_ForwardsWhenAgentExists(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
	})

	var gotName string
	var gotForce bool
	r.killFn = func(_ *agentops.KillDeps, name string, force bool) error {
		gotName, gotForce = name, force
		return nil
	}

	if err := r.Kill(context.Background(), "ratz"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if gotName != "ratz" {
		t.Errorf("name = %q, want ratz", gotName)
	}
	if gotForce {
		t.Error("force should default to false (graceful shutdown)")
	}
}

func TestKill_PropagatesKillFnError(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
	})
	r.killFn = func(*agentops.KillDeps, string, bool) error { return errors.New("tmux down") }
	err := r.Kill(context.Background(), "ratz")
	if err == nil || err.Error() != "tmux down" {
		t.Errorf("err = %v, want tmux down", err)
	}
}

func TestKill_RejectsInvalidName(t *testing.T) {
	r, _ := newFakeReal(t)
	called := false
	r.killFn = func(*agentops.KillDeps, string, bool) error { called = true; return nil }
	err := r.Kill(context.Background(), "../evil")
	if err == nil {
		t.Error("expected error for invalid name")
	}
	if called {
		t.Error("killFn should not be called for invalid name")
	}
}

// TestNewReal_BuildsDepsWithRealAgentops sanity-checks that NewReal populates
// the default Fn seams with the real agentops implementations, so production
// wiring is correct even though other tests swap them.
func TestNewReal_BuildsDepsWithRealAgentops(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	if sup.spawnFn == nil || sup.mergeFn == nil || sup.retireFn == nil || sup.killFn == nil {
		t.Fatal("NewReal should wire all Fn seams to agentops.* by default")
	}
	if sup.spawnDeps == nil || sup.mergeDeps == nil || sup.retireDeps == nil || sup.killDeps == nil {
		t.Fatal("NewReal should populate all *Deps")
	}
	// Verify the Getenv indirection for SPRAWL_ROOT / SPRAWL_AGENT_IDENTITY is
	// wired so agentops sees the supervisor's config rather than the process env.
	if v := sup.spawnDeps.Getenv("SPRAWL_AGENT_IDENTITY"); v != "weave" {
		t.Errorf("spawnDeps.Getenv(SPRAWL_AGENT_IDENTITY) = %q, want weave", v)
	}
	if v := sup.killDeps.Getenv("SPRAWL_ROOT"); v == "" {
		t.Error("killDeps.Getenv(SPRAWL_ROOT) should return the configured sprawlRoot")
	}
}

func TestNewReal_DoesNotRequireTmuxOnPath(t *testing.T) {
	t.Setenv("PATH", "")
	sup, err := NewReal(Config{
		SprawlRoot: t.TempDir(),
		CallerName: "weave",
	})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	if sup == nil {
		t.Fatal("NewReal returned nil supervisor")
	}
}

// guard: ensure Real implements Supervisor.
var _ Supervisor = (*Real)(nil)

// guard: ensure returned AgentInfo fields survive a round-trip (sanity).
func TestSpawn_AgentInfoRoundTrip(t *testing.T) {
	r, _ := newFakeReal(t)
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error) {
		return &state.AgentState{Name: "a", Type: "b", Family: "c", Parent: "d", Status: "e", Branch: "f"}, nil
	}
	info, err := r.Spawn(context.Background(), SpawnRequest{Family: "engineering", Type: "engineer", Prompt: "p", Branch: "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := AgentInfo{Name: "a", Type: "b", Family: "c", Parent: "d", Status: "e", Branch: "f"}
	if *info != want {
		t.Errorf("got %+v, want %+v", *info, want)
	}
}

// smoke: format compile check on all errors
func TestRealErrorMessages_Format(t *testing.T) {
	_ = fmt.Sprintf("%v", errors.New("x"))
}

// --- QUM-387: effectiveCaller context override ---

func TestEffectiveCaller_ContextOverride(t *testing.T) {
	r, _ := newFakeReal(t)

	// No context override — falls back to callerName ("weave").
	got := r.effectiveCaller(context.Background())
	if got != "weave" {
		t.Errorf("effectiveCaller(empty ctx) = %q, want %q", got, "weave")
	}

	// Context override takes precedence.
	ctx := backendpkg.WithCallerIdentity(context.Background(), "finn")
	got = r.effectiveCaller(ctx)
	if got != "finn" {
		t.Errorf("effectiveCaller(ctx with finn) = %q, want %q", got, "finn")
	}
}

func TestReportStatus_UsesExplicitAgentName(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "finn", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active",
	})

	// When agentName is passed explicitly, it should be used even without
	// context override (this is the MCP path after QUM-387 fix).
	result, err := r.ReportStatus(context.Background(), "finn", "complete", "done", "")
	if err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	if result == nil || result.ReportedAt == "" {
		t.Fatal("expected non-nil result with ReportedAt")
	}

	// Verify state was updated for "finn", not "weave".
	st, err := state.LoadAgent(tmpDir, "finn")
	if err != nil {
		t.Fatalf("LoadAgent(finn): %v", err)
	}
	if st.Status != "done" {
		t.Errorf("finn.Status = %q, want done", st.Status)
	}
}

func TestSendAsync_ContextIdentitySetsSender(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	// Create both agents.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "finn", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "manager", Family: "engineering",
		Status: "active",
	})

	// Child "finn" sends async to "weave" via MCP — context carries finn's identity.
	ctx := backendpkg.WithCallerIdentity(context.Background(), "finn")
	result, err := r.SendAsync(ctx, "weave", "status update", "I'm done", "", nil)
	if err != nil {
		t.Fatalf("SendAsync: %v", err)
	}
	if result == nil || result.MessageID == "" {
		t.Fatal("expected non-nil result with MessageID")
	}
}

func TestMessagesList_ContextIdentityScopesMailbox(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "finn", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active",
	})

	// Child "finn" lists its own mailbox via MCP.
	ctx := backendpkg.WithCallerIdentity(context.Background(), "finn")
	result, err := r.MessagesList(ctx, "", 0)
	if err != nil {
		t.Fatalf("MessagesList: %v", err)
	}
	if result.Agent != "finn" {
		t.Errorf("MessagesList.Agent = %q, want %q", result.Agent, "finn")
	}
}

func TestMessagesPeek_ContextIdentityScopesMailbox(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "finn", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active",
	})

	ctx := backendpkg.WithCallerIdentity(context.Background(), "finn")
	result, err := r.MessagesPeek(ctx)
	if err != nil {
		t.Fatalf("MessagesPeek: %v", err)
	}
	if result.Agent != "finn" {
		t.Errorf("MessagesPeek.Agent = %q, want %q", result.Agent, "finn")
	}
}

// --- QUM-384: grandchild parent identity ---

// TestSpawn_UsesEffectiveCallerAsParentIdentity covers the QUM-384 bug: when
// Spawn is invoked with a context carrying a caller identity (e.g. a manager
// agent like "tower" calling spawn through the MCP server hosted by weave),
// the SPRAWL_AGENT_IDENTITY passed to spawnFn must come from
// effectiveCaller(ctx) — NOT from the shared spawnDeps's Getenv which always
// returns r.callerName ("weave"). Otherwise prepareSpawn records "weave" as
// the parent of every grandchild, flattening the agent tree.
func TestSpawn_UsesEffectiveCallerAsParentIdentity(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	if r.callerName != "weave" {
		t.Fatalf("precondition: r.callerName = %q, want weave", r.callerName)
	}

	captured := map[string]string{}
	r.spawnFn = func(deps *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		captured["SPRAWL_AGENT_IDENTITY"] = deps.Getenv("SPRAWL_AGENT_IDENTITY")
		captured["SPRAWL_ROOT"] = deps.Getenv("SPRAWL_ROOT")
		return &state.AgentState{Name: "byte"}, nil
	}

	// Manager "tower" (already a child of weave) is invoking spawn through
	// the MCP server. Context carries tower's caller identity.
	ctx := backendpkg.WithCallerIdentity(context.Background(), "tower")
	if _, err := r.Spawn(ctx, SpawnRequest{
		Family: "engineering",
		Type:   "engineer",
		Prompt: "do work",
		Branch: "dmotles/byte",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if got := captured["SPRAWL_AGENT_IDENTITY"]; got != "tower" {
		t.Errorf("SPRAWL_AGENT_IDENTITY = %q, want %q (grandchildren flatten under weave when this is wrong — QUM-384)", got, "tower")
	}
	// Regression: SPRAWL_ROOT must remain wired to the supervisor's root.
	if got := captured["SPRAWL_ROOT"]; got != tmpDir {
		t.Errorf("SPRAWL_ROOT = %q, want %q", got, tmpDir)
	}
}

// TestSpawn_FallsBackToCallerNameWithoutContextOverride pins the existing
// behavior: when ctx has no caller identity override, the SPRAWL_AGENT_IDENTITY
// seen by spawnFn must equal r.callerName ("weave" in tests). Guards against
// the fix accidentally breaking the common direct-call path.
func TestSpawn_FallsBackToCallerNameWithoutContextOverride(t *testing.T) {
	r, _ := newFakeReal(t)

	var capturedIdentity string
	r.spawnFn = func(deps *agentops.SpawnDeps, _, _, _, _ string) (*state.AgentState, error) {
		capturedIdentity = deps.Getenv("SPRAWL_AGENT_IDENTITY")
		return &state.AgentState{Name: "x"}, nil
	}

	// Plain context.Background() — no caller identity attached.
	if _, err := r.Spawn(context.Background(), SpawnRequest{
		Family: "engineering", Type: "engineer", Prompt: "x", Branch: "b",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if capturedIdentity != "weave" {
		t.Errorf("SPRAWL_AGENT_IDENTITY = %q, want %q (callerName fallback)", capturedIdentity, "weave")
	}
}

// TestStatus_ReturnsHierarchyIncludingGrandchildren pins that Status surfaces
// agents with their actual Parent fields (i.e. supports a depth-2 tree).
// This guards against a regression where Status drops or rewrites the Parent
// field for grandchildren — even today this passes, but the test pins the
// invariant the QUM-384 TUI tree depends on.
func TestStatus_ReturnsHierarchyIncludingGrandchildren(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	// weave (caller) -> tower (manager) -> byte (grandchild)
	// weave -> finn (sibling of tower)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "tower", Type: "manager", Family: "engineering",
		Parent: "weave", Status: "active", Branch: "dmotles/tower",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "byte", Type: "engineer", Family: "engineering",
		Parent: "tower", Status: "active", Branch: "dmotles/byte",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "finn", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active", Branch: "dmotles/finn",
	})

	agents, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	byName := make(map[string]AgentInfo, len(agents))
	for _, a := range agents {
		byName[a.Name] = a
	}

	tower, ok := byName["tower"]
	if !ok {
		t.Fatal("missing tower in Status result")
	}
	if tower.Parent != "weave" {
		t.Errorf("tower.Parent = %q, want weave", tower.Parent)
	}

	byteAgent, ok := byName["byte"]
	if !ok {
		t.Fatal("missing byte (grandchild) in Status result")
	}
	if byteAgent.Parent != "tower" {
		t.Errorf("byte.Parent = %q, want tower (grandchild must NOT be flattened to weave)", byteAgent.Parent)
	}

	finn, ok := byName["finn"]
	if !ok {
		t.Fatal("missing finn (sibling of tower) in Status result")
	}
	if finn.Parent != "weave" {
		t.Errorf("finn.Parent = %q, want weave", finn.Parent)
	}
}

// TestRetire_FallsBackToRegistry_WhenJSONMissing pins the QUM-404 fix: when
// Real.Retire is called for an agent whose JSON is missing but whose runtime
// is in the registry, Retire must reconcile from the registry snapshot
// (instead of failing) and still invoke retireFn + remove the registry entry.
func TestRetire_FallsBackToRegistry_WhenJSONMissing(t *testing.T) {
	t.Run("registry entry exists but JSON missing", func(t *testing.T) {
		r, tmpDir := newFakeReal(t)

		synth := &state.AgentState{
			Name:     "orphan",
			Type:     "manager",
			Family:   "engineering",
			Parent:   "weave",
			Branch:   "dmotles/orphan",
			Worktree: filepath.Join(tmpDir, "worktree-orphan"),
			Status:   "active",
		}
		// Inject runtime via registry — do NOT save JSON.
		rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: tmpDir,
			Agent:      synth,
		})
		if rt == nil {
			t.Fatal("Ensure() returned nil runtime")
		}

		var retireCalls []string
		r.retireFn = func(_ *agentops.RetireDeps, name string, _, _, _, _, _, _ bool) error {
			// Load-bearing contract: by the time retireFn runs, the JSON
			// must exist on disk so agentops.Retire's state.LoadAgent
			// succeeds. Real.Retire must reconcile from the runtime
			// registry before delegating.
			loaded, err := state.LoadAgent(tmpDir, name)
			if err != nil {
				t.Fatalf("retireFn invoked but state.LoadAgent(%q) failed: %v (Real.Retire must reconcile JSON from registry before delegating)", name, err)
			}
			if loaded.Type != "manager" {
				t.Errorf("synthesized state Type = %q, want %q", loaded.Type, "manager")
			}
			if loaded.Parent != "weave" {
				t.Errorf("synthesized state Parent = %q, want %q", loaded.Parent, "weave")
			}
			if loaded.Branch != "dmotles/orphan" {
				t.Errorf("synthesized state Branch = %q, want %q", loaded.Branch, "dmotles/orphan")
			}
			retireCalls = append(retireCalls, name)
			return nil
		}

		err := r.Retire(context.Background(), "orphan",
			false, /* mergeFirst */
			true,  /* abandon */
			false, /* cascade */
			false, /* noValidate */
		)
		if err != nil {
			t.Fatalf("Retire: unexpected error: %v", err)
		}
		if len(retireCalls) != 1 || retireCalls[0] != "orphan" {
			t.Errorf("retireFn calls = %v, want [orphan]", retireCalls)
		}
		if _, ok := r.runtimeRegistry.Get("orphan"); ok {
			t.Error("expected runtime registry to no longer contain orphan after Retire")
		}
	})

	t.Run("registry also missing returns error", func(t *testing.T) {
		r, _ := newFakeReal(t)

		// retireFn returns nil if invoked; with no registry entry and no JSON,
		// Retire has nothing to fall back to and should surface an error.
		r.retireFn = func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error {
			return nil
		}

		err := r.Retire(context.Background(), "ghost", false, true, false, false)
		if err == nil {
			t.Fatal("expected error when both JSON and registry are missing, got nil")
		}
		// Don't be too strict on wording — just sanity-check the error isn't a
		// nil panic surrogate.
		if !strings.Contains(strings.ToLower(err.Error()), "not found") &&
			!strings.Contains(strings.ToLower(err.Error()), "missing") &&
			!strings.Contains(strings.ToLower(err.Error()), "no such") &&
			!strings.Contains(strings.ToLower(err.Error()), "ghost") {
			t.Errorf("error %q should reference the missing agent", err)
		}
	})
}

// TestE2E_StateDivergenceFullFlow drives the QUM-404 acceptance scenario end
// to end against a real Real supervisor with no-op runtime starter (no
// Claude required): a researcher spawns a manager (PrepareSpawn writes the
// JSON), divergence is simulated by deleting the manager's JSON, retire
// reconciles from the registry, and after retire the dir + worktree are
// gone and the name is free for reuse.
func TestE2E_StateDivergenceFullFlow(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	researcher := &state.AgentState{
		Name:     "ghost",
		Type:     "researcher",
		Family:   "engineering",
		Parent:   "weave",
		Branch:   "dmotles/ghost",
		Worktree: filepath.Join(tmpDir, ".sprawl", "worktrees", "ghost"),
		Status:   "active",
		TreePath: "weave/ghost",
	}
	saveTestAgent(t, tmpDir, researcher)

	managerWorktree := filepath.Join(tmpDir, ".sprawl", "worktrees", "tower")
	r.spawnFn = agentops.PrepareSpawn
	r.spawnDeps = &agentops.SpawnDeps{
		WorktreeCreator: &spawnPathWorktreeCreator{path: managerWorktree},
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "ghost" // researcher is the parent
			case "SPRAWL_ROOT":
				return tmpDir
			}
			return ""
		},
		CurrentBranch: func(string) (string, error) { return "main", nil },
		NewSpawnLock: func(string) (func() error, func() error) {
			return func() error { return nil }, func() error { return nil }
		},
		LoadConfig:      func(string) (*config.Config, error) { return &config.Config{}, nil },
		RunScript:       agentops.RunBashScript,
		WorktreeRemove:  func(_, p string, _ bool) error { return os.RemoveAll(p) },
		GitBranchDelete: func(string, string) error { return nil },
	}
	r.runtimeStarter = &runtimeTestStarter{session: &runtimeTestSession{
		sessionID: "sess-tower",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}}

	// Spawn the manager via the live supervisor — researcher is the parent.
	spawnCtx := backendpkg.WithCallerIdentity(context.Background(), "ghost")
	info, err := r.Spawn(spawnCtx, SpawnRequest{
		Family: "engineering",
		Type:   "manager",
		Prompt: "manage the work",
		Branch: "dmotles/tower",
	})
	if err != nil {
		t.Fatalf("Spawn(manager): %v", err)
	}
	mgrName := info.Name
	if mgrName == "" {
		t.Fatal("spawn returned empty manager name")
	}

	// AC: <manager>.json exists with parent=ghost.
	mgrState, err := state.LoadAgent(tmpDir, mgrName)
	if err != nil {
		t.Fatalf("LoadAgent(%s) after spawn: %v", mgrName, err)
	}
	if mgrState.Parent != "ghost" {
		t.Errorf("manager Parent = %q, want %q", mgrState.Parent, "ghost")
	}
	if mgrState.Type != "manager" {
		t.Errorf("manager Type = %q, want manager", mgrState.Type)
	}
	mgrDir := filepath.Join(tmpDir, ".sprawl", "agents", mgrName)
	if _, err := os.Stat(mgrDir); err != nil {
		t.Fatalf("expected agent dir at %s: %v", mgrDir, err)
	}

	// Simulate divergence: delete the JSON only.
	if err := os.Remove(filepath.Join(tmpDir, ".sprawl", "agents", mgrName+".json")); err != nil {
		t.Fatalf("removing JSON to simulate divergence: %v", err)
	}

	// Retire the manager — must succeed via registry fallback. Use abandon
	// to skip merge logic (no real git).
	r.retireFn = agentops.Retire
	r.retireDeps = &agentops.RetireDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "ghost"
			case "SPRAWL_ROOT":
				return tmpDir
			}
			return ""
		},
		WorktreeRemove:      func(_, p string, _ bool) error { return os.RemoveAll(p) },
		GitStatus:           func(string) (string, error) { return "", nil },
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     func(string, string) error { return nil },
		GitBranchIsMerged:   func(string, string) (bool, error) { return true, nil },
		GitBranchSafeDelete: func(string, string) error { return nil },
		LoadAgent:           state.LoadAgent,
		CurrentBranch:       func(string) (string, error) { return "main", nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig:          func(string) (*config.Config, error) { return &config.Config{}, nil },
		RunScript:           func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}
	if err := r.Retire(context.Background(), mgrName,
		false, /* mergeFirst */
		true,  /* abandon */
		false, /* cascade */
		true,  /* noValidate */
	); err != nil {
		t.Fatalf("Retire after divergence: %v", err)
	}

	// AC: dir gone, worktree gone, JSON gone, name free for reuse.
	if _, err := os.Stat(mgrDir); !os.IsNotExist(err) {
		t.Errorf("expected manager dir to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(managerWorktree); !os.IsNotExist(err) {
		t.Errorf("expected manager worktree to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".sprawl", "agents", mgrName+".json")); !os.IsNotExist(err) {
		t.Errorf("expected manager JSON to be removed, stat err = %v", err)
	}
	if _, ok := r.runtimeRegistry.Get(mgrName); ok {
		t.Errorf("expected runtime registry to no longer contain %q", mgrName)
	}

	// AllocateName should now hand out the same manager name (not a stale
	// fallback like "fixer-1") because both JSON and dir are gone.
	freed, err := agent.AllocateName(state.AgentsDir(tmpDir), "manager")
	if err != nil {
		t.Fatalf("AllocateName after retire: %v", err)
	}
	if freed != mgrName {
		t.Errorf("AllocateName = %q, want %q (name should be reusable after clean retire)", freed, mgrName)
	}
}
