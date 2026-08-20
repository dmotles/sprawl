package store

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The dispatch cursor.
//
// WHAT THESE TESTS ARE ACTUALLY DEFENDING, because it is easy to read them as
// "a file holding an integer" and then relax the one property that matters:
// THE CURSOR IS NOT THE CORRECTNESS MECHANISM. event_claims is. The cursor only
// says where a scan may start, so losing it must cost a re-scan and nothing
// else.
//
// Two assertions carry that, and they pull in opposite directions:
//
//   - ABSENT means 0 with no error, which is what makes cursor loss recoverable
//     rather than fatal, and makes "delete the cursor" a supported recovery.
//   - Every OTHER failure to read a cursor — malformed bytes, a missing field, a
//     stored negative, an unreadable file — is an ERROR. Collapsing any of them
//     into 0 would make a corrupt cursor indistinguishable from a first run, and
//     the consequence is not cosmetic: it re-scans the entire log on every poll,
//     forever, with nothing anywhere saying why.
//
// The third property, and the one whose absence a reader will not notice: the
// cursor must ADVANCE. A Save that silently declines to overwrite satisfies
// every round-trip assertion while producing exactly that same forever-re-scan.

func cursorPath(t *testing.T, root, consumer string) string {
	t.Helper()
	return filepath.Join(DispatchDir(root), "cursor-"+consumer+".json")
}

func TestFileCursorStore_RoundTrip(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}

	if err := s.Save("dispatcher", 42); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != 42 {
		t.Errorf("Load after Save(42) = %d, want 42", got)
	}
}

// A seq near the top of int64 round-trips exactly.
//
// Not a boundary-value ritual: `events.seq` is a bigint, and an implementation
// that decodes through `any` — the default shape of a quick
// json.Unmarshal-into-a-map — lands the value in a float64 and loses precision
// above 2^53. Measured: 9007199254740993 decodes as 9007199254740992. That is a
// SILENT REWIND of the cursor, which re-delivers events, and it is the same
// forever-re-scan class the header describes arriving by a third route. 42 is
// representable in a float64, so RoundTrip above cannot see it.
func TestFileCursorStore_LargeSeqRoundTripsExactly(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}

	const want = math.MaxInt64 - 1
	if err := s.Save("dispatcher", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("Load = %d, want %d — a cursor that loses precision silently rewinds and re-delivers", got, want)
	}
}

// The on-disk format is pinned POSITIVELY, by planting a hand-written good
// document and requiring Load to accept it.
//
// Without this, the unusable-cursor table below pins the format only negatively,
// and "negatively" turns out to mean "guesses at it": under a bare-decimal file
// format — `strconv.ParseInt` on the file contents, which is a perfectly
// reasonable and trivially atomic design — the table's `12` row would be a false
// RED while every other row still passed. This test is what makes the format a
// stated contract rather than an inference, so the table is asserting a real
// requirement instead of an assumption about the implementation.
//
// The filename is part of that contract too, which is why cursorPath spells it
// out rather than asking the implementation where it put things.
func TestFileCursorStore_ReadsAHandWrittenCursorDocument(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}
	if err := os.MkdirAll(DispatchDir(root), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cursorPath(t, root, "dispatcher"), []byte(`{"last_seen_seq":9}`), 0o600); err != nil {
		t.Fatalf("planting cursor: %v", err)
	}

	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load on a hand-written cursor document: %v", err)
	}
	if got != 9 {
		t.Errorf("Load = %d, want 9 — the documented on-disk shape is {\"last_seen_seq\":<n>} at %s", got, cursorPath(t, root, "dispatcher"))
	}
}

