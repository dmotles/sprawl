package githooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// leakGuardPath resolves the absolute path to the real scripts/guard-employer-leak
// relative to this test file, so it works regardless of cwd or worktree.
func leakGuardPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(GuardEmployerLeakScript(repoRoot))
	if err != nil {
		t.Fatalf("abs(%q): %v", GuardEmployerLeakScript(repoRoot), err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("leak guard script not found at %s: %v", abs, err)
	}
	return abs
}

// writeList writes a forbidden-terms list to a temp file and returns its path.
// All terms are SYNTHETIC placeholders — this repo is public and no test may
// embed a real forbidden term.
func writeList(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forbidden-terms")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write list: %v", err)
	}
	return path
}

// stageFile writes content to name under repo and stages it.
func stageFile(t *testing.T, repo, name, content string) {
	t.Helper()
	full := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if out, err := gitRun(t, repo, baseEnv(), "add", name); err != nil {
		t.Fatalf("git add %s: %s: %v", name, out, err)
	}
}

// runGuard execs the leak guard in repo with the given list and args.
func runGuard(t *testing.T, repo, list string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(leakGuardPath(t), args...)
	cmd.Dir = repo
	cmd.Env = baseEnv("SPRAWL_FORBIDDEN_TERMS_FILE=" + list)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Synthetic placeholder terms used throughout — never real forbidden terms.
const (
	synthName = "PLACEHOLDERCO"      // employer-name stand-in (ci match)
	synthTerm = "PLACEHOLDERTERM123" // generic secret stand-in (ci match)
	synthID   = "AAAA-BBBB-1111"     // GUID/ID stand-in (exact match)
)

func TestLeakGuard_StagedCatchesPlantedTerm(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "secret-name:ci:"+synthTerm)
	stageFile(t, repo, "leak.txt", "hello "+synthTerm+" world\n")

	out, err := runGuard(t, repo, list)
	if err == nil {
		t.Fatalf("expected guard to block a planted term; got success; output: %s", out)
	}
	if !strings.Contains(out, "secret-name") {
		t.Errorf("expected category %q in output; got: %s", "secret-name", out)
	}
	if !strings.Contains(out, "leak.txt:1") {
		t.Errorf("expected file:line %q in output; got: %s", "leak.txt:1", out)
	}
	// Term-withheld invariant: the term itself must NEVER be printed.
	if strings.Contains(out, synthTerm) {
		t.Errorf("guard leaked the forbidden term into its output: %s", out)
	}
}

func TestLeakGuard_StagedCleanPasses(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "secret-name:ci:"+synthTerm)
	stageFile(t, repo, "clean.txt", "nothing to see here\n")

	if out, err := runGuard(t, repo, list); err != nil {
		t.Fatalf("expected clean staged tree to pass; got error %v; output: %s", err, out)
	}
}

// TestLeakGuard_QUMRefNeverTriggers proves a Linear-style prefix ref (QUM-123)
// and a synthetic prefix of a longer term never match the full-word term.
func TestLeakGuard_QUMRefNeverTriggers(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "employer:ci:"+synthName)
	stageFile(t, repo, "refs.txt", "Fixes QUM-123 and PLA-456 per review\n")

	if out, err := runGuard(t, repo, list); err != nil {
		t.Fatalf("expected QUM-/prefix refs to pass; got error %v; output: %s", err, out)
	}
}

func TestLeakGuard_CaseInsensitiveVsExact(t *testing.T) {
	list := writeList(t, "emp:ci:"+synthName, "id:exact:"+synthID)

	t.Run("ci matches lowercase", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		stageFile(t, repo, "a.txt", "value is "+strings.ToLower(synthName)+"\n")
		if out, err := runGuard(t, repo, list); err == nil {
			t.Fatalf("expected ci term to match lowercased content; output: %s", out)
		}
	})

	t.Run("exact does not match lowercase", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		stageFile(t, repo, "b.txt", "id "+strings.ToLower(synthID)+"\n")
		if out, err := runGuard(t, repo, list); err != nil {
			t.Fatalf("exact match must be case-sensitive (lowercase should pass); output: %s", out)
		}
	})

	t.Run("exact matches exact case", func(t *testing.T) {
		repo := initRepoOnBranch(t, "feature", true)
		stageFile(t, repo, "c.txt", "id "+synthID+"\n")
		if out, err := runGuard(t, repo, list); err == nil {
			t.Fatalf("expected exact term to match exact-case content; output: %s", out)
		}
	})
}

// TestLeakGuard_NoListNoOp proves the guard is safe to install anywhere: absent
// list => exit 0 even with a would-be-forbidden term staged.
func TestLeakGuard_NoListNoOp(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	stageFile(t, repo, "leak.txt", synthTerm+"\n")

	cmd := exec.Command(leakGuardPath(t))
	cmd.Dir = repo
	// Point at a nonexistent list explicitly (also proves the resolved default
	// path is absent in a throwaway repo).
	cmd.Env = baseEnv("SPRAWL_FORBIDDEN_TERMS_FILE=" + filepath.Join(t.TempDir(), "does-not-exist"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("expected no-op pass when list absent; got error %v; output: %s", err, out)
	}
}

// TestLeakGuard_WholeTreeMode proves --all scans tracked (committed) content and
// is independent of the staged-diff scan (the phasing contract).
func TestLeakGuard_WholeTreeMode(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "secret-name:ci:"+synthTerm)
	stageFile(t, repo, "tracked.txt", "contains "+synthTerm+"\n")
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "add tracked"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}

	// Whole-tree scan catches the committed term.
	out, err := runGuard(t, repo, list, "--all")
	if err == nil {
		t.Fatalf("expected --all to catch committed term; output: %s", out)
	}
	if strings.Contains(out, synthTerm) {
		t.Errorf("guard leaked the forbidden term into --all output: %s", out)
	}
	// Staged scan (nothing staged) passes — proves staged vs whole-tree split.
	if out, err := runGuard(t, repo, list); err != nil {
		t.Fatalf("expected staged scan with nothing staged to pass; output: %s", out)
	}
}

