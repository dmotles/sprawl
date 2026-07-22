package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/hub"
	hubstore "github.com/dmotles/sprawl/internal/hub/store"
)

// captureServe swaps serveFn for the duration of a test, capturing the config
// run() resolves.
func captureServe(t *testing.T) *hub.HubConfig {
	t.Helper()
	var captured hub.HubConfig
	orig := serveFn
	serveFn = func(_ context.Context, cfg hub.HubConfig) error {
		captured = cfg
		return nil
	}
	t.Cleanup(func() { serveFn = orig })
	return &captured
}

func TestRun_HubURLDefaultEmpty(t *testing.T) {
	captured := captureServe(t)
	env := func(string) string { return "" }
	if err := run(context.Background(), nil, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.HubURL != "" {
		t.Fatalf("hub-url default: want empty, got %q", captured.HubURL)
	}
}

func TestRun_HubURLPrecedenceFlagOverEnv(t *testing.T) {
	captured := captureServe(t)
	env := func(k string) string {
		if k == hub.EnvHubURL {
			return "https://env.example:443"
		}
		return ""
	}
	args := []string{"--hub-url", "https://flag.example:443"}
	if err := run(context.Background(), args, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.HubURL != "https://flag.example:443" {
		t.Fatalf("flag should win over env: got %q", captured.HubURL)
	}
}

func TestRun_LogsResolvedEndpointHostOnly(t *testing.T) {
	captureServe(t)
	var buf bytes.Buffer
	env := func(string) string { return "" }
	args := []string{"--hub-url", "https://user:s3cr3t@hub.example:8443/rpc?token=abc"}
	if err := run(context.Background(), args, env, &buf); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()
	// The secret/token/userinfo/path must NEVER appear in the logs.
	for _, forbidden := range []string{"s3cr3t", "token=abc", "/rpc", "user:"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, out)
		}
	}
	// The host-only form must be present.
	if !strings.Contains(out, "https://hub.example:8443") {
		t.Fatalf("log missing host-only endpoint: %s", out)
	}
	// Sanity: the log line is JSON with a component attr.
	dec := json.NewDecoder(strings.NewReader(out))
	found := false
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			break
		}
		if rec["component"] == "hubd" && rec["hub_url"] == "https://hub.example:8443" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no JSON log line with component=hubd and host-only hub_url: %s", out)
	}
}

// fakeStore is a no-op Store for exercising DSN plumbing without a database.
type fakeStore struct{ hubstore.Store }

func (fakeStore) Migrate(context.Context) error                     { return nil }
func (fakeStore) Ping(context.Context) error                        { return nil }
func (fakeStore) Close() error                                      { return nil }
func (fakeStore) EnsureUser(context.Context, hubstore.UserID) error { return nil }

func captureBuildStore(t *testing.T) *string {
	t.Helper()
	var gotDSN string
	orig := buildStoreFn
	buildStoreFn = func(_ context.Context, dsn string) (hubstore.Store, error) {
		gotDSN = dsn
		return fakeStore{}, nil
	}
	t.Cleanup(func() { buildStoreFn = orig })
	return &gotDSN
}