// THE CURSOR MUST ADVANCE, and it must also be able to go backwards.
//
// This is the assertion whose absence is invisible. Nothing else in this file
// does Save→Save→Load on one consumer, so an implementation using
// O_CREATE|O_EXCL and swallowing EEXIST — or one that writes a temp file and
// skips the rename when the target exists — passes every other test here while
// the cursor never moves. The host then re-scans the whole log on every poll
// with no error and no log line: the exact symptom the malformed-cursor
// assertions below exist to prevent, arriving by a different route.
//
// The backwards leg is not symmetry for its own sake: resetting a cursor to 0 is
// how the design says a projection is rebuilt, so a monotonic-clamping Save
// would be a defect and would otherwise pass.
func TestFileCursorStore_SaveOverwritesInBothDirections(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}

	if err := s.Save("dispatcher", 5); err != nil {
		t.Fatalf("Save(5): %v", err)
	}
	if err := s.Save("dispatcher", 9); err != nil {
		t.Fatalf("Save(9): %v", err)
	}
	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != 9 {
		t.Fatalf("Load after Save(5) then Save(9) = %d, want 9 — the cursor did not advance, so this host re-scans the whole log on every poll", got)
	}

	if err := s.Save("dispatcher", 0); err != nil {
		t.Fatalf("Save(0) after Save(9): %v", err)
	}
	got, err = s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load after rewind: %v", err)
	}
	if got != 0 {
		t.Errorf("Load after rewinding to 0 = %d, want 0 — resetting a cursor is a supported operation, not a no-op", got)
	}
}

// A cursor that has never been written is the FIRST-RUN state, and it is a
// measured absence rather than a guess: the file is not there. Returning an
// error here would make a host that has never dispatched unable to start, and
// would turn "delete the cursor to force a re-scan" — the recovery the design
// relies on — into a way to brick the host instead.
func TestFileCursorStore_AbsentCursorIsZeroNotAnError(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}

	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load on a fresh root must not error, got: %v", err)
	}
	if got != 0 {
		t.Errorf("Load on a fresh root = %d, want 0", got)
	}
}

// An UNUSABLE cursor is a different fact from an absent one and must not
// collapse into the same answer.
//
// The syntactic case (a truncated document) is the easy one — encoding/json
// rejects it however carelessly Load is written, so an assertion that only
// covers that case proves nothing about Load. The cases that matter are
// SEMANTIC, and each is a live defect route rather than a hypothetical:
//
//	{}                      a cursor whose field is simply absent
//	{"seq":9}               what every host's cursor becomes the day someone
//	                        renames the field in a refactor
//	{"last_seen_seq":-5}    a negative that got past a Save-side check, or was
//	                        hand-edited
//	""                      the artifact a non-atomic Save leaves after a crash
//
// Under a Load that unmarshals into a plain int64 and reports os.IsNotExist as
// the only absence, the first three return (0, nil) or (-5, nil) — a corrupt
// cursor reported as a first run, which is the plausible zero /testing-practices
// forbids. Each must error, and each error must NAME THE PATH: a
// "bad cursor" report with no path sends an operator through every consumer's
// file by hand.
func TestFileCursorStore_UnusableCursorIsAnErrorNamingThePath(t *testing.T) {
	bodies := map[string]string{
		"truncated":     `{"last_seen_seq":`,
		"empty":         ``,
		"no fields":     `{}`,
		"renamed field": `{"seq":9}`,
		"negative":      `{"last_seen_seq":-5}`,
		"not an object": `12`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			s := &FileCursorStore{Root: root}
			if err := s.Save("dispatcher", 7); err != nil {
				t.Fatalf("Save: %v", err)
			}
			path := cursorPath(t, root, "dispatcher")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("planting cursor body: %v", err)
			}

			got, err := s.Load("dispatcher")
			if err == nil {
				t.Fatalf("Load on cursor body %q returned (%d, nil); an unusable cursor must not be reported as a first run", body, got)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("Load error does not name the cursor path %q: %v", path, err)
			}
		})
	}
}

// Two consumers on one host keep independent cursors. The design has more than
// one cursor consumer by construction (the dispatcher now, the embedder and the
// memory extractor later), and one shared file would let any of them skip
// another's unscanned events.
func TestFileCursorStore_ConsumersAreIndependent(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}

	if err := s.Save("dispatcher", 5); err != nil {
		t.Fatalf("Save dispatcher: %v", err)
	}
	if err := s.Save("sweeper", 9); err != nil {
		t.Fatalf("Save sweeper: %v", err)
	}

	d, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load dispatcher: %v", err)
	}
	sw, err := s.Load("sweeper")
	if err != nil {
		t.Fatalf("Load sweeper: %v", err)
	}
	if d != 5 || sw != 9 {
		t.Errorf("cursors interfered: dispatcher=%d (want 5), sweeper=%d (want 9)", d, sw)
	}
}

