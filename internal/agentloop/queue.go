// Package agentloop implements the per-agent harness queue described in
// docs/designs/messaging-overhaul.md §4.3. The queue is a pair of on-disk
// directories (pending/ and delivered/) holding one JSON file per message.
// Enqueues use a file lock for cross-process serialization of sequence
// allocation and atomic rename for crash safety.
package agentloop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/state"
)

// Class is the delivery class of a queued message. Aliased to inboxprompt.Class
// after QUM-437 extracted the prompt formatter into its own leaf package.
type Class = inboxprompt.Class

// Entry is one message in the per-agent harness queue. Aliased to
// inboxprompt.Entry after QUM-437.
type Entry = inboxprompt.Entry

// Recognized message classes.
const (
	ClassAsync     = inboxprompt.ClassAsync
	ClassInterrupt = inboxprompt.ClassInterrupt
)

// canonicalName matches a finalized queue file name:
//
//	{10-digit-seq}-{class}-{id}.json
var canonicalName = regexp.MustCompile(`^(\d{10})-(async|interrupt)-(.+)\.json$`)

// QueueDir returns the directory holding both pending/ and delivered/ for an
// agent. It does not create the directory.
func QueueDir(sprawlRoot, agentName string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "queue")
}

// PendingDir returns the pending/ subdirectory under QueueDir.
func PendingDir(sprawlRoot, agentName string) string {
	return filepath.Join(QueueDir(sprawlRoot, agentName), "pending")
}

// DeliveredDir returns the delivered/ subdirectory under QueueDir.
func DeliveredDir(sprawlRoot, agentName string) string {
	return filepath.Join(QueueDir(sprawlRoot, agentName), "delivered")
}

