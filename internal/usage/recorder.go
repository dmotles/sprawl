package usage

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// WriterFactory returns an io.WriteCloser for the given output path. Used as
// a test seam so suite can inject a slow/fake writer without touching disk.
type WriterFactory func(path string) (io.WriteCloser, error)

// Option configures a Recorder at construction.
type Option func(*Recorder)

// WithWriterFactory injects a custom writer factory. Default is to open
// per-session NDJSON files on disk.
func WithWriterFactory(f WriterFactory) Option {
	return func(r *Recorder) { r.writerFactory = f }
}

// Recorder subscribes (per-agent) to the runtime EventBus and writes one
// NDJSON record per completed turn into
// .sprawl/logs/usage/<agent>/<session_id>.ndjson.
type Recorder struct {
	sprawlRoot    string
	agentName     string
	writerFactory WriterFactory

	// Cached agent metadata (read once at construction).
	agentType   string
	agentFamily string
	parentName  string
	branch      string

	// In-flight turn accumulator + current session/file.
	accum         TurnAccumulator
	currentSessID string
	currentFile   io.WriteCloser

	// lastSessionCost is the previous turn's session-cumulative
	// total_cost_usd, the baseline each turn's own cost is measured against.
	// Cleared on session rotation and re-seeded from disk when this Recorder
	// resumes a session another process already wrote rows for.
	lastSessionCost float64
	costSeeded      bool
}

// NewRecorder constructs a Recorder for agentName under sprawlRoot. It reads
// agent metadata (type, family, parent, branch) once from state.LoadAgent
// and caches it for the Recorder's lifetime. If no state file exists, the
// metadata fields default to empty strings.
func NewRecorder(sprawlRoot, agentName string, opts ...Option) (*Recorder, error) {
	r := &Recorder{sprawlRoot: sprawlRoot, agentName: agentName}
	for _, opt := range opts {
		opt(r)
	}
	if a, err := state.LoadAgent(sprawlRoot, agentName); err == nil && a != nil {
		r.agentType = a.Type
		r.agentFamily = a.Family
		r.parentName = a.Parent
		r.branch = a.Branch
	}
	return r, nil
}

// Handle processes a single RuntimeEvent.
func (r *Recorder) Handle(ev runtime.RuntimeEvent) {
	switch ev.Type {
	case runtime.EventProtocolMessage:
		r.handleProtocolMessage(ev)
	case runtime.EventTurnCompleted:
		r.handleTurnCompleted(ev)
	case runtime.EventInterrupted, runtime.EventBackendFaulted:
		// Only the in-flight tokens are discarded; lastSessionCost is
		// deliberately left alone. An interrupted turn writes no row, but its
		// spend stays in Claude's running cumulative, so the next successful
		// turn's delta absorbs it. Clearing the baseline here would re-charge
		// everything spent in the session so far.
		r.accum.Reset()
	}
}

func (r *Recorder) handleProtocolMessage(ev runtime.RuntimeEvent) {
	if ev.Message == nil || ev.Message.Type != "assistant" {
		return
	}
	// Session rotation: if the session_id has changed, close the prior file
	// and discard any in-flight accumulator (mid-stream rotation semantics).
	if ev.Message.SessionID != "" && ev.Message.SessionID != r.currentSessID {
		if r.currentFile != nil {
			_ = r.currentFile.Close()
			r.currentFile = nil
		}
		r.accum.Reset()
		r.currentSessID = ev.Message.SessionID
		r.lastSessionCost = 0
		r.costSeeded = false
	}

	var am protocol.AssistantMessage
	if err := json.Unmarshal(ev.Message.Raw, &am); err != nil {
		return
	}
	u, model, err := am.ParseUsage()
	if err != nil || u == nil {
		return
	}
	r.accum.Absorb(*u, model)
}

func (r *Recorder) handleTurnCompleted(ev runtime.RuntimeEvent) {
	if !r.accum.HasData() || ev.Result == nil {
		return
	}
	sessID := r.currentSessID
	if sessID == "" {
		sessID = ev.Result.SessionID
	}
	if !r.costSeeded {
		r.lastSessionCost = r.seedCostBaseline(sessID)
		r.costSeeded = true
	}
	sessionCost := ev.Result.TotalCostUsd
	turnCost := deltaFrom(sessionCost, r.lastSessionCost)
	r.lastSessionCost = sessionCost

	u := r.accum.Usage()
	rec := Record{
		SchemaVersion:            RecordSchemaVersion,
		Timestamp:                time.Now().UTC().Format(time.RFC3339Nano),
		AgentName:                r.agentName,
		AgentType:                r.agentType,
		AgentFamily:              r.agentFamily,
		ParentName:               r.parentName,
		SessionID:                sessID,
		Branch:                   r.branch,
		Model:                    r.accum.Model(),
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		TotalCostUsd:             turnCost,
		SessionCostUsd:           sessionCost,
	}
	if err := r.writeRecord(sessID, rec); err != nil {
		// Best-effort: swallow write errors so the runtime hot path is
		// never affected by disk hiccups.
		_ = err
	}
	r.accum.Reset()
}

// seedCostBaseline returns the session-cumulative cost already accounted for in
// sessID's log, so a Recorder resuming a session another process started
// measures its first delta against that instead of against zero.
//
// Without it, a restart mid-session re-charges the whole session: the log is
// opened O_APPEND, so the prior rows survive and the first new row would store
// the full running cumulative as if it were one turn's cost.
//
// Returns 0 whenever the answer cannot be read — no file, an unreadable one, or
// an injected writer factory with no file behind it — which is exactly the
// correct baseline for a session with no rows yet.
func (r *Recorder) seedCostBaseline(sessID string) float64 {
	if sessID == "" {
		return 0
	}
	path := filepath.Join(r.sprawlRoot, ".sprawl", "logs", "usage", r.agentName, sessID+".ndjson")
	var last Record
	var found bool
	if err := scanNDJSON(path, func(line []byte) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return
		}
		last = rec
		found = true
	}); err != nil || !found {
		return 0
	}
	if last.SchemaVersion >= RecordSchemaVersion {
		return last.SessionCostUsd
	}
	// A legacy row's total_cost_usd IS the cumulative; it has no
	// session_cost_usd to read.
	return last.TotalCostUsd
}

func (r *Recorder) writeRecord(sessID string, rec Record) error {
	if r.currentFile == nil {
		w, err := r.openWriter(sessID)
		if err != nil {
			return err
		}
		r.currentFile = w
	}
	b, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := r.currentFile.Write(b); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) openWriter(sessID string) (io.WriteCloser, error) {
	path := filepath.Join(r.sprawlRoot, ".sprawl", "logs", "usage", r.agentName, sessID+".ndjson")
	if r.writerFactory != nil {
		return r.writerFactory(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: world-readable usage dir is intentional
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302/G304: append-only NDJSON log
}

// Close fsyncs and closes the current usage log file.
func (r *Recorder) Close() error {
	if r.currentFile == nil {
		return nil
	}
	if f, ok := r.currentFile.(*os.File); ok {
		_ = f.Sync()
	}
	err := r.currentFile.Close()
	r.currentFile = nil
	return err
}
