// The read API's JSON shapes (QUM-1349 slice C, contract published by ratz).
//
// `null` means "unknown or absent" everywhere in this file, and is NEVER
// rendered as 0 or an empty string: a 0 in a spend column is a lie, an em dash
// is the truth. The `| null` unions below are load-bearing, not defensive.

/**
 * An OPEN goal. The endpoint serves the open set only (contract amendment #1):
 * closed-goal history means walking an unindexed `closes_event_id`, which is
 * deferred rather than served from a browser-reachable endpoint. There is no
 * `state` field, because one that always read "open" would imply a filter that
 * does not exist.
 *
 * The list spans three contract types — `goal_opened`, `agent_spawned` and
 * `rework_requested` — COALESCEd onto one shape. `legacy` marks a goal standing
 * in for a prose-spawned agent; it is surfaced, never filtered, because a list
 * that silently omits half the fleet is worse than no list.
 */
export interface Goal {
  id: string;
  seq: number;
  opened_at: string;
  project_id: string;
  project_name: string;
  workflow_instance_id: string;
  goal_type: string;
  owner: string;
  legacy: boolean;
}

/**
 * A workflow instance, DERIVED from events: nothing writes the
 * `workflow_instances` table, so an instance exists only as a grouping of
 * events. `state` is `in_flight` iff it still holds open contracts — the schema
 * records no failure state, so a crashed instance and a running one look alike.
 */
export interface Workflow {
  workflow_instance_id: string;
  project_id: string;
  project_name: string;
  event_count: number;
  open_contract_count: number;
  first_seq: number;
  last_seq: number;
  first_at: string;
  last_at: string;
  state: "in_flight" | "settled";
}

/**
 * An agent, derived from `turn_finished` events (`uiapi.FleetMember`). There is
 * deliberately no `alive` boolean: turn boundaries are the only liveness signal
 * the log holds, and a long quiet turn is legitimate, so the reader judges from
 * `last_turn_at`.
 *
 * There is no `host`, no `working_on`, no `session_id` and no per-turn outcome:
 * `turn_finished` carries none of them, and a column of em dashes pretending to
 * be data is worse than the honest absence of the column.
 *
 * Rows are grouped by (`agent_name`, `project_id`), so an agent that has worked
 * in two projects appears ONCE PER PROJECT — the project column is what makes
 * two rows with the same name legible rather than a duplicate.
 *
 * `turn_count` and the token figures are LOWER BOUNDS twice over: the source
 * type is spillable, and a turn whose payload has no `agent_name` is excluded.
 */
export interface FleetMember {
  agent_name: string;
  project_id: string;
  project_name: string;
  turn_count: number;
  input_tokens: number;
  output_tokens: number;
  first_seq: number;
  last_seq: number;
  last_turn_at: string;
}

export interface EventRow {
  seq: number;
  id: string;
  type: string;
  at: string;
  project_id: string;
  project_name: string;
  workflow_instance_id: string;
  /** Opaque, varies per event type, capped at 8 KB by the schema. */
  payload: unknown;
}

/**
 * One time bucket of spend, for one project (`uiapi.UsageBucket`). The endpoint
 * serves a LIST of these and no totals object: a bucket yields one row per
 * project, so any total is a client-side sum over the rows actually returned.
 *
 * Spend and tokens come from DIFFERENT event types at different grain:
 * `cost_usd` from `run_finished` (the last one per session, since its value is a
 * session total), tokens from `turn_finished`. Both are `spillable`, so every
 * number here is a LOWER BOUND — spend recorded during a database outage never
 * reaches the log.
 *
 * `?limit=` bounds ROWS, not buckets, so the OLDEST bucket of a full page can be
 * partial. Anything summed over a page inherits that.
 */
export interface UsageBucket {
  /** The truncated bucket start, inclusive. Rows share it across projects. */
  bucket_start: string;
  bucket: "hour" | "day";
  project_id: string;
  project_name: string;
  cost_usd: number;
  /** Sessions that FINISHED a run in this bucket; a running one has no cost. */
  sessions: number;
  turns: number;
  input_tokens: number;
  output_tokens: number;
}

/** An unanswered user question: presence in `open_contracts` IS the state. */
export interface InboxQuestion {
  event_id: string;
  seq: number;
  opened_at: string;
  project_id: string;
  project_name: string;
  workflow_instance_id: string;
  asker: string;
  question: string;
  context: string;
  goal_event_id: string;
}

/**
 * `project_name` is a SHORT DERIVED LABEL (the last path segment of the repo
 * remote), computed server-side so the raw remote URL — which routinely carries
 * internal hostnames, org names and sometimes credentials — never reaches the
 * browser at all. Two projects can share a label; `project_id` is the identity.
 * An empty label is a remote with no repo path.
 */
export type ProjectLabel = string;