// Load addresses the cursor by exact filename and never scans the directory.
//
// NAMED FOR WHAT IT MEASURES. An earlier version of this test was called
// "SaveIsAtomic" and asserted exactly what is asserted here, which is nothing
// about atomicity: a one-line truncate-in-place os.WriteFile — the canonical way
// to leave a half-written cursor after a crash — passes it unchanged. Atomicity
// has no single-threaded observable, so it is constrained by the concurrent
// probe below instead, and this test keeps only the claim it can support.
func TestFileCursorStore_LoadIgnoresLeftoverTempFiles(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}

	if err := s.Save("dispatcher", 11); err != nil {
		t.Fatalf("Save: %v", err)
	}
	junk := cursorPath(t, root, "dispatcher") + ".tmp123"
	if err := os.WriteFile(junk, []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("planting temp file: %v", err)
	}

	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load with a leftover temp file present: %v", err)
	}
	if got != 11 {
		t.Errorf("Load = %d, want 11 — a leftover temp file must not be read as the cursor", got)
	}
}

// A successful Save leaves no temp-file residue.
//
// Unasserted, this is a leaked file per poll on a process that polls every 2-5
// seconds for as long as a host is up.
func TestFileCursorStore_SaveLeavesNoTempResidue(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}

	for i := int64(1); i <= 3; i++ {
		if err := s.Save("dispatcher", i); err != nil {
			t.Fatalf("Save(%d): %v", i, err)
		}
	}

	entries, err := os.ReadDir(DispatchDir(root))
	if err != nil {
		t.Fatalf("reading dispatch dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "cursor-dispatcher.json" {
		t.Errorf("dispatch dir after 3 saves holds %v, want exactly [cursor-dispatcher.json]", names)
	}
}

// EVERY Save REPLACES THE FILE RATHER THAN REWRITING IT IN PLACE.
//
// This is a MECHANISM assertion, and it is deliberate — labelled as one rather
// than dressed up as a property assertion, because /testing-practices' rule
// against mechanism assertions exists to stop a test blocking a correct
// refactor, and here the mechanism IS the requirement. A crash mid-Save must not
// leave a half-written cursor, and replace-by-rename is the only shape on a
// POSIX filesystem that delivers that. A future "refactor" away from it is a
// defect, so pinning it is the point.
//
// The property-level version — a concurrent reader never observes a torn
// document — is asserted below as well, and it is the weaker of the two: a
// truncate-then-write window is a couple of microseconds wide, so a concurrent
// probe MISSES a genuinely non-atomic Save on most runs. That direction of
// unreliability is a false GREEN, which is why the deterministic check is here
// and is the one that carries the claim.
func TestFileCursorStore_EachSaveReplacesTheFileRatherThanRewritingIt(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}
	path := cursorPath(t, root, "dispatcher")

	if err := s.Save("dispatcher", 1); err != nil {
		t.Fatalf("Save(1): %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first Save: %v", err)
	}
	if err := s.Save("dispatcher", 2); err != nil {
		t.Fatalf("Save(2): %v", err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after second Save: %v", err)
	}

	if os.SameFile(first, second) {
		t.Error("the cursor file is the same inode across two Saves, so Save rewrites in place; a crash mid-write would leave a half-written cursor and this host would then refuse to read its own cursor on every poll")
	}
}

