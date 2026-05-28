package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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
		findBinary: resolveClaudeBinary,
		cmdFactory: exec.CommandContext,
	}
}

// resolveClaudeBinary locates the claude binary. If $SPRAWL_CLAUDE is set and
// non-empty it is used verbatim (typically the scripts/run-claude auth shim
// that re-hydrates CLAUDE_CODE_OAUTH_TOKEN — see CLAUDE.md and QUM-518); the
// path must exist. Otherwise it falls back to PATH lookup. Mirrors
// internal/agent/claude.go's RealLauncher.FindBinary so the memory
// regenerate/consolidate paths honor the same override as the rest of sprawl.
func resolveClaudeBinary() (string, error) {
	if override := os.Getenv("SPRAWL_CLAUDE"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("SPRAWL_CLAUDE=%q: %w", override, err)
		}
		return override, nil
	}
	return exec.LookPath("claude")
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
