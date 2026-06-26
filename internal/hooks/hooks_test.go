package hooks

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	testCommitGuard = []byte("#!/usr/bin/env bash\n# canonical commit guard\nexit 0\n")
	testRefGuard    = []byte("#!/usr/bin/env bash\n# canonical ref guard\nexit 0\n")
)

func testAssets() Assets {
	return Assets{CommitGuard: testCommitGuard, RefGuard: testRefGuard}
}

// newTestDeps returns a Deps backed by a real temp hooks dir plus the temp dir
// path. Branch detection defaults to "main"; override deps.DetectBranch per
// test. Output is captured into the returned buffers.
func newTestDeps(t *testing.T) (*Deps, string, *bytes.Buffer) {
	t.Helper()
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	var errb bytes.Buffer
	deps := &Deps{
		HooksDir:     func() (string, error) { return hooksDir, nil },
		DetectBranch: func() (string, error) { return "main", nil },
		MkdirAll:     os.MkdirAll,
		ReadFile:     os.ReadFile,
		WriteFile: func(p string, d []byte, m fs.FileMode) error {
			if err := os.WriteFile(p, d, m); err != nil {
				return err
			}
			return os.Chmod(p, m)
		},
		Remove: os.Remove,
		Now:    func() time.Time { return time.Unix(0, 0).UTC() },
		Stderr: &errb,
	}
	return deps, hooksDir, &errb
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func isExecutable(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode()&0o111 != 0
}

func countMarkers(s string) int {
	return strings.Count(s, StartMarker)
}

// assertBalancedMarkers ensures every start marker has a matching end marker.
func assertBalancedMarkers(t *testing.T, label, body string) {
	t.Helper()
	starts := strings.Count(body, StartMarker)
	ends := strings.Count(body, EndMarker)
	if starts != ends {
		t.Errorf("%s: unbalanced markers start=%d end=%d", label, starts, ends)
	}
}

// beforeBlock returns the bytes preceding the first managed block.
func beforeBlock(t *testing.T, body string) string {
	t.Helper()
	idx := strings.Index(body, StartMarker)
	if idx < 0 {
		t.Fatalf("no managed block in body:\n%s", body)
	}
	return body[:idx]
}

// stripped returns body with the managed block removed (markers inclusive,
// through the end-marker line) — mirrors the production stripBlock so tests can
// assert byte-for-byte restoration of user content.
func stripped(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, StartMarker)
	if start < 0 {
		return body
	}
	end := strings.Index(body[start:], EndMarker)
	if end < 0 {
		t.Fatalf("start marker without end marker:\n%s", body)
	}
	end += start
	nl := strings.IndexByte(body[end:], '\n')
	if nl < 0 {
		return body[:start]
	}
	return body[:start] + body[end+nl+1:]
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func loadManifest(t *testing.T, hooksDir string) Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(hooksDir, ManifestName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return m
}

func entryType(m Manifest, path string) (EntryType, bool) {
	for _, e := range m.Entries {
		if e.Path == path {
			return e.Type, true
		}
	}
	return "", false
}

func TestInstall_EmptyDir_CreatesHooksAndHelpers(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Helpers written verbatim and executable.
	commitHelper := filepath.Join(hooksDir, HelperCommitGuard)
	refHelper := filepath.Join(hooksDir, HelperRefGuard)
	if got := readFile(t, commitHelper); got != string(testCommitGuard) {
		t.Errorf("commit helper body = %q, want verbatim embed", got)
	}
	if got := readFile(t, refHelper); got != string(testRefGuard) {
		t.Errorf("ref helper body = %q, want verbatim embed", got)
	}
	if mode(t, commitHelper) != 0o755 || mode(t, refHelper) != 0o755 {
		t.Errorf("helpers must be mode 0755, got %v / %v", mode(t, commitHelper), mode(t, refHelper))
	}

	// Hook points created with shebang + exactly one managed block, executable.
	for _, hp := range []string{"pre-commit", "reference-transaction"} {
		path := filepath.Join(hooksDir, hp)
		body := readFile(t, path)
		if !strings.HasPrefix(body, "#!") {
			t.Errorf("%s must start with a shebang, got %q", hp, body)
		}
		if countMarkers(body) != 1 {
			t.Errorf("%s must contain exactly one managed block, got %d", hp, countMarkers(body))
		}
		assertBalancedMarkers(t, hp, body)
		if mode(t, path) != 0o755 {
			t.Errorf("%s must be mode 0755, got %v", hp, mode(t, path))
		}
	}

	// pre-commit block invokes the commit-guard helper; ref invokes ref helper.
	if !strings.Contains(readFile(t, filepath.Join(hooksDir, "pre-commit")), HelperCommitGuard) {
		t.Error("pre-commit block must invoke the commit-guard helper")
	}
	if !strings.Contains(readFile(t, filepath.Join(hooksDir, "reference-transaction")), HelperRefGuard) {
		t.Error("reference-transaction block must invoke the ref-guard helper")
	}
}

func TestInstall_ManifestRecordsCreated(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}
	m := loadManifest(t, hooksDir)
	if m.Version != ManifestVersion {
		t.Errorf("manifest version = %d, want %d", m.Version, ManifestVersion)
	}
	if m.ProtectedBranch != "main" {
		t.Errorf("protected branch = %q, want main", m.ProtectedBranch)
	}
	// InstalledAt is sourced from the injected clock (Unix(0) UTC → RFC3339).
	if m.InstalledAt != "1970-01-01T00:00:00Z" {
		t.Errorf("installedAt = %q, want it populated from Now()", m.InstalledAt)
	}
	for _, p := range []string{HelperCommitGuard, HelperRefGuard, "pre-commit", "reference-transaction"} {
		typ, ok := entryType(m, p)
		if !ok {
			t.Errorf("manifest missing entry for %q", p)
			continue
		}
		if typ != EntryCreated {
			t.Errorf("entry %q type = %q, want created", p, typ)
		}
	}
}