// A READER NEVER SEES A TORN CURSOR — the property-level companion to the
// mechanism assertion above, and the only place a concurrent interleaving is
// exercised at all.
//
// TWO savers on ONE consumer, deliberately. One saver would leave a whole defect
// class invisible: a Save using a FIXED temp filename is atomic per writer and
// corrupt under two concurrent writers for the same consumer, which is exactly
// the shape a host running a dispatcher and a sweeper produces.
//
// Read the failure direction before trusting a green: this probe cannot produce
// a false RED (an atomic rename keeps the old inode readable through an in-flight
// open, so a correct Save cannot make it fire), and it CAN produce a false green
// when the scheduler never lands a reader inside the window. So it is coverage,
// not proof — the deterministic proof is the inode check above.
func TestFileCursorStore_ConcurrentSavesAndLoadsNeverTear(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}
	if err := s.Save("dispatcher", 1); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	// 1000 was chosen against a measured cost, not picked round: at 3000 this
	// test alone ran 10.6s under -race, which is a real fraction of the gate for
	// a probe that is coverage rather than proof. The deterministic inode check
	// above is what carries the atomicity claim, so trading iterations for gate
	// time here weakens the weaker of the two instruments, not the load-bearing
	// one. If you are hunting a suspected tearing bug, raise it locally.
	const (
		iterations = 1000
		savers     = 2
		readers    = 3
	)
	// Fully populated before any goroutine starts, then read-only.
	written := map[int64]bool{}
	for i := int64(1); i <= iterations+1; i++ {
		written[i] = true
	}

	var wg sync.WaitGroup
	errCh := make(chan error, savers*iterations+readers*iterations)

	for w := 0; w < savers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int64(2); i <= iterations+1; i++ {
				if err := s.Save("dispatcher", i); err != nil {
					errCh <- fmt.Errorf("Save(%d): %w", i, err)
				}
			}
		}()
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				got, err := s.Load("dispatcher")
				if err != nil {
					errCh <- fmt.Errorf("Load saw a torn cursor: %w", err)
					continue
				}
				if !written[got] {
					errCh <- fmt.Errorf("Load returned %d, which was never written", got)
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	var reported int
	for err := range errCh {
		if reported < 5 {
			t.Error(err)
		}
		reported++
	}
	if reported > 5 {
		t.Errorf("... and %d further tearing observations", reported-5)
	}
}

// The consumer name reaches the filesystem, so it is a BOUNDARY and gets
// validated as one.
//
// Not defensive padding: the design's own consumer names are COMPOSED
// ("notify:<recipient>", "sweeper.poke:<epoch>"), so a consumer string is built
// from runtime values rather than being a fixed literal, and one of those values
// is an agent name.
//
// The payloads are chosen to actually escape, which the obvious ones do not:
// filepath.Join cleans, and the "cursor-" prefix absorbs one "..", so
// "../evil" only reaches <dispatch>/cursor-../evil.json and fails with ENOENT —
// an assertion built on that fires for the wrong reason and would pass an
// implementation with no validation at all. "a/../../../../evil" is the one that
// matters: its parent directory exists, so an unvalidated Save SUCCEEDS and
// writes outside .sprawl/ — past the gitignore class this state depends on, and
// an arbitrary-write primitive keyed on an agent name.
//
// The residue check after the loop is what makes the refusal mean something: an
// implementation that writes the file and THEN returns an error satisfies every
// error assertion.
func TestFileCursorStore_RejectsConsumerNamesThatEscapeTheDirectory(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}

	bad := []string{
		"a/../../../../evil", // escapes to <root>/evil.json — parent exists, so an unvalidated Save succeeds
		"../../../evil",      // escapes to <root>/.sprawl/store/evil.json
		"/etc/passwd",        // absolute
		"../evil",
		"a/b",
		"..",
		".",
		"",
	}
	for _, c := range bad {
		if err := s.Save(c, 1); err == nil {
			t.Errorf("Save(%q) was accepted; a consumer name that is not a single safe path element must be refused", c)
		}
		if _, err := s.Load(c); err == nil {
			t.Errorf("Load(%q) was accepted; a consumer name that is not a single safe path element must be refused", c)
		}
	}

	// Nothing may have been created anywhere by a refused call — not in the
	// dispatch directory, and above all not above it.
	//
	// A ReadDir failure is reported rather than tolerated: `err == nil && ...`
	// would silently pass on EACCES, which is the non-asserting-fallback shape
	// the repo's rules call out. Only absence is acceptable, and it is the
	// expected case here — a refused Save should not even have created the
	// directory.
	entries, err := os.ReadDir(DispatchDir(root))
	switch {
	case err == nil && len(entries) != 0:
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("refused consumer names created %v in the dispatch directory", names)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		t.Errorf("could not check the dispatch directory for residue, so this assertion measured nothing: %v", err)
	}
	for _, leak := range []string{
		filepath.Join(root, "evil.json"),
		filepath.Join(root, ".sprawl", "store", "evil.json"),
	} {
		if _, err := os.Stat(leak); err == nil {
			t.Errorf("a refused consumer name wrote outside the dispatch directory: %s", leak)
		}
	}
}

