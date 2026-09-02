// QUM-1252 AC5: the stall sweeper's threshold knob.
//
// A duration-STRING key on the idle_reclaim.* precedent, and for the same
// reason: Load never prefills, so an absent int key would decode to 0 and 0 has
// to mean something. Here the two ends differ from the reaper's, and THAT is
// what these tests pin — 0 must NOT mean "poke instantly". A threshold of zero
// makes every open goal a stall candidate on the first sweep, which is a
// token-burn footgun reachable by typing `0` into a config file, so 0 falls back
// to the default and the only way to shorten the threshold is to say a duration.
package config

import (
	"strings"
	"testing"
	"time"
)

func TestGoalStallAfter_UnsetIsTheDefault(t *testing.T) {
	c := &Config{}
	got, err := c.GoalStallAfterDuration()
	if err != nil {
		t.Fatalf("GoalStallAfterDuration() error = %v, want nil for an unset key", err)
	}
	if got != DefaultGoalStallAfter {
		t.Errorf("GoalStallAfterDuration() = %v for an unset key, want the default %v", got, DefaultGoalStallAfter)
	}
	if DefaultGoalStallAfter != 30*time.Minute {
		t.Errorf("DefaultGoalStallAfter = %v, want 30m — it must agree with store.DefaultStallAfter, which is what the sweeper falls back to when nothing is wired", DefaultGoalStallAfter)
	}
}

func TestGoalStallAfter_Parses(t *testing.T) {
	c := &Config{GoalStallAfter: " 45s "}
	got, err := c.GoalStallAfterDuration()
	if err != nil {
		t.Fatalf("GoalStallAfterDuration() error = %v", err)
	}
	if got != 45*time.Second {
		t.Errorf("GoalStallAfterDuration() = %v, want 45s", got)
	}
}

// TestGoalStallAfter_ZeroFallsBackToTheDefault is the one that matters, and it
// is a DELIBERATE DIVERGENCE from IdleReclaimAfterDuration, where an explicit
// zero disables the feature. There is no "disabled" reading of a zero stall
// threshold: it does not switch the sweeper off, it makes every open goal
// stalled the moment it is opened. So zero is refused as a value and the default
// stands. A caller that wants a short threshold says so in seconds.
func TestGoalStallAfter_ZeroFallsBackToTheDefault(t *testing.T) {
	for _, raw := range []string{"0", "0s", "-5m"} {
		c := &Config{GoalStallAfter: raw}
		got, err := c.GoalStallAfterDuration()
		if err == nil {
			t.Errorf("GoalStallAfterDuration() with goal_stall.after = %q returned no error; a non-positive threshold must be reported, not silently accepted", raw)
		} else if !strings.Contains(err.Error(), "goal_stall.after") {
			t.Errorf("error should name the key, got: %v", err)
		}
		if got != DefaultGoalStallAfter {
			t.Errorf("GoalStallAfterDuration() with goal_stall.after = %q = %v, want the default %v — a zero threshold makes every open goal a stall candidate immediately", raw, got, DefaultGoalStallAfter)
		}
	}
}

func TestGoalStallAfter_UnparseableReportsAndDefaults(t *testing.T) {
	c := &Config{GoalStallAfter: "30min"}
	got, err := c.GoalStallAfterDuration()
	if err == nil {
		t.Fatal("GoalStallAfterDuration() returned no error for \"30min\"; a typo must be reported, not read as the default")
	}
	if got != DefaultGoalStallAfter {
		t.Errorf("GoalStallAfterDuration() = %v alongside the error, want the default %v so a caller that ignores the error still gets a safe threshold", got, DefaultGoalStallAfter)
	}
}

// TestGoalStallAfter_LoadNeverPrefills guards the property the whole
// string-key design rests on: if Load prefilled a default, "absent" and
// "explicitly set to the default" would be indistinguishable and the zero rule
// above would have nothing to key on.
func TestGoalStallAfter_LoadNeverPrefills(t *testing.T) {
	root := writeIdleConfig(t, "event_log.enabled: true\n")
	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GoalStallAfter != "" {
		t.Errorf("Load prefilled goal_stall.after = %q, want \"\"", c.GoalStallAfter)
	}
}

func TestGoalStallAfter_LoadReadsTheKey(t *testing.T) {
	root := writeIdleConfig(t, "goal_stall.after: 90s\n")
	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := c.GoalStallAfterDuration()
	if err != nil {
		t.Fatalf("GoalStallAfterDuration() error = %v", err)
	}
	if got != 90*time.Second {
		t.Errorf("GoalStallAfterDuration() = %v, want 90s — the key did not survive Load", got)
	}
}
