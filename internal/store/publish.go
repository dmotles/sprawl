package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// `sprawl def publish` and `sprawl def list`: the operator-facing half of cards
// as versioned data (QUM-1251).
//
// BINDING PROHIBITION — publish appends NO EVENT.
//
// QUM-1262 asked whether publishing needs DB-fallback schema validation. It does
// not, and the reason is structural rather than a decision that can be revisited
// casually: `agent_cards` (migration 00001) has no schema_id column and no FK to
// event_type_schemas, so there is no event and no schema id anywhere on this
// path. A later slice that decides publish should also append a `card_published`
// event inherits the QUM-1262 constraint at that moment — it must resolve its
// schema id through SchemaResolver (embedded registry first, DB lookup for
// unknown ids) and must refuse to publish when neither can validate it. Adding
// the event without that resolution is the specific mistake this note exists to
// prevent.

// ErrCardAlreadyPublished reports that name@version already exists.
//
// This is the AC1 immutability refusal, and it is a DATABASE refusal: the INSERT
// below deliberately carries no ON CONFLICT clause, so agent_cards' UNIQUE
// (name, version) raises 23505 and publish reports it. ON CONFLICT DO NOTHING —
// which is exactly right for syncSeedCards, and therefore the obvious thing to
// copy — would report a rejected republish as a success while every spawn kept
// serving the old card.
var ErrCardAlreadyPublished = errors.New("store: a card with this name and version is already published")

// cardColumns is the ONE column list every agent_cards statement is built from.
//
// Shared rather than repeated because a drift between the write list and the read
// list is silent in the worst direction: publish writes a row, CardForType cannot
// read it, and the spawn path falls back to the embedded seed — so the operator
// sees a successful publish and an agent that ignores it.
const cardColumns = `name, version, agent_type, description, prompt, model, effort, content_sha256, render`

const publishCardSQL = `INSERT INTO agent_cards (id, ` + cardColumns + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

const listCardsSQL = `SELECT ` + cardColumns + `
	  FROM agent_cards
	 ORDER BY agent_type, version`

// CardStore is the write surface publish needs. Satisfied by *pgxpool.Pool.
type CardStore interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// CardQuerier is the multi-row read surface ListCards needs. Satisfied by
// *pgxpool.Pool.
type CardQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PublishCard writes one card to agent_cards.
//
// db must be a PRIVILEGED pool, not the agent-facing one: migration 00002 grants
// sprawl_app SELECT only on agent_cards, by design, and that grant must not be
// widened to let a running agent rewrite the definitions its siblings launch
// from. `sprawl def publish` resolves the admin DSN for this the same way
// `sprawl store migrate` does.
func PublishCard(ctx context.Context, db CardStore, c *card.Card) error {
	renderJSON, err := json.Marshal(c.Opts)
	if err != nil {
		return fmt.Errorf("store: encoding render options for %s@%d: %w", c.Name, c.Version, err)
	}
	_, err = db.Exec(ctx, publishCardSQL,
		c.ID(), c.Name, c.Version, c.AgentType, c.Description, c.Body, c.Model, c.Effort, c.ContentSHA256, renderJSON)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolationPublish {
		return fmt.Errorf("store: %s@%d: %w: %w", c.Name, c.Version, ErrCardAlreadyPublished, err)
	}
	return fmt.Errorf("store: publishing card %s@%d: %w", c.Name, c.Version, err)
}

// sqlStateUniqueViolationPublish is pg's unique_violation. Spelled out here
// rather than imported from the integration tests, which are behind a build tag
// this file is not.
const sqlStateUniqueViolationPublish = "23505"

// PublishedCard is one row of agent_cards as `sprawl def list` reports it.
type PublishedCard struct {
	*card.Card
	// RenderErr is the reason this row's render options could not be decoded, or
	// "" if they decoded. Carried per row rather than returned as an error
	// because ListCards must not lose the other rows over one bad one — see
	// ListCards — but must not report a row it could not fully read as fine
	// either.
	RenderErr string
}

// ListCards reads every published card, ordered by (agent_type, version).
//
// Its job is the OPPOSITE of CardForType's. CardForType is on the spawn path and
// refuses any row it cannot fully read, because a half-read card launches an
// agent with a broken system prompt. `def list` is the diagnostic an operator
// reaches for precisely BECAUSE something is wrong, so one unreadable row must
// not blank the listing that would show them which row it is.
func ListCards(ctx context.Context, q CardQuerier) ([]PublishedCard, error) {
	rows, err := q.Query(ctx, listCardsSQL)
	if err != nil {
		return nil, fmt.Errorf("store: listing agent cards: %w", err)
	}
	defer rows.Close()

	var out []PublishedCard
	for rows.Next() {
		var (
			c         card.Card
			renderRaw []byte
		)
		if err := rows.Scan(&c.Name, &c.Version, &c.AgentType, &c.Description, &c.Body,
			&c.Model, &c.Effort, &c.ContentSHA256, &renderRaw); err != nil {
			return nil, fmt.Errorf("store: reading an agent_cards row: %w", err)
		}
		pc := PublishedCard{Card: &c}
		if c.Opts, err = card.ParseRenderOpts(renderRaw); err != nil {
			pc.RenderErr = err.Error()
		}
		out = append(out, pc)
	}
	// rows.Err() is the one failure in a pgx scan loop that shows up nowhere
	// else: a connection that dies mid-iteration simply stops yielding rows, so
	// an unchecked Err() turns a truncated listing into a short one that looks
	// complete — and `def list` is AC1's evidence.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterating agent_cards: %w", err)
	}
	return out, nil
}
