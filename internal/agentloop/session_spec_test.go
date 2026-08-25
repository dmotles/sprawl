package agentloop

import (
	"io"
	"slices"
	"testing"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

// TestBuildAgentSessionSpec_DisallowsLoopOnlyTools pins QUM-470: harness-only
// tools (ScheduleWakeup, Monitor, CronCreate, etc.) must be surfaced as
// SessionSpec.DisallowedTools for every child agent type, and must NOT appear
// in AllowedTools. These tools require an outer harness and have no meaning
// inside a child claude session.
func TestBuildAgentSessionSpec_DisallowsLoopOnlyTools(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "qa"} {
		t.Run(agentType, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)

			disallowed := make(map[string]bool, len(spec.DisallowedTools))
			for _, name := range spec.DisallowedTools {
				disallowed[name] = true
			}
			for _, want := range rootinit.ChildDisallowedTools {
				if !disallowed[want] {
					t.Errorf("SessionSpec.DisallowedTools missing %q for agent type %q (got %v)", want, agentType, spec.DisallowedTools)
				}
			}

			allowed := make(map[string]bool, len(spec.AllowedTools))
			for _, name := range spec.AllowedTools {
				allowed[name] = true
			}
			for _, name := range rootinit.ChildDisallowedTools {
				if allowed[name] {
					t.Errorf("SessionSpec.AllowedTools contains harness-only tool %q for agent type %q (allowed=%v)", name, agentType, spec.AllowedTools)
				}
			}
		})
	}
}

// TestBuildAgentSessionSpec_DisallowedRoundTripsToLaunchArgs verifies that
// the SessionSpec.DisallowedTools list, when threaded through
// claudecli.LaunchOpts, produces a `--disallowed-tools <name>` flag for each
// pinned name. Catches regressions where the field is set on SessionSpec but
// not propagated into the actual claude argv.
func TestBuildAgentSessionSpec_DisallowedRoundTripsToLaunchArgs(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "engineer-agent",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/test",
		SessionID: "sess-engineer",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)

	args := claude.LaunchOpts{DisallowedTools: spec.DisallowedTools}.BuildArgs()

	for _, name := range rootinit.ChildDisallowedTools {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--disallowed-tools" && args[i+1] == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("claude argv missing `--disallowed-tools %s` (got %v)", name, args)
		}
	}
}

func TestBuildAgentSessionSpec_BaseFields(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/finn",
		TreePath:  "weave/finn",
		SessionID: "sess-finn",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)

	// AllowedTools are set by the caller (runtime_launcher) via
	// RunnerDeps.AllowedTools, not by BuildAgentSessionSpec itself.
	// Verify base fields are correct.
	if spec.Identity != "finn" {
		t.Errorf("Identity = %q, want \"finn\"", spec.Identity)
	}
	if spec.SessionID != "sess-finn" {
		t.Errorf("SessionID = %q, want \"sess-finn\"", spec.SessionID)
	}
	if spec.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want \"bypassPermissions\"", spec.PermissionMode)
	}
}

func TestBuildAgentSessionSpec_ModelByAgentType(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantModel string
	}{
		{"engineer gets opus", "engineer", "opus"},
		{"researcher gets opus", "researcher", "opus"},
		{"manager gets opus[1m]", "manager", "opus[1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      tt.agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			// The real seed, for the reason given in
			// TestBuildAgentSessionSpec_EffortLowRoundTripsToLaunchArgs. The
			// per-type defaults asserted here now hold via the SEED agreeing
			// with rootinit.ModelForAgentType, which
			// TestSeeds_AgreeWithTheCompiledInDefaults asserts directly.
			c, err := card.SeedForType(tt.agentType)
			if err != nil {
				t.Fatalf("card.SeedForType(%q): %v", tt.agentType, err)
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, c)
			if spec.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q for agent type %q", spec.Model, tt.wantModel, tt.agentType)
			}
			if spec.Effort != "low" {
				t.Errorf("Effort = %q, want \"low\"", spec.Effort)
			}
		})
	}
}

