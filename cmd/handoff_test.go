package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestHandoffDeps(t *testing.T) (*handoffDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()

	// Set up root-name file
	state.WriteRootName(tmpDir, "sensei")

	// Set up last-session-id file
	memDir := filepath.Join(tmpDir, ".dendra", "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "last-session-id"), []byte("test-session-123"), 0o644)

	// Set up agents directory
	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	var stdout bytes.Buffer
	deps := &handoffDeps{
		stdout: &stdout,
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "sensei"
			}
			return ""
		},
		readStdin: func() ([]byte, error) {
			return []byte("Session summary text"), nil
		},
		listAgents:          state.ListAgents,
		writeSessionSummary: memory.WriteSessionSummary,
		readLastSessionID:   memory.ReadLastSessionID,
		writeSignalFile:     memory.WriteHandoffSignal,
		now: func() time.Time {
			return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
		},
	}

	return deps, tmpDir
}

func TestHandoff_HappyPath(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ash",
		Status: "active",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "elm",
		Status: "active",
	})

	err := runHandoff(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify session summary file was written (filename includes timestamp prefix)
	sessDir := filepath.Join(tmpDir, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(sessDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "session_id: test-session-123") {
		t.Errorf("should contain session_id, got: %s", content)
	}
	if !strings.Contains(content, "timestamp: 2026-04-02T12:00:00Z") {
		t.Errorf("should contain timestamp, got: %s", content)
	}
	if !strings.Contains(content, "handoff: true") {
		t.Errorf("should contain handoff: true, got: %s", content)
	}
	if !strings.Contains(content, "Session summary text") {
		t.Errorf("should contain summary text, got: %s", content)
	}

	// Verify handoff signal file exists
	signalPath := filepath.Join(tmpDir, ".dendra", "memory", "handoff-signal")
	if _, err := os.Stat(signalPath); os.IsNotExist(err) {
		t.Error("handoff-signal file should exist")
	}
}

func TestHandoff_NonRootAgent(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)
	deps.getenv = func(key string) string {
		switch key {
		case "DENDRA_ROOT":
			return tmpDir
		case "DENDRA_AGENT_IDENTITY":
			return "elm"
		}
		return ""
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for non-root agent")
	}
	if !strings.Contains(err.Error(), "handoff can only be run by the root agent") {
		t.Errorf("error should mention root agent restriction, got: %v", err)
	}
}

func TestHandoff_MissingAgentIdentity(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return tmpDir
		}
		return ""
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestHandoff_MissingDendraRoot(t *testing.T) {
	deps, _ := newTestHandoffDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "sensei"
		}
		return ""
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestHandoff_MissingSessionID(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)

	// Remove the last-session-id file
	os.Remove(filepath.Join(tmpDir, ".dendra", "memory", "last-session-id"))

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
}

func TestHandoff_EmptyStdin(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)
	deps.readStdin = func() ([]byte, error) {
		return []byte(""), nil
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for empty stdin")
	}
	if !strings.Contains(err.Error(), "no summary provided") {
		t.Errorf("error should mention 'no summary provided', got: %v", err)
	}
	if !strings.Contains(err.Error(), "dendra handoff") {
		t.Errorf("error should include usage hint with 'dendra handoff', got: %v", err)
	}
	if !strings.Contains(err.Error(), "What was accomplished") {
		t.Errorf("error should suggest summary sections, got: %v", err)
	}

	// Verify no session file was created
	sessDir := filepath.Join(tmpDir, ".dendra", "memory", "sessions")
	if _, err := os.Stat(sessDir); err == nil {
		entries, _ := os.ReadDir(sessDir)
		if len(entries) > 0 {
			t.Error("no session file should be created when summary is empty")
		}
	}

	// Verify no handoff signal file was created
	signalPath := filepath.Join(tmpDir, ".dendra", "memory", "handoff-signal")
	if _, err := os.Stat(signalPath); err == nil {
		t.Error("no handoff-signal file should be created when summary is empty")
	}
}

