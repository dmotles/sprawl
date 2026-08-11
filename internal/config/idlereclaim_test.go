// QUM-1186 lane 3: the idle-reaper config knobs.
//
// These are duration-STRING keys, not int keys, and that is the load-bearing
// design decision here. Load deliberately never prefills (config.go's struct
// doc), so an absent int key decodes to 0 — and the spec wants 0 to mean
// DISABLED. An int knob would therefore ship the reaper switched off for every
// user who has never edited their config, silently, which is exactly the
// guard-evaporation class this repo keeps paying for. With a string key,
// absent ("") and an explicit "0s" are distinguishable, so "absent → default"
// and "0 → disabled" can both be true at once.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeIdleConfig writes a .sprawl/config.yaml under a fresh root and returns
// the root.
func writeIdleConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return root
}

// TestIdleReclaimAfter_DefaultsToDisabled pins a DELIBERATE REVERSAL. This
// test previously asserted the opposite — that an absent key must not disable
// the reaper — on the reasoning that a silently-off memory reclaimer is the
// classic guard-evaporation failure. That reasoning was right in general and
// wrong here, and the thing that changed it was evidence, not taste:
// scripts/e2e-tests/idle-reclaim.sh's busy-agent control reproduced, twice on a
// clean host, an agent being torn down with a live `sleep` still in its process
// tree. Until QUM-1197 gives the predicate a turn signal it can trust, ON is
// the unsafe default. The reaper is not silently off — NewReal logs DISABLED
// with the reason and the enabling command on every start.
func TestIdleReclaimAfter_DefaultsToDisabled(t *testing.T) {
	c := &Config{}
	got, err := c.IdleReclaimAfterDuration()
	if err != nil {
		t.Fatalf("IdleReclaimAfterDuration() error = %v, want nil for an unset key", err)
	}
	if got != 0 {
		t.Errorf("IdleReclaimAfterDuration() = %v for an unset key, want 0 (disabled) — enabling by default reaps agents mid-tool-call (QUM-1197)", got)
	}
	if DefaultIdleReclaimAfter != 0 {
		t.Errorf("DefaultIdleReclaimAfter = %v, want 0", DefaultIdleReclaimAfter)
	}
	// The intended post-QUM-1197 value is kept as its own constant so the
	// reversal did not throw it away.
	if SuggestedIdleReclaimAfter != 15*time.Minute {
		t.Errorf("SuggestedIdleReclaimAfter = %v, want 15m", SuggestedIdleReclaimAfter)
	}
}

// TestIdleReclaimAfter_LoadDoesNotPrefill is the other half of the design: if
// Load prefilled the default, Save would freeze today's default into the user's
// file and "absent" would stop being distinguishable from "explicitly set".
func TestIdleReclaimAfter_LoadDoesNotPrefill(t *testing.T) {
	root := writeIdleConfig(t, "validate: make validate\n")
	c, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.IdleReclaimAfter != "" {
		t.Errorf("Load prefilled idle_reclaim.after = %q, want \"\" (Load must never prefill defaults)", c.IdleReclaimAfter)
	}
	if c.IdleReclaimSweep != "" {
		t.Errorf("Load prefilled idle_reclaim.sweep = %q, want \"\"", c.IdleReclaimSweep)
	}
}

func TestIdleReclaimAfter_ExplicitZeroDisables(t *testing.T) {
	for _, raw := range []string{"0", "0s", "0m"} {
		c := &Config{IdleReclaimAfter: raw}
		got, err := c.IdleReclaimAfterDuration()
		if err != nil {
			t.Fatalf("IdleReclaimAfter(%q) error = %v, want nil", raw, err)
		}
		if got != 0 {
			t.Errorf("IdleReclaimAfter(%q) = %v, want 0 (explicit zero disables the reaper)", raw, got)
		}
	}
}

func TestIdleReclaimAfter_ParsesExplicitValue(t *testing.T) {
	c := &Config{IdleReclaimAfter: "90s"}
	got, err := c.IdleReclaimAfterDuration()
	if err != nil {
		t.Fatalf("IdleReclaimAfterDuration() error = %v", err)
	}
	if got != 90*time.Second {
		t.Errorf("IdleReclaimAfterDuration() = %v, want 90s", got)
	}
}

