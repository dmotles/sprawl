package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestDebugColorsOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"debug", "colors"})
	defer rootCmd.SetArgs(nil)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute debug colors: %v", err)
	}
	out := buf.String()
	if len(strings.TrimSpace(out)) == 0 {
		t.Fatal("debug colors produced empty output")
	}
	if !strings.Contains(out, "interrupt sent to weave") {
		t.Errorf("expected sample message %q in output, got:\n%s", "interrupt sent to weave", out)
	}
	for _, treatment := range []string{"plain-fg", "bold", "inverse", "bg-fill", "bordered"} {
		if !strings.Contains(out, treatment) {
			t.Errorf("expected treatment header %q in output, got:\n%s", treatment, out)
		}
	}
}

func TestDebugColorsAllRolesPresent(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"debug", "colors"})
	defer rootCmd.SetArgs(nil)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute debug colors: %v", err)
	}
	out := buf.String()
	roles := []string{
		"Primary",
		"Accent",
		"Info",
		"Success",
		"Warning",
		"Error",
		"Busy",
		"System",
		"FgSubtle",
	}
	for _, role := range roles {
		if !strings.Contains(out, role) {
			t.Errorf("expected role label %q in output, got:\n%s", role, out)
		}
	}
}
