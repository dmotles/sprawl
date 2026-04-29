package cmd

import (
	"strings"
	"testing"
)

func TestSpawn_StandaloneCLIRejectedAfterSameProcessCutover(t *testing.T) {
	err := runSpawn(nil, "engineering", "engineer", "implement login page", "feature/login")
	if err == nil {
		t.Fatal("expected standalone sprawl spawn rejection")
	}
	if !strings.Contains(err.Error(), "sprawl enter") || !strings.Contains(err.Error(), "sprawl_spawn") {
		t.Fatalf("error = %q, want sprawl enter + sprawl_spawn guidance", err)
	}
}
