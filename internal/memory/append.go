// Package memory — append.go implements `sprawl memory append-session`
// (QUM-515). This is the incremental counterpart to RegenerateTimeline:
// summarize a single session and merge its row into timeline.md by date,
// guarded by a flock to make concurrent writers safe.
package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// DefaultAppendLockTimeout is the default budget AppendSession waits for
// timeline.md.lock before giving up.
const DefaultAppendLockTimeout = 5 * time.Second

// ErrTimelineLockContended is returned when the per-timeline flock could not
// be acquired within AppendOptions.LockTimeout.
var ErrTimelineLockContended = errors.New("timeline.md flock contended")

// AppendOptions bundles all dependencies + flags for AppendSessionWithOptions.
type AppendOptions struct {
	SprawlRoot  string
	SessionID   string
	DryRun      bool
	Stdout      io.Writer
	Invoker     ClaudeInvoker
	Cfg         RegenerateConfig
	LockTimeout time.Duration
}

// AppendResult communicates whether AppendSessionWithOptions actually
// modified timeline.md, and the row that was (or would have been) added.
type AppendResult struct {
	NoOp bool
	Row  string
}

// AppendSession is the 3-arg public wrapper around AppendSessionWithOptions
// that wires the default CLI invoker and discards the AppendResult.
func AppendSession(sprawlRoot, sessionID, model string) error {
	_, err := AppendSessionWithOptions(context.Background(), AppendOptions{
		SprawlRoot:  sprawlRoot,
		SessionID:   sessionID,
		Invoker:     NewCLIInvoker(),
		Cfg:         RegenerateConfig{Model: model},
		LockTimeout: DefaultAppendLockTimeout,
	})
	return err
}

// AppendSessionWithOptions appends (or merges) a single session's summary
// row into `<root>/.sprawl/memory/timeline.md`, sorted by date prefix. The
// operation is idempotent: if a row containing the session id is already
// present, no LLM call is made and AppendResult.NoOp is true.
func AppendSessionWithOptions(ctx context.Context, opts AppendOptions) (AppendResult, error) {
	if opts.Invoker == nil {
		return AppendResult{}, fmt.Errorf("AppendSession: Invoker is required")
	}
	if opts.SprawlRoot == "" {
		return AppendResult{}, fmt.Errorf("AppendSession: SprawlRoot is required")
	}
	if opts.SessionID == "" {
		return AppendResult{}, fmt.Errorf("AppendSession: SessionID is required")
	}
	if opts.Cfg.Model == "" {
		opts.Cfg.Model = "haiku"
	}
	if opts.LockTimeout == 0 {
		opts.LockTimeout = DefaultAppendLockTimeout
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}

	memDir := filepath.Join(opts.SprawlRoot, ".sprawl", "memory")
	timelinePath := filepath.Join(memDir, "timeline.md")
	lockPath := filepath.Join(memDir, "timeline.md.lock")

	if err := os.MkdirAll(memDir, 0o755); err != nil { //nolint:gosec // G301: world-readable memory dir is intentional
		return AppendResult{}, fmt.Errorf("creating memory directory: %w", err)
	}

	fl := flock.New(lockPath)
	deadline := time.Now().Add(opts.LockTimeout)
	for {
		got, err := fl.TryLock()
		if err != nil {
			return AppendResult{}, fmt.Errorf("acquiring timeline lock: %w", err)
		}
		if got {
			break
		}
		if time.Now().After(deadline) {
			return AppendResult{}, ErrTimelineLockContended
		}
		select {
		case <-ctx.Done():
			return AppendResult{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer func() { _ = fl.Unlock() }()

	// Read existing timeline (ENOENT → empty).
	var existingRows []string
	data, err := os.ReadFile(timelinePath)
	if err != nil && !os.IsNotExist(err) {
		return AppendResult{}, fmt.Errorf("reading timeline.md: %w", err)
	}
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			existingRows = append(existingRows, line)
		}
	}

	// Idempotency check: if any existing row references this session id,
	// no-op without invoking the LLM.
	idMarker := " " + opts.SessionID + " |"
	for _, row := range existingRows {
		if strings.Contains(row, idMarker) {
			return AppendResult{NoOp: true}, nil
		}
	}

	// Read the session summary file.
	sessionPath := filepath.Join(sessionsDir(opts.SprawlRoot), opts.SessionID+".md")
	session, body, err := ReadSessionSummary(sessionPath)
	if err != nil {
		return AppendResult{}, fmt.Errorf("reading session %s at %s: %w", opts.SessionID, sessionPath, err)
	}

	newRow, err := SummarizeSession(ctx, opts.Invoker, opts.Cfg, session, body)
	if err != nil {
		return AppendResult{}, fmt.Errorf("summarizing session %s: %w", opts.SessionID, err)
	}
	if verr := ValidateTimelineRow(newRow); verr != nil {
		newRow = PlaceholderRow(session)
	}

	// Sorted insertion by date prefix (first 10 chars).
	merged := make([]string, 0, len(existingRows)+1)
	inserted := false
	newPrefix := newRow
	if len(newRow) >= 10 {
		newPrefix = newRow[:10]
	}
	for _, row := range existingRows {
		rowPrefix := row
		if len(row) >= 10 {
			rowPrefix = row[:10]
		}
		if !inserted && rowPrefix > newPrefix {
			merged = append(merged, newRow)
			inserted = true
		}
		merged = append(merged, row)
	}
	if !inserted {
		merged = append(merged, newRow)
	}

	content := strings.Join(merged, "\n")
	if len(merged) > 0 {
		content += "\n"
	}

	if opts.DryRun {
		if _, err := io.WriteString(opts.Stdout, newRow+"\n"); err != nil {
			return AppendResult{}, fmt.Errorf("writing dry-run output: %w", err)
		}
		return AppendResult{Row: newRow}, nil
	}

	// Atomic write.
	tmp, err := os.CreateTemp(memDir, ".tmp-timeline-*")
	if err != nil {
		return AppendResult{}, fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return AppendResult{}, fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return AppendResult{}, fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil { //nolint:gosec // G302: world-readable timeline is intentional
		return AppendResult{}, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, timelinePath); err != nil {
		return AppendResult{}, fmt.Errorf("renaming temp file: %w", err)
	}
	success = true
	return AppendResult{Row: newRow}, nil
}
