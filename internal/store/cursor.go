package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The per-host dispatch cursor (QUM-1250, M1b).
//
// THE CURSOR IS NOT THE CORRECTNESS MECHANISM, and every decision in this file
// follows from that. Exactly-once is carried by `event_claims` — a conditional
// insert on a composite primary key, which is Appendix B item 1. The cursor only
// says where a catch-up scan may START, so the design calls it "the host's only
// local dispatch state" and "reconstructible" in the same breath.
//
// The consequence that is easy to get backwards: LOSING THE CURSOR MUST COST A
// RE-SCAN AND NOTHING ELSE. That is why it lives in a local file rather than in
// Postgres. A cursor in the database would invite a transactional
// cursor-advance — advance the cursor and perform the side effect in one
// transaction — and that is precisely the design NOT chosen, because the side
// effect (create a worktree, launch a session) is not transactional and cannot
// be made so. Putting the cursor out of reach of the append transaction keeps
// the honest mechanism, claims, load-bearing.
//
// Directly testable consequence, asserted in cursor_test.go: delete the cursor,
// re-run, and no work is repeated.
//
// A pair of near-identical failure modes are treated as OPPOSITES here, and the
// difference is the whole reason Load has an error return:
//
//   - ABSENT is a first run. It is a MEASURED absence — the file is not there —
//     so it reads as 0 with no error, and "delete the cursor" stays a supported
//     recovery rather than a way to brick a host.
//   - UNUSABLE (bad bytes, a missing field, a stored negative, an unreadable
//     file) is an ERROR. Reporting any of those as 0 would make a corrupt cursor
//     indistinguishable from a first run — a plausible zero — and the symptom is
//     not cosmetic: the host silently re-scans the entire log on every poll,
//     forever, with nothing anywhere saying why.
type CursorStore interface {
	// Load returns the highest seq this consumer has finished scanning, or 0 if
	// it has never scanned. It errors for every failure that is NOT absence.
	Load(consumer string) (int64, error)
	// Save records a new position. Rewinding is legal (it is how a re-scan is
	// requested); a negative position is not.
	Save(consumer string, seq int64) error
}

// FileCursorStore keeps one small JSON document per consumer under the sprawl
// root.
//
// Under .sprawl/ specifically, so the file falls inside the `.sprawl/*`
// gitignore class that scripts/test-gitignore-classes.sh ASSERTS rather than
// assumes. A cursor is not a secret, but it is per-host state in a tree that is
// public and shared by many agents, and that class is the mechanism keeping
// every such file out of a commit.
//
// Concurrency-safe with no mutex, and that is a property of the write shape
// rather than an oversight: every Save is a write-to-temp-then-rename, so a
// reader either sees the whole previous document or the whole next one. A mutex
// would only serialise writers WITHIN one process, which is not the case that
// matters — a host runs more than one cursor consumer, and rename is atomic
// across processes as well as goroutines. The temp file carries a unique suffix
// (os.CreateTemp) for exactly that reason: a fixed temp name is atomic per
// writer and corrupt under two.
type FileCursorStore struct {
	// Root is the sprawl root; cursors land under Root/.sprawl/store/dispatch.
	Root string
}

var _ CursorStore = (*FileCursorStore)(nil)

// DispatchDir returns the dispatch-state directory for a sprawl root.
func DispatchDir(root string) string {
	return filepath.Join(root, ".sprawl", "store", "dispatch")
}

// cursorDoc is the on-disk shape.
//
// LastSeenSeq is a POINTER so that "the field is absent" is distinguishable from
// "the field is zero". With a plain int64 the two collapse, and `{}` — the shape
// a field rename leaves behind on every host — would decode to 0 with no error,
// which is the plausible zero this file exists to avoid.
//
// It is decoded into this struct rather than through `any`/map[string]any
// deliberately: a JSON number routed through `any` becomes a float64 and loses
// precision above 2^53, which on a bigint seq is a SILENT REWIND of the cursor.
type cursorDoc struct {
	LastSeenSeq *int64 `json:"last_seen_seq"`
}

// validConsumerChars is the character set a consumer name may use.
//
// Consumer names are COMPOSED at runtime — "notify:<recipient>",
// "sweeper.poke:<epoch>" — so one of the inputs is an agent name, and this is a
// boundary rather than an internal constant.
//
// WHAT THE CHECK ACTUALLY BUYS, measured rather than assumed, because the two
// halves differ and the difference is the interesting part:
//
//   - On the LOAD path it is load-bearing. Load calls os.ReadFile on the joined
//     path, so without this check "a/../../../../evil" reads <root>/evil.json —
//     an arbitrary-read primitive keyed on an agent name.
//   - On the SAVE path it is defence in depth. os.CreateTemp refuses a pattern
//     containing a path separator, so Save as written already fails on those
//     names. Verified by control: removing this check and keeping the
//     temp+rename shape leaves the escape blocked, while removing it and writing
//     with a direct os.WriteFile — the obvious first implementation — lands real
//     files at <root>/evil.json and <root>/.sprawl/store/evil.json, outside the
//     gitignore class this state depends on. So the check guards the shape a
//     future edit is most likely to reach for.
//
// It is a positive character-set test rather than a search for "..", because the
// obvious payloads do NOT escape: filepath.Join cleans, and the "cursor-" prefix
// absorbs one "..", so "../evil" merely fails with ENOENT. A blocklist tuned
// against those would look effective and let the one that works through.
const validConsumerChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.:_-"