// NEGATIVE CONTROL for the traversal test above: subjects known clean, where the
// probe must stay QUIET.
//
// Direction stated explicitly because naming a control never tells you it is
// aimed right. This is the NEGATIVE control: a subject known clean, where the
// traversal probe must stay quiet. The POSITIVE control is the complementary
// one — the traversal assertions run against a validation-free Save, where
// "a/../../../../evil" must land a real file at <root>/evil.json; that mutation
// and its output are recorded in this commit's message. This half exists
// because a validator that refused EVERYTHING would satisfy every traversal
// assertion perfectly while making the cursor unusable for every real consumer.
//
// It saves a DISTINCT seq per name and reads each back, rather than checking
// only that Load does not error. Checking only for an absent error would be
// near-vacuous — Load of a name that was never written also returns no error —
// and it would miss the live risk, which is COLLISION rather than rejection: an
// implementation that sanitises ':' and '.' away maps notify:alice and
// notify.alice onto one file, and consumer names are composed from agent names.
// ConsumersAreIndependent cannot catch that, because "dispatcher" and "sweeper"
// stay distinct under any normalisation.
func TestFileCursorStore_ComposedConsumerNamesAreAcceptedAndDistinct(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}

	// Deliberately includes near-collisions under a sanitising implementation:
	// ':' vs '.' vs '_' spelling the same words.
	//
	// A case-differing pair (notify:Alice vs notify:alice) is deliberately NOT
	// here. It would be a guaranteed false RED on a case-insensitive filesystem
	// — APFS and HFS+ default, and Windows — where the two names share one file
	// however correct the implementation is. Case sensitivity is a property of
	// the filesystem, not of FileCursorStore, and the ':'/'.'/'_' triple already
	// catches every sanitising-collision defect the case pair would.
	names := []string{
		"dispatcher",
		"notify:alice",
		"notify.alice",
		"notify_alice",
		"sweeper.poke:3",
		"sweeper.poke:4",
		"spawn_intent-1",
	}
	for i, n := range names {
		if err := s.Save(n, int64(i+1)); err != nil {
			t.Fatalf("Save(%q) was refused, but this is a consumer name the design composes: %v", n, err)
		}
	}
	for i, n := range names {
		got, err := s.Load(n)
		if err != nil {
			t.Errorf("Load(%q): %v", n, err)
			continue
		}
		if got != int64(i+1) {
			t.Errorf("Load(%q) = %d, want %d — two distinct consumer names collided onto one cursor", n, got, i+1)
		}
	}
}

