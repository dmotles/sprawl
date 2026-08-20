package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Git metadata for run_finished: the commit a run was based on, and a digest of
// what was uncommitted while it ran.
//
// The digest exists so a benchmark replay can tell "this run started from a
// clean tree at SHA X" from "this run started from SHA X plus 40 files of
// somebody else's work in progress". Those two produce very different agent
// behaviour and, without the digest, are indistinguishable in the log.

// fakeGit records the git invocations it was asked for and replies from a table
// keyed on the joined args.
type fakeGit struct {
	replies map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeGit) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	return []byte(f.replies[key]), nil
}

func TestHeadSHA_ReturnsTheResolvedCommit(t *testing.T) {
	g := &fakeGit{replies: map[string]string{
		"rev-parse HEAD": "0123456789abcdef0123456789abcdef01234567\n",
	}}
	got, err := HeadSHA(context.Background(), g.run, "/repo")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if got != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("HeadSHA = %q; trailing newline must be trimmed or every recorded SHA carries one", got)
	}
}

// TestHeadSHA_FailureIsReportedNotSwallowed pins that an unresolvable HEAD is an
// error rather than an empty string.
//
// An empty SHA recorded in the log is a plausible-looking value that means
// "unknown", and a reader cannot tell it from a genuine measurement. The caller
// decides what to do about it; this function must not decide silently.
func TestHeadSHA_FailureIsReportedNotSwallowed(t *testing.T) {
	boom := errors.New("not a git repository")
	g := &fakeGit{errs: map[string]error{"rev-parse HEAD": boom}}
	got, err := HeadSHA(context.Background(), g.run, "/repo")
	if err == nil {
		t.Fatal("an unresolvable HEAD must be an error, not an empty SHA that reads as a measurement")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying git failure should stay in the chain: %v", err)
	}
	if got != "" {
		t.Errorf("HeadSHA returned %q alongside an error", got)
	}
}

func TestDirtyDigest_CleanTreeIsEmpty(t *testing.T) {
	g := &fakeGit{replies: map[string]string{"diff HEAD --name-only -z": ""}}
	got, err := DirtyDigest(context.Background(), g.run, "/repo")
	if err != nil {
		t.Fatalf("DirtyDigest: %v", err)
	}
	if got != "" {
		t.Errorf("DirtyDigest on a clean tree = %q, want empty — empty is what distinguishes clean from dirty, so a clean tree must not get a digest of nothing", got)
	}
}

// TestDirtyDigest_IsStableAcrossListingOrder pins order-independence.
//
// git's output order is not something a caller can rely on, so if the digest
// depended on it, two runs over an identical working tree would record different
// digests and the field would be useless for exactly the comparison it exists
// for.
func TestDirtyDigest_IsStableAcrossListingOrder(t *testing.T) {
	forward := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "a.go\x00b.go\x00c.go\x00",
		"hash-object -- a.go":      "aaa\n",
		"hash-object -- b.go":      "bbb\n",
		"hash-object -- c.go":      "ccc\n",
	}}
	reverse := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "c.go\x00b.go\x00a.go\x00",
		"hash-object -- a.go":      "aaa\n",
		"hash-object -- b.go":      "bbb\n",
		"hash-object -- c.go":      "ccc\n",
	}}

	d1, err := DirtyDigest(context.Background(), forward.run, "/repo")
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	d2, err := DirtyDigest(context.Background(), reverse.run, "/repo")
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if d1 != d2 {
		t.Errorf("the digest depends on git's listing order (%s vs %s); two runs over the same tree would record different digests", d1, d2)
	}
	if d1 == "" {
		t.Error("a dirty tree produced an empty digest, so the comparison above was between two empty strings")
	}
}

// TestDirtyDigest_ChangesWithContent is the discriminating leg: without it, a
// function returning a constant would satisfy the stability test perfectly.
func TestDirtyDigest_ChangesWithContent(t *testing.T) {
	base := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "a.go\x00",
		"hash-object -- a.go":      "aaa\n",
	}}
	edited := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "a.go\x00",
		"hash-object -- a.go":      "zzz\n", // same file, different content
	}}
	renamed := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "b.go\x00",
		"hash-object -- b.go":      "aaa\n", // same content, different name
	}}

	d := func(g *fakeGit) string {
		t.Helper()
		got, err := DirtyDigest(context.Background(), g.run, "/repo")
		if err != nil {
			t.Fatalf("DirtyDigest: %v", err)
		}
		return got
	}
	dBase, dEdited, dRenamed := d(base), d(edited), d(renamed)
	if dBase == dEdited {
		t.Error("editing a file's CONTENT did not change the digest — the digest is not a function of the content")
	}
	if dBase == dRenamed {
		t.Error("changing which file is dirty did not change the digest — the digest is not a function of the file names")
	}
}

