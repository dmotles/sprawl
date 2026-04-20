package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/host"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/sprawlmcp"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tui"
	"github.com/spf13/cobra"
)

// enterDeps holds dependencies for the enter command, enabling testability.
type enterDeps struct {
	getenv        func(string) string
	getwd         func() (string, error)
	runProgram    func(tea.Model) error
	newSession    func(sprawlRoot string, forceFresh bool) (*tui.Bridge, bool, error)
	newSupervisor func(sprawlRoot string) supervisor.Supervisor
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
		getwd:  os.Getwd,
		runProgram: func(model tea.Model) error {
			p := tea.NewProgram(model)

			// Catch SIGTERM/SIGHUP and forward as quit to the Bubble Tea program.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
			go func() {
				if _, ok := <-sigCh; ok {
					p.Quit()
				}
			}()
			defer signal.Stop(sigCh)

			_, err := p.Run()
			return err
		},
		newSession: defaultNewSession,
		newSupervisor: func(sprawlRoot string) supervisor.Supervisor {
			return supervisor.NewReal(supervisor.Config{
				SprawlRoot: sprawlRoot,
				CallerName: "enter",
			})
		},
	}
}

// sprawlOpsMCPTools returns the Claude-addressable tool names for the in-process
// sprawl-ops MCP server. Keep in sync with the definitions in
// internal/sprawlmcp/tools.go.
func sprawlOpsMCPTools() []string {
	return []string{
		"mcp__sprawl-ops__sprawl_spawn",
		"mcp__sprawl-ops__sprawl_status",
		"mcp__sprawl-ops__sprawl_delegate",
		"mcp__sprawl-ops__sprawl_message",
		"mcp__sprawl-ops__sprawl_merge",
		"mcp__sprawl-ops__sprawl_retire",
		"mcp__sprawl-ops__sprawl_kill",
	}
}

// buildEnterClaudeArgs returns the argv for the Claude subprocess launched by
// `sprawl enter`. Combines the stream-json flags with the same tool whitelist
// the tmux root loop applies (rootinit.RootTools) plus the sprawl-ops MCP
// tool names.
func buildEnterClaudeArgs() []string {
	allowed := make([]string, 0, len(rootinit.RootTools)+len(sprawlOpsMCPTools()))
	allowed = append(allowed, rootinit.RootTools...)
	allowed = append(allowed, sprawlOpsMCPTools()...)

	opts := claude.LaunchOpts{
		Print:           true,
		InputFormat:     "stream-json",
		OutputFormat:    "stream-json",
		Verbose:         true,
		PermissionMode:  "bypassPermissions",
		AllowedTools:    allowed,
		DisallowedTools: rootinit.DisallowedTools,
	}
	return opts.BuildArgs()
}

// buildSessionEnv returns the environment variables for the Claude Code subprocess.
func buildSessionEnv() []string {
	return append(os.Environ(),
		"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
		"SPRAWL_AGENT_IDENTITY=weave",
	)
}

// defaultNewSession launches a Claude Code subprocess and returns a Bridge.
//
// When forceFresh is true, Prepare's resume decision is bypassed and a fresh
// session is built. Callers use this for resume-failure fallback — the TUI's
// restartFunc forces fresh if the previous subprocess was a resume attempt
// that exited within resumeFailureWindow.
//
// Returns (bridge, wasResume, error). wasResume indicates whether Prepare
// took the resume path; callers use it to decide if a fast exit warrants a
// force-fresh retry.
func defaultNewSession(sprawlRoot string, forceFresh bool) (*tui.Bridge, bool, error) {
	return newSessionImpl(sprawlRoot, forceFresh, rootinit.DefaultDeps(), os.Stderr)
}

