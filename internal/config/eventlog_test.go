package config

import "testing"

// The event-log feature flag (QUM-1249).
//
// It is a STRING and not a bool, and that is forced rather than chosen: this
// package's registry() PANICS on any field kind but string and int (pinned by
// TestRegistry_OnlySupportedKinds), so a bool field would not compile past its
// first test run. The accessor is what turns the string into a decision, and
// these tests pin the parse — including that an unparseable value reads as OFF
// rather than as ON.

func TestEventLogEnabled_DefaultsOff(t *testing.T) {
	c := &Config{}
	if c.EventLogEnabled() {
		t.Error("an absent event_log.enabled must read as OFF — the store is opt-in, and a default-on store would try to reach a database on every host that has never configured one")
	}
}

func TestEventLogEnabled_AcceptsTheUsualTruths(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "True", "1", "yes", "on"} {
		c := &Config{EventLog: v}
		if !c.EventLogEnabled() {
			t.Errorf("event_log.enabled=%q must read as ON", v)
		}
	}
}

func TestEventLogEnabled_AcceptsTheUsualFalsehoods(t *testing.T) {
	for _, v := range []string{"false", "FALSE", "0", "no", "off", ""} {
		c := &Config{EventLog: v}
		if c.EventLogEnabled() {
			t.Errorf("event_log.enabled=%q must read as OFF", v)
		}
	}
}

// TestEventLogEnabled_GarbageReadsAsOff pins the failure direction.
//
// The two directions are not symmetric. A typo read as OFF means an operator who
// meant to enable the store sees no events and goes looking — a visible,
// self-correcting failure. A typo read as ON means every host in the fleet starts
// trying to reach a database that may not exist, on a value nobody intended. Fail
// toward the quiet default.
func TestEventLogEnabled_GarbageReadsAsOff(t *testing.T) {
	for _, v := range []string{"ture", "enabled", "y e s", "maybe", "2"} {
		c := &Config{EventLog: v}
		if c.EventLogEnabled() {
			t.Errorf("event_log.enabled=%q is not a recognised boolean and must read as OFF, not ON", v)
		}
	}
}

// TestEventLogEnabled_IsAReferenceTableKey pins that the key is discoverable.
//
// Every exported field here is a config key and the reference table is derived
// by reflection, so this cannot silently fail — but a key absent from
// `sprawl config show`/`--help` is a feature nobody can find, and the assertion
// costs one line.
func TestEventLogEnabled_IsAReferenceTableKey(t *testing.T) {
	var found bool
	for _, k := range KnownKeys() {
		if k == "event_log.enabled" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("event_log.enabled is not among the accepted config keys %v", KnownKeys())
	}
}
