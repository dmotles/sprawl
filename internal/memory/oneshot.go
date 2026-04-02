package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

var (
	// ErrEmptyResponse is returned when Claude produces no output.
	ErrEmptyResponse = errors.New("claude returned empty response")
	// ErrTimeout is returned when the context deadline is exceeded.
	ErrTimeout = errors.New("claude invocation timed out")
)

// ClaudeInvoker runs a one-shot Claude subprocess and returns the output.
type ClaudeInvoker interface {
	Invoke(ctx context.Context, prompt string, opts ...InvokeOption) (string, error)
}

// InvokeOption configures a single Invoke call.
type InvokeOption func(*invokeConfig)

type invokeConfig struct {
	model string
}

// WithModel sets the model to use for the invocation.
func WithModel(model string) InvokeOption {
	return func(c *invokeConfig) { c.model = model }
}

// CLIInvoker implements ClaudeInvoker by shelling out to `claude -p`.
type CLIInvoker struct {
	findBinary func() (string, error)
	cmdFactory func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewCLIInvoker returns a CLIInvoker that uses the real claude binary.
func NewCLIInvoker() *CLIInvoker {
	return &CLIInvoker{
		findBinary: func() (string, error) { return exec.LookPath("claude") },
		cmdFactory: exec.CommandContext,
	}
}

// Invoke runs claude -p with the given prompt on stdin and returns the response.
func (c *CLIInvoker) Invoke(ctx context.Context, prompt string, opts ...InvokeOption) (string, error) {
	var cfg invokeConfig
	for _, o := range opts {
		o(&cfg)
	}

	binaryPath, err := c.findBinary()
	if err != nil {
		return "", fmt.Errorf("finding claude binary: %w", err)
	}

	args := []string{"-p"}
	if cfg.model != "" {
		args = append(args, "--model", cfg.model)
	}

	cmd := c.cmdFactory(ctx, binaryPath, args...)
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
		}
		log.Printf("claude stderr: %s", stderr.String())
		return "", fmt.Errorf("claude exited with error: %w (stderr: %s)", err, stderr.String())
	}

	result := strings.TrimSpace(stdout.String())
	if result == "" {
		return "", fmt.Errorf("%w", ErrEmptyResponse)
	}

	return result, nil
}
