// Package hooks installs and removes the Sprawl guard git hooks (the QUM-808
// pre-commit commit guard, the QUM-837 reference-transaction guard, and the
// QUM-872 employer-leak guard chained into the pre-commit hook) into an
// arbitrary repository's shared hooks directory.
//
// The guard logic is carried in the sprawl binary via go:embed (the canonical
// scripts/guard-main-commit, scripts/guard-main-ref, and
// scripts/guard-employer-leak) and injected here as an Assets value, so a target
// repo needs no scripts/ directory of its own.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Stable markers delimiting the Sprawl-managed block appended to a hook file
// that already has user content. They MUST remain byte-stable across releases —
// uninstall and idempotent re-install both key on them.
const (
	StartMarker = "# >>> sprawl-managed (do not edit) >>>"
	EndMarker   = "# <<< sprawl-managed <<<"

	// ManifestName is the manifest file written into the hooks dir; it is the
	// source of truth for a surgical uninstall.
	ManifestName = ".sprawl-hooks-manifest.json"

	// Helper script filenames written into the hooks dir (created outright).
	HelperCommitGuard = "sprawl-guard-main-commit"
	HelperRefGuard    = "sprawl-guard-main-ref"
	HelperLeakGuard   = "sprawl-guard-employer-leak"

	// ManifestVersion is the current on-disk manifest schema version.
	ManifestVersion = 1

	shebang = "#!/usr/bin/env bash\n"
)

// Assets carries the embedded canonical guard script bodies. Injected from the
// binary's go:embed at the cmd layer so internal/hooks stays embed-free and
// fully unit-testable.
type Assets struct {
	CommitGuard []byte // canonical scripts/guard-main-commit body
	RefGuard    []byte // canonical scripts/guard-main-ref body
	LeakGuard   []byte // canonical scripts/guard-employer-leak body (QUM-872)
}

// Deps is the dependency-injection surface for install/uninstall. Every
// external interaction (git, filesystem, clock, output) is a function value so
// tests can drive scenarios against a temp dir without real git.
type Deps struct {
	// HooksDir resolves the target hooks directory: core.hooksPath when set,
	// else <git-common-dir>/hooks. Must be correct from a worktree too.
	HooksDir func() (string, error)
	// DetectBranch resolves the repo's default branch (origin/HEAD →
	// init.defaultBranch → "main").
	DetectBranch func() (string, error)
	MkdirAll     func(path string, perm fs.FileMode) error
	ReadFile     func(path string) ([]byte, error)
	// WriteFile writes data to path with the given mode, atomically.
	WriteFile func(path string, data []byte, mode fs.FileMode) error
	Remove    func(path string) error
	Now       func() time.Time
	// Stderr receives all status, summary, and warning output. (Per
	// cli-ux-best-practices, sprawl commands write human-readable status to
	// stderr; there is no machine-parseable stdout payload here.)
	Stderr io.Writer
}

// EntryType records whether Sprawl created a hook file outright (and therefore
// owns it entirely) or merely appended a managed block to a pre-existing file.
type EntryType string

const (
	EntryCreated  EntryType = "created"
	EntryAppended EntryType = "appended"
)

// ManifestEntry is one Sprawl-owned artifact. Path is relative to the hooks dir.
type ManifestEntry struct {
	Path string    `json:"path"`
	Type EntryType `json:"type"`
}

// Manifest is the persisted record of exactly what Sprawl installed.
type Manifest struct {
	Version         int             `json:"version"`
	ProtectedBranch string          `json:"protectedBranch"`
	InstalledAt     string          `json:"installedAt,omitempty"`
	Entries         []ManifestEntry `json:"entries"`
}

// hookPoint describes a git hook Sprawl chains into.
type hookPoint struct {
	file   string // hook filename, e.g. "pre-commit"
	helper string // helper script the managed block invokes
	// invoke renders the shell command the managed block runs (the helper is
	// located relative to the running hook so core.hooksPath is respected).
	invoke func(branch string) string
}