// acquireQueueLock opens (creating if needed) the queue's lock file and takes
// an exclusive flock. The returned closer releases the lock and closes the fd.
func acquireQueueLock(queueDir string) (func(), error) {
	if err := os.MkdirAll(queueDir, 0o755); err != nil { //nolint:gosec // G301: world-readable queue dir is intentional
		return nil, fmt.Errorf("creating queue directory: %w", err)
	}
	lockPath := filepath.Join(queueDir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644) //nolint:gosec // G302/G304: world-readable lock file, path is composed from known parents
	if err != nil {
		return nil, fmt.Errorf("opening queue lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock queue: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// maxSeq scans a directory for canonical filenames and returns the largest
// seq prefix (0 if none). Missing directory is treated as empty.
func maxSeq(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := canonicalName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest, nil
}

// Enqueue writes e to pending/ under the agent's queue. It assigns Seq and
// (if empty) ID and EnqueuedAt, serializing sequence allocation with an
// exclusive file lock. The finalized Entry is returned.
func Enqueue(sprawlRoot, agentName string, e Entry) (Entry, error) {
	queueDir := QueueDir(sprawlRoot, agentName)
	pending := PendingDir(sprawlRoot, agentName)
	delivered := DeliveredDir(sprawlRoot, agentName)

	if err := os.MkdirAll(pending, 0o755); err != nil { //nolint:gosec // G301: world-readable queue dir is intentional
		return Entry{}, fmt.Errorf("creating pending directory: %w", err)
	}
	if err := os.MkdirAll(delivered, 0o755); err != nil { //nolint:gosec // G301: world-readable queue dir is intentional
		return Entry{}, fmt.Errorf("creating delivered directory: %w", err)
	}

	unlock, err := acquireQueueLock(queueDir)
	if err != nil {
		return Entry{}, err
	}
	defer unlock()

	if e.ID == "" {
		id, err := state.GenerateUUID()
		if err != nil {
			return Entry{}, err
		}
		e.ID = id
	}
	if e.EnqueuedAt == "" {
		e.EnqueuedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if e.Class == "" {
		e.Class = ClassAsync
	}

	pendingMax, err := maxSeq(pending)
	if err != nil {
		return Entry{}, err
	}
	deliveredMax, err := maxSeq(delivered)
	if err != nil {
		return Entry{}, err
	}
	next := pendingMax
	if deliveredMax > next {
		next = deliveredMax
	}
	e.Seq = next + 1

	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return Entry{}, fmt.Errorf("marshaling entry: %w", err)
	}

	finalName := fmt.Sprintf("%010d-%s-%s.json", e.Seq, e.Class, e.ID)
	tmpPath := filepath.Join(pending, ".tmp-"+e.ID)
	finalPath := filepath.Join(pending, finalName)

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // G302/G304: world-readable queue file, path is composed
	if err != nil {
		return Entry{}, fmt.Errorf("creating tmp file: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return Entry{}, fmt.Errorf("writing tmp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return Entry{}, fmt.Errorf("fsync tmp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return Entry{}, fmt.Errorf("closing tmp file: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return Entry{}, fmt.Errorf("renaming tmp to final: %w", err)
	}

	// Best-effort directory fsync for POSIX rename durability. Some
	// filesystems don't support fsync on directories; ignore failures.
	if dir, err := os.Open(pending); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}

	return e, nil
}

// listDir reads a canonical queue directory in seq order.
func listDir(dir string) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !canonicalName.MatchString(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]Entry, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: path is composed from known parents
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// ListPending returns pending entries in seq order.
func ListPending(sprawlRoot, agentName string) ([]Entry, error) {
	return listDir(PendingDir(sprawlRoot, agentName))
}

// ListDelivered returns delivered entries in seq order.
func ListDelivered(sprawlRoot, agentName string) ([]Entry, error) {
	return listDir(DeliveredDir(sprawlRoot, agentName))
}

// findByID returns the canonical filename in dir whose Entry.ID equals id, or
// "" if not found. IDs may contain hyphens, so we match by parsing each file.
func findByID(dir, id string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !canonicalName.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: path is composed from known parents
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			// Skip unparseable files rather than failing the whole op.
			continue
		}
		if entry.ID == id {
			return e.Name(), nil
		}
	}
	return "", nil
}

// MarkDelivered moves the pending entry with ID entryID to delivered/. It
// returns an error if no such pending entry exists.
func MarkDelivered(sprawlRoot, agentName, entryID string) error {
	queueDir := QueueDir(sprawlRoot, agentName)
	pending := PendingDir(sprawlRoot, agentName)
	delivered := DeliveredDir(sprawlRoot, agentName)

	if err := os.MkdirAll(delivered, 0o755); err != nil { //nolint:gosec // G301: world-readable queue dir is intentional
		return fmt.Errorf("creating delivered directory: %w", err)
	}

	unlock, err := acquireQueueLock(queueDir)
	if err != nil {
		return err
	}
	defer unlock()

	name, err := findByID(pending, entryID)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("entry %q not found in pending", entryID)
	}

	if err := os.Rename(filepath.Join(pending, name), filepath.Join(delivered, name)); err != nil {
		return fmt.Errorf("moving to delivered: %w", err)
	}
	return nil
}

// CleanupPending removes orphaned .tmp-* files in pending/ and any pending
// entries whose ID already exists in delivered/ (duplicate redelivery).
// Missing directories are not an error.
func CleanupPending(sprawlRoot, agentName string) error {
	queueDir := QueueDir(sprawlRoot, agentName)
	pending := PendingDir(sprawlRoot, agentName)
	delivered := DeliveredDir(sprawlRoot, agentName)

	if _, err := os.Stat(queueDir); os.IsNotExist(err) {
		return nil
	}

	unlock, err := acquireQueueLock(queueDir)
	if err != nil {
		return err
	}
	defer unlock()

	// Build set of delivered IDs.
	deliveredIDs := make(map[string]bool)
	if dEntries, err := os.ReadDir(delivered); err == nil {
		for _, e := range dEntries {
			if e.IsDir() || !canonicalName.MatchString(e.Name()) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(delivered, e.Name())) //nolint:gosec // G304
			if err != nil {
				continue
			}
			var entry Entry
			if err := json.Unmarshal(data, &entry); err != nil {
				continue
			}
			deliveredIDs[entry.ID] = true
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading delivered: %w", err)
	}

	pEntries, err := os.ReadDir(pending)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading pending: %w", err)
	}
	for _, e := range pEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(pending, name)
		if strings.HasPrefix(name, ".tmp-") {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing tmp file %s: %w", name, err)
			}
			continue
		}
		if !canonicalName.MatchString(name) {
			continue
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304
		if err != nil {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		if deliveredIDs[entry.ID] {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing duplicate %s: %w", name, err)
			}
		}
	}
	return nil
}
