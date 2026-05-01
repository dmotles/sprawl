package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppend_PersistsToFile verifies that Append writes entries to disk and a
// fresh History instance reading the same file recovers them in order.
func TestAppend_PersistsToFile(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	if err := h.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	h.Append("a")
	h.Append("b")

	h2 := NewHistory(root)
	if err := h2.Load(); err != nil {
		t.Fatalf("Load (reread): %v", err)
	}
	if h2.Len() != 2 {
		t.Fatalf("Len = %d, want 2", h2.Len())
	}
	if got := h2.At(0); got != "a" {
		t.Errorf("At(0) = %q, want %q", got, "a")
	}
	if got := h2.At(1); got != "b" {
		t.Errorf("At(1) = %q, want %q", got, "b")
	}
}

// TestAppend_SkipsEmpty: Append("") is a no-op.
func TestAppend_SkipsEmpty(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	h.Append("")
	if h.Len() != 0 {
		t.Errorf("Len after Append(\"\") = %d, want 0", h.Len())
	}
}

// TestAppend_SkipsConsecutiveDup: dedup of consecutive duplicates only.
func TestAppend_SkipsConsecutiveDup(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	for _, s := range []string{"a", "a", "b", "b", "a"} {
		h.Append(s)
	}
	want := []string{"a", "b", "a"}
	if h.Len() != len(want) {
		t.Fatalf("Len = %d, want %d", h.Len(), len(want))
	}
	for i, w := range want {
		if got := h.At(i); got != w {
			t.Errorf("At(%d) = %q, want %q", i, got, w)
		}
	}
}

// TestAppend_TrimsAtCap: with a small cap, oldest entries are evicted.
// Uses an unexported setCap test seam to avoid needing 1001 appends.
func TestAppend_TrimsAtCap(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	h.setCap(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		h.Append(s)
	}
	if h.Len() != 3 {
		t.Fatalf("Len = %d, want 3", h.Len())
	}
	want := []string{"c", "d", "e"}
	for i, w := range want {
		if got := h.At(i); got != w {
			t.Errorf("In-memory At(%d) = %q, want %q", i, got, w)
		}
	}
	// On-disk should match too.
	h2 := NewHistory(root)
	h2.setCap(3)
	if err := h2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h2.Len() != 3 {
		t.Fatalf("Disk Len = %d, want 3", h2.Len())
	}
	for i, w := range want {
		if got := h2.At(i); got != w {
			t.Errorf("Disk At(%d) = %q, want %q", i, got, w)
		}
	}
}

// TestAppend_MultilineRoundTrip: entries with newlines, tabs, quotes round-trip.
func TestAppend_MultilineRoundTrip(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	entry := "line1\nline2\twith \"quotes\" and \\backslash"
	h.Append(entry)

	h2 := NewHistory(root)
	if err := h2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h2.Len() != 1 {
		t.Fatalf("Len = %d, want 1", h2.Len())
	}
	if got := h2.At(0); got != entry {
		t.Errorf("At(0) = %q, want %q", got, entry)
	}
}

// TestNewHistory_MissingFileOK: a sprawlRoot dir without input-history is OK.
func TestNewHistory_MissingFileOK(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	if err := h.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if h.Len() != 0 {
		t.Errorf("Len = %d, want 0", h.Len())
	}
}

// TestNewHistory_CorruptLineSkipped: a garbage line is skipped, valid lines
// are loaded.
func TestNewHistory_CorruptLineSkipped(t *testing.T) {
	root := t.TempDir()
	sprawlDir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(sprawlDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(sprawlDir, "input-history")
	// First line is invalid JSON, second is a valid JSON-encoded string.
	content := "not-json-at-all\n" + `"valid entry"` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h := NewHistory(root)
	if err := h.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (corrupt skipped)", h.Len())
	}
	if got := h.At(0); got != "valid entry" {
		t.Errorf("At(0) = %q, want %q", got, "valid entry")
	}
}

// TestPrevNext_StashAndRestore: navigate back, then forward; live buffer
// is restored at the front.
func TestPrevNext_StashAndRestore(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	for _, s := range []string{"a", "b", "c"} {
		h.Append(s)
	}
	// First Prev stashes "live" and returns newest entry.
	got, ok := h.Prev("live")
	if !ok || got != "c" {
		t.Fatalf("Prev(\"live\") = (%q,%v), want (\"c\",true)", got, ok)
	}
	got, ok = h.Prev("ignored")
	if !ok || got != "b" {
		t.Fatalf("Prev = (%q,%v), want (\"b\",true)", got, ok)
	}
	got, isLive, ok := h.Next()
	if !ok || isLive || got != "c" {
		t.Fatalf("Next = (%q,isLive=%v,%v), want (\"c\",false,true)", got, isLive, ok)
	}
	got, isLive, ok = h.Next()
	if !ok || !isLive || got != "live" {
		t.Fatalf("Next at front = (%q,isLive=%v,%v), want (\"live\",true,true)", got, isLive, ok)
	}
}