var hookPoints = []hookPoint{
	{
		file:   "pre-commit",
		helper: HelperCommitGuard,
		// The pre-commit block chains two guards: the QUM-808 main-commit guard
		// and the QUM-872 employer-leak guard (staged scan). `|| exit $?`
		// propagates a rejection immediately, so each runs fail-fast even when
		// chained ahead of user content that lacks `set -e` and ends in `exit 0`
		// (which would otherwise swallow it). The leak guard no-ops when no
		// forbidden-terms list is present, so it is safe in any repo.
		invoke: func(branch string) string {
			return fmt.Sprintf("%q/%s %q || exit $?\n%q/%s || exit $?",
				"$SPRAWL_HOOKS_DIR", HelperCommitGuard, branch,
				"$SPRAWL_HOOKS_DIR", HelperLeakGuard)
		},
	},
	{
		file:   "reference-transaction",
		helper: HelperRefGuard,
		// git passes the phase as $1; forward it plus the protected branch.
		invoke: func(branch string) string {
			return fmt.Sprintf("%q/%s \"$@\" %q || exit $?", "$SPRAWL_HOOKS_DIR", HelperRefGuard, branch)
		},
	},
}

// branchRE allows only characters safe to inline into the hook block (no shell
// metacharacters). Leading '-' and '..' are rejected separately.
var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("protected branch is empty; pass --branch <name> or run inside a repo with a detectable default branch")
	}
	if !branchRE.MatchString(branch) {
		return fmt.Errorf("invalid protected branch %q: only letters, digits, and ._/- are allowed", branch)
	}
	if strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return fmt.Errorf("invalid protected branch %q: must not start with '-' or contain '..'", branch)
	}
	return nil
}

// managedBlock renders the full delimited block (ending with a newline) for a
// hook point and protected branch.
func managedBlock(hp hookPoint, branch string) string {
	var b strings.Builder
	b.WriteString(StartMarker + "\n")
	b.WriteString(fmt.Sprintf("# Installed by `sprawl hooks install` (QUM-842). Protects branch: %s.\n", branch))
	b.WriteString("# Remove with: sprawl hooks uninstall\n")
	// Locate the Sprawl helper beside the running hook (works under
	// core.hooksPath and from any worktree).
	b.WriteString(`SPRAWL_HOOKS_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"` + "\n")
	b.WriteString(hp.invoke(branch) + "\n")
	b.WriteString(EndMarker + "\n")
	return b.String()
}

func hasMarkers(content string) bool {
	return strings.Contains(content, StartMarker)
}

// insertManagedBlock inserts block into existing content (which has no managed
// markers yet) immediately after the shebang line, so the guard runs fail-fast
// BEFORE any pre-existing user logic — a user hook that ends in `exit 0` can no
// longer render the guard inert. User bytes are preserved verbatim; uninstall
// strips the block by its markers regardless of position. When there is no
// shebang the block is prepended.
func insertManagedBlock(existing, block string) string {
	if strings.HasPrefix(existing, "#!") {
		if nl := strings.IndexByte(existing, '\n'); nl >= 0 {
			return existing[:nl+1] + block + existing[nl+1:]
		}
		// Shebang with no trailing newline.
		return existing + "\n" + block
	}
	return block + existing
}

// replaceBlock swaps the existing managed block (markers inclusive, through the
// end of the end-marker line) for block, leaving all other bytes intact.
func replaceBlock(content, block string) string {
	before, after, ok := splitAroundBlock(content)
	if !ok {
		return insertManagedBlock(content, block)
	}
	return before + block + after
}

// stripBlock removes the managed block (markers inclusive). Returns the result
// and whether a block was found.
func stripBlock(content string) (string, bool) {
	before, after, ok := splitAroundBlock(content)
	if !ok {
		return content, false
	}
	return before + after, true
}

// splitAroundBlock returns the content before the start marker and after the
// end-marker line. ok is false if a complete block is not present.
func splitAroundBlock(content string) (before, after string, ok bool) {
	start := strings.Index(content, StartMarker)
	if start < 0 {
		return "", "", false
	}
	endIdx := strings.Index(content[start:], EndMarker)
	if endIdx < 0 {
		return "", "", false
	}
	endIdx += start
	before = content[:start]
	if nl := strings.IndexByte(content[endIdx:], '\n'); nl >= 0 {
		after = content[endIdx+nl+1:]
	}
	return before, after, true
}

func readManifest(deps *Deps, hooksDir string) (Manifest, bool) {
	data, err := deps.ReadFile(filepath.Join(hooksDir, ManifestName))
	if err != nil {
		return Manifest{}, false
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, false
	}
	return m, true
}

func removeIfExists(deps *Deps, path string) error {
	if err := deps.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", filepath.Base(path), err)
	}
	return nil
}

