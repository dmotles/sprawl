package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func (fakeStore) Migrate(context.Context) error { return nil }
func (fakeStore) Ping(context.Context) error    { return nil }
func (fakeStore) Close() error                  { return nil }

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

func TestRun_NoDSN_NoStoreInjected(t *testing.T) {
	captured := captureServe(t)
	captureBuildStore(t)
	if err := run(context.Background(), nil, func(string) string { return "" }, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if captured.Store != nil {
		t.Fatal("no DSN: expected nil Store (NewServer defaults to memStore)")
	}
}

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
