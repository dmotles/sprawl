package agentloop

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/dmotles/dendra/internal/protocol"
)

// RealCommandStarter launches a real Claude Code subprocess.
type RealCommandStarter struct{}

// buildClaudeArgs constructs the CLI argument slice for a Claude Code subprocess.
func buildClaudeArgs(config ProcessConfig) []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--model", "opus[1m]",
		"--effort", "medium",
		"--permission-mode", "bypassPermissions",
		"--session-id", config.SessionID,
	}

	if config.SystemPromptFile != "" {
		args = append(args, "--system-prompt-file", config.SystemPromptFile)
	} else if config.SystemPrompt != "" {
		args = append(args, "--system-prompt", config.SystemPrompt)
	}
	if config.Resume {
		args = append(args, "--resume", config.SessionID)
	}
	if config.SettingSources != "" {
		args = append(args, "--setting-sources", config.SettingSources)
	}
	return args
}

// Start builds the CLI args, launches the subprocess, and returns I/O handles.
func (s *RealCommandStarter) Start(ctx context.Context, config ProcessConfig) (MessageReader, MessageWriter, WaitFunc, CancelFunc, error) {
	claudePath := config.ClaudePath
	if claudePath == "" {
		claudePath = "claude"
	}

	args := buildClaudeArgs(config)

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = config.WorkDir

	// Build environment.
	env := os.Environ()
	env = append(env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
	if config.AgentName != "" {
		env = append(env, fmt.Sprintf("DENDRA_AGENT_IDENTITY=%s", config.AgentName))
	}
	if config.DendraRoot != "" {
		env = append(env, fmt.Sprintf("DENDRA_ROOT=%s", config.DendraRoot))
	}
	for k, v := range config.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("starting command: %w", err)
	}

	reader := protocol.NewReader(stdout)
	writer := protocol.NewWriter(stdin)

	waitFn := func() error {
		return cmd.Wait()
	}

	cancelFn := func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}

	return reader, writer, waitFn, cancelFn, nil
}
