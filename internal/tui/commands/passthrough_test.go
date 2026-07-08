package commands

import "testing"

// TestCompactRegisteredAsPassthrough locks in the QUM-865 registration: /compact
// is a passthrough command that takes optional guidance args, carries a
// description (so it surfaces in the popover), and is gated behind the compact
// capability.
func TestCompactRegisteredAsPassthrough(t *testing.T) {
	var compact *Command
	for _, c := range All() {
		if c.Name == "/compact" {
			cc := c
			compact = &cc
			break
		}
	}
	if compact == nil {
		t.Fatal("/compact not registered")
	}
	if compact.Kind != KindPassthrough {
		t.Errorf("/compact Kind = %v, want KindPassthrough", compact.Kind)
	}
	if !compact.TakesArgs {
		t.Error("/compact must take args (optional guidance)")
	}
	if compact.Capability != CapCompact {
		t.Errorf("/compact Capability = %v, want CapCompact", compact.Capability)
	}
	if compact.Description == "" {
		t.Error("/compact must have a description for the popover")
	}
}

// TestMatchEnabled_GatesCapabilityCommands proves a capability-tagged command is
// matched only when its capability is enabled; otherwise ok=false so the line
// falls through to the backend as ordinary text (QUM-865 AC6).
func TestMatchEnabled_GatesCapabilityCommands(t *testing.T) {
	disabled := func(Capability) bool { return false }
	enabled := func(Capability) bool { return true }

	if _, _, ok := MatchEnabled("/compact focus on tests", disabled); ok {
		t.Error("MatchEnabled(/compact, disabled) ok=true, want false (must fall through)")
	}
	cmd, args, ok := MatchEnabled("/compact focus on tests", enabled)
	if !ok {
		t.Fatal("MatchEnabled(/compact, enabled) ok=false, want true")
	}
	if cmd.Name != "/compact" {
		t.Errorf("cmd.Name = %q, want /compact", cmd.Name)
	}
	if args != "focus on tests" {
		t.Errorf("args = %q, want %q", args, "focus on tests")
	}
}

// TestMatchEnabled_CapNoneAlwaysMatches proves uncapability-gated commands
// (CapNone) match regardless of the predicate — even a never-enable predicate.
func TestMatchEnabled_CapNoneAlwaysMatches(t *testing.T) {
	never := func(Capability) bool { return false }
	if _, _, ok := MatchEnabled("/help", never); !ok {
		t.Error("MatchEnabled(/help, never) ok=false, want true (CapNone is ungated)")
	}
	// A nil predicate must still allow CapNone commands.
	if _, _, ok := MatchEnabled("/help", nil); !ok {
		t.Error("MatchEnabled(/help, nil) ok=false, want true")
	}
	// ...but a nil predicate gates capability commands off.
	if _, _, ok := MatchEnabled("/compact", nil); ok {
		t.Error("MatchEnabled(/compact, nil) ok=true, want false")
	}
}

// TestFilterSortedEnabled_GatesCompact proves the popover feed excludes a
// capability command when its capability is disabled and includes it when
// enabled, while CapNone commands are always present.
func TestFilterSortedEnabled_GatesCompact(t *testing.T) {
	disabled := func(Capability) bool { return false }
	enabled := func(Capability) bool { return true }

	has := func(cmds []Command, name string) bool {
		for _, c := range cmds {
			if c.Name == name {
				return true
			}
		}
		return false
	}

	off := FilterSortedEnabled("comp", disabled)
	if has(off, "/compact") {
		t.Error("FilterSortedEnabled(comp, disabled) includes /compact, want excluded")
	}
	on := FilterSortedEnabled("comp", enabled)
	if !has(on, "/compact") {
		t.Error("FilterSortedEnabled(comp, enabled) missing /compact")
	}
	// CapNone command present regardless of predicate.
	if !has(FilterSortedEnabled("h", disabled), "/help") {
		t.Error("FilterSortedEnabled(h, disabled) missing /help (CapNone must be ungated)")
	}
}