// TestBuildAgentSessionSpec_ExplicitModelBeatsTypeDefault pins QUM-851: a
// non-empty AgentState.Model overrides the per-type default; an empty Model
// falls back to ModelForAgentType.
func TestBuildAgentSessionSpec_ExplicitModelBeatsTypeDefault(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		model     string
		wantModel string
	}{
		{"explicit model overrides engineer default", "engineer", "opus[1m]", "opus[1m]"},
		{"explicit fable overrides manager default", "manager", "fable", "fable"},
		{"empty model falls back to engineer default", "engineer", "", "opus"},
		{"empty model falls back to manager default", "manager", "", "opus[1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      tt.agentType,
				Model:     tt.model,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)
			if spec.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q (type=%q, explicit=%q)", spec.Model, tt.wantModel, tt.agentType, tt.model)
			}
		})
	}
}

// TestBuildAgentSessionSpec_NoAgentsArgv pins QUM-716 (#4.5): the `--agents`
// plumbing has been removed end-to-end. No agent type — including engineer —
// should produce a claude argv containing `--agents`.
func TestBuildAgentSessionSpec_NoAgentsArgv(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "weave", "qa"} {
		t.Run(agentType, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)
			args := claude.LaunchOpts{
				Model:           spec.Model,
				Effort:          spec.Effort,
				PermissionMode:  spec.PermissionMode,
				SessionID:       spec.SessionID,
				AllowedTools:    spec.AllowedTools,
				DisallowedTools: spec.DisallowedTools,
			}.BuildArgs()
			for _, a := range args {
				if a == "--agents" {
					t.Errorf("argv contains --agents for agent type %q (QUM-716 regression): %v", agentType, args)
				}
			}
		})
	}
}

// TestBuildAgentSessionSpec_EnablesReplayUserMessages pins QUM-817: every child
// agent must be launched with --replay-user-messages so the CLI echoes each
// consumed stdin user message back on stdout as an isReplay frame. That echo is
// the consumption ack the runtime keys delivery confirmation (MarkDelivered),
// task completion, and no-reinjection on. Without it, the entire Slice-2
// input-path contract silently breaks (messages re-inject every wake; tasks
// never mark done).
func TestBuildAgentSessionSpec_EnablesReplayUserMessages(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "test-agent",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/test",
		SessionID: "sess-test",
	}
	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)
	if !spec.ReplayUserMessages {
		t.Fatal("SessionSpec.ReplayUserMessages = false, want true (QUM-817 consumption ack)")
	}
	// Round-trip to the actual claude argv.
	args := claude.LaunchOpts{
		InputFormat:        "stream-json",
		ReplayUserMessages: spec.ReplayUserMessages,
	}.BuildArgs()
	found := false
	for _, a := range args {
		if a == "--replay-user-messages" {
			found = true
		}
	}
	if !found {
		t.Errorf("claude argv missing --replay-user-messages: %v", args)
	}
}

// TestBuildAgentSessionSpec_EffortLowRoundTripsToLaunchArgs pins QUM-1276:
// every child agent type launches at `--effort low`. Asserting the struct
// field alone is not enough — BuildArgs drops Effort entirely when empty, so
// the flag itself is what has to be pinned.
//
// "weave" is in the list because TestBuildAgentSessionSpec_NoAgentsArgv drives
// that type through this function too; production builds weave's spec in
// buildEnterSessionSpec instead, which cmd/enter_backend_test.go covers.
func TestBuildAgentSessionSpec_EffortLowRoundTripsToLaunchArgs(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "qa", "weave"} {
		t.Run(agentType, func(t *testing.T) {
			agentState := &state.AgentState{
				Name:      "test-agent",
				Type:      agentType,
				Worktree:  "/tmp/worktrees/test",
				SessionID: "sess-test",
			}
			// The REAL seed, not nil: after QUM-1251 production resolves a card
			// on every launch, so passing nil here would leave this pin
			// measuring a configuration production never sends. "weave" has no
			// card of its own, and nil is the honest input for it — the resolver
			// hands that case the engineer card, which this function cannot see.
			//
			// The "weave" miss is expected and is the ONLY expected miss, so it
			// is named rather than absorbed by a bare `err != nil { c = nil }`.
			// That swallow would have degraded this pin into exactly the nil-card
			// configuration the comment above says production never sends, and
			// silently: typo the //go:embed glob and every type would fall to nil
			// while the test stayed green claiming it exercised the real-seed path.
			var c *card.Card
			seed, err := card.SeedForType(agentType)
			switch {
			case err == nil:
				c = seed
			case agentType != "weave":
				t.Fatalf("SeedForType(%q): %v", agentType, err)
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, c)

			args := claude.LaunchOpts{
				Model:          spec.Model,
				Effort:         spec.Effort,
				PermissionMode: spec.PermissionMode,
				SessionID:      spec.SessionID,
			}.BuildArgs()

			if !argsContainPair(args, "--effort", "low") {
				t.Errorf("claude argv missing `--effort low` for agent type %q (got %v)", agentType, args)
			}
		})
	}
}