func TestRun_NoDSN_BuildsAndInjectsMemStore(t *testing.T) {
	captured := captureServe(t)
	captureBuildStore(t)
	if err := run(context.Background(), nil, func(string) string { return "" }, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Boot now owns store creation for the no-DSN path so it can EnsureUser
	// the singleton; the built memStore is injected into HubConfig.
	if captured.Store == nil {
		t.Fatal("no DSN: expected an injected memStore")
	}
	// EnsureUser must have run at boot: a register against MVPUserID succeeds
	// rather than hitting the users FK (ErrNotFound).
	if err := captured.Store.RegisterInstance(context.Background(), hubstore.InstanceRegistration{
		HostID: "h", RunID: "r", RepoLabel: "repo", UserID: hub.MVPUserID,
	}); err != nil {
		t.Fatalf("EnsureUser not run at boot: RegisterInstance failed: %v", err)
	}
}

func TestRun_DSN_EnsuresUserAtBoot(t *testing.T) {
	captureServe(t)
	var ensured bool
	orig := buildStoreFn
	buildStoreFn = func(_ context.Context, _ string) (hubstore.Store, error) {
		return ensureRecordingStore{onEnsure: func() { ensured = true }}, nil
	}
	t.Cleanup(func() { buildStoreFn = orig })
	env := func(k string) string {
		if k == "SPRAWL_HUB_DSN" {
			return "postgres://localhost/hub"
		}
		return ""
	}
	if err := run(context.Background(), nil, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ensured {
		t.Fatal("EnsureUser was not called at boot for the DSN path")
	}
}

// ensureRecordingStore records whether EnsureUser was invoked.
type ensureRecordingStore struct {
	hubstore.Store
	onEnsure func()
}

func (s ensureRecordingStore) EnsureUser(context.Context, hubstore.UserID) error {
	s.onEnsure()
	return nil
}
func (ensureRecordingStore) Close() error { return nil }

func TestRun_DSNFromEnv_BuildsAndInjectsStore(t *testing.T) {
	captured := captureServe(t)
	gotDSN := captureBuildStore(t)
	env := func(k string) string {
		if k == "SPRAWL_HUB_DSN" {
			return "postgres://localhost/hub"
		}
		return ""
	}
	if err := run(context.Background(), nil, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if *gotDSN != "postgres://localhost/hub" {
		t.Fatalf("buildStore DSN = %q, want env value", *gotDSN)
	}
	if captured.Store == nil {
		t.Fatal("DSN set: expected an injected Store")
	}
}

func TestRun_DSNFlagBeatsEnv(t *testing.T) {
	captureServe(t)
	gotDSN := captureBuildStore(t)
	env := func(k string) string {
		if k == "SPRAWL_HUB_DSN" {
			return "postgres://env/hub"
		}
		return ""
	}
	args := []string{"--dsn", "postgres://flag/hub"}
	if err := run(context.Background(), args, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if *gotDSN != "postgres://flag/hub" {
		t.Fatalf("buildStore DSN = %q, want flag value", *gotDSN)
	}
}

func TestRun_BrowserLoginDisabledWhenUnset(t *testing.T) {
	captured := captureServe(t)
	captureBuildStore(t)
	// Neither SPRAWL_HUB_LOGIN_TOKEN nor SPRAWL_HUB_COOKIE_KEY set.
	if err := run(context.Background(), nil, func(string) string { return "" }, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Login != nil {
		t.Fatal("browser login should be disabled (nil) when its env vars are unset")
	}
	// Host bearer auth is unaffected: a store is still injected and usable.
	if captured.Store == nil {
		t.Fatal("store must still be injected when browser login is disabled")
	}
}

func TestRun_BrowserLoginEnabledWhenConfigured(t *testing.T) {
	captured := captureServe(t)
	captureBuildStore(t)
	// 32-byte base64 key + a login token enable browser login.
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	env := func(k string) string {
		switch k {
		case hub.EnvHubLoginToken:
			return "a-login-token"
		case hub.EnvHubCookieKey:
			return key
		}
		return ""
	}
	if err := run(context.Background(), nil, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Login == nil {
		t.Fatal("browser login should be enabled when both env vars are set")
	}
}

func TestPGConfig_BlobURLFromEnv(t *testing.T) {
	// Unset SPRAWL_HUB_BLOB_URL must yield an empty BlobURL so NewPGStore keeps
	// its mem:// default (see internal/hub/store/pg.go). t.Setenv("") isolates
	// the test from any ambient shell value.
	t.Run("unset yields empty (mem:// default preserved)", func(t *testing.T) {
		t.Setenv(hub.EnvHubBlobURL, "")
		cfg := pgConfig("postgres://localhost/hub")
		if cfg.BlobURL != "" {
			t.Fatalf("BlobURL: want empty when env unset, got %q", cfg.BlobURL)
		}
		if cfg.DSN != "postgres://localhost/hub" {
			t.Fatalf("DSN passthrough: got %q", cfg.DSN)
		}
	})

	t.Run("set flows into BlobURL", func(t *testing.T) {
		t.Setenv(hub.EnvHubBlobURL, "file:///var/lib/hub/blobs")
		cfg := pgConfig("postgres://localhost/hub")
		if cfg.BlobURL != "file:///var/lib/hub/blobs" {
			t.Fatalf("BlobURL: want env value, got %q", cfg.BlobURL)
		}
	})

	t.Run("secret url still flows", func(t *testing.T) {
		t.Setenv(hub.EnvHubSecretURL, "base64key://YWJjZA==")
		cfg := pgConfig("postgres://localhost/hub")
		if cfg.SecretURL != "base64key://YWJjZA==" {
			t.Fatalf("SecretURL: want env value, got %q", cfg.SecretURL)
		}
	})
}

// TestMain1_LogsBootErrorToStderr guards against the crashloop-undiagnosable
// regression: a boot failure returned by run() must be written to stderr (w)
// before main() calls os.Exit(1), instead of being silently discarded.
func TestMain1_LogsBootErrorToStderr(t *testing.T) {
	captureServe(t) // guard: the error path returns before serveFn, but avoid a real listen

	orig := buildStoreFn
	buildStoreFn = func(context.Context, string) (hubstore.Store, error) {
		return nil, errors.New("boom-store-init")
	}
	t.Cleanup(func() { buildStoreFn = orig })

	env := func(k string) string {
		if k == "SPRAWL_HUB_DSN" {
			return "postgres://localhost/hub"
		}
		return ""
	}

	var buf bytes.Buffer
	err := main1(nil, env, &buf)
	if err == nil {
		t.Fatal("main1: expected boot error, got nil")
	}
	if !strings.Contains(buf.String(), "boom-store-init") {
		t.Fatalf("main1 did not log boot error to stderr; buf=%q", buf.String())
	}
}

func TestRun_DebugEndpointFromEnv(t *testing.T) {
	captured := captureServe(t)
	env := func(k string) string {
		if k == "SPRAWL_HUB_DEBUG_ENDPOINT" {
			return "1"
		}
		return ""
	}
	if err := run(context.Background(), nil, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !captured.DebugEndpoint {
		t.Fatal("SPRAWL_HUB_DEBUG_ENDPOINT=1 should enable the debug endpoint")
	}
}
