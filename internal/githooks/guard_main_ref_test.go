package githooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// guardMainRefScriptPath resolves the absolute path to the real
// scripts/guard-main-ref relative to this test file, so it works regardless of
// the test's cwd or which worktree it runs in.
func guardMainRefScriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(GuardMainRefScript(repoRoot))
	if err != nil {
		t.Fatalf("abs(%q): %v", GuardMainRefScript(repoRoot), err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("guard-main-ref script not found at %s: %v", abs, err)
	}
	return abs
}

// runRefHook execs the reference-transaction hook with the given phase as argv[1]
// and feeds stdinLines on stdin (each line "<old> <new> <ref>"). Returns combined
// output and the run error.
func runRefHook(t *testing.T, script, phase string, env []string, stdinLines ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(script, phase)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(strings.Join(stdinLines, "\n") + "\n")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const (
	zeroOID = "0000000000000000000000000000000000000000"
	someOID = "1111111111111111111111111111111111111111"
)

// TestGuardMainRef_PreparedPhase_BlocksNonRootOnMain is the core unit: a
// non-root agent updating refs/heads/main in the prepared phase is rejected.
func TestGuardMainRef_PreparedPhase_BlocksNonRootOnMain(t *testing.T) {
	script := guardMainRefScriptPath(t)
	out, err := runRefHook(t, script, "prepared",
		baseEnv("SPRAWL_AGENT_IDENTITY=engineer"),
		zeroOID+" "+someOID+" refs/heads/main")
	if err == nil {
		t.Fatalf("expected hook to reject refs/heads/main update by non-root agent; output: %s", out)
	}
	if !strings.Contains(out, "refusing to update 'refs/heads/main'") {
		t.Errorf("expected rejection message mentioning refs/heads/main; got: %s", out)
	}
}

// TestGuardMainRef_AllowsWeave: the root identity may advance main.
func TestGuardMainRef_AllowsWeave(t *testing.T) {
	script := guardMainRefScriptPath(t)
	out, err := runRefHook(t, script, "prepared",
		baseEnv("SPRAWL_AGENT_IDENTITY=weave"),
		zeroOID+" "+someOID+" refs/heads/main")
	if err != nil {
		t.Fatalf("expected weave to be allowed; output: %s: %v", out, err)
	}
}

// TestGuardMainRef_AllowsUnsetIdentity: a human running git directly (no
// identity) may advance main — preserve the QUM-808 human carve-out.
func TestGuardMainRef_AllowsUnsetIdentity(t *testing.T) {
	script := guardMainRefScriptPath(t)
	out, err := runRefHook(t, script, "prepared",
		baseEnv(),
		zeroOID+" "+someOID+" refs/heads/main")
	if err != nil {
		t.Fatalf("expected unset identity (human) to be allowed; output: %s: %v", out, err)
	}
}

// TestGuardMainRef_IgnoresNonMainRefs: a non-root agent updating any ref other
// than the literal refs/heads/main (feature branch, HEAD symref line, a
// remote-tracking ref) is NOT blocked — exact-match proof.
func TestGuardMainRef_IgnoresNonMainRefs(t *testing.T) {
	script := guardMainRefScriptPath(t)
	out, err := runRefHook(t, script, "prepared",
		baseEnv("SPRAWL_AGENT_IDENTITY=engineer"),
		zeroOID+" "+someOID+" refs/heads/feature",
		zeroOID+" "+someOID+" refs/heads/main-foo",
		zeroOID+" "+someOID+" refs/remotes/origin/main",
		zeroOID+" "+someOID+" HEAD")
	if err != nil {
		t.Fatalf("expected non-main ref updates to be allowed; output: %s: %v", out, err)
	}
}

// TestGuardMainRef_OnlyActsInPreparedPhase: the committed and aborted phases
// must be inert (git re-fires the same lines in the aborted phase after a
// prepared rejection; acting there would double-report).
func TestGuardMainRef_OnlyActsInPreparedPhase(t *testing.T) {
	script := guardMainRefScriptPath(t)
	for _, phase := range []string{"committed", "aborted"} {
		out, err := runRefHook(t, script, phase,
			baseEnv("SPRAWL_AGENT_IDENTITY=engineer"),
			zeroOID+" "+someOID+" refs/heads/main")
		if err != nil {
			t.Fatalf("phase %q: expected inert exit 0; output: %s: %v", phase, out, err)
		}
		// Prove the phase gate actively suppressed the block rather than the
		// block-path simply not running for another reason.
		if strings.Contains(out, "refusing to update") {
			t.Errorf("phase %q: expected no rejection message (phase gate must suppress it); got: %s", phase, out)
		}
	}
}

// TestGuardMainRef_RealCommitNoVerifyRefused is the headline regression: with
// the real hook installed as .git/hooks/reference-transaction, a non-root
// agent's `git commit --no-verify` on main is ABORTED (the bypass that defeated
// the QUM-808 pre-commit guard does NOT defeat this one), the commit does not
// land, and weave is still allowed.
func TestGuardMainRef_RealCommitNoVerifyRefused(t *testing.T) {
	script := guardMainRefScriptPath(t)
	repo := initRepoOnBranch(t, "main", true)

	hookBytes, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	hookPath := filepath.Join(repo, ".git", "hooks", "reference-transaction")
	if err := os.WriteFile(hookPath, hookBytes, 0o755); err != nil { //nolint:gosec // test hook must be executable
		t.Fatalf("install hook: %v", err)
	}

	commitCount := func(env []string) string {
		out, _ := gitRun(t, repo, env, "rev-list", "--count", "HEAD")
		return strings.TrimSpace(out)
	}
	before := commitCount(baseEnv())

	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if out, err := gitRun(t, repo, baseEnv("SPRAWL_AGENT_IDENTITY=engineer"), "add", "file.txt"); err != nil {
		t.Fatalf("git add: %s: %v", out, err)
	}

	// The whole point: --no-verify skips pre-commit but NOT reference-transaction.
	out, err := gitRun(t, repo, baseEnv("SPRAWL_AGENT_IDENTITY=engineer"), "commit", "--no-verify", "-m", "should be refused")
	if err == nil {
		t.Fatalf("expected --no-verify commit to be aborted by reference-transaction hook; output: %s", out)
	}
	if got := commitCount(baseEnv()); got != before {
		t.Fatalf("commit landed on main despite ref guard: count %s -> %s", before, got)
	}

	// weave (root) must still be able to advance main.
	if out, err := gitRun(t, repo, baseEnv("SPRAWL_AGENT_IDENTITY=weave"), "commit", "--no-verify", "-m", "weave allowed"); err != nil {
		t.Fatalf("expected weave commit to succeed; output: %s: %v", out, err)
	}
	if got := commitCount(baseEnv()); got == before {
		t.Fatalf("weave commit did not land: count stayed %s", before)
	}
}
