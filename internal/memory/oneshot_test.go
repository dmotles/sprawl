package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestHelperProcess is the subprocess entry point used by the helper-process
// pattern. It is not a real test; it exits immediately when the sentinel env
// var is absent.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	// If HELPER_ECHO_STDIN is set, read stdin and write it to stdout.
	if os.Getenv("HELPER_ECHO_STDIN") == "1" {
		buf := make([]byte, 1<<16)
		n, _ := os.Stdin.Read(buf)
		fmt.Fprint(os.Stdout, string(buf[:n]))
		os.Exit(0)
	}

	if s := os.Getenv("HELPER_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("HELPER_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}

	code := 0
	if s := os.Getenv("HELPER_EXIT_CODE"); s != "" {
		code, _ = strconv.Atoi(s)
	}
	os.Exit(code)
}

// helperCmdFactory returns a cmdFactory that spawns the test binary itself as
// a subprocess, running TestHelperProcess with the given environment overrides.
func helperCmdFactory(env map[string]string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcess", "--"}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		return cmd
	}
}

func TestCLIInvoker_HappyPath(t *testing.T) {
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: helperCmdFactory(map[string]string{
			"HELPER_STDOUT": "  hello world  \n",
		}),
	}

	got, err := inv.Invoke(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestCLIInvoker_NonZeroExit(t *testing.T) {
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: helperCmdFactory(map[string]string{
			"HELPER_EXIT_CODE": "1",
			"HELPER_STDERR":    "something went wrong",
		}),
	}

	_, err := inv.Invoke(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error should contain stderr, got: %v", err)
	}
}

func TestCLIInvoker_Timeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already expired

	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: helperCmdFactory(map[string]string{
			"HELPER_STDOUT": "should not matter",
		}),
	}

	_, err := inv.Invoke(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected error for expired context")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected error to wrap ErrTimeout, got: %v", err)
	}
}

func TestCLIInvoker_EmptyResponse(t *testing.T) {
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: helperCmdFactory(map[string]string{
			"HELPER_STDOUT": "   \n\t  ",
		}),
	}

	_, err := inv.Invoke(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error for empty response")
	}
	if !errors.Is(err, ErrEmptyResponse) {
		t.Errorf("expected error to wrap ErrEmptyResponse, got: %v", err)
	}
}

func TestCLIInvoker_WithModel(t *testing.T) {
	var capturedArgs []string
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedArgs = args
			// Delegate to the helper process so the command actually runs.
			return helperCmdFactory(map[string]string{
				"HELPER_STDOUT": "ok",
			})(ctx, name, args...)
		},
	}

	_, err := inv.Invoke(context.Background(), "test prompt", WithModel("opus-4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for i, a := range capturedArgs {
		if a == "--model" && i+1 < len(capturedArgs) && capturedArgs[i+1] == "opus-4" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected args to contain --model opus-4, got %v", capturedArgs)
	}
}

func TestCLIInvoker_BinaryNotFound(t *testing.T) {
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "", fmt.Errorf("exec: \"claude\": executable file not found in $PATH") },
		cmdFactory: helperCmdFactory(nil),
	}

	_, err := inv.Invoke(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected error when binary not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %v", err)
	}
}

func TestCLIInvoker_StderrDiscardedOnSuccess(t *testing.T) {
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: helperCmdFactory(map[string]string{
			"HELPER_STDOUT": "real output",
			"HELPER_STDERR": "debug noise",
		}),
	}

	got, err := inv.Invoke(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "real output" {
		t.Errorf("got %q, want %q", got, "real output")
	}
	if strings.Contains(got, "debug noise") {
		t.Error("stderr content should not appear in returned output")
	}
}

func TestCLIInvoker_PromptPassedViaStdin(t *testing.T) {
	inv := &CLIInvoker{
		findBinary: func() (string, error) { return "/usr/bin/claude", nil },
		cmdFactory: helperCmdFactory(map[string]string{
			"HELPER_ECHO_STDIN": "1",
		}),
	}

	prompt := "summarize the state of the world"
	got, err := inv.Invoke(context.Background(), prompt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != prompt {
		t.Errorf("stdin echo = %q, want %q", got, prompt)
	}
}

func TestMockClaudeInvoker(t *testing.T) {
	// Demonstrate that the ClaudeInvoker interface can be trivially mocked
	// for consumer tests that depend on it.
	mock := &mockClaudeInvoker{
		response: "mocked response",
		err:      nil,
	}

	var invoker ClaudeInvoker = mock
	got, err := invoker.Invoke(context.Background(), "any prompt", WithModel("fast"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "mocked response" {
		t.Errorf("got %q, want %q", got, "mocked response")
	}
	if mock.lastPrompt != "any prompt" {
		t.Errorf("lastPrompt = %q, want %q", mock.lastPrompt, "any prompt")
	}
	if len(mock.lastOpts) != 1 {
		t.Errorf("expected 1 option, got %d", len(mock.lastOpts))
	}

	// Verify error propagation
	mock.err = fmt.Errorf("api unavailable")
	_, err = invoker.Invoke(context.Background(), "will fail")
	if err == nil {
		t.Fatal("expected error from mock")
	}
	if err.Error() != "api unavailable" {
		t.Errorf("error = %q, want %q", err.Error(), "api unavailable")
	}
}

// mockClaudeInvoker is a minimal mock of ClaudeInvoker for consumer tests.
type mockClaudeInvoker struct {
	response   string
	err        error
	lastPrompt string
	lastOpts   []InvokeOption
}

func (m *mockClaudeInvoker) Invoke(_ context.Context, prompt string, opts ...InvokeOption) (string, error) {
	m.lastPrompt = prompt
	m.lastOpts = opts
	return m.response, m.err
}
