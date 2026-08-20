package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// repoConfigPath is the repo's own .sprawl/config.yaml, reached relative to
// this test file (internal/config/).
//
// It is used for exactly ONE assertion — that the checked-in file still Loads
// clean under the strict parser (TestLoad_RepoConfigLoadsClean). It is
// deliberately NOT used as the round-trip fixture: it is a live file that
// agents edit, it differs per worktree and branch, and a test whose
// expectations move when someone adds a key is asserting nothing stable. The
// round-trip property lives on an inline fixture instead.
const repoConfigPath = "../../.sprawl/config.yaml"

// setKey Sets a key and fails the test if the Set is rejected. It is the single
// adaptation point for Config.Set's signature (QUM-1086 makes Set return an
// error so an unknown key can no longer be silently persisted).
func setKey(t *testing.T, cfg *Config, key, value string) {
	t.Helper()
	if err := cfg.Set(key, value); err != nil {
		t.Fatalf("Set(%q, %q): %v", key, value, err)
	}
}

// writeConfig plants config.yaml under a fresh temp sprawl root and returns the
// root.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing config.yaml: %v", err)
	}
	return root
}

// readSavedConfig returns the raw text of the config.yaml under a sprawl root.
func readSavedConfig(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".sprawl", "config.yaml"))
	if err != nil {
		t.Fatalf("reading saved config: %v", err)
	}
	return string(data)
}

// roundTripFixture is an inline config exercising all three keys that were
// map-only before QUM-1086 (worktree.setup, worktree.teardown, memory_model)
// with NON-EMPTY values, plus one key of each other type. It is inline rather
// than read from the repo precisely so that every assertion below is
// substantive: the repo's own file has no memory_model at all and an empty
// worktree.teardown, so two thirds of a round-trip assertion over it would
// compare "" against "".
const roundTripFixture = `validate: make validate
validate_timeout: 20m
validate_popup_after_seconds: 15
pause_timeout_seconds: 45
hub_url: http://hub.example:8080
hub_token_file: .sprawl/secrets/hub-token
memory_model: sonnet
worktree.setup: 'echo setup && ln -sf a b'
worktree.teardown: echo teardown
idle_reclaim.after: 20m
idle_reclaim.sweep: 45s
event_log.enabled: "true"
`

// TestLoad_RepoConfigLoadsClean guards the worst possible outcome of making the
// parser strict: a strict parser that REJECTS the repo's own checked-in
// config.yaml would break every agent spawn in this repo. Named and standalone
// so it cannot be lost to a restructuring of the round-trip tests.
func TestLoad_RepoConfigLoadsClean(t *testing.T) {
	data, err := os.ReadFile(repoConfigPath)
	if err != nil {
		// A hard failure, never a skip: a skipped fixture check is a
		// non-asserting fallback (QUM-997).
		t.Fatalf("reading the repo's own %s: %v (this fixture is required, not optional)", repoConfigPath, err)
	}
	root := writeConfig(t, string(data))

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("the repo's own checked-in config.yaml must Load clean under the strict parser: %v", err)
	}
	// And the key the commit guards depend on must actually arrive.
	if got, ok := cfg.Get("worktree.setup"); !ok || got == "" {
		t.Fatalf("Get(worktree.setup) = (%q, %v); the repo's config defines it and it installs the QUM-808/QUM-837 guards", got, ok)
	}
}

// TestSaveRoundTrip_PreservesEveryKey pins the data-loss path: `sprawl config
// set` is Load -> Set -> Save, so anything Load drops or Save fails to emit is
// permanently deleted from disk.
func TestSaveRoundTrip_PreservesEveryKey(t *testing.T) {
	root := writeConfig(t, roundTripFixture)

	before, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Precondition: the fixture must actually populate every known key, or the
	// round-trip below would be comparing zero values to zero values.
	for _, k := range KnownKeys() {
		if v, ok := before.Get(k); !ok || v == "" {
			t.Fatalf("fixture does not set %q (Get = %q, %v); the round-trip assertion would be vacuous for it", k, v, ok)
		}
	}

	if err := before.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after, err := Load(root)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if !reflect.DeepEqual(*after, *before) {
		t.Errorf("Load -> Save -> Load must be lossless.\nbefore: %+v\nafter:  %+v\nfile:\n%s",
			*before, *after, readSavedConfig(t, root))
	}
}