// argsContainPair reports whether flag is immediately followed by value in args.
// Adjacency matters: a substring/join match is satisfied by unrelated tokens.
func argsContainPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// TestSeeds_AgreeWithTheCompiledInDefaults is what keeps the two tests above
// honest after QUM-1251.
//
// Before cards, "engineer launches at opus" and "every child launches at
// --effort low" were properties of Go: rootinit.ModelForAgentType and a literal
// "low" in BuildAgentSessionSpec. Now the card supplies both, so those tests
// pass only because every seed happens to AGREE with the compiled-in default.
// That agreement is exactly the kind of coincidence that rots silently — edit a
// seed's model and TestBuildAgentSessionSpec_ModelByAgentType starts failing
// somewhere that says nothing about seeds — so it is asserted here directly.
//
// It lives in this package rather than internal/card because internal/card
// cannot import rootinit: rootinit already depends on card (via internal/agent),
// so the test would be an import cycle.
func TestSeeds_AgreeWithTheCompiledInDefaults(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "qa"} {
		t.Run(agentType, func(t *testing.T) {
			c, err := card.SeedForType(agentType)
			if err != nil {
				t.Fatalf("card.SeedForType(%q): %v", agentType, err)
			}
			if want := rootinit.ModelForAgentType(agentType); c.Model != want {
				t.Errorf("seed %s@%d declares model %q but ModelForAgentType(%q) is %q — the two disagree, so which one a child launches with now depends on whether a card resolved",
					c.Name, c.Version, c.Model, agentType, want)
			}
			// QUM-1276 is now a property of the seeds. Asserted on the card
			// rather than on the spec because the spec's fallback would mask an
			// empty Effort, and an empty one is what BuildArgs drops entirely.
			if c.Effort != "low" {
				t.Errorf("seed %s@%d declares effort %q, want \"low\" — every child agent launches at low effort (QUM-1276)", c.Name, c.Version, c.Effort)
			}
			if !slices.Contains(rootinit.ValidSpawnModels, c.Model) {
				t.Errorf("seed %s@%d declares model %q, which is not in rootinit.ValidSpawnModels %v — `claude --model` would reject it at launch",
					c.Name, c.Version, c.Model, rootinit.ValidSpawnModels)
			}
		})
	}
}