// Install writes the guard helpers + managed blocks into the target repo's
// hooks dir and records a manifest. branchOverride, when non-empty, sets the
// protected branch; otherwise it is auto-detected. Idempotent.
func Install(deps *Deps, assets Assets, branchOverride string) error {
	if len(assets.CommitGuard) == 0 || len(assets.RefGuard) == 0 || len(assets.LeakGuard) == 0 {
		return fmt.Errorf("hook guard assets are not embedded in this binary")
	}

	branch := branchOverride
	if branch == "" {
		detected, err := deps.DetectBranch()
		if err != nil {
			return fmt.Errorf("detecting default branch: %w", err)
		}
		branch = detected
	}
	// Validate BEFORE any filesystem writes so a rejection is atomic.
	if err := validateBranch(branch); err != nil {
		return err
	}

	hooksDir, err := deps.HooksDir()
	if err != nil {
		return fmt.Errorf("resolving hooks dir: %w", err)
	}
	if err := deps.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}

	// Preserve created-vs-appended disposition across re-install.
	prior := map[string]EntryType{}
	if m, ok := readManifest(deps, hooksDir); ok {
		for _, e := range m.Entries {
			prior[e.Path] = e.Type
		}
	}

	var entries []ManifestEntry

	for _, h := range []struct {
		name string
		body []byte
	}{
		{HelperCommitGuard, assets.CommitGuard},
		{HelperRefGuard, assets.RefGuard},
		{HelperLeakGuard, assets.LeakGuard},
	} {
		if err := deps.WriteFile(filepath.Join(hooksDir, h.name), h.body, 0o755); err != nil {
			return fmt.Errorf("writing helper %s: %w", h.name, err)
		}
		entries = append(entries, ManifestEntry{Path: h.name, Type: EntryCreated})
	}

	for _, hp := range hookPoints {
		entry, err := installHookPoint(deps, hooksDir, hp, branch, prior[hp.file])
		if err != nil {
			return err
		}
		entries = append(entries, entry)
	}

	manifest := Manifest{
		Version:         ManifestVersion,
		ProtectedBranch: branch,
		InstalledAt:     deps.Now().UTC().Format(time.RFC3339),
		Entries:         entries,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	data = append(data, '\n')
	if err := deps.WriteFile(filepath.Join(hooksDir, ManifestName), data, 0o644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	printInstallSummary(deps, hooksDir, branch, entries)
	return nil
}

func installHookPoint(deps *Deps, hooksDir string, hp hookPoint, branch string, prior EntryType) (ManifestEntry, error) {
	path := filepath.Join(hooksDir, hp.file)
	block := managedBlock(hp, branch)

	existing, err := deps.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if err := deps.WriteFile(path, []byte(shebang+block), 0o755); err != nil {
				return ManifestEntry{}, fmt.Errorf("creating %s: %w", hp.file, err)
			}
			return ManifestEntry{Path: hp.file, Type: EntryCreated}, nil
		}
		return ManifestEntry{}, fmt.Errorf("reading %s: %w", hp.file, err)
	}

	content := string(existing)
	var newContent string
	var typ EntryType
	switch {
	case hasMarkers(content):
		newContent = replaceBlock(content, block)
		// Re-install: trust the prior manifest disposition. When the manifest
		// was lost, a wholly-managed file and an appended-to file are
		// byte-indistinguishable, so default to the SAFE classification
		// (appended → uninstall strips rather than deletes; never lose a user
		// file).
		typ = prior
		if typ == "" {
			typ = EntryAppended
		}
	default:
		newContent = insertManagedBlock(content, block)
		typ = EntryAppended
		if hp.file == "reference-transaction" {
			fmt.Fprintf(deps.Stderr,
				"warning: chained a managed block onto your existing %q hook. Sprawl's guard runs first and\n"+
					"consumes stdin, so a pre-existing reference-transaction hook that reads ref updates from\n"+
					"stdin may not work as before. Review %s manually.\n",
				hp.file, path)
		}
	}
	if err := deps.WriteFile(path, []byte(newContent), 0o755); err != nil {
		return ManifestEntry{}, fmt.Errorf("updating %s: %w", hp.file, err)
	}
	return ManifestEntry{Path: hp.file, Type: typ}, nil
}

