// QUM-725: tests for WalkDeadAncestors. RED phase — the helper does not
// exist yet. These tests pin the contract that drives SendMessage's and
// ReportStatus's "route up to first live ancestor" rerouting.
package supervisor

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// buildProbes builds a LivenessProbe and ParentLookup from two maps for
// table-driven tests.
func buildProbes(
	livs map[string]liveness.AgentLiveness,
	parents map[string]string,
) (LivenessProbe, ParentLookup) {
	liv := func(name string) (liveness.AgentLiveness, bool) {
		v, ok := livs[name]
		return v, ok
	}
	pl := func(name string) (string, error) {
		if _, ok := parents[name]; !ok {
			// unknown agent for parent lookup is treated as no parent.
			return "", nil
		}
		return parents[name], nil
	}
	return liv, pl
}

func TestWalkDeadAncestors_TargetAlive_ReturnsZero(t *testing.T) {
	liv, pl := buildProbes(
		map[string]liveness.AgentLiveness{
			"engineer": liveness.Running,
		},
		map[string]string{"engineer": "weave"},
	)
	live, dead, err := WalkDeadAncestors("engineer", liv, pl)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if live != "" || len(dead) != 0 {
		t.Errorf("alive target: got (live=%q, dead=%v); want (\"\", nil)", live, dead)
	}
}

func TestWalkDeadAncestors_TargetDead_ParentAlive(t *testing.T) {
	liv, pl := buildProbes(
		map[string]liveness.AgentLiveness{
			"engineer": liveness.Died,
			"weave":    liveness.Running,
		},
		map[string]string{"engineer": "weave", "weave": ""},
	)
	live, dead, err := WalkDeadAncestors("engineer", liv, pl)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if live != "weave" {
		t.Errorf("live = %q, want weave", live)
	}
	if len(dead) != 1 || dead[0] != "engineer" {
		t.Errorf("dead = %v, want [engineer]", dead)
	}
}

func TestWalkDeadAncestors_TargetDead_ParentDead_GrandparentAlive(t *testing.T) {
	liv, pl := buildProbes(
		map[string]liveness.AgentLiveness{
			"engineer": liveness.Died,
			"manager":  liveness.Died,
			"weave":    liveness.Running,
		},
		map[string]string{"engineer": "manager", "manager": "weave", "weave": ""},
	)
	live, dead, err := WalkDeadAncestors("engineer", liv, pl)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if live != "weave" {
		t.Errorf("live = %q, want weave", live)
	}
	if len(dead) != 2 || dead[0] != "engineer" || dead[1] != "manager" {
		t.Errorf("dead = %v, want [engineer manager] (chain order)", dead)
	}
}

func TestWalkDeadAncestors_RootWeaveAlive(t *testing.T) {
	// Whole chain to root weave dies; root weave projects Running. Walk
	// terminates at weave.
	liv, pl := buildProbes(
		map[string]liveness.AgentLiveness{
			"engineer": liveness.Died,
			"manager":  liveness.Died,
			"foreman":  liveness.Died,
			"weave":    liveness.Running,
		},
		map[string]string{"engineer": "manager", "manager": "foreman", "foreman": "weave", "weave": ""},
	)
	live, dead, err := WalkDeadAncestors("engineer", liv, pl)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if live != "weave" {
		t.Errorf("live = %q, want weave (root by construction alive)", live)
	}
	if len(dead) != 3 {
		t.Errorf("dead = %v, want 3 hops", dead)
	}
}

func TestWalkDeadAncestors_DepthOverflow_Errors(t *testing.T) {
	// Build a deeply-nested chain of Died nodes that exceeds the 16-deep cap.
	livs := map[string]liveness.AgentLiveness{}
	parents := map[string]string{}
	prev := ""
	for i := 0; i < 32; i++ {
		name := "n" + string(rune('A'+i))
		livs[name] = liveness.Died
		if prev != "" {
			parents[prev] = name
		}
		prev = name
	}
	parents[prev] = "" // chain root with no parent — but still Died.
	liv, pl := buildProbes(livs, parents)
	_, _, err := WalkDeadAncestors("nA", liv, pl)
	if err == nil {
		t.Fatal("expected error on depth overflow, got nil")
	}
}

func TestWalkDeadAncestors_Cycle_Errors(t *testing.T) {
	liv, pl := buildProbes(
		map[string]liveness.AgentLiveness{
			"a": liveness.Died,
			"b": liveness.Died,
		},
		// a -> b -> a (cycle).
		map[string]string{"a": "b", "b": "a"},
	)
	_, _, err := WalkDeadAncestors("a", liv, pl)
	if err == nil {
		t.Fatal("expected error on cycle, got nil")
	}
}

func TestWalkDeadAncestors_UnknownTarget_Errors(t *testing.T) {
	liv, pl := buildProbes(
		map[string]liveness.AgentLiveness{},
		map[string]string{},
	)
	_, _, err := WalkDeadAncestors("ghost", liv, pl)
	if err == nil {
		t.Fatal("expected error when target unknown to probe, got nil")
	}
	// Surface "ghost" somewhere in the error so logs are useful.
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %q, expected to mention unknown name", err)
	}
}

func TestWalkDeadAncestors_ParentLookupError_Propagates(t *testing.T) {
	livs := map[string]liveness.AgentLiveness{"engineer": liveness.Died}
	liv := func(name string) (liveness.AgentLiveness, bool) {
		v, ok := livs[name]
		return v, ok
	}
	wantErr := errors.New("disk read failed")
	pl := func(name string) (string, error) { return "", wantErr }
	_, _, err := WalkDeadAncestors("engineer", liv, pl)
	if err == nil {
		t.Fatal("expected error to propagate from ParentLookup, got nil")
	}
	if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), wantErr.Error()) {
		t.Errorf("err = %v, expected to wrap %v", err, wantErr)
	}
}
