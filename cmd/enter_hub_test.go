package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/hub"
)

// TestDefaultHubDialOut_OfflineNoOp: with no hub URL configured anywhere, the
// dial-out is a silent no-op — no registration attempt, no log noise.
func TestDefaultHubDialOut_OfflineNoOp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from the dev's real user config
	var buf bytes.Buffer
	defaultHubDialOut(func(string) string { return "" }, &buf, t.TempDir())
	if buf.Len() != 0 {
		t.Fatalf("offline path should be silent, got: %q", buf.String())
	}
}

// TestDefaultHubDialOut_URLButNoTokenSkips: a hub URL with no token resolvable
// logs a skip and does NOT attempt to dial (which would surface a connection
// error instead of the "no token" message).
func TestDefaultHubDialOut_URLButNoTokenSkips(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from the dev's real user config
	var buf bytes.Buffer
	getenv := func(k string) string {
		if k == hub.EnvHubURL {
			return "http://127.0.0.1:0"
		}
		return ""
	}
	defaultHubDialOut(getenv, &buf, t.TempDir())
	out := buf.String()
	if !strings.Contains(out, "no token") {
		t.Fatalf("expected a no-token skip message, got: %q", out)
	}
	// The endpoint is logged host-only (redacted), never with a token.
	if strings.Contains(out, "sprawl_hub_") {
		t.Fatalf("log leaked a token: %q", out)
	}
}

// TestDefaultHubDialOut_BadTokenFileModeSkips: a token file with the wrong
// permissions is refused and the dial-out bails (non-fatal).
func TestDefaultHubDialOut_BadTokenFileModeSkips(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from the dev's real user config
	root := t.TempDir()
	tokFile := filepath.Join(root, "tok")
	if err := os.WriteFile(tokFile, []byte("sprawl_hub_x_y"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	getenv := func(k string) string {
		if k == hub.EnvHubURL {
			return "localhost:8080"
		}
		return ""
	}
	// Point config-less resolution at the file by placing it via env is not
	// possible (env is the token VALUE, not a path), so exercise the file path
	// through a written config.
	writeHubConfig(t, root, "localhost:8080", "tok")
	defaultHubDialOut(getenv, &buf, root)
	if !strings.Contains(buf.String(), "0600") {
		t.Fatalf("expected a 0600 mode rejection, got: %q", buf.String())
	}
}

// TestDefaultHubDialOut_UserConfigURLWiredIn: a hub_url set ONLY in the
// user-level config (no env, no flag, no project config) is resolved and
// drives the dial-out to the no-token skip path — proving the user-config
// layer is wired into defaultHubDialOut.
func TestDefaultHubDialOut_UserConfigURLWiredIn(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := config.SaveUserConfig(os.UserConfigDir, config.UserConfig{HubURL: "http://127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	defaultHubDialOut(func(string) string { return "" }, &buf, t.TempDir())
	if !strings.Contains(buf.String(), "no token") {
		t.Fatalf("expected user-config URL to reach the no-token skip path, got: %q", buf.String())
	}
}

func writeHubConfig(t *testing.T, root, hubURL, tokenFile string) {
	t.Helper()
	dir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "hub_url: \"" + hubURL + "\"\nhub_token_file: \"" + tokenFile + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
