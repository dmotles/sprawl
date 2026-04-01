package cmd

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

func TestPokeCmd_ExactArgs(t *testing.T) {
	cmd := pokeCmd
	// Should reject 0 args
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("expected error when no args provided")
	}
	// Should reject 1 arg
	if err := cmd.Args(cmd, []string{"agent"}); err == nil {
		t.Error("expected error when 1 arg provided")
	}
	// Should accept exactly 2 args
	if err := cmd.Args(cmd, []string{"agent", "message"}); err != nil {
		t.Errorf("expected no error for 2 args, got: %v", err)
	}
	// Should reject 3 args
	if err := cmd.Args(cmd, []string{"agent", "msg", "extra"}); err == nil {
		t.Error("expected error when 3 args provided")
	}
}

func TestRunPoke_HappyPath(t *testing.T) {
	var writtenPath string
	var writtenData []byte
	var writtenPerm fs.FileMode
	var stdout bytes.Buffer

	deps := &pokeDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return "/tmp/test-root"
			}
			return ""
		},
		writeFile: func(path string, data []byte, perm fs.FileMode) error {
			writtenPath = path
			writtenData = data
			writtenPerm = perm
			return nil
		},
		stdout: &stdout,
	}

	err := runPoke(deps, "palm", "Hey, you okay?")
	if err != nil {
		t.Fatalf("runPoke error: %v", err)
	}

	// Verify file path
	wantPath := "/tmp/test-root/.dendra/agents/palm.poke"
	if writtenPath != wantPath {
		t.Errorf("writtenPath = %q, want %q", writtenPath, wantPath)
	}

	// Verify file content
	if string(writtenData) != "Hey, you okay?" {
		t.Errorf("writtenData = %q, want %q", string(writtenData), "Hey, you okay?")
	}

	// Verify permissions
	if writtenPerm != 0644 {
		t.Errorf("writtenPerm = %o, want %o", writtenPerm, 0644)
	}

	// Verify stdout confirmation
	output := stdout.String()
	if !strings.Contains(output, "[poke]") {
		t.Errorf("output should contain [poke], got: %q", output)
	}
	if !strings.Contains(output, "palm") {
		t.Errorf("output should contain agent name, got: %q", output)
	}
}

func TestRunPoke_MissingDendraRoot(t *testing.T) {
	deps := &pokeDeps{
		getenv:    func(string) string { return "" },
		writeFile: func(string, []byte, fs.FileMode) error { return nil },
		stdout:    &bytes.Buffer{},
	}

	err := runPoke(deps, "palm", "hello")
	if err == nil {
		t.Fatal("expected error when DENDRA_ROOT is empty")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestRunPoke_WriteFileFails(t *testing.T) {
	deps := &pokeDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return "/tmp/test-root"
			}
			return ""
		},
		writeFile: func(string, []byte, fs.FileMode) error {
			return fs.ErrPermission
		},
		stdout: &bytes.Buffer{},
	}

	err := runPoke(deps, "palm", "hello")
	if err == nil {
		t.Fatal("expected error when writeFile fails")
	}
}