// newSessionImpl is the testable body of defaultNewSession.
func newSessionImpl(sprawlRoot string, forceFresh bool, rinitDeps *rootinit.Deps, logW io.Writer) (*tui.Bridge, bool, error) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return nil, false, fmt.Errorf("finding claude binary: %w", err)
	}

	rootName := "weave"

	// Phase A: decide between resume and fresh paths. Writes last-session-id
	// and (on fresh path) persists SYSTEM.md to disk. The TUI does not yet
	// consume SYSTEM.md via --system-prompt-file (Phase 3); we still inject
	// the system prompt through the SDK initialize message on fresh paths.
	var prepared *rootinit.PreparedSession
	if forceFresh {
		prepared, err = rootinit.PrepareFresh(context.Background(), rinitDeps, rootinit.ModeTUI, sprawlRoot, rootName, logW)
	} else {
		prepared, err = rootinit.Prepare(context.Background(), rinitDeps, rootinit.ModeTUI, sprawlRoot, rootName, logW)
	}
	if err != nil {
		return nil, false, fmt.Errorf("preparing session: %w", err)
	}

	// Claude subprocess args. Resume short-circuits --session-id and any
	// --system-prompt-file (enforced by LaunchOpts.BuildArgs); on fresh we
	// pass --session-id so the transcript Claude writes matches what we
	// persisted to last-session-id, enabling resume on next launch.
	allowed := make([]string, 0, len(rootinit.RootTools)+len(sprawlOpsMCPTools()))
	allowed = append(allowed, rootinit.RootTools...)
	allowed = append(allowed, sprawlOpsMCPTools()...)
	opts := claude.LaunchOpts{
		Print:           true,
		InputFormat:     "stream-json",
		OutputFormat:    "stream-json",
		Verbose:         true,
		PermissionMode:  "bypassPermissions",
		AllowedTools:    allowed,
		DisallowedTools: rootinit.DisallowedTools,
		SessionID:       prepared.SessionID,
		Resume:          prepared.Resume,
	}
	args := opts.BuildArgs()

	// System prompt: only inject on fresh paths. On resume, Claude restores
	// the original system prompt from the resumed transcript; sending one
	// again would duplicate it.
	var systemPrompt string
	if prepared.Resume {
		fmt.Fprintf(logW, "[enter] resuming session %s\n", prepared.SessionID)
	} else {
		contextBlob, _ := memory.BuildContextBlob(sprawlRoot, rootName)
		systemPrompt = agent.BuildRootPrompt(agent.PromptConfig{
			RootName:    rootName,
			AgentCLI:    "claude-code",
			ContextBlob: contextBlob,
			Mode:        "tui",
		})
		fmt.Fprintf(logW, "[enter] starting session %s\n", prepared.SessionID)
	}

	cmd := exec.Command(claudePath, args...) //nolint:gosec // claudePath is from LookPath, not untrusted input
	cmd.Dir = sprawlRoot
	cmd.Stderr = os.Stderr
	cmd.Env = buildSessionEnv()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("starting claude: %w", err)
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

	// Create supervisor and MCP server
	sup := supervisor.NewReal(supervisor.Config{
		SprawlRoot: sprawlRoot,
		CallerName: rootName,
	})
	mcpServer := sprawlmcp.New(sup)
	mcpBridge := host.NewMCPBridge()
	mcpBridge.Register("sprawl-ops", mcpServer)

	session := host.NewSession(transport, host.SessionConfig{
		SystemPrompt:   systemPrompt,
		MCPServerNames: []string{"sprawl-ops"},
		MCPBridge:      mcpBridge,
	})

	ctx := context.Background()
	bridge := tui.NewBridge(ctx, session)
	return bridge, prepared.Resume, nil
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
		cwd, err := deps.getwd()
		if err != nil {
			return fmt.Errorf("SPRAWL_ROOT not set and cannot determine working directory: %w", err)
		}
		sprawlRoot = cwd
		fmt.Fprintf(os.Stderr, "SPRAWL_ROOT not set — defaulting to %s\n", sprawlRoot)
	}

	// Single-weave invariant: acquire the flock before any init work, and
	// hold it for the lifetime of the sprawl enter process (including
	// across TUI-driven session restarts). Released on return.
	lock, err := rootinit.AcquireWeaveLock(sprawlRoot)
	if err != nil {
		printWeaveLockError(os.Stderr, err, deps.getenv("SPRAWL_NAMESPACE"), sprawlRoot)
		return err
	}
	defer func() { _ = lock.Release() }()

	accentColor := state.ReadAccentColor(sprawlRoot)
	repoName := filepath.Base(sprawlRoot)
	version := state.ReadVersion(sprawlRoot)
	if version == "" {
		version = buildVersion
	}

	// Track last session's start time + whether it was a resume attempt so
	// that the TUI's restartFunc can fall back to a fresh session if a
	// resume attempt died within resumeFailureWindow.
	var lastStartedAt time.Time
	var lastWasResume bool

	var bridge *tui.Bridge
	if deps.newSession != nil {
		var err error
		var wasResume bool
		bridge, wasResume, err = deps.newSession(sprawlRoot, false)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		lastStartedAt = time.Now()
		lastWasResume = wasResume
	}

	// Create a supervisor for the tree panel to poll agent status.
	var sup supervisor.Supervisor
	if deps.newSupervisor != nil {
		sup = deps.newSupervisor(sprawlRoot)
	}
	var restartFunc func() (*tui.Bridge, error)
	if deps.newSession != nil {
		restartFunc = func() (*tui.Bridge, error) {
			forceFresh := lastWasResume && time.Since(lastStartedAt) < resumeFailureWindow
			if forceFresh {
				fmt.Fprintf(os.Stderr, "[enter] resume died fast — falling back to fresh session\n")
			}
			newBridge, wasResume, err := deps.newSession(sprawlRoot, forceFresh)
			if err == nil {
				bridge = newBridge // update so runEnter closes the latest bridge
				lastStartedAt = time.Now()
				lastWasResume = wasResume
			}
			return newBridge, err
		}
	}
	model := tui.NewAppModel(accentColor, repoName, version, bridge, sup, sprawlRoot, restartFunc)
	err = deps.runProgram(model)

	// Graceful shutdown: stop all agents with a timeout.
	if sup != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		agents, statusErr := sup.Status(shutdownCtx)
		if statusErr == nil {
			for _, a := range agents {
				fmt.Fprintf(os.Stderr, "Stopping agent %s...\n", a.Name)
				_ = sup.Kill(shutdownCtx, a.Name)
				fmt.Fprintf(os.Stderr, "  %s -> stopped\n", a.Name)
			}
		}
		_ = sup.Shutdown(shutdownCtx)
	}

	if bridge != nil {
		_ = bridge.Close()
	}

	if err != nil {
		return fmt.Errorf("TUI exited with error: %w", err)
	}

	fmt.Fprintln(os.Stderr, "TUI session ended.")
	return nil
}