// TestPrev_PastOldestStays: chosen semantics — Prev past the oldest entry
// returns ok=true with the oldest entry (clamps, does not advance off the end).
func TestPrev_PastOldestStays(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	h.Append("oldest")
	h.Append("newest")
	if got, ok := h.Prev("live"); !ok || got != "newest" {
		t.Fatalf("Prev #1 = (%q,%v), want (\"newest\",true)", got, ok)
	}
	if got, ok := h.Prev("live"); !ok || got != "oldest" {
		t.Fatalf("Prev #2 = (%q,%v), want (\"oldest\",true)", got, ok)
	}
	// Past the oldest: clamp at oldest.
	got, ok := h.Prev("live")
	if !ok || got != "oldest" {
		t.Errorf("Prev past oldest = (%q,%v), want (\"oldest\",true) [clamp semantics]", got, ok)
	}
}

// TestNext_BeforeAnyPrev: Next with no Prev history returns ok=false.
func TestNext_BeforeAnyPrev(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	h.Append("a")
	h.Append("b")
	got, isLive, ok := h.Next()
	if ok {
		t.Errorf("Next before any Prev = (%q,isLive=%v,ok=%v), want ok=false", got, isLive, ok)
	}
}

// TestSearchOlder_FindsNewestFirst: substring search, descending from fromIdx.
func TestSearchOlder_FindsNewestFirst(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	for _, s := range []string{"apple", "banana", "apricot", "cherry"} {
		h.Append(s)
	}
	// Search starting from the end (length): should find "apricot" at idx 2.
	entry, idx, ok := h.SearchOlder("ap", h.Len())
	if !ok || entry != "apricot" || idx != 2 {
		t.Errorf("SearchOlder(\"ap\", %d) = (%q,%d,%v), want (\"apricot\",2,true)", h.Len(), entry, idx, ok)
	}
	// Search again starting from idx 2 (exclusive): should find "apple" at idx 0.
	entry, idx, ok = h.SearchOlder("ap", 2)
	if !ok || entry != "apple" || idx != 0 {
		t.Errorf("SearchOlder(\"ap\", 2) = (%q,%d,%v), want (\"apple\",0,true)", entry, idx, ok)
	}
	// Search starting from idx 0: nothing older.
	_, _, ok = h.SearchOlder("ap", 0)
	if ok {
		t.Error("SearchOlder(\"ap\", 0) should return ok=false")
	}
}

// TestSearchOlder_NoMatch: empty query and no-match return ok=false.
func TestSearchOlder_NoMatch(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	h.Append("apple")
	h.Append("banana")

	if _, _, ok := h.SearchOlder("", h.Len()); ok {
		t.Error("SearchOlder(\"\", ...) should return ok=false")
	}
	if _, _, ok := h.SearchOlder("zzzz", h.Len()); ok {
		t.Error("SearchOlder(\"zzzz\", ...) should return ok=false")
	}
}

// TestEphemeral_NoSprawlRoot: NewHistory("") works in memory; no file on disk.
func TestEphemeral_NoSprawlRoot(t *testing.T) {
	cwd, _ := os.Getwd()
	h := NewHistory("")
	if err := h.Load(); err != nil {
		t.Fatalf("Load on empty root: %v", err)
	}
	h.Append("a")
	h.Append("b")
	if h.Len() != 2 {
		t.Errorf("Len = %d, want 2", h.Len())
	}
	// No file should exist anywhere obvious. Sanity-check cwd has no input-history.
	stray := filepath.Join(cwd, "input-history")
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("ephemeral history wrote unexpected file at %s", stray)
	}
}

// TestReset_ClearsCursorAndStash: after Prev,Prev,Reset, Next is ok=false.
func TestReset_ClearsCursorAndStash(t *testing.T) {
	root := t.TempDir()
	h := NewHistory(root)
	_ = h.Load()
	h.Append("a")
	h.Append("b")
	if _, ok := h.Prev("live"); !ok {
		t.Fatalf("Prev #1 expected ok=true")
	}
	if _, ok := h.Prev("live"); !ok {
		t.Fatalf("Prev #2 expected ok=true")
	}
	h.Reset()
	got, isLive, ok := h.Next()
	if ok {
		t.Errorf("Next after Reset = (%q,isLive=%v,ok=%v), want ok=false", got, isLive, ok)
	}
}

// guard against accidental import-pruning of strings during early TDD.
var _ = strings.TrimSpace
