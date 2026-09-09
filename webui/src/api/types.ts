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
 * An agent, derived from `turn_finished` events. There is deliberately no
 * `alive` boolean: turn boundaries are the only liveness signal the log holds,
 * and a long quiet turn is legitimate, so the reader judges from `last_turn_at`.
 *
 * `host` and `working_on` are omitted from this type on purpose. The API always
 * returns them null — `turn_finished` carries no host key, and joining an agent
 * to its open contract needs payload identity fields that are not consistently
 * populated — so a column for them would be six em dashes pretending to be data.
 */
export interface FleetAgent {
  agent_name: string;
  session_id: string;
  last_turn_at: string;
  last_turn_seq: number;
  last_turn_outcome: string;
  turns: number;
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
 * Spend and tokens come from DIFFERENT event types at different grain:
 * `cost_usd` is summed over `run_finished` (whose value is session-cumulative),
 * tokens over `turn_finished`. Both are `spillable`, so every number here is a
 * LOWER BOUND — spend recorded during a database outage never reaches the log.
 */
export interface Usage {
  totals: {
    cost_usd: number | null;
    input_tokens: number | null;
    output_tokens: number | null;
    turns: number | null;
  };
  series: { bucket: string; cost_usd: number }[];
  bucket: "hour" | "day";
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
