package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// aggregateLine is the minimal subset of fields decoded for aggregation.
type aggregateLine struct {
	SchemaVersion            int     `json:"schema_version"`
	AgentName                string  `json:"agent_name"`
	Timestamp                string  `json:"timestamp"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	TotalCostUsd             float64 `json:"total_cost_usd"`
	SessionCostUsd           float64 `json:"session_cost_usd"`
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
// the LIFETIME total token + cost counts for that agent — every session it has
// ever had.
//
// It has no production callers as of QUM-1093, which moved the status path to
// SumForAgentSession; it is retained per that issue's no-change constraint.
// Note the lifetime semantics `sprawl usage` and the /usage modal depend on live
// in SumGrouped and SumByAgent, not here.
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

// SumForAgentSession sums a SINGLE session's usage log,
// .sprawl/logs/usage/<agent>/<sessionID>.ndjson — one file per session, so the
// session is resolved from the filename rather than by filtering records. This
// is the current-session figure the status tool reports (QUM-1093); the
// lifetime figure is SumForAgent.
//
// An empty sessionID ("no session yet"), a session file that does not exist,
// and an empty file all return the zero TokenTotals with a nil error. There is
// deliberately NO fallback to the lifetime sum: a fallback would make the
// QUM-1093 over-reporting intermittent — present for some agents and absent for
// others depending on state — which is worse than the bug it hides. Malformed
// NDJSON lines are skipped, matching SumForAgent and LoadRecords.
//
// A non-empty sessionID that is not a single safe path element is rejected with
// an error: it reaches a filesystem path from the agent's state file, so a
// corrupt (or hostile) value must not escape the agent's usage directory. The
// check is stricter than "no traversal" — "." and ".." are legal Linux filenames
// but are rejected too. That strictness costs nothing, because the WRITER
// (Recorder.openWriter, recorder.go:156) joins the same sessionID+".ndjson" with no
// validation at all: an ID containing a separator could never have produced a
// readable log here in the first place.
//
// An error rather than a zero, deliberately: a library reporting 0 for "I
// refused to look" would lie to every future caller. Swallowing it to 0 is the
// status path's policy, not this function's — and it stays silent there rather
// than logging, because Status re-runs on every TUI refresh and a persistently
// corrupt state file would spam the log every tick.
func SumForAgentSession(sprawlRoot, agent, sessionID string) (TokenTotals, error) {
	if sessionID == "" {
		return TokenTotals{}, nil
	}
	// ContainsAny subsumes a filepath.Base mismatch: Base can only differ from
	// its input when the input holds a separator.
	if strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
		return TokenTotals{}, fmt.Errorf("usage: invalid session id %q", sessionID)
	}
	var t TokenTotals
	path := filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", agent, sessionID+".ndjson")
	if err := scanFile(path, func(line aggregateLine) {
		t.InputTokens += line.InputTokens
		t.OutputTokens += line.OutputTokens
		t.CacheReadInputTokens += line.CacheReadInputTokens
		t.CacheCreationInputTokens += line.CacheCreationInputTokens
		t.TotalCostUsd += line.TotalCostUsd
	}); err != nil {
		return TokenTotals{}, err
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

// usageFilePaths returns the session log files f selects, so every reader
// resolves the same set. Shared by LoadRecords and CountLegacyRows: the note
// CountLegacyRows feeds is a statement ABOUT the rows LoadRecords returned, so
// the two must not drift apart.
func usageFilePaths(sprawlRoot string, f Filter) ([]string, error) {
	agent := f.Agent
	if agent == "" {
		agent = "*"
	}
	return filepath.Glob(filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", agent, "*.ndjson"))
}

// withinWindow reports whether an RFC3339 timestamp falls in f's [Since, Until)
// window. An unparseable timestamp is excluded, matching the previous behavior
// of every caller.
func (f Filter) withinWindow(timestamp string) bool {
	if f.Since.IsZero() && f.Until.IsZero() {
		return true
	}
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return false
	}
	if !f.Since.IsZero() && ts.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !ts.Before(f.Until) {
		return false
	}
	return true
}

// LoadRecords returns all records under sprawlRoot, optionally filtered,
// sorted ascending by Timestamp (RFC3339 string compare).
func LoadRecords(sprawlRoot string, f Filter) ([]Record, error) {
	var out []Record
	matches, err := usageFilePaths(sprawlRoot, f)
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		if err := scanRecords(path, func(r Record) {
			if !f.withinWindow(r.Timestamp) {
				return
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
// SumForAgentSession / LoadRecords callers.
func scanNDJSON(path string, onLine func([]byte)) error {
	f, err := os.Open(path) //nolint:gosec // G304: path produced by filepath.Glob over a trusted root, or by SumForAgentSession from a validated single-element session id
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

// costNoiseTolerance is the largest drop in a cumulative cost series treated as
// measurement noise (rounding, re-pricing) rather than a context reset. Real
// per-turn costs are orders of magnitude larger, so nothing legitimate is lost
// below it.
const costNoiseTolerance = 1e-4

// deltaFrom converts one cumulative reading into that turn's own cost, given
// the previous reading in the same series.
//
// A reading materially BELOW its predecessor means Claude restarted the
// cumulative after a context reset, so the reading is itself the new segment's
// first value and is counted whole — clamping the negative difference to zero
// would silently drop that turn's spend (QUM-1247).
//
// A reading below its predecessor by less than costNoiseTolerance is a wobble,
// not a restart, and yields 0. The distinction matters because the two failure
// modes are wildly asymmetric: mistaking a fraction-of-a-cent dip for a reset
// re-charges the ENTIRE session cumulative, which on a $54 series is the most
// expensive wrong answer available.
func deltaFrom(cur, last float64) float64 {
	if cur < last {
		if last-cur <= costNoiseTolerance {
			return 0
		}
		return cur
	}
	return cur - last
}

// repairLegacyCosts rewrites the cost column of pre-QUM-1247 rows, which stored
// the session-cumulative total_cost_usd rather than the turn's own cost, so
// summing them inflated totals by roughly N/2x.
//
// Rows are addressed by index because the two scan paths decode into different
// structs; a is the row count plus closures that read and write one row's
// fields. The caller must pass exactly ONE file's rows, in file order: each
// session file is an independent cumulative series, and a baseline carried
// across a file boundary would score the next file's first row against the
// previous file's high-water mark.
//
// Rows already at RecordSchemaVersion or later hold true per-turn deltas and are
// passed through untouched. Crucially they still ADVANCE the baseline, to their
// recorded cumulative: both binaries write to the same session file, so a legacy
// row following a corrected one continues the same series, and restarting its
// baseline at zero would re-charge everything spent so far. That ordering is
// reachable whenever an older binary appends to a file a newer one already
// wrote — a resume from a pre-fix build.
func repairLegacyCosts(a costRowAccess) {
	var last float64
	for i := 0; i < a.n; i++ {
		if a.version(i) >= RecordSchemaVersion {
			last = a.sessionCost(i)
			continue
		}
		cur := a.cost(i)
		a.repair(i, deltaFrom(cur, last), cur)
		last = cur
	}
}

// costRowAccess adapts one file's decoded rows for repairLegacyCosts,
// independent of which struct they were decoded into.
type costRowAccess struct {
	n           int
	version     func(int) int
	cost        func(int) float64
	sessionCost func(int) float64
	// repair records a legacy row's reconstructed per-turn cost, the original
	// cumulative it came from, and stamps the row as carrying current-schema
	// semantics — an exported row must not be repaired a second time by
	// whatever re-ingests it.
	repair func(i int, delta, cumulative float64)
}

// scanFile decodes one usage file, repairs any legacy rows, then invokes fn per
// row. It buffers the whole file because a legacy row's cost is only computable
// from its predecessor; streaming each line straight to fn cannot do that.
func scanFile(path string, fn func(aggregateLine)) error {
	var rows []aggregateLine
	if err := scanNDJSON(path, func(line []byte) {
		var rec aggregateLine
		if err := json.Unmarshal(line, &rec); err != nil {
			return
		}
		rows = append(rows, rec)
	}); err != nil {
		return err
	}
	repairLegacyCosts(costRowAccess{
		n:           len(rows),
		version:     func(i int) int { return rows[i].SchemaVersion },
		cost:        func(i int) float64 { return rows[i].TotalCostUsd },
		sessionCost: func(i int) float64 { return rows[i].SessionCostUsd },
		repair: func(i int, delta, cumulative float64) {
			rows[i].TotalCostUsd = delta
			rows[i].SessionCostUsd = cumulative
			rows[i].SchemaVersion = RecordSchemaVersion
		},
	})
	for _, r := range rows {
		fn(r)
	}
	return nil
}

// scanRecords is scanFile's typed-Record counterpart, with the same per-file
// buffering and legacy repair. Repair happens here rather than in LoadRecords so
// that it precedes any Since/Until filtering: a delta needs its predecessor, and
// a predecessor dropped by the filter would leave the survivor carrying a raw
// cumulative.
func scanRecords(path string, fn func(Record)) error {
	var rows []Record
	if err := scanNDJSON(path, func(line []byte) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return
		}
		rows = append(rows, rec)
	}); err != nil {
		return err
	}
	repairLegacyCosts(costRowAccess{
		n:           len(rows),
		version:     func(i int) int { return rows[i].SchemaVersion },
		cost:        func(i int) float64 { return rows[i].TotalCostUsd },
		sessionCost: func(i int) float64 { return rows[i].SessionCostUsd },
		repair: func(i int, delta, cumulative float64) {
			rows[i].TotalCostUsd = delta
			rows[i].SessionCostUsd = cumulative
			rows[i].SchemaVersion = RecordSchemaVersion
		},
	})
	for _, r := range rows {
		fn(r)
	}
	return nil
}

// CountLegacyRows reports how many of the rows matching f were written before
// the QUM-1247 cost fix (and so had their per-turn cost reconstructed on read),
// alongside the total row count. `sprawl usage` reports this so a reconstructed
// figure is never mistaken for one read straight off disk.
//
// It re-scans rather than reusing LoadRecords because repair overwrites the
// cost column but deliberately leaves SchemaVersion alone, making the legacy
// rows still identifiable.
func CountLegacyRows(sprawlRoot string, f Filter) (legacy, total int, err error) {
	matches, err := usageFilePaths(sprawlRoot, f)
	if err != nil {
		return 0, 0, err
	}
	for _, path := range matches {
		if err := scanNDJSON(path, func(line []byte) {
			var rec Record
			if err := json.Unmarshal(line, &rec); err != nil {
				return
			}
			if !f.withinWindow(rec.Timestamp) {
				return
			}
			total++
			if rec.SchemaVersion < RecordSchemaVersion {
				legacy++
			}
		}); err != nil {
			return 0, 0, err
		}
	}
	return legacy, total, nil
}
