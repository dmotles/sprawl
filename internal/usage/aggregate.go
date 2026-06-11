package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// aggregateLine is the minimal subset of fields decoded for aggregation.
type aggregateLine struct {
	AgentName                string  `json:"agent_name"`
	Timestamp                string  `json:"timestamp"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	TotalCostUsd             float64 `json:"total_cost_usd"`
}

// GroupKey selects how SumGrouped buckets records.
type GroupKey string

const (
	GroupAgent   GroupKey = "agent"
	GroupModel   GroupKey = "model"
	GroupSession GroupKey = "session"
	GroupDay     GroupKey = "day"
)

// Filter narrows the records considered by SumGrouped / LoadRecords / TailRecords.
type Filter struct {
	Agent string
	Since time.Time
	Until time.Time
}

// SumByAgent treewalks .sprawl/logs/usage/* and returns total token + cost
// counts keyed by agent name (the per-agent log directory). A zero-value
// since includes all records (current behavior); otherwise only records
// whose RFC3339 Timestamp is at or after since are summed (QUM-798).
func SumByAgent(sprawlRoot string, since time.Time) (map[string]TokenTotals, error) {
	out := map[string]TokenTotals{}
	matches, err := filepath.Glob(filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", "*", "*.ndjson"))
	if err != nil {
		return nil, err
	}
	hasSince := !since.IsZero()
	for _, path := range matches {
		agent := filepath.Base(filepath.Dir(path))
		if err := scanFile(path, func(line aggregateLine) {
			if hasSince {
				ts, err := time.Parse(time.RFC3339, line.Timestamp)
				if err != nil || ts.Before(since) {
					return
				}
			}
			t := out[agent]
			t.InputTokens += line.InputTokens
			t.OutputTokens += line.OutputTokens
			t.CacheReadInputTokens += line.CacheReadInputTokens
			t.CacheCreationInputTokens += line.CacheCreationInputTokens
			t.TotalCostUsd += line.TotalCostUsd
			out[agent] = t
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SumForAgent treewalks .sprawl/logs/usage/<agent>/*.ndjson and returns
// the total token + cost counts for that agent.
func SumForAgent(sprawlRoot, agent string) (TokenTotals, error) {
	var t TokenTotals
	matches, err := filepath.Glob(filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", agent, "*.ndjson"))
	if err != nil {
		return TokenTotals{}, err
	}
	for _, path := range matches {
		if err := scanFile(path, func(line aggregateLine) {
			t.InputTokens += line.InputTokens
			t.OutputTokens += line.OutputTokens
			t.CacheReadInputTokens += line.CacheReadInputTokens
			t.CacheCreationInputTokens += line.CacheCreationInputTokens
			t.TotalCostUsd += line.TotalCostUsd
		}); err != nil {
			return TokenTotals{}, err
		}
	}
	return t, nil
}

// SumGrouped buckets records by the given GroupKey, optionally filtered.
func SumGrouped(sprawlRoot string, group GroupKey, f Filter) (map[string]TokenTotals, error) {
	out := map[string]TokenTotals{}
	recs, err := LoadRecords(sprawlRoot, f)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		key, ok := groupKeyFor(r, group)
		if !ok {
			continue
		}
		t := out[key]
		t.InputTokens += r.InputTokens
		t.OutputTokens += r.OutputTokens
		t.CacheReadInputTokens += r.CacheReadInputTokens
		t.CacheCreationInputTokens += r.CacheCreationInputTokens
		t.TotalCostUsd += r.TotalCostUsd
		out[key] = t
	}
	return out, nil
}

func groupKeyFor(r Record, group GroupKey) (string, bool) {
	switch group {
	case GroupAgent:
		return r.AgentName, true
	case GroupModel:
		return r.Model, true
	case GroupSession:
		return r.AgentName + "/" + r.SessionID, true
	case GroupDay:
		t, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil {
			return "", false
		}
		return t.UTC().Format("2006-01-02"), true
	}
	return "", false
}

// LoadRecords returns all records under sprawlRoot, optionally filtered,
// sorted ascending by Timestamp (RFC3339 string compare).
func LoadRecords(sprawlRoot string, f Filter) ([]Record, error) {
	var out []Record
	pattern := filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", "*", "*.ndjson")
	if f.Agent != "" {
		pattern = filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", f.Agent, "*.ndjson")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	hasSince := !f.Since.IsZero()
	hasUntil := !f.Until.IsZero()
	for _, path := range matches {
		if err := scanRecords(path, func(r Record) {
			if hasSince || hasUntil {
				ts, err := time.Parse(time.RFC3339, r.Timestamp)
				if err != nil {
					return
				}
				if hasSince && ts.Before(f.Since) {
					return
				}
				if hasUntil && !ts.Before(f.Until) {
					return
				}
			}
			out = append(out, r)
		}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Timestamp < out[j].Timestamp
	})
	return out, nil
}

// TailRecords returns the last n records (highest timestamps) in ascending
// order. n<=0 returns empty.
func TailRecords(sprawlRoot string, f Filter, n int) ([]Record, error) {
	if n <= 0 {
		return nil, nil
	}
	recs, err := LoadRecords(sprawlRoot, f)
	if err != nil {
		return nil, err
	}
	if len(recs) <= n {
		return recs, nil
	}
	return recs[len(recs)-n:], nil
}

// scanNDJSON opens path and invokes onLine for each non-empty NDJSON line.
// Returns nil if path does not exist. Malformed lines are skipped silently
// by onLine (callers attempt json.Unmarshal on the bytes and continue on
// error). Shared by scanFile (legacy aggregate aggregation) and scanRecords
// (typed Record decode for LoadRecords) — see SumByAgent / SumForAgent /
// LoadRecords callers.
func scanNDJSON(path string, onLine func([]byte)) error {
	f, err := os.Open(path) //nolint:gosec // G304: path produced by filepath.Glob over trusted root
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		onLine(line)
	}
	return scanner.Err()
}

func scanFile(path string, fn func(aggregateLine)) error {
	return scanNDJSON(path, func(line []byte) {
		var rec aggregateLine
		if err := json.Unmarshal(line, &rec); err != nil {
			return
		}
		fn(rec)
	})
}

func scanRecords(path string, fn func(Record)) error {
	return scanNDJSON(path, func(line []byte) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return
		}
		fn(rec)
	})
}
