package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Degraded-mode spill.
//
// SCOPE, and it is narrow on purpose: only TELEMETRY AND LIFECYCLE events spill.
// Goal open/close never does, because a goal recorded in a local file is
// invisible to every other host and to the sweeper — it would look like work
// nobody is doing while the agent that opened it believes it was recorded.
// Which types are eligible is a property of the SCHEMA (EventTypeSchema.Spillable),
// not a flag the caller passes: a caller that set "spillable" on a goal-open
// would silently lose it, and nothing downstream would ever notice.
//
// REPLAY IS M1B, deliberately. What ships here is the write side plus
// DeadLetter, so that "never a silent drop" is structurally true today rather
// than promised: an event either lands in the log, lands in a spill file, lands
// in the dead-letter dir, or surfaces an error to its caller. There is no fifth
// outcome.

// SpillRecordVersion is the on-disk schema version for a spill line. A replayer
// that meets an unknown version must dead-letter the line, never guess at it.
const SpillRecordVersion = 1

// SpillRecord is one spilled event, serialised as NDJSON.
//
// It carries the resolved schema NAME and VERSION alongside the pinned
// SchemaID. The id alone is what the appender needs, but a replayer running a
// build that no longer carries that id would otherwise have nothing but an
// opaque uuid to put in its dead-letter reason.
type SpillRecord struct {
	Version            int             `json:"v"`
	EventID            uuid.UUID       `json:"event_id"`
	ProjectID          uuid.UUID       `json:"project_id"`
	WorkflowInstanceID uuid.UUID       `json:"workflow_instance_id"`
	SchemaID           uuid.UUID       `json:"schema_id"`
	SchemaName         string          `json:"schema_name"`
	SchemaVersion      int             `json:"schema_version"`
	AgentSessionID     *uuid.UUID      `json:"agent_session_id,omitempty"`
	OwnerAgentID       *uuid.UUID      `json:"owner_agent_id,omitempty"`
	ClosesEventID      *uuid.UUID      `json:"closes_event_id,omitempty"`
	Payload            json.RawMessage `json:"payload"`
	At                 time.Time       `json:"at"`
	// Reason is why this record spilled, so a replay can distinguish a
	// transient outage from a permanent rejection.
	Reason string `json:"reason"`
}

// Spiller is where events go when the event log is unreachable.
type Spiller interface {
	Write(ctx context.Context, rec SpillRecord) error
}

// Spill directory layout, under <sprawlRoot>/.sprawl/logs/ledger-spill/.
// Covered by the repo's `.sprawl/*` gitignore rule, which
// scripts/test-gitignore-classes.sh asserts rather than assumes.
const (
	spillDirName      = "ledger-spill"
	deadLetterDirName = "dead-letter"
)

// Retention. A spill directory is an outage artifact: it must not grow without
// bound on a host whose DSN was mistyped six months ago, and it must not be so
// short-lived that a weekend outage loses telemetry.
const (
	// SpillRetention is how long a spill file is kept.
	SpillRetention = 14 * 24 * time.Hour
	// SpillMaxBytes caps the whole directory. Whichever bound bites first wins.
	SpillMaxBytes = 64 << 20
)

// FileSpiller writes NDJSON spill files, one per UTC day.
type FileSpiller struct {
	// Root is the sprawl root; files land under Root/.sprawl/logs/ledger-spill.
	Root string
	Now  func() time.Time

	mu sync.Mutex
}

func (s *FileSpiller) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SpillDir returns the spill directory for a sprawl root.
func SpillDir(root string) string {
	return filepath.Join(root, ".sprawl", "logs", spillDirName)
}

// DeadLetterDir returns the dead-letter directory for a sprawl root.
func DeadLetterDir(root string) string {
	return filepath.Join(SpillDir(root), deadLetterDirName)
}

// Write appends rec to today's spill file.
func (s *FileSpiller) Write(_ context.Context, rec SpillRecord) error {
	if rec.Version == 0 {
		rec.Version = SpillRecordVersion
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: marshalling spill record: %w", err)
	}
	return s.appendLine(SpillDir(s.Root), s.fileName(), line)
}

// DeadLetter records a spilled line that could not be replayed.
//
// Its existence is the "never silently drop" half of the requirement: a replayer
// that hits an invalid or unresolvable record must move it here with a reason
// rather than skipping it, because a skipped line is indistinguishable from a
// line that was never written.
func (s *FileSpiller) DeadLetter(rec SpillRecord, reason string) error {
	rec.Reason = reason
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: marshalling dead-letter record: %w", err)
	}
	return s.appendLine(DeadLetterDir(s.Root), s.fileName(), line)
}

func (s *FileSpiller) fileName() string {
	return s.now().UTC().Format("2006-01-02") + ".ndjson"
}

func (s *FileSpiller) appendLine(dir, name string, line []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("store: creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: path is derived from the sprawl root
	if err != nil {
		return fmt.Errorf("store: opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("store: writing %s: %w", path, err)
	}
	return nil
}

// Prune enforces retention: files older than SpillRetention go, and then oldest
// files go until the directory is under SpillMaxBytes.
//
// It returns the paths it removed so a caller can report them. Silent deletion
// of an outage artifact would leave an operator with no way to tell "no outage"
// from "the evidence was reaped".
func (s *FileSpiller) Prune() ([]string, error) {
	dir := SpillDir(s.Root)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", dir, err)
	}

	type spillFile struct {
		path string
		mod  time.Time
		size int64
	}
	var files []spillFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("store: stat %s: %w", e.Name(), err)
		}
		files = append(files, spillFile{filepath.Join(dir, e.Name()), info.ModTime(), info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })

	var removed []string
	cutoff := s.now().Add(-SpillRetention)
	var total int64
	var kept []spillFile
	for _, f := range files {
		if f.mod.Before(cutoff) {
			if err := os.Remove(f.path); err != nil {
				return removed, fmt.Errorf("store: removing %s: %w", f.path, err)
			}
			removed = append(removed, f.path)
			continue
		}
		kept = append(kept, f)
		total += f.size
	}
	// Oldest-first eviction until under the byte cap. Dead-lettered files live
	// in a subdirectory and are never evicted by size: they are the record of
	// what could not be replayed.
	for i := 0; i < len(kept) && total > SpillMaxBytes; i++ {
		if err := os.Remove(kept[i].path); err != nil {
			return removed, fmt.Errorf("store: removing %s: %w", kept[i].path, err)
		}
		removed = append(removed, kept[i].path)
		total -= kept[i].size
	}
	return removed, nil
}
