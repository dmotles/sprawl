package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
)

// TestConfigSet_UnknownKey_ErrorsWithReferenceAndDoesNotWrite: today `sprawl
// config set worktree_setup ...` silently persists the typo, so the user
// believes the guard script is installed and it is not. It must fail loudly,
// carry the reference, and leave the file untouched.
func TestConfigSet_UnknownKey_ErrorsWithReferenceAndDoesNotWrite(t *testing.T) {
	deps, root := newTestConfigDeps(t)
	if err := os.MkdirAll(filepath.Join(root, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(root, ".sprawl", "config.yaml")
	original := "validate: make validate\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := runConfigSet(deps, "worktree_setup", "echo oops")
	if err == nil {
		t.Fatal("runConfigSet on an unknown key must fail instead of persisting a typo")
	}
	msg := err.Error()
	for _, want := range []string{"worktree_setup", "worktree.setup"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must contain %q; got:\n%s", want, msg)
		}
	}
	for _, k := range config.KnownKeys() {
		if !strings.Contains(msg, k) {
			t.Errorf("error must carry the full reference (missing %q); got:\n%s", k, msg)
		}
	}

	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("read back: %v", rerr)
	}
	if string(after) != original {
		t.Errorf("a rejected Set must not rewrite config.yaml.\nbefore: %q\nafter:  %q", original, string(after))
	}
}

// TestConfigSet_KnownKey_StillSucceeds is the negative control for the test
// above: the same harness with a recognized key must write.
func TestConfigSet_KnownKey_StillSucceeds(t *testing.T) {
	deps, root := newTestConfigDeps(t)
	if err := runConfigSet(deps, "worktree.setup", "echo hi"); err != nil {
		t.Fatalf("runConfigSet on a recognized key must succeed: %v", err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := cfg.Get("worktree.setup"); !ok || got != "echo hi" {
		t.Errorf("Get(worktree.setup) = (%q, %v), want (\"echo hi\", true)", got, ok)
	}
}

// TestConfigGet_UnknownKey_Errors: today `sprawl config get bogus` prints
// nothing and exits 0 — a silent success that reads as "the key is set to
// empty". It must be an error naming the key.
func TestConfigGet_UnknownKey_Errors(t *testing.T) {
	deps, _ := newTestConfigDeps(t)
	err := runConfigGet(deps, "bogus_key")
	if err == nil {
		t.Fatal("runConfigGet on an unknown key must fail rather than print nothing and exit 0")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("error must name the key; got: %v", err)
	}
}

// TestConfigSetFromFile_UnknownKey_Errors: `--file` is a separate
// arg-resolution route into runConfigSet, and it is the route most likely to be
// used for worktree.setup (a multi-line bash script). A typo there must fail
// the same way.
func TestConfigSetFromFile_UnknownKey_Errors(t *testing.T) {
	deps, root := newTestConfigDeps(t)
	scriptPath := filepath.Join(root, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	key, value, err := resolveConfigSetValue(deps, []string{"worktree_setup"}, scriptPath)
	if err != nil {
		t.Fatalf("resolveConfigSetValue: %v", err)
	}
	if err := runConfigSet(deps, key, value); err == nil {
		t.Fatal("`config set --file` with an unknown key must fail")
	}
}

// TestConfigShow_BrokenConfig_Errors: `show` is the third CLI verb and the one a
// user reaches for when something looks wrong. It must report the parse failure
// rather than print "No configuration set", which reads as "the file is empty".
func TestConfigShow_BrokenConfig_Errors(t *testing.T) {
	deps, root := newTestConfigDeps(t)
	if err := os.MkdirAll(filepath.Join(root, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "config.yaml"),
		[]byte("liveness:\n  interval: 15m\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := runConfigShow(deps)
	if err == nil {
		t.Fatal("runConfigShow on a broken config must fail, not print \"No configuration set\"")
	}
	if !strings.Contains(err.Error(), "QUM-1071") {
		t.Errorf("the retired-key remedy must reach the `config show` user; got: %v", err)
	}
}

// TestConfigCmd_LongIsGenerated: the hand-maintained partial key list in
// cmd/config.go must be replaced by the generated reference, so the reference
// is reachable BEFORE the user breaks the file. Asserts the Long text contains
// config.Reference() verbatim — a hand-written copy would drift.
func TestConfigCmd_LongIsGenerated(t *testing.T) {
	if !strings.Contains(configCmd.Long, config.Reference()) {
		t.Errorf("`sprawl config --help` must embed config.Reference() verbatim.\nLong:\n%s\n\nReference():\n%s", configCmd.Long, config.Reference())
	}
	// Every recognized key must therefore be documented — including the three
	// the old hand-written list omitted.
	for _, k := range config.KnownKeys() {
		if !strings.Contains(configCmd.Long, k) {
			t.Errorf("`config --help` must document %q", k)
		}
	}
}
