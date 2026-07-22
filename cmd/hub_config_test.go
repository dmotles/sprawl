package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
)

func newTestHubConfigDeps(t *testing.T, stdin string) (*hubConfigDeps, *bytes.Buffer, func() (string, error)) {
	t.Helper()
	dir := t.TempDir()
	userConfigDir := func() (string, error) { return dir, nil }
	var errb bytes.Buffer
	deps := &hubConfigDeps{
		UserConfigDir: userConfigDir,
		Stdin:         strings.NewReader(stdin),
		Stderr:        &errb,
	}
	return deps, &errb, userConfigDir
}

func TestHubURLSet_PersistsToUserConfig(t *testing.T) {
	deps, _, ucd := newTestHubConfigDeps(t, "")
	if err := runHubURLSet(deps, "https://hub.example:443"); err != nil {
		t.Fatalf("runHubURLSet: %v", err)
	}
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubURL != "https://hub.example:443" {
		t.Errorf("HubURL = %q, want https://hub.example:443", got.HubURL)
	}
}

func TestHubTokenSet_FromArg_PersistsAndNeverLogged(t *testing.T) {
	deps, errb, ucd := newTestHubConfigDeps(t, "")
	const secret = "sprawl_hub_tid_secretmaterial"
	if err := runHubTokenSet(deps, []string{secret}); err != nil {
		t.Fatalf("runHubTokenSet: %v", err)
	}
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubToken != secret {
		t.Errorf("HubToken = %q, want the secret", got.HubToken)
	}
	// The token must never be echoed to the status/hint stream.
	if strings.Contains(errb.String(), secret) {
		t.Fatalf("token leaked to stderr: %q", errb.String())
	}
	// A confirmation MUST still be emitted (otherwise the never-logged
	// assertion above passes vacuously on total silence).
	if !strings.Contains(strings.ToLower(errb.String()), "token") {
		t.Errorf("expected a redacted confirmation mentioning the token was saved, got %q", errb.String())
	}
}

func TestHubTokenSet_FromArg_Trimmed(t *testing.T) {
	deps, _, ucd := newTestHubConfigDeps(t, "")
	if err := runHubTokenSet(deps, []string{"  sprawl_hub_c_d\n"}); err != nil {
		t.Fatalf("runHubTokenSet: %v", err)
	}
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubToken != "sprawl_hub_c_d" {
		t.Errorf("HubToken = %q, want trimmed sprawl_hub_c_d", got.HubToken)
	}
}

func TestHubURLSet_EmptyErrors(t *testing.T) {
	deps, _, ucd := newTestHubConfigDeps(t, "")
	if err := runHubURLSet(deps, "   "); err == nil {
		t.Fatal("expected an error for an empty/whitespace URL")
	}
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubURL != "" {
		t.Errorf("HubURL = %q, want empty (nothing written)", got.HubURL)
	}
}

func TestHubTokenSet_FromStdin_Trimmed(t *testing.T) {
	deps, _, ucd := newTestHubConfigDeps(t, "  sprawl_hub_a_b\n")
	if err := runHubTokenSet(deps, nil); err != nil {
		t.Fatalf("runHubTokenSet: %v", err)
	}
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubToken != "sprawl_hub_a_b" {
		t.Errorf("HubToken = %q, want trimmed sprawl_hub_a_b", got.HubToken)
	}
}

func TestHubTokenSet_NoArgEmptyStdin_Errors(t *testing.T) {
	deps, _, ucd := newTestHubConfigDeps(t, "   \n")
	if err := runHubTokenSet(deps, nil); err == nil {
		t.Fatal("expected an error when no token provided via arg or stdin")
	}
	// Nothing should have been written.
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubToken != "" {
		t.Errorf("HubToken = %q, want empty (nothing written)", got.HubToken)
	}
}

func TestHubURLSet_PreservesExistingToken(t *testing.T) {
	deps, _, ucd := newTestHubConfigDeps(t, "")
	if err := runHubTokenSet(deps, []string{"sprawl_hub_keep_me"}); err != nil {
		t.Fatalf("token set: %v", err)
	}
	if err := runHubURLSet(deps, "https://hub.example"); err != nil {
		t.Fatalf("url set: %v", err)
	}
	got, err := config.LoadUserConfig(ucd)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got.HubToken != "sprawl_hub_keep_me" {
		t.Errorf("token clobbered by url set: %q", got.HubToken)
	}
	if got.HubURL != "https://hub.example" {
		t.Errorf("HubURL = %q, want https://hub.example", got.HubURL)
	}
}