// TestSaveRoundTrip_NestedBlockDoesNotSilentlyDropGuards is the QUM-1078
// reproduction, expressed as the property that actually matters rather than as
// a parser detail: a config.yaml carrying a nested block must NOT silently lose
// worktree.setup.
//
// Two dispositions satisfy that property and this test accepts either, because
// the defect is the silence and not the severity:
//
//	(a) Load hard-errors, naming the offending key  -- QUM-1086's choice
//	(b) Load succeeds and worktree.setup survives   -- QUM-1078's original ask
//
// Today's code does NEITHER: yaml.v3 partially decodes into map[string]string,
// errors on the nested node, and Load's error branch throws the fully-populated
// map away -- so Load returns a nil error AND an empty key set, and the
// following Save writes that empty set over the file.
func TestSaveRoundTrip_NestedBlockDoesNotSilentlyDropGuards(t *testing.T) {
	// A leftover `liveness:` block is the real population QUM-1071 created.
	root := writeConfig(t, roundTripFixture+"liveness:\n  interval: 15m\n  enabled: true\n")

	cfg, err := Load(root)
	if err != nil {
		// Disposition (a). Held to the same strictness as arm (b): it must be
		// THIS error class about THIS key, not any error whose text happens to
		// contain the word.
		var uke *UnknownKeysError
		if !errors.As(err, &uke) {
			t.Fatalf("Load failed for an unrelated reason (%T): %v", err, err)
		}
		if len(uke.Problems) != 1 || uke.Problems[0].Key != "liveness" {
			t.Fatalf("Load must report exactly the `liveness` key; got %+v", uke.Problems)
		}
		return
	}

	// Disposition (b): Load succeeded, so the flat keys must have survived --
	// both in memory and across a Save.
	if v, ok := cfg.Get("worktree.setup"); !ok || v == "" {
		t.Fatalf("Load succeeded but silently dropped worktree.setup: Get = (%q, %v). "+
			"A nested block must not disable the QUM-808/QUM-837 commit guards without saying so.", v, ok)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := Load(root)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if v, ok := after.Get("worktree.setup"); !ok || v == "" {
		t.Fatalf("Save wrote a config that lost worktree.setup: Get = (%q, %v)", v, ok)
	}
}

// TestSetThenPauseTimeout_SameProcess is Defect 2: the two in-memory
// representations already disagree. Set("pause_timeout_seconds", ...) updates
// the side map but not the struct field, so the typed accessor keeps returning
// the old value for the life of the process.
func TestSetThenPauseTimeout_SameProcess(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	setKey(t, cfg, "pause_timeout_seconds", "60")

	if got := cfg.PauseTimeout(); got != 60*time.Second {
		t.Errorf("after Set(pause_timeout_seconds, 60), PauseTimeout() = %v, want %v "+
			"(the typed accessor must see a Set in the same process)", got, 60*time.Second)
	}
	if got, ok := cfg.Get("pause_timeout_seconds"); !ok || got != "60" {
		t.Errorf("Get(pause_timeout_seconds) = (%q, %v), want (\"60\", true)", got, ok)
	}
}

// TestSetThenValidatePopupAfter_SameProcess is the same divergence on the other
// int accessor, which no test exercises today.
func TestSetThenValidatePopupAfter_SameProcess(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	setKey(t, cfg, "validate_popup_after_seconds", "25")
	if got := cfg.ValidatePopupAfter(); got != 25*time.Second {
		t.Errorf("ValidatePopupAfter() = %v, want %v", got, 25*time.Second)
	}
}

// TestSave_CannotSilentlyDropAField: with every known key set to a non-zero
// value, Save must emit every one of them. Because Save marshals the struct,
// this holds by construction for any field added later -- which is the property
// the AC is really after. Unset keys are omitted (see
// TestSave_OmitsUnsetKeys), so no empty-value junk accumulates in the file.
func TestSave_CannotSilentlyDropAField(t *testing.T) {
	root := t.TempDir()
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, k := range KnownKeys() {
		setKey(t, cfg, k, nonZeroValueFor(t, k))
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data := readSavedConfig(t, root)
	for _, k := range KnownKeys() {
		if !strings.Contains(data, k+":") {
			t.Errorf("Save dropped the set key %q; file:\n%s", k, data)
		}
	}
	// A config Save writes must be one Load accepts -- Save must never emit a
	// key the strict parser then rejects.
	if _, err := Load(root); err != nil {
		t.Fatalf("a config written by Save must Load without error: %v", err)
	}
}

// TestSave_OmitsUnsetKeys: an unset key is not written, so `sprawl config set
// validate x` on a fresh repo produces a one-key file rather than nine keys of
// empty-value noise. Empty and absent are equivalent to every consumer
// (agentops/spawn.go, agentops/retire.go, rootinit/deps.go all treat "" as
// unset), so nothing is lost.
func TestSave_OmitsUnsetKeys(t *testing.T) {
	root := t.TempDir()
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	setKey(t, cfg, "validate", "make test")
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data := readSavedConfig(t, root)
	if !strings.Contains(data, "validate:") {
		t.Errorf("the set key must be written; file:\n%s", data)
	}
	for _, k := range KnownKeys() {
		if k == "validate" {
			continue
		}
		if strings.Contains(data, k+":") {
			t.Errorf("unset key %q must not be written; file:\n%s", k, data)
		}
	}
}
