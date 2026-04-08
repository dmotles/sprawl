package cmd

import (
	"strings"
	"testing"
)

func TestRunBashScript_Success(t *testing.T) {
	out, err := runBashScript("echo hello", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("output should contain 'hello', got: %q", string(out))
	}
}

func TestRunBashScript_Failure_ReturnsOutput(t *testing.T) {
	out, err := runBashScript("echo fail-output && exit 1", "", nil)
	if err == nil {
		t.Fatal("expected error for failing script")
	}
	if !strings.Contains(string(out), "fail-output") {
		t.Errorf("output should contain 'fail-output', got: %q", string(out))
	}
}

func TestRunBashScript_SetsWorkDir(t *testing.T) {
	tmpDir := t.TempDir()
	out, err := runBashScript("pwd", tmpDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), tmpDir) {
		t.Errorf("output should contain tmpDir %q, got: %q", tmpDir, string(out))
	}
}

func TestRunBashScript_SetsEnvVars(t *testing.T) {
	env := map[string]string{"MY_VAR": "test-value"}
	out, err := runBashScript("echo $MY_VAR", "", env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "test-value") {
		t.Errorf("output should contain 'test-value', got: %q", string(out))
	}
}

func TestRunBashScript_BashE_StopsOnError(t *testing.T) {
	script := "false\necho should-not-reach"
	out, err := runBashScript(script, "", nil)
	if err == nil {
		t.Fatal("expected error for script with failing command")
	}
	if strings.Contains(string(out), "should-not-reach") {
		t.Errorf("output should NOT contain 'should-not-reach' (bash -e should stop), got: %q", string(out))
	}
}
