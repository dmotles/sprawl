package usage

import (
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
)

func TestTurnAccumulator_AbsorbSumsTokens(t *testing.T) {
	var a TurnAccumulator
	a.Absorb(protocol.Usage{
		InputTokens:              10,
		OutputTokens:             5,
		CacheReadInputTokens:     100,
		CacheCreationInputTokens: 2,
	}, "claude-opus-4-7")
	a.Absorb(protocol.Usage{
		InputTokens:              4,
		OutputTokens:             3,
		CacheReadInputTokens:     50,
		CacheCreationInputTokens: 1,
	}, "claude-opus-4-7")

	got := a.Usage()
	if got.InputTokens != 14 {
		t.Errorf("InputTokens = %d, want 14", got.InputTokens)
	}
	if got.OutputTokens != 8 {
		t.Errorf("OutputTokens = %d, want 8", got.OutputTokens)
	}
	if got.CacheReadInputTokens != 150 {
		t.Errorf("CacheReadInputTokens = %d, want 150", got.CacheReadInputTokens)
	}
	if got.CacheCreationInputTokens != 3 {
		t.Errorf("CacheCreationInputTokens = %d, want 3", got.CacheCreationInputTokens)
	}
}

func TestTurnAccumulator_LatestNonEmptyModelWins(t *testing.T) {
	var a TurnAccumulator
	a.Absorb(protocol.Usage{InputTokens: 1}, "claude-opus-4-7")
	a.Absorb(protocol.Usage{InputTokens: 1}, "")
	a.Absorb(protocol.Usage{InputTokens: 1}, "claude-opus-4-7-20260301")
	a.Absorb(protocol.Usage{InputTokens: 1}, "")

	if got := a.Model(); got != "claude-opus-4-7-20260301" {
		t.Errorf("Model = %q, want latest non-empty %q", got, "claude-opus-4-7-20260301")
	}
}

func TestTurnAccumulator_HasDataFlips(t *testing.T) {
	var a TurnAccumulator
	if a.HasData() {
		t.Error("HasData before Absorb = true, want false")
	}
	a.Absorb(protocol.Usage{InputTokens: 1}, "m")
	if !a.HasData() {
		t.Error("HasData after Absorb = false, want true")
	}
	a.Reset()
	if a.HasData() {
		t.Error("HasData after Reset = true, want false")
	}
}

func TestTurnAccumulator_ResetZeroesEverything(t *testing.T) {
	var a TurnAccumulator
	a.Absorb(protocol.Usage{InputTokens: 99, OutputTokens: 99}, "m")
	a.Reset()
	got := a.Usage()
	if got != (protocol.Usage{}) {
		t.Errorf("Usage after Reset = %+v, want zero value", got)
	}
	if a.Model() != "" {
		t.Errorf("Model after Reset = %q, want empty", a.Model())
	}
}
