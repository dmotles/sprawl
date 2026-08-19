// Package usage records per-turn token usage and cost into NDJSON logs
// under .sprawl/logs/usage/<agent>/<session_id>.ndjson. See QUM-368.
package usage

// RecordSchemaVersion is the current on-disk usage row schema version.
//
// Version 1 (QUM-1247) redefined TotalCostUsd from session-cumulative to
// per-turn delta. Rows written before that fix carry no schema_version key and
// decode as 0; aggregation reconstructs their deltas on read rather than
// blending them with corrected rows. Bump this whenever the meaning of an
// existing column changes, not merely when a column is added.
const RecordSchemaVersion = 1

// Record is the on-disk schema for a single completed turn's usage row.
// All fields are emitted (no omitempty) so downstream tooling can rely on
// stable column presence.
type Record struct {
	SchemaVersion            int    `json:"schema_version"`
	Timestamp                string `json:"timestamp"`
	AgentName                string `json:"agent_name"`
	AgentType                string `json:"agent_type"`
	AgentFamily              string `json:"agent_family"`
	ParentName               string `json:"parent_name"`
	SessionID                string `json:"session_id"`
	Branch                   string `json:"branch"`
	Model                    string `json:"model"`
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	// TotalCostUsd is this turn's cost ALONE — the delta over the previous
	// turn's cumulative. Note the name predates QUM-1247 and reads like a
	// running total; it is not one, and summing the column is correct.
	TotalCostUsd float64 `json:"total_cost_usd"`
	// SessionCostUsd is the raw session-cumulative total_cost_usd from
	// Claude's result frame, retained unmodified for auditability. It is
	// monotone within a session segment and restarts from a low value after a
	// context reset, so it must never be summed.
	SessionCostUsd float64 `json:"session_cost_usd"`
}

// TokenTotals is the aggregate result of summing usage records.
type TokenTotals struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	TotalCostUsd             float64
}