// checkConsumer refuses any name that is not a single safe path element.
func checkConsumer(consumer string) error {
	if consumer == "" {
		return fmt.Errorf("store: cursor consumer name is empty")
	}
	if consumer == "." || consumer == ".." {
		return fmt.Errorf("store: cursor consumer name %q is a path element with directory meaning", consumer)
	}
	for _, r := range consumer {
		if !strings.ContainsRune(validConsumerChars, r) {
			return fmt.Errorf("store: cursor consumer name %q contains %q, which is not allowed: a consumer name becomes a filename, so it must be a single safe path element (allowed: letters, digits, and .:_-)",
				consumer, r)
		}
	}
	return nil
}

func (s *FileCursorStore) path(consumer string) string {
	return filepath.Join(DispatchDir(s.Root), "cursor-"+consumer+".json")
}

// Load reads this consumer's cursor. An absent cursor is (0, nil); everything
// else that goes wrong is an error naming the path.
func (s *FileCursorStore) Load(consumer string) (int64, error) {
	if err := checkConsumer(consumer); err != nil {
		return 0, err
	}
	path := s.path(consumer)

	body, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from the sprawl root and a charset-validated consumer name
	if errors.Is(err, os.ErrNotExist) {
		// The one case that is not an error: this consumer has never scanned.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: reading dispatch cursor %s: %w", path, err)
	}

	var doc cursorDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0, fmt.Errorf("store: dispatch cursor %s does not parse (delete it to re-scan from the start of the log): %w", path, err)
	}
	if doc.LastSeenSeq == nil {
		return 0, fmt.Errorf("store: dispatch cursor %s has no last_seen_seq field (delete it to re-scan from the start of the log)", path)
	}
	if *doc.LastSeenSeq < 0 {
		return 0, fmt.Errorf("store: dispatch cursor %s holds a negative position %d, which cannot be a log position (delete it to re-scan from the start of the log)",
			path, *doc.LastSeenSeq)
	}
	return *doc.LastSeenSeq, nil
}

// Save records seq for this consumer, atomically.
//
// Validation happens BEFORE anything is written, so a refused Save mutates
// nothing — including leaving no temp file behind. The reverse order returns the
// same error while putting the rejected value on disk, where the next Load
// either fails forever or returns a nonsense position.
func (s *FileCursorStore) Save(consumer string, seq int64) error {
	if err := checkConsumer(consumer); err != nil {
		return err
	}
	if seq < 0 {
		return fmt.Errorf("store: refusing to record dispatch cursor %d for consumer %q: a log position is never negative (0 is legal, and is how a re-scan is requested)", seq, consumer)
	}

	dir := DispatchDir(s.Root)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("store: creating %s: %w", dir, err)
	}
	body, err := json.Marshal(cursorDoc{LastSeenSeq: &seq})
	if err != nil {
		return fmt.Errorf("store: marshalling dispatch cursor: %w", err)
	}

	// A UNIQUE temp name, not a fixed one. A fixed ".tmp" is atomic for one
	// writer and mutually destructive for two, and a host runs more than one
	// cursor consumer.
	tmp, err := os.CreateTemp(dir, "cursor-"+consumer+".json.tmp")
	if err != nil {
		return fmt.Errorf("store: creating a temporary cursor file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Removes the temp file on every failure path below. A no-op after a
	// successful rename, since the name no longer exists.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: setting mode on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: writing %s: %w", tmpName, err)
	}
	// DELIBERATELY NOT fsync'd before the rename, which is the one place this
	// departs from the usual atomic-replace recipe. Stated with its reason
	// because it looks like an omission:
	//
	// fsync would buy DURABILITY — a guarantee that a machine losing power right
	// here comes back with the new value rather than the old one. This component
	// does not need that. The cursor is reconstructible by design, and the worst
	// case of losing the last write is that the dispatcher re-scans from an
	// earlier seq, where event_claims makes the repeated events no-ops. That is
	// the property the whole file is built around.
	//
	// It is not free, either: Save runs once per dispatched event, and an fsync
	// measured ~10ms on this host — so paying for durability nobody needs would
	// cap the dispatcher at ~100 events/second and add that latency to every
	// event's side effect. The rename still gives ATOMICITY, which is the
	// property that matters (a reader never sees a torn document), and atomicity
	// does not depend on the sync.
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.path(consumer)); err != nil {
		return fmt.Errorf("store: replacing dispatch cursor %s: %w", s.path(consumer), err)
	}
	return nil
}
