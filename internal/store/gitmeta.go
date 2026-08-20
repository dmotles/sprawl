package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Git provenance recorded on run_finished.
//
// WHY THE DIGEST EXISTS: a replay or a benchmark needs to tell "this run started
// from a clean tree at SHA X" apart from "this run started from SHA X plus forty
// files of half-finished work". Those produce very different agent behaviour, and
// without a digest the two are indistinguishable in the log — both just say X.

// GitRunner runs git in a directory. Injected so every assertion about the
// digest can be made without a repository, and so nothing here shells out in a
// unit test (this repo's convention).
type GitRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// RealGit runs the real git binary.
func RealGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// HeadSHA resolves the commit a run is based on.
//
// A failure is returned rather than absorbed into an empty string: an empty SHA
// in the log is a plausible-looking value meaning "unknown", and a reader cannot
// tell it from a real measurement. The caller decides what to do about it.
func HeadSHA(ctx context.Context, run GitRunner, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("store: resolving HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// deletedMarker stands in for the blob hash of a dirty file that no longer
// exists on disk.
//
// It is load-bearing rather than a fallback. `git hash-object` fails for a
// deleted path, and SKIPPING such a file would make a tree with deletions digest
// IDENTICALLY to a clean tree — the most misleading answer available, since a
// deletion is a large change. The marker keeps the deletion in the digest.
const deletedMarker = "<deleted>"

// DirtyDigest returns a stable digest of the uncommitted changes, or "" for a
// clean tree.
//
// Empty means clean, and that is the only reason it is allowed to be empty: a
// clean tree must not get a digest, or "clean" and "dirty in some way that
// happened to hash to this" become indistinguishable.
//
// Names are SORTED before hashing, because git's listing order is not something
// a caller can rely on — if the digest depended on it, two runs over an
// identical tree could record different digests, defeating the one comparison
// the field exists for.
func DirtyDigest(ctx context.Context, run GitRunner, dir string) (string, error) {
	out, err := run(ctx, dir, "diff", "HEAD", "--name-only", "-z")
	if err != nil {
		return "", fmt.Errorf("store: listing dirty files: %w", err)
	}

	var names []string
	for _, n := range strings.Split(string(out), "\x00") {
		// git -z terminates every entry with a NUL, so the final split yields
		// an empty string. Hashing a phantom empty filename would make the
		// digest depend on whether the trailing separator was present.
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		blob := deletedMarker
		if b, err := run(ctx, dir, "hash-object", "--", name); err == nil {
			blob = strings.TrimSpace(string(b))
		}
		// NUL-separated so a name containing the separator cannot be confused
		// with a name/hash boundary.
		fmt.Fprintf(h, "%s\x00%s\n", name, blob)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
