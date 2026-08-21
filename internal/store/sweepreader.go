package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The sweeper's candidate query (QUM-1250).
//
// ONE QUERY, and that is a decision rather than an optimisation. The alternative
// is a Go loop doing five follow-up reads per open goal — last activity, poke
// count, last poke, quarantine marker, other open contracts — on a pass that runs
// on a timer over every open goal in a project. Beyond the round trips, five
// separate reads are five separate chances to get the same predicate subtly
// different, and the whole file is about predicates that fail toward "poke it
// anyway".
//
// EVERY TERM IS DERIVED FROM THE LOG. Nothing here reads a status table or a
// counter: liveness is max(at) over turn-boundary events (Appendix B item 4), the
// epoch is a count of goal_poke events, and quarantine is the existence of a
// goal_stuck event. That is what makes the sweeper's state reconstructible and
// what makes two hosts agree without coordinating.
//
// THE OWNER IS THE PAYLOAD's `owner`, not events.owner_agent_id, for the reason
// notify.go states: the column is a uuid and M1b has no registry mapping agents
// to uuids. Reading it would produce a well-typed value nothing could poke.

// openGoalsSQL assembles one StalledCandidate per open goal.
//
// Reads open_contracts for the opener set — the projection is maintained in the
// append transaction, so it cannot disagree with the log, and the anti-join
// alternative is a full scan per sweep.
//
// The correlated subqueries are deliberate over joins: each one is
// naturally-scalar (a max, two counts, a boolean), and expressing them as joins
// would multiply rows and require a GROUP BY over every selected column — which
// is exactly the shape that silently produces a wrong count when someone adds a
// column later.
//
// `other_open_contracts` is the transitive-block term and EXCLUDES this goal
// itself. Without that exclusion every open goal is its own blocker and the
// sweeper never pokes anything — a total, silent failure that looks like a quiet
// fleet.
const openGoalsSQL = `
	SELECT
	    g.id,
	    g.workflow_instance_id,
	    COALESCE(g.payload->>'owner', '')     AS owner,
	    COALESCE(g.payload->>'goal_type', '') AS goal_type,
	    g.at,
	    (SELECT max(a.at) FROM events a
	      WHERE a.project_id = g.project_id
	        AND a.schema_id = ANY($2)
	        AND a.payload->>'agent_name' = g.payload->>'owner')            AS last_activity,
	    (SELECT count(*) FROM events p
	      WHERE p.project_id = g.project_id
	        AND p.schema_id = ANY($3)
	        AND p.payload->>'goal_event_id' = g.id::text)                  AS pokes,
	    (SELECT max(p.at) FROM events p
	      WHERE p.project_id = g.project_id
	        AND p.schema_id = ANY($3)
	        AND p.payload->>'goal_event_id' = g.id::text)                  AS last_poke_at,
	    EXISTS (SELECT 1 FROM events s
	             WHERE s.project_id = g.project_id
	               AND s.schema_id = ANY($4)
	               AND s.payload->>'goal_event_id' = g.id::text)           AS quarantined,
	    (SELECT count(*) FROM open_contracts oc2
	       JOIN events o2 ON o2.id = oc2.event_id
	      WHERE o2.project_id = g.project_id
	        AND o2.id <> g.id
	        AND COALESCE(o2.payload->>'owner', o2.payload->>'recipient', '')
	            = g.payload->>'owner')                                     AS other_open
	  FROM open_contracts oc
	  JOIN events g ON g.id = oc.event_id
	 WHERE g.project_id = $1
	   AND g.schema_id = ANY($5)
	 ORDER BY g.seq`

// PgSweepReader produces stall candidates through a pgx pool.
type PgSweepReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

var _ SweepReader = (*PgSweepReader)(nil)

// activitySchemaIDs are the turn-boundary types liveness is derived from.
//
// run_started AND turn_finished, both. run_started alone would call an agent
// mid-first-turn dead after the stall threshold; turn_finished alone would call
// an agent that has started but not yet finished a turn dead, which is precisely
// the long-quiet-turn case the deleted heartbeat got wrong.
func activitySchemaIDs(reg *Registry) []uuid.UUID {
	return append(schemaIDsFor(reg, "run_started"), schemaIDsFor(reg, "turn_finished")...)
}

func (r *PgSweepReader) OpenGoals(ctx context.Context, projectID uuid.UUID) ([]StalledCandidate, error) {
	rows, err := r.Pool.Query(ctx, openGoalsSQL,
		projectID,
		activitySchemaIDs(r.Registry),
		schemaIDsFor(r.Registry, "goal_poke"),
		schemaIDsFor(r.Registry, "goal_stuck"),
		schemaIDsFor(r.Registry, "goal_opened"),
	)
	if err != nil {
		return nil, fmt.Errorf("store: reading open goals for the sweeper: %w", err)
	}
	defer rows.Close()

	var out []StalledCandidate
	for rows.Next() {
		var (
			c            StalledCandidate
			lastActivity *time.Time
			lastPoke     *time.Time
		)
		if err := rows.Scan(
			&c.GoalEventID, &c.WorkflowID, &c.Owner, &c.GoalType, &c.OpenedAt,
			&lastActivity, &c.Pokes, &lastPoke, &c.Quarantined, &c.OtherOpenContracts,
		); err != nil {
			return nil, fmt.Errorf("store: scanning a stall candidate: %w", err)
		}
		// NULL stays the ZERO time rather than becoming now(). The sweeper reads
		// a zero LastOwnerActivity as "this owner has never taken a turn" and
		// falls back to the goal's own age; substituting now() would make every
		// such goal permanently fresh and therefore never swept.
		if lastActivity != nil {
			c.LastOwnerActivity = *lastActivity
		}
		if lastPoke != nil {
			c.LastPokeAt = *lastPoke
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PgSweepElection is the advisory-lock election.
//
// pg_try_advisory_xact_lock, never the session form: a session lock outlives an
// abandoned transaction and would wedge the sweeper for every host until that
// connection died, with no timeout and no local symptom. Appendix B item 7 and
// the appender's own header say the same thing for the same reason.
//
// TRY rather than a blocking acquire, because losing the election is the normal
// state of every host that is not the elected one — blocking would hold a
// connection per host per sweep interval, forever.
//
// AND IT IS NOT LOAD-BEARING. Pokes are (goal, epoch) conditional inserts, so two
// sweepers running concurrently already produce one poke. This is an efficiency
// measure with exactly the status of the NOTIFY doorbell, and the whole sweeper
// suite runs with it disabled — which is what proves the claim rather than
// asserting it.
func PgSweepElection(pool PgPool, projectID uuid.UUID) func(ctx context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		var won bool
		if err := pool.QueryRow(ctx,
			`SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`, projectID).Scan(&won); err != nil {
			return false, fmt.Errorf("store: sweeper election: %w", err)
		}
		return won, nil
	}
}