// Uninstall removes exactly what Sprawl owns per the manifest (or, if the
// manifest is missing, falls back to marker-based removal). Idempotent and safe
// when nothing is installed.
func Uninstall(deps *Deps) error {
	hooksDir, err := deps.HooksDir()
	if err != nil {
		return fmt.Errorf("resolving hooks dir: %w", err)
	}

	manifest, ok := readManifest(deps, hooksDir)
	if !ok {
		return uninstallFallback(deps, hooksDir)
	}

	var removed []string
	for _, e := range manifest.Entries {
		path := filepath.Join(hooksDir, e.Path)
		switch e.Type {
		case EntryCreated:
			if err := removeIfExists(deps, path); err != nil {
				return err
			}
			removed = append(removed, "deleted "+e.Path)
		case EntryAppended:
			data, err := deps.ReadFile(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return fmt.Errorf("reading %s: %w", e.Path, err)
			}
			stripped, found := stripBlock(string(data))
			if !found {
				continue
			}
			if err := deps.WriteFile(path, []byte(stripped), 0o755); err != nil {
				return fmt.Errorf("stripping %s: %w", e.Path, err)
			}
			removed = append(removed, "stripped managed block from "+e.Path)
		}
	}
	if err := removeIfExists(deps, filepath.Join(hooksDir, ManifestName)); err != nil {
		return err
	}
	removed = append(removed, "removed "+ManifestName)

	printUninstallSummary(deps, hooksDir, manifest.ProtectedBranch, removed)
	return nil
}

// uninstallFallback handles a missing manifest. Without the manifest, a
// wholly-managed (created) hook file is byte-indistinguishable from a user file
// Sprawl merely chained onto, so it NEVER deletes a hook file — it only strips
// the managed block (a created hook degrades to a harmless shebang-only stub
// rather than risking deletion of user data, M2). Only the unambiguously
// Sprawl-named helper scripts are deleted outright.
func uninstallFallback(deps *Deps, hooksDir string) error {
	var removed []string
	for _, hp := range hookPoints {
		path := filepath.Join(hooksDir, hp.file)
		data, err := deps.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if !hasMarkers(content) {
			continue
		}
		stripped, _ := stripBlock(content)
		if err := deps.WriteFile(path, []byte(stripped), 0o755); err != nil {
			return fmt.Errorf("stripping %s: %w", hp.file, err)
		}
		removed = append(removed, "stripped managed block from "+hp.file)
	}
	for _, h := range []string{HelperCommitGuard, HelperRefGuard, HelperLeakGuard} {
		path := filepath.Join(hooksDir, h)
		if _, err := deps.ReadFile(path); err != nil {
			continue
		}
		if err := removeIfExists(deps, path); err != nil {
			return err
		}
		removed = append(removed, "deleted "+h)
	}

	if len(removed) == 0 {
		fmt.Fprintf(deps.Stderr, "Nothing to uninstall — no Sprawl-managed hooks found in %s\n", hooksDir)
		return nil
	}
	fmt.Fprintf(deps.Stderr, "warning: no %s manifest found; fell back to marker-based removal.\n", ManifestName)
	printUninstallSummary(deps, hooksDir, "", removed)
	return nil
}

func printInstallSummary(deps *Deps, hooksDir, branch string, entries []ManifestEntry) {
	fmt.Fprintf(deps.Stderr, "Installed Sprawl main-pollution guards (protected branch: %s)\n", branch)
	for _, e := range entries {
		label := "created"
		if e.Type == EntryAppended {
			label = "managed block appended"
		}
		fmt.Fprintf(deps.Stderr, "  %s  (%s)\n", filepath.Join(hooksDir, e.Path), label)
	}
	fmt.Fprintf(deps.Stderr, "  %s\n", filepath.Join(hooksDir, ManifestName))
	fmt.Fprintf(deps.Stderr, "Non-root agents (SPRAWL_AGENT_IDENTITY set, != weave) can no longer commit or push to %q.\n", branch)
	fmt.Fprintf(deps.Stderr, "Undo any time with: sprawl hooks uninstall\n")
}

func printUninstallSummary(deps *Deps, hooksDir, branch string, removed []string) {
	fmt.Fprintf(deps.Stderr, "Removed Sprawl main-pollution guards from %s\n", hooksDir)
	for _, r := range removed {
		fmt.Fprintf(deps.Stderr, "  %s\n", r)
	}
	if branch != "" {
		fmt.Fprintf(deps.Stderr, "Non-root agents are no longer blocked from %q. Re-add with: sprawl hooks install\n", branch)
	} else {
		fmt.Fprintf(deps.Stderr, "Re-add the guards any time with: sprawl hooks install\n")
	}
}
