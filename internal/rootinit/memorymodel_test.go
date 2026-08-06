package rootinit

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
)

// TestLoadMemoryModel_ConfigError_IsLoud pins the fix for the seventh swallowed
// config.Load: defaultLoadMemoryModel returned "" on any error, so a broken
// config.yaml silently downgraded the memory-distillation model with nothing
// said anywhere.
//
// The disposition is warn-and-continue, not fatal: killing post-session
// consolidation over a config typo loses the session summary, which costs more
// than using the default model. And with the authoritative Load in runEnter,
// this path is unreachable in the normal flow — the only way to reach it is a
// config edited mid-session — which is exactly why a warning is proportionate
// here where a hard error is proportionate in hubd (a separate process with no
// upstream check at all).
func TestLoadMemoryModel_ConfigError_IsLoud(t *testing.T) {
	var warn bytes.Buffer
	load := func(string) (*config.Config, error) {
		return nil, errors.New("unrecognized key `worktree_setup` on line 2")
	}

	got := loadMemoryModelFrom(load, &warn, "/tmp/root")

	if got != "" {
		t.Errorf("a config error must fall back to the default model (\"\"), got %q", got)
	}
	out := warn.String()
	if out == "" {
		t.Fatal("a config error must be reported; returning \"\" silently is the defect")
	}
	for _, want := range []string{"worktree_setup", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning must contain %q so the cause and the consequence are both visible; got:\n%s", want, out)
		}
	}
}

// TestLoadMemoryModel_Success_IsSilent is the negative control: the happy path
// must return the configured model and write nothing. Without it, an
// implementation that always warned would pass the test above.
func TestLoadMemoryModel_Success_IsSilent(t *testing.T) {
	var warn bytes.Buffer
	load := func(string) (*config.Config, error) {
		return &config.Config{MemoryModel: "sonnet"}, nil
	}

	if got := loadMemoryModelFrom(load, &warn, "/tmp/root"); got != "sonnet" {
		t.Errorf("loadMemoryModelFrom = %q, want %q (the typed MemoryModel field must drive this)", got, "sonnet")
	}
	if warn.Len() != 0 {
		t.Errorf("the happy path must be silent; got:\n%s", warn.String())
	}
}

// TestLoadMemoryModel_UnsetIsSilent: no memory_model key is the common case and
// must not warn — only a config that fails to LOAD is worth a warning.
func TestLoadMemoryModel_UnsetIsSilent(t *testing.T) {
	var warn bytes.Buffer
	load := func(string) (*config.Config, error) { return &config.Config{}, nil }

	if got := loadMemoryModelFrom(load, &warn, "/tmp/root"); got != "" {
		t.Errorf("loadMemoryModelFrom = %q, want \"\" when memory_model is unset", got)
	}
	if warn.Len() != 0 {
		t.Errorf("an unset memory_model must be silent; got:\n%s", warn.String())
	}
}
