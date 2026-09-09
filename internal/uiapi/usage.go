package uiapi

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Buckets ?bucket= accepts. A closed set, checked in Go: the value reaches
// date_trunc as a bound parameter, but an unvalidated one is a runtime database
// error on a browser-reachable endpoint rather than a 400.
const (
	BucketHour = "hour"
	BucketDay  = "day"
	// DefaultBucket is the day. An hourly default makes a week of history 168
	// rows the reader has to add up by eye.
	DefaultBucket = BucketDay
)

// UsageBucket is spend and token consumption over one time bucket.
//
// # Every figure here is a LOWER BOUND, and the view must say so
//
// Both source event types are `spillable`: under load their events are dropped
// by design rather than queued. So this reports what the log RETAINED, which is
// less than what was spent, and by an unknown amount. A total presented as
// authoritative would be wrong in the expensive direction — it invites "we only
// spent this much" from data that cannot support the claim.
//
// # The OLDEST bucket in a response may be partial
//
// ?limit= bounds ROWS, and a bucket contributes one row per project, so a
// response cut by the limit can end mid-bucket. Read the oldest bucket of a
// full page as a floor rather than a total; narrow the range to see it whole.
//
// # CostUSD comes from run_finished, never from turn_finished
//
// This is the QUM-1247 defect and it is not a style preference. The CLI reports
// cost per turn CUMULATIVELY for the session, so each turn's figure already
// includes every earlier turn's: summing them counts turn 1 N times and
// overstates spend by a measured 4-10x. `turn_finished` carries a `cost_usd`
// for exactly that reason, and this query does not read it.
//
// For the same reason spend takes the LAST run_finished per session rather than
// the sum over them (see listUsageSQL): that event is itself a session total,
// so two of them for one session are two statements of the same number, not two
// costs to add.
type UsageBucket struct {
	// BucketStart is the truncated timestamp, inclusive.
	BucketStart time.Time `json:"bucket_start"`
	Bucket      string    `json:"bucket"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	// CostUSD is a lower bound from run_finished totals. See the type doc.
	CostUSD float64 `json:"cost_usd"`
	// Sessions is how many sessions FINISHED a run in this bucket. A session
	// still running has no run_finished yet and contributes no cost anywhere.
	Sessions     int   `json:"sessions"`
	Turns        int   `json:"turns"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// UsageReader reads spend and token consumption over time.
type UsageReader interface {
	ListUsage(ctx context.Context, opts ListOptions) ([]UsageBucket, error)
}

// PgUsageReader is the Postgres implementation of UsageReader.
type PgUsageReader struct{ Pool Pool }

// Three CTEs, because spend and tokens come from different event types and
// neither can substitute for the other:
//
//   - `sessions` picks, per session_id, the LAST run_finished by seq. That event
//     is a session total, so summing several of them for one session adds the
//     same money twice. DISTINCT ON is what makes it the last one rather than
//     an arbitrary one, and it needs `ORDER BY session_id, seq DESC` to mean
//     that — the ordering is part of the semantics here, not presentation.
//   - `spend` buckets those session totals.
//   - `tokens` buckets turn_finished, which is where token counts live. Its
//     cost_usd is deliberately NOT read; see the UsageBucket doc.
//
// FULL OUTER JOIN, not an inner or left one: a bucket can have turns but no
// finished run (work still in progress) or a finished run but no retained turns
// (spilled). Either kind of join drops one of those, and the row it drops is
// real activity. COALESCE picks whichever side is present.
//
// ORDER BY 1 DESC, 2 — the project id is a TIE-BREAK, not decoration. One bucket
// yields one row per project, so ordering by the bucket alone leaves row order
// inside a bucket up to the planner: two identical requests can return different
// rows, and a LIMIT that cuts inside a bucket can cut differently each time.
//
// The truncation itself remains: LIMIT counts ROWS, not buckets. That is
// documented on UsageBucket rather than papered over — making the limit count
// buckets needs a second aggregation, which is not worth it until someone has a
// chart wide enough to notice.
//
// $2 is the date_trunc field, passed as a bound parameter and validated against
// a closed set in Go before it gets here — never interpolated.
//
// Both source types are matched by NAME through event_type_schemas, so a v2 of
// either keeps counting instead of silently zeroing the view. Costs are cast
// through jsonb_typeof for the reason given on the fleet query: one malformed
// payload must not error the whole view.
const listUsageSQL = `
	WITH sessions AS (
		SELECT DISTINCT ON (e.payload->>'session_id')
		       e.payload->>'session_id' AS session_id,
		       e.project_id, e.at,
		       CASE WHEN jsonb_typeof(e.payload->'cost_usd') = 'number'
		            THEN (e.payload->>'cost_usd')::numeric ELSE 0 END AS cost_usd
		  FROM events e
		  JOIN event_type_schemas s ON s.id = e.schema_id
		 WHERE s.name = 'run_finished'
		   AND COALESCE(e.payload->>'session_id', '') <> ''
		   AND ($1::uuid IS NULL OR e.project_id = $1::uuid)
		 ORDER BY e.payload->>'session_id', e.seq DESC
	), spend AS (
		SELECT date_trunc($2, at) AS bucket, project_id,
		       sum(cost_usd)::float8 AS cost_usd, count(*) AS sessions
		  FROM sessions
		 GROUP BY 1, 2
	), tokens AS (
		SELECT date_trunc($2, e.at) AS bucket, e.project_id,
		       count(*) AS turns,
		       COALESCE(sum(CASE WHEN jsonb_typeof(e.payload->'input_tokens') = 'number'
		                         THEN (e.payload->>'input_tokens')::bigint END), 0) AS input_tokens,
		       COALESCE(sum(CASE WHEN jsonb_typeof(e.payload->'output_tokens') = 'number'
		                         THEN (e.payload->>'output_tokens')::bigint END), 0) AS output_tokens
		  FROM events e
		  JOIN event_type_schemas s ON s.id = e.schema_id
		 WHERE s.name = 'turn_finished'
		   AND ($1::uuid IS NULL OR e.project_id = $1::uuid)
		 GROUP BY 1, 2
	)
	SELECT COALESCE(sp.bucket, tk.bucket),
	       COALESCE(sp.project_id, tk.project_id),
	       COALESCE(p.remote_url, ''),
	       COALESCE(sp.cost_usd, 0), COALESCE(sp.sessions, 0),
	       COALESCE(tk.turns, 0), COALESCE(tk.input_tokens, 0), COALESCE(tk.output_tokens, 0)
	  FROM spend sp
	  FULL OUTER JOIN tokens tk
	    ON tk.bucket = sp.bucket AND tk.project_id = sp.project_id
	  LEFT JOIN projects p ON p.id = COALESCE(sp.project_id, tk.project_id)
	 ORDER BY 1 DESC, 2
	 LIMIT $3`

// ListUsage returns usage buckets, most recent first.
func (r PgUsageReader) ListUsage(ctx context.Context, opts ListOptions) ([]UsageBucket, error) {
	bucket := opts.Bucket
	if bucket == "" {
		bucket = DefaultBucket
	}
	// Belt and braces. parseListOptions rejects an unknown bucket with a 400,
	// but this reader is exported and a caller reaching it directly must not be
	// able to choose the date_trunc field.
	if bucket != BucketHour && bucket != BucketDay {
		return nil, fmt.Errorf("uiapi: bucket must be %q or %q, got %q", BucketHour, BucketDay, bucket)
	}

	rows, err := r.Pool.Query(ctx, listUsageSQL, opts.ProjectID, bucket, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("uiapi: querying usage: %w", err)
	}
	defer rows.Close()

	out := []UsageBucket{}
	for rows.Next() {
		var (
			u         UsageBucket
			remoteURL string
		)
		if err := rows.Scan(&u.BucketStart, &u.ProjectID, &remoteURL,
			&u.CostUSD, &u.Sessions, &u.Turns, &u.InputTokens, &u.OutputTokens); err != nil {
			return nil, fmt.Errorf("uiapi: scanning a usage bucket: %w", err)
		}
		u.Bucket = bucket
		u.ProjectName = ProjectName(remoteURL)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("uiapi: reading usage: %w", err)
	}
	return out, nil
}
