package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/hub"
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
