// Package usage records per-turn token usage and cost into NDJSON logs
// under .sprawl/logs/usage/<agent>/<session_id>.ndjson. See QUM-368.
package usage

// Record is the on-disk schema for a single completed turn's usage row.
// All fields are emitted (no omitempty) so downstream tooling can rely on
// stable column presence.
type Record struct {
	Timestamp                string  `json:"timestamp"`
	AgentName                string  `json:"agent_name"`
	AgentType                string  `json:"agent_type"`
	AgentFamily              string  `json:"agent_family"`
	ParentName               string  `json:"parent_name"`
	SessionID                string  `json:"session_id"`
	Branch                   string  `json:"branch"`
	Model                    string  `json:"model"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	TotalCostUsd             float64 `json:"total_cost_usd"`
}

// TokenTotals is the aggregate result of summing usage records.
type TokenTotals struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	TotalCostUsd             float64
}
