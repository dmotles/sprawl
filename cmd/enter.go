package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/host"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tui"
	"github.com/spf13/cobra"
)

// enterDeps holds dependencies for the enter command, enabling testability.
type enterDeps struct {
	getenv     func(string) string
	runProgram func(tea.Model) error
	newSession func(sprawlRoot string) (*tui.Bridge, error)
}

var defaultEnterDeps *enterDeps

func init() {
	rootCmd.AddCommand(enterCmd)
}

var enterCmd = &cobra.Command{
	Use:   "enter",
	Short: "Launch the TUI dashboard",
	Long:  "Launch a fullscreen terminal UI for monitoring and interacting with agents. Works in any terminal — no tmux required.",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveEnterDeps()
		return runEnter(deps)
	},
}

func resolveEnterDeps() *enterDeps {
	if defaultEnterDeps != nil {
		return defaultEnterDeps
	}

	return &enterDeps{
		getenv: os.Getenv,
		runProgram: func(model tea.Model) error {
			p := tea.NewProgram(model)
			_, err := p.Run()
			return err
		},
		newSession: defaultNewSession,
	}
}

// defaultNewSession launches a Claude Code subprocess and returns a Bridge.
func defaultNewSession(sprawlRoot string) (*tui.Bridge, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("finding claude binary: %w", err)
	}

	// Build system prompt using the same pattern as rootloop.go.
	rootName := "enter"
	contextBlob, _ := memory.BuildContextBlob(sprawlRoot, rootName)
	systemPrompt := agent.BuildRootPrompt(agent.PromptConfig{
		RootName:    rootName,
		AgentCLI:    "claude-code",
		ContextBlob: contextBlob,
	})

	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
	}

	cmd := exec.Command(claudePath, args...) //nolint:gosec // claudePath is from LookPath, not untrusted input
	cmd.Dir = sprawlRoot
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	reader := protocol.NewReader(stdout)
	writer := protocol.NewWriter(stdin)
	transport := &enterTransport{
		reader: reader,
		writer: writer,
		kill: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
	}

	session := host.NewSession(transport, host.SessionConfig{
		SystemPrompt: systemPrompt,
	})

	ctx := context.Background()
	bridge := tui.NewBridge(ctx, session)
	return bridge, nil
}

// enterTransport wraps a Claude Code subprocess for the host protocol.
type enterTransport struct {
	reader *protocol.Reader
	writer *protocol.Writer
	kill   func() error
}

func (t *enterTransport) Send(_ context.Context, msg any) error {
	return t.writer.WriteJSON(msg)
}

func (t *enterTransport) Recv(_ context.Context) (*protocol.Message, error) {
	return t.reader.Next()
}

func (t *enterTransport) Close() error {
	closeErr := t.writer.Close()
	if closeErr != nil {
		_ = t.kill()
		return closeErr
	}
	_ = t.kill()
	return nil
}

func runEnter(deps *enterDeps) error {
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set — run 'sprawl init' first")
	}

	accentColor := state.ReadAccentColor(sprawlRoot)
	repoName := filepath.Base(sprawlRoot)
	version := state.ReadVersion(sprawlRoot)
	if version == "" {
		version = buildVersion
	}

	var bridge *tui.Bridge
	if deps.newSession != nil {
		var err error
		bridge, err = deps.newSession(sprawlRoot)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
	}

	model := tui.NewAppModel(accentColor, repoName, version, bridge)
	err := deps.runProgram(model)

	if bridge != nil {
		_ = bridge.Close()
	}

	if err != nil {
		return fmt.Errorf("TUI exited with error: %w", err)
	}

	fmt.Fprintln(os.Stderr, "TUI session ended.")
	return nil
}
