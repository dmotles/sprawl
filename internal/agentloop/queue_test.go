package agentloop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAgent = "test-agent"

func readRawJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func sampleEntry(id string, class Class) Entry {
	return Entry{
		ID:      id,
		Class:   class,
		From:    "weave",
		Subject: "hello",
		Body:    "body text",
		Tags:    []string{"a", "b"},
	}
}

func TestEnqueue_WritesFinalFileNoTmpLeftover(t *testing.T) {
	root := t.TempDir()
	e := sampleEntry("msg_01", ClassAsync)

	stored, err := Enqueue(root, testAgent, e)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if stored.Seq != 1 {
		t.Errorf("Seq = %d, want 1", stored.Seq)
	}
	if stored.EnqueuedAt == "" {
		t.Fatal("EnqueuedAt empty")
	}
	if _, err := time.Parse(time.RFC3339, stored.EnqueuedAt); err != nil {
		t.Errorf("EnqueuedAt %q not RFC3339: %v", stored.EnqueuedAt, err)
	}

	files := listFiles(t, PendingDir(root, testAgent))
	if len(files) != 1 {
		t.Fatalf("expected 1 file in pending, got %d: %v", len(files), files)
	}
	want := "0000000001-async-msg_01.json"
	if files[0] != want {
		t.Errorf("filename = %q, want %q", files[0], want)
	}
	for _, f := range files {
		if strings.HasPrefix(f, ".tmp-") {
			t.Errorf("found leftover tmp file: %q", f)
		}
	}
}

func TestEnqueue_SequenceIncrementsAcrossPendingAndDelivered(t *testing.T) {
	root := t.TempDir()

	a, err := Enqueue(root, testAgent, sampleEntry("a", ClassAsync))
	if err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	b, err := Enqueue(root, testAgent, sampleEntry("b", ClassAsync))
	if err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	if err := MarkDelivered(root, testAgent, a.ID); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	c, err := Enqueue(root, testAgent, sampleEntry("c", ClassAsync))
	if err != nil {
		t.Fatalf("enqueue c: %v", err)
	}

	if a.Seq != 1 || b.Seq != 2 || c.Seq != 3 {
		t.Errorf("seqs = %d,%d,%d; want 1,2,3", a.Seq, b.Seq, c.Seq)
	}
}

func TestEnqueue_JSONShape(t *testing.T) {
	root := t.TempDir()
	e := sampleEntry("msg_json", ClassInterrupt)
	stored, err := Enqueue(root, testAgent, e)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	files := listFiles(t, PendingDir(root, testAgent))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	path := filepath.Join(PendingDir(root, testAgent), files[0])
	raw := readRawJSON(t, path)

	if seq, ok := raw["seq"].(float64); !ok || int(seq) != 1 {
		t.Errorf("seq = %v, want 1", raw["seq"])
	}
	if class, _ := raw["class"].(string); class != "interrupt" {
		t.Errorf("class = %v, want interrupt", raw["class"])
	}
	tags, ok := raw["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("tags = %v, want [a b]", raw["tags"])
	}
	ts, _ := raw["enqueued_at"].(string)
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("enqueued_at %q not RFC3339: %v", ts, err)
	}
	_ = stored
}

