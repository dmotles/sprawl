package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestRecordLegacySpawn_RefusesWhenTheStoreCannotAnswer.
//
// The three unusable states are kept distinguishable here for the reason they
// are everywhere else in this package: "recorded" and "not recorded because the
// log is off" have different remedies, and a spawn path that could not tell them
// apart would log the same warning for a host that is working as designed and a
// host whose database has gone away.
func TestRecordLegacySpawn_RefusesWhenTheStoreCannotAnswer(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	for _, tc := range []struct {
		name   string
		ledger *Ledger
		want   string
	}{
		{"nil ledger is the disabled store", nil, "disabled"},
		{"zero ledger is the disabled store", &Ledger{}, "disabled"},
		{"degraded ledger", &Ledger{enabled: true, degradedErr: boom}, "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := tc.ledger.RecordLegacySpawn(context.Background(), LegacyAgent{AgentName: "finn", AgentType: "engineer"})
			if err == nil {
				t.Fatalf("RecordLegacySpawn returned %s and no error over an unusable store", id)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q, so the operator cannot tell which state this is", err, tc.want)
			}

			closeID, err := tc.ledger.RecordLegacyRetire(context.Background(), "finn", "retired", false)
			if err == nil {
				t.Fatalf("RecordLegacyRetire returned %s and no error over an unusable store", closeID)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("retire error %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestRecordLegacySpawn_RefusesAnUnidentifiableAgent.
//
// Both fields are refused BEFORE the store gate, so these run against a disabled
// ledger and still have to fail for the argument reason — which is what the
// message assertions pin. A nameless spawn can never be closed (the retire is
// looked up by name) and a typeless one prints as a blank row in `sprawl goals`.
func TestRecordLegacySpawn_RefusesAnUnidentifiableAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent LegacyAgent
		want  string
	}{
		{"no name", LegacyAgent{AgentType: "engineer"}, "agent name"},
		{"no type", LegacyAgent{AgentName: "finn"}, "agent type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (*Ledger)(nil).RecordLegacySpawn(context.Background(), tc.agent)
			if err == nil {
				t.Fatal("an unidentifiable agent was recorded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the missing field (%q)", err, tc.want)
			}
		})
	}
}

// TestRecordLegacyRetire_RefusesABlankOutcome. agent_retired requires it, and a
// close with no outcome is a row saying the work ended without saying how.
func TestRecordLegacyRetire_RefusesABlankOutcome(t *testing.T) {
	_, err := (*Ledger)(nil).RecordLegacyRetire(context.Background(), "finn", "", false)
	if err == nil {
		t.Fatal("a retire with no outcome was accepted")
	}
	if !strings.Contains(err.Error(), "outcome") {
		t.Errorf("error %q does not mention the outcome", err)
	}
}

// TestOpenLegacySpawn_RefusesABlankAgentName. payload->>'agent_name' is NULL on
// events that carry no name, so an empty string is a surprising match rather
// than an empty result.
func TestOpenLegacySpawn_RefusesABlankAgentName(t *testing.T) {
	r := &PgLegacyReader{}
	_, err := r.OpenLegacySpawn(context.Background(), uuid.New(), "")
	if err == nil {
		t.Fatal("a blank agent name was looked up rather than refused")
	}
	if !strings.Contains(err.Error(), "agent name") {
		t.Errorf("error %q does not say an agent name is required", err)
	}
}
