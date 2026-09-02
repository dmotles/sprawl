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
// alternative is a full scan per sweep. That set is openerSchemaIDs ($5):
// goal_opened AND rework_requested, because a rework opens a contract exactly as
// a goal does and a crashed reworker is a stall by every term below. A rework's
// goal_type is reported as the literal `rework` rather than the subject of the
// goal it follows — the same deliberate divergence from goalreader.go that
// operability.go's header explains.
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
//
// THE STALL IS MEASURED AGAINST THE POKE TARGET, NOT THE OWNER (AC5). A goal
// contract's `owner` is who the RESULT IS REPORTED TO — create_goal sets it to
// the CALLER — while the agent actually doing the work is named on the goal's
// newest spawn_requested. Measuring last_activity against the owner made every
// goal opened by a busy manager permanently fresh, so a goal whose worker had
// crashed was never a stall candidate and the poke path was unreachable by the
// very scenario it exists for. The same substitution applies to other_open:
// counting the OWNER's other open contracts declares every delegated goal
// transitively blocked, because the owner necessarily holds the goal it
// delegated.
//
// The CTE exists because a SELECT alias cannot be referenced from the same
// SELECT list, and `target` is needed in three places — repeating the COALESCE
// three times is exactly how the terms drift apart.
const openGoalsSQL = `
	WITH goals AS (
	  SELECT g.id, g.project_id, g.workflow_instance_id, g.seq, g.at,
	         COALESCE(g.payload->>'owner', '')     AS owner,
	         COALESCE(NULLIF(g.payload->>'goal_type', ''),
	                  CASE WHEN g.schema_id = ANY($8) THEN 'rework' END,
	                  '')                          AS goal_type,
	         COALESCE((SELECT s.payload->>'agent_name' FROM events s
	                    WHERE s.project_id = g.project_id
	                      AND s.schema_id = ANY($7)
	                      AND s.workflow_instance_id = g.workflow_instance_id
	                    ORDER BY s.seq DESC LIMIT 1), '')  AS assignee
	    FROM open_contracts oc
	    JOIN events g ON g.id = oc.event_id
	   WHERE g.project_id = $1
	     AND g.schema_id = ANY($5)
	), t AS (
	  SELECT goals.*, COALESCE(NULLIF(assignee, ''), owner) AS target FROM goals
	)
	SELECT
	    t.id,
	    t.workflow_instance_id,
	    t.owner,
	    t.assignee,
	    t.goal_type,
	    t.at,
	    (SELECT max(a.at) FROM events a
	      WHERE a.project_id = t.project_id
	        AND a.schema_id = ANY($2)
	        AND a.payload->>'agent_name' = t.target)                       AS last_activity,
	    (SELECT count(*) FROM events p
	      WHERE p.project_id = t.project_id
	        AND p.schema_id = ANY($3)
	        AND p.payload->>'goal_event_id' = t.id::text)                  AS pokes,
	    (SELECT max(p.at) FROM events p
	      WHERE p.project_id = t.project_id
	        AND p.schema_id = ANY($3)
	        AND p.payload->>'goal_event_id' = t.id::text)                  AS last_poke_at,
	    EXISTS (SELECT 1 FROM events s
	             WHERE s.project_id = t.project_id
	               AND s.schema_id = ANY($4)
	               AND s.payload->>'goal_event_id' = t.id::text)           AS quarantined,
	    (SELECT count(*) FROM open_contracts oc2
	       JOIN events o2 ON o2.id = oc2.event_id
	      WHERE o2.project_id = t.project_id
	        AND o2.id <> t.id
	        AND o2.schema_id <> ALL($6)
	        AND COALESCE(o2.payload->>'owner', o2.payload->>'recipient', '')
	            = t.target)                                                AS other_open
	  FROM t
	 ORDER BY t.seq`

// PgSweepReader produces stall candidates through a pgx pool.
type PgSweepReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

var _ SweepReader = (*PgSweepReader)(nil)

// openerSchemaIDs are the contract-opening types a stall can be measured on.
//
// The same pair `reworkable` names in rework.go, and for the same reason: a
// rework opens a contract that follows one. Enumerating goal_opened alone made
// an outstanding rework unreachable by the poke path — the contract stayed open
// forever with nobody swept for it (QUM-1336). agent_spawned is deliberately
// NOT here: the operator listing shows those so a human can see them, but a
// prose-spawned agent has no goal to be stalled against and no assignee to aim
// a poke at.
func openerSchemaIDs(reg *Registry) []uuid.UUID {
	return append(schemaIDsFor(reg, "goal_opened"), schemaIDsFor(reg, "rework_requested")...)
}

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
		openerSchemaIDs(r.Registry),
		schemaIDsFor(r.Registry, "owner_notify"),
		schemaIDsFor(r.Registry, "spawn_requested"),
		schemaIDsFor(r.Registry, "rework_requested"),
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
			&c.GoalEventID, &c.WorkflowID, &c.Owner, &c.Assignee, &c.GoalType, &c.OpenedAt,
			&lastActivity, &c.Pokes, &lastPoke, &c.Quarantined, &c.OtherOpenContracts,
		); err != nil {
			return nil, fmt.Errorf("store: scanning a stall candidate: %w", err)
		}
		// NULL stays the ZERO time rather than becoming now(). The sweeper reads
		// a zero LastOwnerActivity as "the poke target has never taken a turn" and
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
