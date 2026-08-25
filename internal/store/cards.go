package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/jackc/pgx/v5"
)

// CardForType reads the current published agent card for an agent type.
//
// This is on the SPAWN path: every launch resolves the card for the agent it is
// about to start. It therefore sits under the store's standing constraint that
// agents never brick on the store — so it reports every problem as an ERROR and
// leaves the decision to the caller, which falls back to the compiled-in seed.
//
// It never returns a zero-valued card alongside a nil error, and that is the
// point rather than a style preference: a zero card has an empty Body, so a
// caller that treated one as success would launch an agent with an EMPTY system
// prompt — no role, no guardrails, nothing. An error routes the same situation to
// the embedded seed instead.
//
// Highest version wins. First-match would keep serving legacy@1 forever once the
// slim v2 cards land, and would look correct while doing it, because today every
// agent type has exactly one version.
func (l *Ledger) CardForType(ctx context.Context, agentType string) (*card.Card, error) {
	// Nil-safe like every other Ledger method (Enabled, ProjectID, Pool): a nil
	// Ledger is the store-disabled default, which is the majority
	// configuration, and a method that panicked on it would take the spawn path
	// down for everyone. Pool() is checked as well as l, because a DEGRADED
	// Ledger is Enabled and has no pool — a guard keyed on Enabled() alone waves
	// it through and then dereferences nil.
	if l == nil || l.Pool() == nil {
		return nil, fmt.Errorf("store: no usable event log to read the %q agent card from", agentType)
	}

	var (
		c         card.Card
		renderRaw []byte
	)
	err := l.Pool().QueryRow(ctx,
		`SELECT name, version, agent_type, description, prompt, model, effort, content_sha256, render
		   FROM agent_cards
		  WHERE agent_type = $1
		  ORDER BY version DESC
		  LIMIT 1`, agentType).
		Scan(&c.Name, &c.Version, &c.AgentType, &c.Description, &c.Body, &c.Model, &c.Effort, &c.ContentSHA256, &renderRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: no published agent card for type %q", agentType)
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading the %q agent card: %w", agentType, err)
	}

	// Refused rather than read as all-false. ParseRenderOpts explains why at
	// length; the short form is that these flags gate the sub-agent banner and
	// the sandbox warning, and "nobody specified" is indistinguishable from
	// "explicitly none" once it is in a struct.
	c.Opts, err = card.ParseRenderOpts(renderRaw)
	if err != nil {
		return nil, fmt.Errorf("store: the published %q agent card has unusable render options: %w", agentType, err)
	}
	return &c, nil
}