// TestBuildAgentSessionSpec_CardGovernsModelAndEffort is AC2 at the seam AC2
// names: change the card, and the SessionSpec the child launches with changes.
//
// Asserted through BuildArgs as well as on the struct, because BuildArgs drops
// Effort entirely when empty — so the struct field alone does not pin that the
// flag reaches the CLI.
func TestBuildAgentSessionSpec_CardGovernsModelAndEffort(t *testing.T) {
	agentState := &state.AgentState{
		Name:      "test-agent",
		Type:      "engineer",
		Worktree:  "/tmp/worktrees/test",
		SessionID: "sess-test",
	}
	// Deliberately unlike both the engineer seed (opus/low) and the per-type
	// default, so neither can satisfy the assertions below.
	c := &card.Card{
		Name: "slim-engineer", Version: 2, AgentType: "engineer",
		Model: "haiku", Effort: "high", Body: "b",
	}

	spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, c)

	if spec.Model != "haiku" {
		t.Errorf("Model = %q, want \"haiku\" from the card — the card is what governs the model (AC2)", spec.Model)
	}
	if spec.Effort != "high" {
		t.Errorf("Effort = %q, want \"high\" from the card", spec.Effort)
	}
	args := claude.LaunchOpts{
		Model: spec.Model, Effort: spec.Effort,
		PermissionMode: spec.PermissionMode, SessionID: spec.SessionID,
	}.BuildArgs()
	if !argsContainPair(args, "--model", "haiku") {
		t.Errorf("claude argv missing `--model haiku`: %v", args)
	}
	if !argsContainPair(args, "--effort", "high") {
		t.Errorf("claude argv missing `--effort high`: %v", args)
	}
}

// TestBuildAgentSessionSpec_ModelPrecedence pins all three layers at once.
//
// The card-vs-nil axis is explicit in the table: passing a card everywhere would
// leave the no-card path unmeasured, and passing nil everywhere would leave the
// card path unmeasured — and each of those is the configuration of a real
// deployment (the event log is off by default).
func TestBuildAgentSessionSpec_ModelPrecedence(t *testing.T) {
	cardWith := func(model string) *card.Card {
		return &card.Card{Name: "c", Version: 1, AgentType: "engineer", Model: model, Effort: "low", Body: "b"}
	}
	for _, tt := range []struct {
		name       string
		agentType  string
		explicit   string
		card       *card.Card
		wantModel  string
		wantEffort string
	}{
		{"no card falls back to the type default", "engineer", "", nil, "opus", "low"},
		{"no card, manager type default", "manager", "", nil, "opus[1m]", "low"},
		{"the card beats the type default", "engineer", "", cardWith("fable"), "fable", "low"},
		{"an explicit model beats the card", "engineer", "sonnet", cardWith("fable"), "sonnet", "low"},
		{"an explicit model beats the type default", "engineer", "sonnet", nil, "sonnet", "low"},
		{"a card with no model keeps the type default", "manager", "", cardWith(""), "opus[1m]", "low"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			agentState := &state.AgentState{
				Name: "test-agent", Type: tt.agentType, Model: tt.explicit,
				Worktree: "/tmp/worktrees/test", SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, tt.card)
			if spec.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q (type=%q explicit=%q card=%v)", spec.Model, tt.wantModel, tt.agentType, tt.explicit, tt.card != nil)
			}
			if spec.Effort != tt.wantEffort {
				t.Errorf("Effort = %q, want %q", spec.Effort, tt.wantEffort)
			}
		})
	}
}

// TestBuildAgentSessionSpec_NilCardStillLaunchesAtLowEffort is the AC3 half of
// this seam: with the event log unreachable the resolver hands back a seed, but
// with no card at all — the pre-M2 shape, and what a nil pointer here means —
// the effort floor must still hold. A nil card that produced an EMPTY Effort
// would drop `--effort` from the argv entirely and silently launch the child at
// the CLI's default.
func TestBuildAgentSessionSpec_NilCardStillLaunchesAtLowEffort(t *testing.T) {
	for _, agentType := range []string{"engineer", "researcher", "manager", "qa", "weave"} {
		t.Run(agentType, func(t *testing.T) {
			agentState := &state.AgentState{
				Name: "test-agent", Type: agentType,
				Worktree: "/tmp/worktrees/test", SessionID: "sess-test",
			}
			spec := BuildAgentSessionSpec(agentState, "/tmp/prompt.md", "/tmp/root", io.Discard, nil)
			args := claude.LaunchOpts{
				Model: spec.Model, Effort: spec.Effort,
				PermissionMode: spec.PermissionMode, SessionID: spec.SessionID,
			}.BuildArgs()
			if !argsContainPair(args, "--effort", "low") {
				t.Errorf("claude argv missing `--effort low` with no card for type %q (got %v)", agentType, args)
			}
		})
	}
}