// A negative sequence is a programming error, not a rewind. Rewinding to 0 is
// legitimate and covered above; a negative can only come from arithmetic that
// went wrong upstream.
//
// The second half is the part that is easy to miss: A REFUSED SAVE MUST MUTATE
// NOTHING. An implementation that marshals, writes, and then validates returns
// the error this test wants while leaving -1 on disk, so the next Load either
// errors forever or returns a nonsense position.
func TestFileCursorStore_RejectsNegativeSeqWithoutMutating(t *testing.T) {
	s := &FileCursorStore{Root: t.TempDir()}

	if err := s.Save("dispatcher", 5); err != nil {
		t.Fatalf("Save(5): %v", err)
	}
	if err := s.Save("dispatcher", -1); err == nil {
		t.Error("Save(-1) was accepted; a negative cursor cannot come from a real log position")
	}
	got, err := s.Load("dispatcher")
	if err != nil {
		t.Fatalf("Load after a refused Save: %v", err)
	}
	if got != 5 {
		t.Errorf("cursor is %d after a REFUSED Save(-1), want 5 — a rejected input must not reach the file", got)
	}

	// A refused Save must also leave no temp file behind. "Marshal, write the
	// temp, validate, return the error, forget to unlink" satisfies the value
	// assertion above and leaks a file per rejected call.
	entries, err := os.ReadDir(DispatchDir(s.Root))
	if err != nil {
		t.Fatalf("reading dispatch dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "cursor-dispatcher.json" {
		t.Errorf("dispatch dir after a refused Save holds %v, want exactly [cursor-dispatcher.json]", names)
	}
}

// The cursor lives under .sprawl/, which is what puts it inside the `.sprawl/*`
// gitignore class that scripts/test-gitignore-classes.sh asserts rather than
// assumes. A cursor is not a secret, but it IS per-host state in a shared tree,
// and that class is the mechanism keeping every such file out of the repo.
func TestDispatchDir_IsUnderTheSprawlStateDirectory(t *testing.T) {
	got := DispatchDir("/tmp/example-root")
	want := filepath.Join("/tmp/example-root", ".sprawl", "store", "dispatch")
	if got != want {
		t.Errorf("DispatchDir = %q, want %q", got, want)
	}
}

// Owner-only modes, matching FileSpiller (see TestFileSpiller_FileModeIsOwnerOnly).
//
// The cursor's CONTENT is one integer, but its FILENAME embeds composed consumer
// names — which include agent names and notification recipients — so the
// directory listing is the leak surface, and this host runs many agents under
// one uid.
func TestFileCursorStore_ModesAreOwnerOnly(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}
	if err := s.Save("dispatcher", 1); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(cursorPath(t, root, "dispatcher"))
	if err != nil {
		t.Fatalf("stat cursor: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("cursor file mode is %v, want no group/other bits", fi.Mode().Perm())
	}

	di, err := os.Stat(DispatchDir(root))
	if err != nil {
		t.Fatalf("stat dispatch dir: %v", err)
	}
	if di.Mode().Perm()&0o007 != 0 {
		t.Errorf("dispatch dir mode is %v, want no other bits", di.Mode().Perm())
	}
}

// A Load that fails for a reason OTHER than absence must surface.
//
// Distinct from the unusable-content cases above: those are a readable file with
// bad bytes, this is a file that cannot be read at all. Only os.ErrNotExist may
// become (0, nil) — otherwise a permission problem on the dispatch directory
// reads as "first run" on every poll, forever, and the host silently re-scans
// the whole log every few seconds with nothing saying why.
//
// The skip is derived from an INDEPENDENT instrument — os.ReadFile, not s.Load —
// and it MEASURES the precondition rather than proxying it. Do not "simplify" it
// to a uid check: euid != 0 is only a proxy for "mode 0000 denies reads", and it
// is wrong under CAP_DAC_OVERRIDE (routinely granted in containers) and on
// filesystems where the mode is advisory, where it produces a false RED. Do not
// derive it from s.Load either — a skip condition the bug under test can satisfy
// is a test that disables itself on failure.
func TestFileCursorStore_UnreadableCursorIsAnError(t *testing.T) {
	root := t.TempDir()
	s := &FileCursorStore{Root: root}
	if err := s.Save("dispatcher", 4); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := cursorPath(t, root, "dispatcher")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if _, err := os.ReadFile(path); err == nil { //nolint:gosec // G304: path is the test's own temp dir
		t.Skip("mode 0000 does not deny reads here, so this probe cannot observe the property")
	}

	got, err := s.Load("dispatcher")
	if err == nil {
		t.Fatalf("Load on an unreadable cursor returned (%d, nil); only absence may read as a first run", got)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("Load error does not name the cursor path %q: %v", path, err)
	}
}