// TestLeakGuard_OnlyAddedLinesStaged proves new-additions-only phasing: a
// pre-existing occurrence in a tracked file is NOT re-flagged when an unrelated
// clean line is staged.
func TestLeakGuard_OnlyAddedLinesStaged(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "secret-name:ci:"+synthTerm)
	stageFile(t, repo, "doc.txt", "line1 "+synthTerm+"\n")
	if out, err := gitRun(t, repo, baseEnv(), "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %s: %v", out, err)
	}

	// Append a clean line and stage the edit.
	stageFile(t, repo, "doc.txt", "line1 "+synthTerm+"\nline2 clean\n")
	if out, err := runGuard(t, repo, list); err != nil {
		t.Fatalf("staged scan must not re-flag a pre-existing occurrence; output: %s", out)
	}
}

func TestLeakGuard_CommentsAndBlankLinesSkipped(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "# a comment", "", "  ", "secret-name:ci:"+synthTerm)
	// Body deliberately contains text that a NON-skipping parser would match if
	// it mis-parsed the "# a comment" or "  " (whitespace) list lines into terms.
	stageFile(t, repo, "clean.txt", "# a comment appears here and has extra  spaces\n")
	if out, err := runGuard(t, repo, list); err != nil {
		t.Fatalf("comments/blank lines must be skipped without error; output: %s", out)
	}
}

// TestLeakGuard_UnicodeFilename proves a new file whose name has non-ASCII bytes
// (git quotes such paths in diff headers by default, core.quotePath=on) is still
// scanned — a naive `+++ b/…` header parser would skip it (fail-open leak).
func TestLeakGuard_UnicodeFilename(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "secret-name:ci:"+synthTerm)
	stageFile(t, repo, "naïve-é.txt", "body has "+synthTerm+"\n")
	if out, err := runGuard(t, repo, list); err == nil {
		t.Fatalf("expected guard to scan a unicode-named file; leak slipped through; output: %s", out)
	}
}

// TestLeakGuard_MnemonicPrefixConfig proves the staged scan is robust to a user's
// diff.mnemonicPrefix/diff.noprefix git config, which would otherwise change the
// diff header prefix (`+i/…` / no prefix) and defeat a hardcoded `b/` parser.
func TestLeakGuard_MnemonicPrefixConfig(t *testing.T) {
	for _, cfg := range [][2]string{{"diff.mnemonicPrefix", "true"}, {"diff.noprefix", "true"}} {
		t.Run(cfg[0], func(t *testing.T) {
			repo := initRepoOnBranch(t, "feature", true)
			if out, err := gitRun(t, repo, baseEnv(), "config", cfg[0], cfg[1]); err != nil {
				t.Fatalf("git config %s: %s: %v", cfg[0], out, err)
			}
			list := writeList(t, "secret-name:ci:"+synthTerm)
			stageFile(t, repo, "leak.txt", "body has "+synthTerm+"\n")
			if out, err := runGuard(t, repo, list); err == nil {
				t.Fatalf("guard defeated by %s=%s; leak slipped through; output: %s", cfg[0], cfg[1], out)
			}
		})
	}
}

// TestLeakGuard_MiscasedMatchtypeFailsSafe proves an uppercase/miscased matchtype
// falls back to the BROADER case-insensitive match rather than silently becoming
// a case-sensitive scan that a lowercase leak could evade.
func TestLeakGuard_MiscasedMatchtypeFailsSafe(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "emp:CI:"+synthName) // uppercase CI
	stageFile(t, repo, "a.txt", "value "+strings.ToLower(synthName)+"\n")
	if out, err := runGuard(t, repo, list); err == nil {
		t.Fatalf("miscased matchtype must still catch a lowercase leak; output: %s", out)
	}
}

// TestLeakGuard_RealCommitRefusedFromSubdir installs the guard as a real
// pre-commit hook and attempts a commit FROM A NESTED SUBDIR (the QUM-808
// cwd-drift lesson). The commit must be refused and no commit may land.
func TestLeakGuard_RealCommitRefusedFromSubdir(t *testing.T) {
	repo := initRepoOnBranch(t, "feature", true)
	list := writeList(t, "secret-name:ci:"+synthTerm)

	guardBytes, err := os.ReadFile(leakGuardPath(t))
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, guardBytes, 0o755); err != nil {
		t.Fatalf("install hook: %v", err)
	}

	subdir := filepath.Join(repo, "nested", "deep")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "leak.txt"), []byte(synthTerm+"\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	env := baseEnv("SPRAWL_FORBIDDEN_TERMS_FILE=" + list)
	commitCount := func() string {
		out, _ := gitRun(t, repo, baseEnv(), "rev-list", "--count", "HEAD")
		return strings.TrimSpace(out)
	}

	// Stage + commit from the nested subdir.
	addCmd := exec.Command("git", "add", "leak.txt")
	addCmd.Dir = subdir
	addCmd.Env = env
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add from subdir: %s: %v", out, err)
	}

	before := commitCount()
	commitCmd := exec.Command("git", "commit", "-m", "should be refused")
	commitCmd.Dir = subdir
	commitCmd.Env = env
	out, err := commitCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected commit to be refused by hook from subdir; output: %s", out)
	}
	if got := commitCount(); got != before {
		t.Fatalf("commit landed despite guard: count %s -> %s", before, got)
	}
}
