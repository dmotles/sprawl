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
	u := r.accum.Usage()
	rec := Record{
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
		TotalCostUsd:             ev.Result.TotalCostUsd,
	}
	if err := r.writeRecord(sessID, rec); err != nil {
		// Best-effort: swallow write errors so the runtime hot path is
		// never affected by disk hiccups.
		_ = err
	}
	r.accum.Reset()
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