func TestInstall_AppendsToExistingHook_PreservesUserContentByteForByte(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	userContent := "#!/bin/sh\necho \"my custom pre-commit\"\nexit 0\n"
	preCommit := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommit, []byte(userContent), 0o755); err != nil {
		t.Fatalf("seed pre-commit: %v", err)
	}

	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	body := readFile(t, preCommit)
	// The guard block is inserted right after the shebang so it runs fail-fast
	// (before a user `exit 0` could render it inert). User bytes are preserved:
	// stripping the block must reproduce the original content exactly.
	if got := stripped(t, body); got != userContent {
		t.Errorf("user content not preserved byte-for-byte:\n got %q\nwant %q", got, userContent)
	}
	if !strings.HasPrefix(body, "#!/bin/sh\n") {
		t.Errorf("user shebang must remain first; got %q", body)
	}
	// Guard runs BEFORE the user's own command (fail-fast ordering).
	if strings.Index(body, StartMarker) > strings.Index(body, "my custom pre-commit") {
		t.Error("managed guard block must precede the user's existing logic")
	}
	if countMarkers(body) != 1 {
		t.Errorf("expected one managed block, got %d", countMarkers(body))
	}
	assertBalancedMarkers(t, "pre-commit", body)
	if mode(t, preCommit) != 0o755 {
		t.Errorf("pre-commit must remain mode 0755 after chaining, got %v", mode(t, preCommit))
	}

	m := loadManifest(t, hooksDir)
	if typ, _ := entryType(m, "pre-commit"); typ != EntryAppended {
		t.Errorf("pre-commit manifest type = %q, want appended", typ)
	}
	// reference-transaction had no pre-existing file → created.
	if typ, _ := entryType(m, "reference-transaction"); typ != EntryCreated {
		t.Errorf("reference-transaction manifest type = %q, want created", typ)
	}
}

func TestInstall_Idempotent_NoDuplicateBlocks(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install 1: %v", err)
	}
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install 2: %v", err)
	}
	for _, hp := range []string{"pre-commit", "reference-transaction"} {
		body := readFile(t, filepath.Join(hooksDir, hp))
		if countMarkers(body) != 1 {
			t.Errorf("%s has %d managed blocks after re-install, want 1", hp, countMarkers(body))
		}
	}
	// Helper bodies remain verbatim (not duplicated/corrupted) after re-install.
	if got := readFile(t, filepath.Join(hooksDir, HelperCommitGuard)); got != string(testCommitGuard) {
		t.Errorf("commit helper body changed after re-install: %q", got)
	}
	if got := readFile(t, filepath.Join(hooksDir, HelperRefGuard)); got != string(testRefGuard) {
		t.Errorf("ref helper body changed after re-install: %q", got)
	}
	m := loadManifest(t, hooksDir)
	if len(m.Entries) != 4 {
		t.Errorf("manifest has %d entries after re-install, want 4", len(m.Entries))
	}
}