// TestDirtyDigest_DeletedFileStillCounts pins that a file git reports as changed
// but which no longer exists on disk does not silently drop out of the digest.
//
// `git hash-object` fails for a deleted path. If that error were swallowed by
// skipping the file, deleting a file would produce the SAME digest as a clean
// tree — the most misleading possible answer, since a deletion is a large change.
func TestDirtyDigest_DeletedFileStillCounts(t *testing.T) {
	deleted := &fakeGit{
		replies: map[string]string{"diff HEAD --name-only -z": "gone.go\x00"},
		errs:    map[string]error{"hash-object -- gone.go": errors.New("could not open 'gone.go'")},
	}
	got, err := DirtyDigest(context.Background(), deleted.run, "/repo")
	if err != nil {
		t.Fatalf("a deleted dirty file must not fail the digest: %v", err)
	}
	if got == "" {
		t.Fatal("a deleted file produced an empty digest, which is indistinguishable from a CLEAN tree")
	}

	clean := &fakeGit{replies: map[string]string{"diff HEAD --name-only -z": ""}}
	cleanDigest, err := DirtyDigest(context.Background(), clean.run, "/repo")
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if got == cleanDigest {
		t.Error("a tree with a deleted file digests identically to a clean tree")
	}

	// THE DECISIVE LEG. The two assertions above do NOT catch an implementation
	// that skips unhashable files: skipping leaves the digest as the hash of an
	// empty stream, which is a non-empty hex string and is not equal to the
	// clean tree's "". Measured — with the skip planted, both assertions above
	// pass. What separates the implementations is that under a skip, ANY tree
	// whose only dirty file is deleted digests the SAME, so the identity of the
	// deleted file is lost.
	other := &fakeGit{
		replies: map[string]string{"diff HEAD --name-only -z": "different.go\x00"},
		errs:    map[string]error{"hash-object -- different.go": errors.New("could not open 'different.go'")},
	}
	otherDigest, err := DirtyDigest(context.Background(), other.run, "/repo")
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	if got == otherDigest {
		t.Error("deleting gone.go and deleting different.go produce the same digest — unhashable files are being skipped, so which file was deleted is not recorded at all")
	}
}

// TestDirtyDigest_ToleratesTrailingSeparator pins that git's -z output, which
// ends with a NUL, does not yield a phantom empty filename. An empty name would
// make the digest depend on whether the trailing separator was present.
func TestDirtyDigest_ToleratesTrailingSeparator(t *testing.T) {
	withNUL := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "a.go\x00",
		"hash-object -- a.go":      "aaa\n",
	}}
	withoutNUL := &fakeGit{replies: map[string]string{
		"diff HEAD --name-only -z": "a.go",
		"hash-object -- a.go":      "aaa\n",
	}}
	a, err := DirtyDigest(context.Background(), withNUL.run, "/repo")
	if err != nil {
		t.Fatalf("with NUL: %v", err)
	}
	b, err := DirtyDigest(context.Background(), withoutNUL.run, "/repo")
	if err != nil {
		t.Fatalf("without NUL: %v", err)
	}
	if a != b {
		t.Errorf("the trailing NUL changed the digest (%s vs %s), so a phantom empty filename is being hashed", a, b)
	}
}

// TestDirtyDigest_ListingFailureIsReported pins that an unusable working tree is
// an error rather than a digest of nothing, for the same reason as HeadSHA:
// "clean" and "could not tell" must not render identically.
func TestDirtyDigest_ListingFailureIsReported(t *testing.T) {
	g := &fakeGit{errs: map[string]error{"diff HEAD --name-only -z": errors.New("not a git repository")}}
	if _, err := DirtyDigest(context.Background(), g.run, "/repo"); err == nil {
		t.Error("a failed diff must be an error; returning an empty digest would report the tree as clean")
	}
}
