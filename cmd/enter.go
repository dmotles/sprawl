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
	getenv          func(string) string
	getwd           func() (string, error)
	runProgram      func(tea.Model) error
	newSession      func(sprawlRoot string, forceFresh bool) (*tui.Bridge, bool, error)
	newSupervisor   func(sprawlRoot string) supervisor.Supervisor
	finalizeHandoff func(ctx context.Context, sprawlRoot string, stdout io.Writer) error
}

// restartState holds the rolling state tracked across restarts: when the last
// subprocess was started and whether it was launched via --resume. Used by the
// restart function to decide whether a resume attempt died fast enough to
// warrant a force-fresh fallback.
type restartState struct {
	lastStartedAt time.Time
	lastWasResume bool
}

// makeRestartFunc builds the closure that the TUI calls when it wants a fresh
// subprocess (after a crash, EOF, or /handoff).
//
// state is mutated in-place on each call; pass the same pointer across the
// lifetime of runEnter. bridgeOut, if non-nil, is updated on success so
// runEnter closes the latest bridge on shutdown.
func makeRestartFunc(
	newSession func(sprawlRoot string, forceFresh bool) (*tui.Bridge, bool, error),
	finalize func(ctx context.Context, sprawlRoot string, stdout io.Writer) error,
	sprawlRoot string,
	state *restartState,
	bridgeOut **tui.Bridge,
	logW io.Writer,
) func() (*tui.Bridge, error) {
	return func() (*tui.Bridge, error) {
		forceFresh := state.lastWasResume && time.Since(state.lastStartedAt) < resumeFailureWindow
		if forceFresh {
			fmt.Fprintf(logW, "[enter] resume died fast — falling back to fresh session\n")
		}
		// Phase D: run post-session housekeeping (consolidation when a handoff
		// signal is present, otherwise a noop). Errors are logged and do not
		// block the restart — matches cmd/rootloop.go's tmux-mode behavior.
		if finalize != nil {
			if err := finalize(context.Background(), sprawlRoot, logW); err != nil {
				fmt.Fprintf(logW, "[enter] finalize handoff failed: %v\n", err)
			}
		}
		newBridge, wasResume, err := newSession(sprawlRoot, forceFresh)
		if err == nil {
			if bridgeOut != nil {
				*bridgeOut = newBridge
			}
			state.lastStartedAt = time.Now()
			state.lastWasResume = wasResume
		}
		return newBridge, err
	}
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
		finalizeHandoff: func(ctx context.Context, sprawlRoot string, stdout io.Writer) error {
			return rootinit.FinalizeHandoff(ctx, rootinit.DefaultDeps(), sprawlRoot, stdout)
		},
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

// buildEnterLaunchOpts constructs claude.LaunchOpts for the TUI-mode weave
// subprocess from a rootinit.PreparedSession. Matches the tmux root loop's
// launch shape (--system-prompt-file, --session-id, --model opus[1m], plus
// the root tool allowlist) and adds the stream-json flag block the TUI
// transport needs.
//
// On resume (prepared.Resume == true), --system-prompt-file and --session-id
// are intentionally omitted — BuildArgs enforces this because the resumed
// transcript carries its own session ID and system prompt.
func buildEnterLaunchOpts(prepared *rootinit.PreparedSession) claude.LaunchOpts {
	allowed := make([]string, 0, len(prepared.RootTools)+len(sprawlOpsMCPTools()))
	allowed = append(allowed, prepared.RootTools...)
	allowed = append(allowed, sprawlOpsMCPTools()...)

	opts := claude.LaunchOpts{
		Print:           true,
		InputFormat:     "stream-json",
		OutputFormat:    "stream-json",
		Verbose:         true,
		PermissionMode:  "bypassPermissions",
		AllowedTools:    allowed,
		DisallowedTools: prepared.Disallowed,
		Model:           prepared.Model,
		SessionID:       prepared.SessionID,
	}
	if prepared.Resume {
		opts.Resume = true
	} else {
		opts.SystemPromptFile = prepared.PromptPath
	}
	return opts
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
	// and (on fresh path) persists SYSTEM.md to disk and renders the system
	// prompt. The Claude subprocess consumes the rendered prompt via
	// --system-prompt-file below.
	var prepared *rootinit.PreparedSession
	if forceFresh {
		prepared, err = rootinit.PrepareFresh(context.Background(), rinitDeps, rootinit.ModeTUI, sprawlRoot, rootName, logW)
	} else {
		prepared, err = rootinit.Prepare(context.Background(), rinitDeps, rootinit.ModeTUI, sprawlRoot, rootName, logW)
	}
	if err != nil {
		return nil, false, fmt.Errorf("preparing session: %w", err)
	}

	args := buildEnterLaunchOpts(prepared).BuildArgs()

	if prepared.Resume {
		fmt.Fprintf(logW, "[enter] resuming session %s\n", prepared.SessionID)
	} else {
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
	state := &restartState{}

	var bridge *tui.Bridge
	if deps.newSession != nil {
		var err error
		var wasResume bool
		bridge, wasResume, err = deps.newSession(sprawlRoot, false)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		state.lastStartedAt = time.Now()
		state.lastWasResume = wasResume
	}

	// Create a supervisor for the tree panel to poll agent status.
	var sup supervisor.Supervisor
	if deps.newSupervisor != nil {
		sup = deps.newSupervisor(sprawlRoot)
	}
	var restartFunc func() (*tui.Bridge, error)
	if deps.newSession != nil {
		restartFunc = makeRestartFunc(deps.newSession, deps.finalizeHandoff, sprawlRoot, state, &bridge, os.Stderr)
	}
	model := tui.NewAppModel(accentColor, repoName, version, bridge, sup, sprawlRoot, restartFunc)

	if bridge != nil && state.lastWasResume {
		if sessionID, sErr := memory.ReadLastSessionID(sprawlRoot); sErr == nil && sessionID != "" {
			if homeDir, hErr := os.UserHomeDir(); hErr == nil {
				path := memory.SessionLogPath(homeDir, sprawlRoot, sessionID)
				entries, lErr := tui.LoadTranscript(path, tui.ReplayMaxMessages)
				if lErr != nil {
					fmt.Fprintf(os.Stderr, "[enter] transcript replay failed: %v (continuing with empty viewport)\n", lErr)
				} else if len(entries) > 0 {
					model.PreloadTranscript(entries)
				}
			}
		}
	}

	err = deps.runProgram(model)

	// Phase D on clean shutdown: if the TUI exited normally (e.g. the user
	// quit via the Ctrl-C confirm dialog after `/handoff`), run the
	// consolidation pipeline one last time so the final session's handoff
	// signal (if any) is consumed. Skipped on TUI crash — we don't want to
	// consolidate off the back of a broken transcript.
	if err == nil && deps.finalizeHandoff != nil {
		if finErr := deps.finalizeHandoff(context.Background(), sprawlRoot, os.Stderr); finErr != nil {
			fmt.Fprintf(os.Stderr, "[enter] finalize handoff on shutdown failed: %v\n", finErr)
		}
	}

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
