// Package tui — input history (QUM-410).
//
// History is a per-sprawlRoot persistent shell-style input history. Entries
// are JSON-encoded one per line in <sprawlRoot>/input-history. Capacity is
// capped (default 1000); consecutive duplicates and empty entries are
// skipped. An empty sprawlRoot makes the history ephemeral (in-memory only).
package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	historyFileName    = "input-history"
	defaultHistoryCap  = 1000
	historyFilePerm    = 0o644
	historyTmpFilePerm = 0o644
)

// History stores recent user inputs and supports shell-style navigation.
type History struct {
	cap      int
	path     string // empty => ephemeral
	entries  []string
	cursor   int    // index into entries; len(entries) means "at live buffer"
	stash    string // saved live buffer set by first Prev
	hasStash bool
}

// NewHistory returns a new History rooted at sprawlRoot. The file is loaded
// lazily — call Load() to read it. If sprawlRoot is "" the history is
// ephemeral (no file IO).
func NewHistory(sprawlRoot string) *History {
	h := &History{cap: defaultHistoryCap}
	if sprawlRoot != "" {
		h.path = filepath.Join(sprawlRoot, ".sprawl", historyFileName)
	}
	h.cursor = 0
	return h
}

// setCap is an unexported test seam — sets the in-memory cap. Trims existing
// entries if needed.
func (h *History) setCap(n int) {
	if n <= 0 {
		n = defaultHistoryCap
	}
	h.cap = n
	if len(h.entries) > h.cap {
		h.entries = append([]string(nil), h.entries[len(h.entries)-h.cap:]...)
	}
}

// Load reads the history file. ENOENT is treated as success with empty
// entries. Lines that fail to JSON-decode as a string are skipped silently.
// Returns nil for ephemeral histories.
func (h *History) Load() error {
	if h.path == "" {
		return nil
	}
	data, err := os.ReadFile(h.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var loaded []string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var s string
		if jerr := json.Unmarshal(line, &s); jerr != nil {
			continue
		}
		loaded = append(loaded, s)
	}
	if len(loaded) > h.cap {
		loaded = loaded[len(loaded)-h.cap:]
	}
	h.entries = loaded
	h.cursor = len(h.entries)
	h.stash = ""
	h.hasStash = false
	return nil
}

// Append adds entry to the history. Empty strings and exact duplicates of the
// most-recent entry are skipped. Persists to disk if a file path is set.
func (h *History) Append(entry string) {
	if entry == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return
	}
	h.entries = append(h.entries, entry)
	if len(h.entries) > h.cap {
		h.entries = append([]string(nil), h.entries[len(h.entries)-h.cap:]...)
	}
	h.cursor = len(h.entries)
	_ = h.save()
}

// save writes the in-memory entries to disk atomically (tmp+rename). Best
// effort — errors are returned but callers don't propagate them.
func (h *History) save() error {
	if h.path == "" {
		return nil
	}
	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range h.entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(dir, historyFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, historyFilePerm); err != nil {
		// non-fatal
		_ = err
	}
	return os.Rename(tmpPath, h.path)
}

// Prev moves the cursor toward older entries and returns the entry at the new
// position. The first call stashes liveBuf so a future Next can restore it.
// At the oldest entry, repeated calls clamp and continue to return it.
// Returns ok=false only if there are no entries.
func (h *History) Prev(liveBuf string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if !h.hasStash {
		h.stash = liveBuf
		h.hasStash = true
		h.cursor = len(h.entries)
	}
	if h.cursor > 0 {
		h.cursor--
	}
	return h.entries[h.cursor], true
}

// Next moves the cursor toward newer entries.
//   - Returns the next entry with isLive=false when one exists.
//   - When stepping past the newest entry while a stash exists, returns the
//     stash with isLive=true exactly once, then resets so subsequent Next
//     calls (without an intervening Prev) return ok=false.
//   - Returns ok=false when no Prev has set a stash.
func (h *History) Next() (string, bool, bool) {
	if !h.hasStash {
		return "", false, false
	}
	if h.cursor < len(h.entries)-1 {
		h.cursor++
		return h.entries[h.cursor], false, true
	}
	// At or past newest — return stash and reset.
	stash := h.stash
	h.Reset()
	return stash, true, true
}

// Reset clears cursor and stash state.
func (h *History) Reset() {
	h.cursor = len(h.entries)
	h.stash = ""
	h.hasStash = false
}

// SearchOlder finds the newest entry strictly older than fromIdx whose value
// contains query. Returns the entry, its index, and ok=true. An empty query
// returns ok=false.
func (h *History) SearchOlder(query string, fromIdx int) (string, int, bool) {
	if query == "" {
		return "", 0, false
	}
	if fromIdx > len(h.entries) {
		fromIdx = len(h.entries)
	}
	for i := fromIdx - 1; i >= 0; i-- {
		if strings.Contains(h.entries[i], query) {
			return h.entries[i], i, true
		}
	}
	return "", 0, false
}

// Len returns the number of stored entries.
func (h *History) Len() int { return len(h.entries) }

// At returns the entry at index i. Panics if out of range.
func (h *History) At(i int) string { return h.entries[i] }