func TestInstall_AppendNoTrailingNewline_KeepsMarkerOnOwnLine(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	// User hook with NO trailing newline — the classic concatenation-corruption case.
	userContent := "#!/bin/sh\necho hi"
	preCommit := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommit, []byte(userContent), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}
	body := readFile(t, preCommit)
	// Block inserted after the shebang; the start marker sits at column 0
	// (never "echo hi# >>> ...") and the user's command is preserved.
	if got := beforeBlock(t, body); got != "#!/bin/sh\n" {
		t.Errorf("guard block must follow the shebang line; got prefix %q", got)
	}
	if !strings.Contains(body, "\n"+StartMarker) {
		t.Error("start marker must begin its own line")
	}
	if !strings.Contains(body, "echo hi") {
		t.Error("user command must be preserved")
	}
	assertBalancedMarkers(t, "pre-commit", body)
}

func TestInstall_ReinstallPreservesAppendedDisposition(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	userContent := "#!/bin/sh\necho hi\n"
	preCommit := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommit, []byte(userContent), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install 1: %v", err)
	}
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install 2: %v", err)
	}
	body := readFile(t, preCommit)
	if got := stripped(t, body); got != userContent {
		t.Errorf("user content lost on re-install; stripped=%q want %q", got, userContent)
	}
	if countMarkers(body) != 1 {
		t.Errorf("re-install duplicated block: %d markers", countMarkers(body))
	}
	m := loadManifest(t, hooksDir)
	if typ, _ := entryType(m, "pre-commit"); typ != EntryAppended {
		t.Errorf("re-install changed disposition to %q, want appended", typ)
	}
}

func TestInstall_ReplacesStaleBlock_NewBranch(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	if err := Install(deps, testAssets(), "main"); err != nil {
		t.Fatalf("Install main: %v", err)
	}
	if err := Install(deps, testAssets(), "develop"); err != nil {
		t.Fatalf("Install develop: %v", err)
	}
	body := readFile(t, filepath.Join(hooksDir, "pre-commit"))
	if countMarkers(body) != 1 {
		t.Errorf("expected single block, got %d", countMarkers(body))
	}
	if !strings.Contains(body, "develop") {
		t.Error("expected new branch 'develop' in managed block")
	}
	if strings.Contains(body, `"main"`) {
		t.Errorf("stale branch 'main' should be gone; body:\n%s", body)
	}
	if m := loadManifest(t, hooksDir); m.ProtectedBranch != "develop" {
		t.Errorf("manifest branch = %q, want develop", m.ProtectedBranch)
	}
}

