package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/hub/store"
)

func newTestHubMigrateDeps() (*hubMigrateDeps, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	deps := &hubMigrateDeps{
		Getenv: func(string) string { return "" },
		Stdout: &out,
		Stderr: &errb,
		Apply:  func(context.Context, string) error { return nil },
		Status: func(context.Context, string) ([]store.MigrationStatus, error) { return nil, nil },
	}
	return deps, &out, &errb
}

func TestHubMigrate_ResolveDSN_FlagBeatsEnv(t *testing.T) {
	getenv := func(k string) string {
		if k == "SPRAWL_HUB_DSN" {
			return "env-dsn"
		}
		return ""
	}
	got, err := resolveHubDSN(getenv, "flag-dsn")
	if err != nil {
		t.Fatalf("resolveHubDSN: %v", err)
	}
	if got != "flag-dsn" {
		t.Errorf("dsn = %q, want flag-dsn (flag beats env)", got)
	}
}

func TestHubMigrate_ResolveDSN_EnvFallback(t *testing.T) {
	getenv := func(k string) string {
		if k == "SPRAWL_HUB_DSN" {
			return "env-dsn"
		}
		return ""
	}
	got, err := resolveHubDSN(getenv, "")
	if err != nil {
		t.Fatalf("resolveHubDSN: %v", err)
	}
	if got != "env-dsn" {
		t.Errorf("dsn = %q, want env-dsn", got)
	}
}

func TestHubMigrate_ResolveDSN_MissingErrors(t *testing.T) {
	_, err := resolveHubDSN(func(string) string { return "" }, "")
	if err == nil {
		t.Fatal("expected error when no DSN is provided")
	}
	// Actionable: must point the agent at the flag/env.
	if !strings.Contains(err.Error(), "--dsn") || !strings.Contains(err.Error(), "SPRAWL_HUB_DSN") {
		t.Errorf("error = %q, want mention of --dsn and SPRAWL_HUB_DSN", err)
	}
}

func TestHubMigrate_Apply_HappyPath(t *testing.T) {
	deps, _, errb := newTestHubMigrateDeps()
	var gotDSN string
	deps.Apply = func(_ context.Context, dsn string) error {
		gotDSN = dsn
		return nil
	}
	if err := runHubMigrate(context.Background(), deps, "the-dsn"); err != nil {
		t.Fatalf("runHubMigrate: %v", err)
	}
	if gotDSN != "the-dsn" {
		t.Errorf("Apply dsn = %q, want the-dsn", gotDSN)
	}
	if !strings.Contains(errb.String(), "migrat") {
		t.Errorf("stderr = %q, want a migration status message", errb.String())
	}
}

func TestHubMigrate_Apply_ErrorWrapped(t *testing.T) {
	deps, _, _ := newTestHubMigrateDeps()
	sentinel := errors.New("boom")
	deps.Apply = func(context.Context, string) error { return sentinel }
	err := runHubMigrate(context.Background(), deps, "the-dsn")
	if err == nil {
		t.Fatal("expected error from Apply to propagate")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped sentinel", err)
	}
}

func TestHubMigrate_Apply_NoDSNErrors(t *testing.T) {
	deps, _, _ := newTestHubMigrateDeps()
	called := false
	deps.Apply = func(context.Context, string) error { called = true; return nil }
	err := runHubMigrate(context.Background(), deps, "")
	if err == nil {
		t.Fatal("expected error when no DSN provided")
	}
	if called {
		t.Error("Apply should not be called without a DSN")
	}
}

func TestHubMigrateStatus_PrintsVersions(t *testing.T) {
	deps, out, _ := newTestHubMigrateDeps()
	deps.Status = func(context.Context, string) ([]store.MigrationStatus, error) {
		return []store.MigrationStatus{
			{Version: 1, Source: "00001_phase0_init.sql", Applied: true},
			{Version: 2, Source: "00002_future.sql", Applied: false},
		}, nil
	}
	if err := runHubMigrateStatus(context.Background(), deps, "the-dsn"); err != nil {
		t.Fatalf("runHubMigrateStatus: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "00001_phase0_init.sql") {
		t.Errorf("stdout = %q, want migration 1 listed", s)
	}
	if !strings.Contains(s, "applied") || !strings.Contains(s, "pending") {
		t.Errorf("stdout = %q, want applied+pending states shown", s)
	}
}
