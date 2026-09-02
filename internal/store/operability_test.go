package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestOperability_RefuseWhenTheStoreCannotAnswer.
//
// "Nothing is outstanding" is the answer an operator acts on by going home, so
// a store that cannot reach Postgres must not be able to produce it. All three
// unusable states, both entry points: a refusal on only one of the two is the
// shape that looks correct in review and leaves the other reporting an empty
// list from a switched-off log.
func TestOperability_RefuseWhenTheStoreCannotAnswer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		ledger *Ledger
		want   string
	}{
		{"nil ledger is the disabled store", nil, "disabled"},
		{"zero ledger", &Ledger{}, "disabled"},
		{"degraded", &Ledger{enabled: true, degradedErr: errors.New("dial tcp: refused")}, "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goals, err := tc.ledger.AllOpenGoals(ctx)
			if err == nil {
				t.Errorf("AllOpenGoals answered %d goal(s) from a store that cannot read", len(goals))
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say the store is %s; got: %v", tc.want, err)
			}
			wfs, err := tc.ledger.OpenWorkflows(ctx)
			if err == nil {
				t.Errorf("OpenWorkflows answered %d workflow(s) from a store that cannot read", len(wfs))
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say the store is %s; got: %v", tc.want, err)
			}
		})
	}
}
