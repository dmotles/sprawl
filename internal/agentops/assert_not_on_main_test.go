package agentops

import (
	"os/exec"
	"strings"
	"testing"
)

// initRepoOnBranchForAssert creates a temp git repo whose checked-out branch is
// `branch`, with one commit so HEAD resolves. Returns the worktree dir.
func initRepoOnBranchForAssert(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init")
	run("symbolic-ref", "HEAD", "refs/heads/"+branch)
	run("commit", "--allow-empty", "-m", "initial")
	return dir
}

// detachHEAD puts the repo into a detached-HEAD state (HEAD points at a commit,
// not a branch ref) — symbolic-ref then returns empty, which is NOT the incident.
func detachHEAD(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %s: %v", out, err)
	}
}

func TestAssertNotOnMain(t *testing.T) {
	mainRepo := initRepoOnBranchForAssert(t, "main")
	featureRepo := initRepoOnBranchForAssert(t, "dmotles/qum-837-foo")
	detachedRepo := initRepoOnBranchForAssert(t, "main")
	detachHEAD(t, detachedRepo)

	cases := []struct {
		name     string
		worktree string
		identity string
		wantErr  bool
	}{
		{name: "main + non-root agent => error", worktree: mainRepo, identity: "engineer", wantErr: true},
		{name: "main + weave => exempt", worktree: mainRepo, identity: "weave", wantErr: false},
		{name: "main + empty identity (human) => exempt", worktree: mainRepo, identity: "", wantErr: false},
		{name: "feature branch + non-root agent => ok", worktree: featureRepo, identity: "engineer", wantErr: false},
		{name: "detached HEAD + non-root agent => ok (not the incident)", worktree: detachedRepo, identity: "engineer", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertNotOnMain(tc.worktree, tc.identity)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for identity %q on %s, got nil", tc.identity, tc.worktree)
				}
				if !strings.Contains(err.Error(), "main") {
					t.Errorf("expected error to mention 'main'; got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("expected no error for identity %q on %s, got: %v", tc.identity, tc.worktree, err)
			}
		})
	}
}
