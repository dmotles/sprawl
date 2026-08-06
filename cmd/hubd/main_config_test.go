package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_BadProjectConfig_ReturnsError: hubd is a SEPARATE process with no
// upstream config check, so the swallow at its config.Load has to become fatal
// here or nowhere.
//
// Silently reading "" from a broken file produces a hubd that logs
// `configured=false` and serves with no uplink — a wrong-but-quiet operating
// mode, i.e. the QUM-997 shape the repo bans. The plumbing already exists:
// run() returns errors for flag-parse and store failures and main1 prints them.
func TestRun_BadProjectConfig_ReturnsError(t *testing.T) {
	captureServe(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "config.yaml"),
		[]byte("hub.url: http://x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var buf bytes.Buffer
	err := run(context.Background(), nil, rootEnv(root), &buf)
	if err == nil {
		t.Fatal("run must fail on a project config with an unrecognized key rather than serving with no uplink")
	}
	for _, want := range []string{"hub.url", "hub_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the bad key and its did-you-mean %q; got: %v", want, err)
		}
	}
}

// TestRun_ProjectConfigAbsentOrValid_StillServes is the negative control, and
// it bounds the blast radius of the change above: hubd must still start with no
// SPRAWL_ROOT, with a SPRAWL_ROOT that has no config file at all, and with a
// valid config. Only a genuinely broken file may stop it.
func TestRun_ProjectConfigAbsentOrValid_StillServes(t *testing.T) {
	// Each case carries its OWN assertion closure, so renaming a case can never
	// silently stop asserting.
	cases := []struct {
		name    string
		mkEnv   func(t *testing.T) func(string) string
		wantHub string
	}{
		{
			name:    "no SPRAWL_ROOT",
			mkEnv:   func(*testing.T) func(string) string { return func(string) string { return "" } },
			wantHub: "",
		},
		{
			name: "root with no config file",
			mkEnv: func(t *testing.T) func(string) string {
				return rootEnv(t.TempDir())
			},
			wantHub: "",
		},
		{
			name: "root with a valid config",
			mkEnv: func(t *testing.T) func(string) string {
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, ".sprawl"), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, ".sprawl", "config.yaml"),
					[]byte("hub_url: http://valid.example\n"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
				return rootEnv(root)
			},
			wantHub: "http://valid.example",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			captured := captureServe(t)
			if err := run(context.Background(), nil, c.mkEnv(t), &bytes.Buffer{}); err != nil {
				t.Fatalf("run must succeed: %v", err)
			}
			if captured.HubURL != c.wantHub {
				t.Errorf("HubURL = %q, want %q", captured.HubURL, c.wantHub)
			}
		})
	}
}

// rootEnv returns a getenv that reports the given SPRAWL_ROOT and nothing else.
func rootEnv(root string) func(string) string {
	return func(k string) string {
		if k == "SPRAWL_ROOT" {
			return root
		}
		return ""
	}
}