func TestHandoff_WhitespaceOnlyStdin(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)
	deps.readStdin = func() ([]byte, error) {
		return []byte("   \n\t\n  "), nil
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for whitespace-only stdin")
	}
	if !strings.Contains(err.Error(), "no summary provided") {
		t.Errorf("error should mention 'no summary provided', got: %v", err)
	}

	// Verify no session file was created
	sessDir := filepath.Join(tmpDir, ".dendra", "memory", "sessions")
	if _, err := os.Stat(sessDir); err == nil {
		entries, _ := os.ReadDir(sessDir)
		if len(entries) > 0 {
			t.Error("no session file should be created when summary is whitespace-only")
		}
	}

	// Verify no handoff signal file was created
	signalPath := filepath.Join(tmpDir, ".dendra", "memory", "handoff-signal")
	if _, err := os.Stat(signalPath); err == nil {
		t.Error("no handoff-signal file should be created when summary is whitespace-only")
	}
}

func TestHandoff_TerseInput(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)
	deps.readStdin = func() ([]byte, error) {
		return []byte("done"), nil
	}

	err := runHandoff(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify session file was created
	sessDir := filepath.Join(tmpDir, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}

	// Verify handoff signal file exists
	signalPath := filepath.Join(tmpDir, ".dendra", "memory", "handoff-signal")
	if _, err := os.Stat(signalPath); os.IsNotExist(err) {
		t.Error("handoff-signal file should exist for terse but non-empty input")
	}
}

func TestHandoff_NoAgents(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)

	err := runHandoff(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sessDir := filepath.Join(tmpDir, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(sessDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "agents_active: []") {
		t.Errorf("should contain empty agents_active, got: %s", content)
	}
}

func TestHandoff_WriteSessionSummaryFailure(t *testing.T) {
	deps, _ := newTestHandoffDeps(t)
	deps.writeSessionSummary = func(dendraRoot string, session memory.Session, body string) error {
		return fmt.Errorf("simulated write failure")
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error from writeSessionSummary failure")
	}
	if !strings.Contains(err.Error(), "simulated write failure") {
		t.Errorf("error should contain failure message, got: %v", err)
	}
}

func TestHandoff_WriteSignalFileFailure(t *testing.T) {
	deps, _ := newTestHandoffDeps(t)
	deps.writeSignalFile = func(dendraRoot string) error {
		return fmt.Errorf("simulated signal failure")
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error from writeSignalFile failure")
	}
	if !strings.Contains(err.Error(), "simulated signal failure") {
		t.Errorf("error should contain failure message, got: %v", err)
	}
}

func TestHandoff_ExitInstructions(t *testing.T) {
	deps, _ := newTestHandoffDeps(t)

	err := runHandoff(deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := deps.stdout.(*bytes.Buffer).String()

	if !strings.Contains(output, "Handoff complete. Session summary written.") {
		t.Errorf("should contain success message, got: %s", output)
	}
	if !strings.Contains(output, "/exit") {
		t.Errorf("should contain /exit instruction, got: %s", output)
	}
	if !strings.Contains(output, "Ctrl+D") {
		t.Errorf("should contain Ctrl+D instruction, got: %s", output)
	}
	if !strings.Contains(output, "Ctrl+C") {
		t.Errorf("should contain Ctrl+C instruction, got: %s", output)
	}
	if !strings.Contains(output, "sensei loop will automatically restart") {
		t.Errorf("should mention sensei loop restart, got: %s", output)
	}
}

func TestHandoff_ErrorNoExitInstructions(t *testing.T) {
	deps, tmpDir := newTestHandoffDeps(t)
	// Make the agent a non-root agent to trigger an error
	deps.getenv = func(key string) string {
		switch key {
		case "DENDRA_ROOT":
			return tmpDir
		case "DENDRA_AGENT_IDENTITY":
			return "not-root"
		}
		return ""
	}

	err := runHandoff(deps)
	if err == nil {
		t.Fatal("expected error for non-root agent")
	}

	output := deps.stdout.(*bytes.Buffer).String()
	if strings.Contains(output, "/exit") {
		t.Errorf("should not contain exit instructions on error, got: %s", output)
	}
	if strings.Contains(output, "Ctrl+D") {
		t.Errorf("should not contain exit instructions on error, got: %s", output)
	}
}