// TestIdleReclaimAfter_UnparseableIsNotSilentlyDisabled is the trap
// ValidateTimeoutDuration falls into (config.go returns 0 on a parse error).
// Copying that arm here would make a typo — "15min" — silently switch off a
// memory reclaimer with no error anywhere. The accessor must return the default
// AND a non-nil error, never 0.
// TestIdleReclaimAfter_UnparseableIsNotSilentlyAccepted: the anti-typo property
// survives the reversal, but it now rests entirely on the ERROR rather than on
// the returned value. With the default at 0 a typo lands on the same value as
// "off", so the value can no longer distinguish them — the error is the only
// thing that can, which is why it is asserted to name the key. Note the
// consequence, stated rather than discovered later: someone ENABLING the reaper
// with a typo gets it off, and their only signal is NewReal's WARN.
func TestIdleReclaimAfter_UnparseableIsNotSilentlyAccepted(t *testing.T) {
	c := &Config{IdleReclaimAfter: "15min"}
	got, err := c.IdleReclaimAfterDuration()
	if err == nil {
		t.Fatal("IdleReclaimAfter(\"15min\") error = nil, want a parse error; a typo must not pass silently")
	}
	if got != DefaultIdleReclaimAfter {
		t.Errorf("IdleReclaimAfter(\"15min\") = %v, want the default %v alongside the error", got, DefaultIdleReclaimAfter)
	}
	if !strings.Contains(err.Error(), "idle_reclaim.after") {
		t.Errorf("error %q does not name the key it came from", err)
	}
}

func TestIdleReclaimSweep_DefaultWhenUnset(t *testing.T) {
	c := &Config{}
	got, err := c.IdleReclaimSweepDuration()
	if err != nil {
		t.Fatalf("IdleReclaimSweepDuration() error = %v, want nil", err)
	}
	if got != DefaultIdleReclaimSweep {
		t.Errorf("IdleReclaimSweepDuration() = %v, want %v", got, DefaultIdleReclaimSweep)
	}
}

func TestIdleReclaimSweep_UnparseableIsNotSilentlyDisabled(t *testing.T) {
	c := &Config{IdleReclaimSweep: "banana"}
	got, err := c.IdleReclaimSweepDuration()
	if err == nil {
		t.Fatal("IdleReclaimSweep(\"banana\") error = nil, want a parse error")
	}
	if got != DefaultIdleReclaimSweep {
		t.Errorf("IdleReclaimSweep(\"banana\") = %v, want the default %v", got, DefaultIdleReclaimSweep)
	}
}

// TestIdleReclaimKeys_AreSettableAndReferenced pins the keys through the
// reflected registry: `sprawl config` must be able to set them, and
// Reference() must advertise them with a default and a purpose. Without this
// the fields could exist while being unreachable from the CLI.
func TestIdleReclaimKeys_AreSettableAndReferenced(t *testing.T) {
	c := &Config{}
	if err := c.Set("idle_reclaim.after", "20m"); err != nil {
		t.Fatalf("Set(idle_reclaim.after): %v", err)
	}
	if got, _ := c.Get("idle_reclaim.after"); got != "20m" {
		t.Errorf("Get(idle_reclaim.after) = %q, want %q", got, "20m")
	}
	if err := c.Set("idle_reclaim.sweep", "30s"); err != nil {
		t.Fatalf("Set(idle_reclaim.sweep): %v", err)
	}

	ref := Reference()
	// "0 (DISABLED)" rather than a bare "0": the reference table is where an
	// operator looks to decide whether a key is safe to set, and a bare 0 reads
	// as "unset" rather than as a deliberate off.
	//
	// The issue key moved QUM-1197 -> QUM-1213, and that is the POINT of the
	// assertion rather than a re-baseline to clear a red: what must be named is
	// the blocker that is actually live. QUM-1197's mechanism was withdrawn and
	// its missing term is now implemented, so a reference table still telling an
	// operator "do not enable until QUM-1197 is fixed" would be a false record
	// about a live decision — the class this whole arc exists to remove.
	// "SIDECHAIN" is pinned because the QUM-1197 ruling of 2026-08-11 promoted
	// that gap from a documented limit to a HARD PRECONDITION on enabling the
	// reaper: no e2e run yet joins the two measured halves of the sidechain case.
	// The reference table is where whoever proposes flipping the knob looks, so
	// the precondition has to be legible there or it is not a gate at all — and a
	// gate nobody reads is how this arc's worst records happened.
	for _, want := range []string{"idle_reclaim.after", "idle_reclaim.sweep", "0 (DISABLED)", "QUM-1213", "SIDECHAIN", "1m"} {
		if !strings.Contains(ref, want) {
			t.Errorf("Reference() is missing %q; got:\n%s", want, ref)
		}
	}
}