func TestListPending_OrdersBySeq(t *testing.T) {
	root := t.TempDir()
	ids := []string{"first", "second", "third"}
	for _, id := range ids {
		if _, err := Enqueue(root, testAgent, sampleEntry(id, ClassAsync)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	got, err := ListPending(root, testAgent)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("got[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
		if e.ID != ids[i] {
			t.Errorf("got[%d].ID = %q, want %q", i, e.ID, ids[i])
		}
	}
}

func TestMarkDelivered_MovesFile(t *testing.T) {
	root := t.TempDir()
	stored, err := Enqueue(root, testAgent, sampleEntry("mv", ClassAsync))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := MarkDelivered(root, testAgent, stored.ID); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	pending := listFiles(t, PendingDir(root, testAgent))
	if len(pending) != 0 {
		t.Errorf("pending not empty: %v", pending)
	}
	delivered := listFiles(t, DeliveredDir(root, testAgent))
	want := "0000000001-async-mv.json"
	if len(delivered) != 1 || delivered[0] != want {
		t.Errorf("delivered = %v, want [%s]", delivered, want)
	}
}

func TestMarkDelivered_UnknownID_ReturnsError(t *testing.T) {
	root := t.TempDir()
	if _, err := Enqueue(root, testAgent, sampleEntry("real", ClassAsync)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := MarkDelivered(root, testAgent, "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestCleanupPending_RemovesOrphanTmpFiles(t *testing.T) {
	root := t.TempDir()
	pending := PendingDir(root, testAgent)
	if err := os.MkdirAll(pending, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tmpPath := filepath.Join(pending, ".tmp-abc123")
	if err := os.WriteFile(tmpPath, []byte("junk partial"), 0o644); err != nil {
		t.Fatalf("writing tmp: %v", err)
	}

	if err := CleanupPending(root, testAgent); err != nil {
		t.Fatalf("CleanupPending: %v", err)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file still present: err=%v", err)
	}
}

func TestCleanupPending_DedupesAgainstDelivered(t *testing.T) {
	root := t.TempDir()
	stored, err := Enqueue(root, testAgent, sampleEntry("dup", ClassAsync))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := MarkDelivered(root, testAgent, stored.ID); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	// Simulate a redelivery race: a pending file with the same id reappears.
	dupName := fmt.Sprintf("%010d-%s-%s.json", stored.Seq, stored.Class, stored.ID)
	dupPath := filepath.Join(PendingDir(root, testAgent), dupName)
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(PendingDir(root, testAgent), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dupPath, data, 0o644); err != nil {
		t.Fatalf("write dup: %v", err)
	}

	if err := CleanupPending(root, testAgent); err != nil {
		t.Fatalf("CleanupPending: %v", err)
	}

	if _, err := os.Stat(dupPath); !os.IsNotExist(err) {
		t.Errorf("duplicate pending still present: err=%v", err)
	}
	delivered := listFiles(t, DeliveredDir(root, testAgent))
	if len(delivered) != 1 {
		t.Errorf("delivered = %v, want 1 entry", delivered)
	}
}

func TestCleanupPending_IdempotentOnEmptyDirs(t *testing.T) {
	root := t.TempDir()
	if err := CleanupPending(root, testAgent); err != nil {
		t.Fatalf("CleanupPending on fresh dir: %v", err)
	}
	// calling twice should still be fine
	if err := CleanupPending(root, testAgent); err != nil {
		t.Fatalf("second CleanupPending: %v", err)
	}
}

func TestEnqueue_CrashSafety_NoPartialFileVisible(t *testing.T) {
	root := t.TempDir()
	pending := PendingDir(root, testAgent)
	if err := os.MkdirAll(pending, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tmpPath := filepath.Join(pending, ".tmp-deadbeef")
	if err := os.WriteFile(tmpPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("writing tmp: %v", err)
	}

	got, err := ListPending(root, testAgent)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	for _, e := range got {
		if e.ID == "deadbeef" {
			t.Errorf("ListPending surfaced a tmp file: %+v", e)
		}
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d entries", len(got))
	}

	if err := CleanupPending(root, testAgent); err != nil {
		t.Fatalf("CleanupPending: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp still present after cleanup: err=%v", err)
	}
}

func TestEnqueue_ConcurrentWritersAssignUniqueSeqs(t *testing.T) {
	root := t.TempDir()
	const n = 10

	var wg sync.WaitGroup
	results := make([]Entry, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("c-%d", i)
			got, err := Enqueue(root, testAgent, sampleEntry(id, ClassAsync))
			results[i] = got
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	seqs := make(map[int]bool)
	for _, r := range results {
		if r.Seq < 1 || r.Seq > n {
			t.Errorf("seq %d out of range", r.Seq)
		}
		if seqs[r.Seq] {
			t.Errorf("duplicate seq %d", r.Seq)
		}
		seqs[r.Seq] = true
	}
	for i := 1; i <= n; i++ {
		if !seqs[i] {
			t.Errorf("missing seq %d (want contiguous 1..%d)", i, n)
		}
	}

	// Also verify all files landed with the canonical name pattern.
	files := listFiles(t, PendingDir(root, testAgent))
	if len(files) != n {
		t.Fatalf("files = %d, want %d: %v", len(files), n, files)
	}
	re := regexp.MustCompile(`^\d{10}-async-c-\d+\.json$`)
	for _, f := range files {
		if !re.MatchString(f) {
			t.Errorf("unexpected filename %q", f)
		}
	}
}
