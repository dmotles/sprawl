package agentloop

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/dmotles/sprawl/internal/procutil"
	"github.com/dmotles/sprawl/internal/protocol"
)

// RealCommandStarter launches a real Claude Code subprocess.
type RealCommandStarter struct{}

// buildClaudeCmd constructs the *exec.Cmd for launching the claude subprocess.
// Extracted as a seam (QUM-458) so tests can assert SysProcAttr wiring without
// invoking Start(). The real implementer wires procutil.SetPdeathsig here.
func buildClaudeCmd(ctx context.Context, config ProcessConfig) *exec.Cmd {
	claudePath := config.ClaudePath
	if claudePath == "" {
		claudePath = "claude"
	}
	args := config.Args.BuildArgs()
	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = config.WorkDir
	// QUM-458: ensure claude dies if sprawl enter is SIGKILL'd, and runs in its
	// own process group so cancelFn can kill the whole tree.
	procutil.SetPdeathsig(cmd)
	return cmd
}

// Start builds the CLI args, launches the subprocess, and returns I/O handles.
func (s *RealCommandStarter) Start(ctx context.Context, config ProcessConfig) (MessageReader, MessageWriter, WaitFunc, CancelFunc, error) {
	cmd := buildClaudeCmd(ctx, config)

	// Build environment.
	env := os.Environ()
	env = append(env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
	if config.AgentName != "" {
		env = append(env, fmt.Sprintf("SPRAWL_AGENT_IDENTITY=%s", config.AgentName))
	}
	if config.SprawlRoot != "" {
		env = append(env, fmt.Sprintf("SPRAWL_ROOT=%s", config.SprawlRoot))
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
			return procutil.KillProcessGroup(cmd.Process)
		}
		return nil
	}

	return reader, writer, waitFn, cancelFn, nil
}
