package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestDebugCmdRegistered(t *testing.T) {
	var debugCmd *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Use == "debug" {
			debugCmd = c
			break
		}
	}
	if debugCmd == nil {
		t.Fatal("debug subcommand not registered on rootCmd")
	}
	hasColors := false
	for _, sub := range debugCmd.Commands() {
		if sub.Use == "colors" {
			hasColors = true
			break
		}
	}
	if !hasColors {
		t.Error("debug command missing child subcommand 'colors'")
	}
}

func TestDebugCmdNoArgsPrintsHelp(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"debug"})
	defer rootCmd.SetArgs(nil)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute debug: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Available Commands") {
		t.Errorf("expected help output to contain 'Available Commands', got:\n%s", out)
	}
	if !strings.Contains(out, "colors") {
		t.Errorf("expected help output to list 'colors' subcommand, got:\n%s", out)
	}
}

func TestDebugUnknownSubcommandErrors(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"debug", "bogus"})
	defer rootCmd.SetArgs(nil)
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown subcommand, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("expected error to mention 'unknown command', got: %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected error to mention the bad arg 'bogus', got: %v", err)
	}
}
