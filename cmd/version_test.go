package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion_DefaultValues(t *testing.T) {
	var buf bytes.Buffer
	SetVersionInfo("dev", "none", "unknown")
	cmd := rootCmd
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dev") {
		t.Errorf("expected output to contain 'dev', got: %s", out)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("expected output to contain 'none', got: %s", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected output to contain 'unknown', got: %s", out)
	}
}

func TestVersion_CustomValues(t *testing.T) {
	var buf bytes.Buffer
	SetVersionInfo("1.2.3", "abc1234", "2026-04-06T00:00:00Z")
	cmd := rootCmd
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.2.3") {
		t.Errorf("expected output to contain '1.2.3', got: %s", out)
	}
	if !strings.Contains(out, "abc1234") {
		t.Errorf("expected output to contain 'abc1234', got: %s", out)
	}
	if !strings.Contains(out, "2026-04-06T00:00:00Z") {
		t.Errorf("expected output to contain '2026-04-06T00:00:00Z', got: %s", out)
	}
}