func TestInstall_BranchOverrideWins(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	deps.DetectBranch = func() (string, error) { return "trunk", nil }
	if err := Install(deps, testAssets(), "release"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if m := loadManifest(t, hooksDir); m.ProtectedBranch != "release" {
		t.Errorf("branch override ignored: got %q, want release", m.ProtectedBranch)
	}
}

func TestInstall_UsesDetectedBranchWhenNoOverride(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	deps.DetectBranch = func() (string, error) { return "trunk", nil }
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if m := loadManifest(t, hooksDir); m.ProtectedBranch != "trunk" {
		t.Errorf("detected branch not used: got %q, want trunk", m.ProtectedBranch)
	}
}

func TestInstall_RejectsUnsafeBranchName(t *testing.T) {
	unsafe := []string{
		"evil; rm -rf /",
		"a b",
		"$(whoami)",
		"`id`",
		"foo\nbar",
		"-leading-dash",
		"a\"b",
		"a'b",
		"a..b",
	}
	for _, name := range unsafe {
		deps, hooksDir, _ := newTestDeps(t)
		if err := Install(deps, testAssets(), name); err == nil {
			t.Errorf("expected error for unsafe branch %q", name)
		}
		// Rejection must be atomic: no Sprawl artifacts left behind.
		for _, p := range []string{HelperCommitGuard, HelperRefGuard, "pre-commit", "reference-transaction", ManifestName} {
			if _, err := os.Stat(filepath.Join(hooksDir, p)); !os.IsNotExist(err) {
				t.Errorf("branch %q: %s written despite validation failure", name, p)
			}
		}
	}
}

func TestInstall_EmptyDetectedBranch_Errors(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	deps.DetectBranch = func() (string, error) { return "", nil }
	if err := Install(deps, testAssets(), ""); err == nil {
		t.Fatal("expected error when no branch can be determined")
	}
}

func TestUninstall_Created_DeletesFilesAndManifest(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Uninstall(deps); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	for _, p := range []string{HelperCommitGuard, HelperRefGuard, "pre-commit", "reference-transaction", ManifestName} {
		if _, err := os.Stat(filepath.Join(hooksDir, p)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, stat err = %v", p, err)
		}
	}
}

func TestUninstall_Appended_PreservesUserContent(t *testing.T) {
	deps, hooksDir, _ := newTestDeps(t)
	userContent := "#!/bin/sh\necho \"keep me\"\nexit 0\n"
	preCommit := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommit, []byte(userContent), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Uninstall(deps); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	body := readFile(t, preCommit)
	if body != userContent {
		t.Errorf("user pre-commit not restored byte-for-byte:\n got %q\nwant %q", body, userContent)
	}
	if countMarkers(body) != 0 {
		t.Error("managed block not stripped")
	}
	if !isExecutable(t, preCommit) {
		t.Error("pre-commit should remain executable")
	}
	// reference-transaction was created → deleted.
	if _, err := os.Stat(filepath.Join(hooksDir, "reference-transaction")); !os.IsNotExist(err) {
		t.Error("created reference-transaction should be deleted")
	}
	// helpers deleted, manifest gone.
	if _, err := os.Stat(filepath.Join(hooksDir, ManifestName)); !os.IsNotExist(err) {
		t.Error("manifest should be removed")
	}
}

func TestUninstall_NothingInstalled_SafeExit0(t *testing.T) {
	deps, hooksDir, errb := newTestDeps(t)
	// Seed an unmanaged user hook Sprawl never touched.
	preCommit := filepath.Join(hooksDir, "pre-commit")
	userContent := "#!/bin/sh\necho mine\n"
	if err := os.WriteFile(preCommit, []byte(userContent), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Uninstall(deps); err != nil {
		t.Fatalf("Uninstall with nothing installed should succeed, got %v", err)
	}
	low := strings.ToLower(errb.String())
	if !strings.Contains(low, "nothing") && !strings.Contains(low, "no sprawl") && !strings.Contains(low, "not installed") {
		t.Errorf("expected a 'nothing installed' message, got: %q", errb.String())
	}
	// An unmanaged user hook must be left completely untouched.
	if got := readFile(t, preCommit); got != userContent {
		t.Errorf("unmanaged user hook was modified: got %q", got)
	}
}

func TestUninstall_ManifestMissing_MarkerFallback(t *testing.T) {
	deps, hooksDir, errb := newTestDeps(t)
	userContent := "#!/bin/sh\necho keep\n"
	preCommit := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommit, []byte(userContent), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Install(deps, testAssets(), ""); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Simulate a lost manifest.
	if err := os.Remove(filepath.Join(hooksDir, ManifestName)); err != nil {
		t.Fatalf("rm manifest: %v", err)
	}
	if err := Uninstall(deps); err != nil {
		t.Fatalf("Uninstall fallback: %v", err)
	}
	body := readFile(t, preCommit)
	if strings.Count(body, StartMarker)+strings.Count(body, EndMarker) != 0 {
		t.Error("fallback left an orphaned marker behind")
	}
	if body != userContent {
		t.Errorf("fallback damaged user content: got %q want %q", body, userContent)
	}
	// Without a manifest, fallback must NEVER delete a hook file (it cannot
	// distinguish a created file from a user file it chained onto). The
	// wholly-managed reference-transaction hook is stripped to a harmless
	// shebang-only stub, not deleted — preventing user-data loss (M2).
	refTxn := filepath.Join(hooksDir, "reference-transaction")
	rt, err := os.Stat(refTxn)
	if err != nil {
		t.Fatalf("reference-transaction must survive fallback as a stub: %v", err)
	}
	if rt.Mode().Perm() == 0 {
		t.Error("reference-transaction stub should retain its mode")
	}
	if strings.Contains(readFile(t, refTxn), StartMarker) {
		t.Error("fallback must strip the managed block from reference-transaction")
	}
	// The unambiguously Sprawl-named helpers are deleted outright.
	for _, h := range []string{HelperCommitGuard, HelperRefGuard} {
		if _, err := os.Stat(filepath.Join(hooksDir, h)); !os.IsNotExist(err) {
			t.Errorf("fallback should delete helper %q", h)
		}
	}
	if !strings.Contains(strings.ToLower(errb.String()), "manifest") {
		t.Errorf("expected a manifest-missing warning, got: %q", errb.String())
	}
}
