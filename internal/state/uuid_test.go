package state_test

import (
	"regexp"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// uuidV4Pattern pins the full RFC 4122 v4 textual form: 8-4-4-4-12 lowercase
// hex, the literal version nibble '4' opening the third group, and a variant
// nibble in {8,9,a,b} opening the fourth. The version and variant positions are
// spelled out rather than matched as generic hex because they are the two bits
// GenerateUUID actually sets by hand (buf[6], buf[8]); a generic [0-9a-f]
// pattern there would pass even if both masks were dropped entirely.
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGenerateUUID_ShapeVersionAndVariant(t *testing.T) {
	got, err := state.GenerateUUID()
	if err != nil {
		t.Fatalf("GenerateUUID() error = %v, want nil", err)
	}
	if !uuidV4Pattern.MatchString(got) {
		t.Errorf("GenerateUUID() = %q, want RFC 4122 v4 form %s", got, uuidV4Pattern)
	}
}

// TestGenerateUUID_Distinct guards the property every caller relies on:
// queue entry IDs, spawn IDs and MCP request IDs are keys, so a repeat is a
// silent collision rather than a visible failure.
func TestGenerateUUID_Distinct(t *testing.T) {
	const draws = 1000
	seen := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		got, err := state.GenerateUUID()
		if err != nil {
			t.Fatalf("GenerateUUID() draw %d: error = %v, want nil", i, err)
		}
		if _, dup := seen[got]; dup {
			t.Fatalf("GenerateUUID() returned %q twice within %d draws", got, draws)
		}
		seen[got] = struct{}{}
	}
}
