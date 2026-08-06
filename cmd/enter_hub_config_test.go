package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
)

// TestDefaultHubDialOut_TakesConfigFromCaller: the disposition for
// cmd/enter_hub.go's swallowed config.Load is to ELIMINATE the load rather than
// convert it. defaultHubDialOut is reached from exactly one non-test site —
// runEnter's `go deps.hubDialOut(...)` — which is downstream of the
// authoritative Load, so a second load there can only re-derive a result the
// caller already has, and its `err == nil` guard can only hide a failure the
// caller already handled. Its doc comment forbids returning an error, so there
// is no third option; passing the already-validated *Config is the strongest
// disposition available.
//
// This is asserted structurally: with NO config.yaml on disk at all, the hub
// URL must still be read from the injected *Config. An implementation that
// still called config.Load internally would see an absent file, get a
// zero-value config, and take the offline no-op path.
func TestDefaultHubDialOut_TakesConfigFromCaller(t *testing.T) {
	// Isolate from the dev's real user config: defaultHubDialOut still reads
	// LoadUserConfig, and hub.ResolveHubURL ranks USER above PROJECT, so a real
	// user hub_url would mask the caller-supplied one (and a real user token
	// would attempt a live 10s dial).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir() // deliberately NO .sprawl/config.yaml
	var buf bytes.Buffer

	cfg := &config.Config{HubURL: "http://from-caller.example:8080"}
	defaultHubDialOut(func(string) string { return "" }, &buf, root, cfg)

	// No token is available, so registration is skipped — but the skip message
	// proves the caller-supplied URL was the one resolved.
	out := buf.String()
	if !strings.Contains(out, "from-caller.example") {
		t.Errorf("the caller-supplied HubURL must drive resolution (a nil-config or internal reload would take the offline no-op path); log:\n%s", out)
	}
}

// TestDefaultHubDialOut_NilConfigIsOfflineNoOp: the signature admits nil (test
// doubles and any future caller without a config), and nil must be the offline
// no-op, never a panic.
func TestDefaultHubDialOut_NilConfigIsOfflineNoOp(t *testing.T) {
	// Isolate from the dev's real user config: defaultHubDialOut still reads
	// LoadUserConfig, and hub.ResolveHubURL ranks USER above PROJECT, so a real
	// user hub_url would mask the caller-supplied one (and a real user token
	// would attempt a live 10s dial).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var buf bytes.Buffer
	defaultHubDialOut(func(string) string { return "" }, &buf, t.TempDir(), nil)
	if buf.Len() != 0 {
		t.Errorf("a nil config with no env must be a silent offline no-op; got:\n%s", buf.String())
	}
}

// TestDefaultHubDialOut_IgnoresOnDiskConfig is the negative control for
// TestDefaultHubDialOut_TakesConfigFromCaller: with a config file on disk that
// says one thing and a caller-supplied *Config that says another, the CALLER
// wins. Without this, an implementation that ignored the parameter and reloaded
// from disk could still pass the test above by coincidence in a root that
// happened to have a matching file.
func TestDefaultHubDialOut_IgnoresOnDiskConfig(t *testing.T) {
	// Isolate from the dev's real user config: defaultHubDialOut still reads
	// LoadUserConfig, and hub.ResolveHubURL ranks USER above PROJECT, so a real
	// user hub_url would mask the caller-supplied one (and a real user token
	// would attempt a live 10s dial).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "config.yaml"),
		[]byte("hub_url: http://on-disk.example:9999\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var buf bytes.Buffer
	defaultHubDialOut(func(string) string { return "" }, &buf, root,
		&config.Config{HubURL: "http://from-caller.example:8080"})

	out := buf.String()
	if strings.Contains(out, "on-disk.example") {
		t.Errorf("defaultHubDialOut must not re-read config.yaml; it took the on-disk value:\n%s", out)
	}
	if !strings.Contains(out, "from-caller.example") {
		t.Errorf("the caller-supplied HubURL must win; log:\n%s", out)
	}
}
