package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsCmd_ExactArgs(t *testing.T) {
	cmd := logsCmd
	// Cobra ExactArgs(1) validator should reject 0 args
	err := cmd.Args(cmd, []string{})
	if err == nil {
		t.Error("expected error when no args provided")
	}

	// Should accept exactly 1 arg
	err = cmd.Args(cmd, []string{"alice"})
	if err != nil {
		t.Errorf("expected no error for 1 arg, got: %v", err)
	}
}

func TestRunLogs_MissingDendraRoot(t *testing.T) {
	deps := &logsDeps{
		getenv:   func(string) string { return "" },
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   &bytes.Buffer{},
	}

	err := runLogs(deps, "alice", 0)
	if err == nil {
		t.Fatal("expected error when DENDRA_ROOT is empty")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestRunLogs_NoLogsDir(t *testing.T) {
	tmpDir := t.TempDir()
	deps := &logsDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   &bytes.Buffer{},
	}

	err := runLogs(deps, "alice", 0)
	if err == nil {
		t.Fatal("expected error when logs dir does not exist")
	}
	if !strings.Contains(err.Error(), "no logs found") {
		t.Errorf("error should mention 'no logs found', got: %v", err)
	}
}

func TestRunLogs_EmptyLogsDir(t *testing.T) {
	tmpDir := t.TempDir()
	logsDir := filepath.Join(tmpDir, ".dendra", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("creating logs dir: %v", err)
	}

	deps := &logsDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   &bytes.Buffer{},
	}

	err := runLogs(deps, "alice", 0)
	if err == nil {
		t.Fatal("expected error when logs dir is empty")
	}
	if !strings.Contains(err.Error(), "no logs found") {
		t.Errorf("error should mention 'no logs found', got: %v", err)
	}
}

func TestRunLogs_ShowsAllLogs(t *testing.T) {
	tmpDir := t.TempDir()
	logsDir := filepath.Join(tmpDir, ".dendra", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("creating logs dir: %v", err)
	}

	// Create two log files
	if err := os.WriteFile(filepath.Join(logsDir, "session-001.log"), []byte("log line 1\nlog line 2\n"), 0o644); err != nil {
		t.Fatalf("writing log file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "session-002.log"), []byte("log line 3\n"), 0o644); err != nil {
		t.Fatalf("writing log file: %v", err)
	}

	var buf bytes.Buffer
	deps := &logsDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   &buf,
	}

	err := runLogs(deps, "alice", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()

	// Verify session headers
	if !strings.Contains(output, "=== Session session-001 ===") {
		t.Errorf("output should contain session-001 header, got:\n%s", output)
	}
	if !strings.Contains(output, "=== Session session-002 ===") {
		t.Errorf("output should contain session-002 header, got:\n%s", output)
	}

	// Verify log content
	if !strings.Contains(output, "log line 1") {
		t.Errorf("output should contain 'log line 1', got:\n%s", output)
	}
	if !strings.Contains(output, "log line 3") {
		t.Errorf("output should contain 'log line 3', got:\n%s", output)
	}

	// Verify ordering: session-001 before session-002
	idx1 := strings.Index(output, "session-001")
	idx2 := strings.Index(output, "session-002")
	if idx1 >= idx2 {
		t.Errorf("session-001 should appear before session-002 in output")
	}
}

func TestRunLogs_TailLines(t *testing.T) {
	tmpDir := t.TempDir()
	logsDir := filepath.Join(tmpDir, ".dendra", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("creating logs dir: %v", err)
	}

	// Create a log file with 10 lines
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "session-001.log"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing log file: %v", err)
	}

	var buf bytes.Buffer
	deps := &logsDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   &buf,
	}

	err := runLogs(deps, "alice", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	outputLines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	if len(outputLines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(outputLines), outputLines)
	}
	if outputLines[0] != "line 8" {
		t.Errorf("first tail line = %q, want %q", outputLines[0], "line 8")
	}
	if outputLines[1] != "line 9" {
		t.Errorf("second tail line = %q, want %q", outputLines[1], "line 9")
	}
	if outputLines[2] != "line 10" {
		t.Errorf("third tail line = %q, want %q", outputLines[2], "line 10")
	}
}

func TestRunLogs_TailMoreThanAvailable(t *testing.T) {
	tmpDir := t.TempDir()
	logsDir := filepath.Join(tmpDir, ".dendra", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("creating logs dir: %v", err)
	}

	// Create a log file with 5 lines
	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "session-001.log"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing log file: %v", err)
	}

	var buf bytes.Buffer
	deps := &logsDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return tmpDir
			}
			return ""
		},
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   &buf,
	}

	err := runLogs(deps, "alice", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	outputLines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	if len(outputLines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %v", len(outputLines), outputLines)
	}
	if outputLines[0] != "line 1" {
		t.Errorf("first line = %q, want %q", outputLines[0], "line 1")
	}
	if outputLines[4] != "line 5" {
		t.Errorf("last line = %q, want %q", outputLines[4], "line 5")
	}
}
