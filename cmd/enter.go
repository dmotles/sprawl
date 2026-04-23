// Package cmd / enter.go — TUI-mode root weave launcher.
//
// Architectural contract: ONE supervisor per `sprawl enter` invocation.
// The single `supervisor.Supervisor` built in runEnter is shared across
// three call sites that must observe the same in-process state:
//
//  1. the tree-panel status poller (tickAgentsCmd in internal/tui),
//  2. the HandoffRequested() channel listener goroutine (see runEnter),
//  3. the MCP server wired to the claude subprocess (sprawlmcp.New).
//
// Do NOT create a second supervisor inside newSessionImpl or anywhere
// else in this package. If two supervisors exist, the sprawl_handoff MCP
// tool fires on one channel while the TUI listens on the other, the
// teardown/restart never runs, and the user is stuck in a stale session.
// See QUM-329 postmortem for the full failure mode and tests that guard
// against regression.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/host"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/messages"
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
	getenv     func(string) string
	getwd      func() (string, error)
	runProgram func(model tea.Model, onStart func(sender func(tea.Msg))) error
	// newSession launches a Claude Code subprocess and wires the TUI bridge.
	// onResumeFailure, if non-nil, is invoked from the subprocess's stderr
	// scanner when the "No conversation found" marker trips — used by the
	// restart loop to force-fresh the next session regardless of elapsed time.
	// sup is the runEnter-scoped supervisor (see architectural contract at
	// top of this file). It is passed to the MCP server inside
	// newSessionImpl so the sprawl_handoff tool and the TUI
	// HandoffRequested() listener observe the same channel. QUM-329.
	newSession      func(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (*tui.Bridge, bool, error)
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
	// resumeMarkerTripped is set by the subprocess stderr scanner when the
	// "No conversation found with session ID:" marker fires (QUM-261). The
	// next restart consumes and clears it to force a fresh session even when
	// claude stayed alive past resumeFailureWindow before the marker arrived.
	resumeMarkerTripped atomic.Bool
}

// makeRestartFunc builds the closure that the TUI calls when it wants a fresh
// subprocess (after a crash, EOF, or /handoff).
//
// state is mutated in-place on each call; pass the same pointer across the
// lifetime of runEnter. bridgeOut, if non-nil, is updated on success so
// runEnter closes the latest bridge on shutdown.
func makeRestartFunc(
	newSession func(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (*tui.Bridge, bool, error),
	sup supervisor.Supervisor,
	finalize func(ctx context.Context, sprawlRoot string, stdout io.Writer) error,
	sprawlRoot string,
	state *restartState,
	bridgeOut **tui.Bridge,
	logW io.Writer,
) func() (*tui.Bridge, error) {
	return func() (*tui.Bridge, error) {
		markerTripped := state.resumeMarkerTripped.Swap(false)
		forceFresh := markerTripped || (state.lastWasResume && time.Since(state.lastStartedAt) < resumeFailureWindow)
		switch {
		case markerTripped:
			fmt.Fprintf(logW, "[enter] resume failed (no conversation found) — falling back to fresh session\n")
		case forceFresh:
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
		onResumeFailure := func() { state.resumeMarkerTripped.Store(true) }
		newBridge, wasResume, err := newSession(sprawlRoot, sup, forceFresh, onResumeFailure)
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
		runProgram: func(model tea.Model, onStart func(sender func(tea.Msg))) error {
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

			if onStart != nil {
				onStart(func(msg tea.Msg) { p.Send(msg) })
			}

			_, err := p.Run()
			return err
		},
		newSession: defaultNewSession,
		finalizeHandoff: func(ctx context.Context, sprawlRoot string, stdout io.Writer) error {
			deps := rootinit.DefaultDeps()
			deps.LogPrefix = "[enter]"
			return rootinit.FinalizeHandoff(ctx, deps, sprawlRoot, stdout)
		},
		newSupervisor: func(sprawlRoot string) supervisor.Supervisor {
			sup, err := supervisor.NewReal(supervisor.Config{
				SprawlRoot: sprawlRoot,
				CallerName: "enter",
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[enter] supervisor unavailable: %v\n", err)
				return nil
			}
			return sup
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
		"mcp__sprawl-ops__sprawl_handoff",
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
func defaultNewSession(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (*tui.Bridge, bool, error) {
	deps := rootinit.DefaultDeps()
	deps.LogPrefix = "[enter]"
	return newSessionImpl(sprawlRoot, sup, forceFresh, deps, os.Stderr, onResumeFailure)
}

// newSessionImpl is the testable body of defaultNewSession.
//
// sup is the runEnter-scoped supervisor and MUST be the same instance that
// runEnter registers with the TUI's HandoffRequested() listener. See the
// architectural contract at the top of this file (QUM-329).
func newSessionImpl(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, rinitDeps *rootinit.Deps, logW io.Writer, onResumeFailure func()) (*tui.Bridge, bool, error) {
	if sup == nil {
		return nil, false, fmt.Errorf("newSessionImpl: supervisor must be non-nil (see QUM-329 architectural contract)")
	}
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
	cmd.Env = buildSessionEnv()

	// QUM-261: wrap stderr so the "No conversation found with session ID:"
	// marker — emitted when --resume targets an evicted session but claude
	// stays alive awaiting input — kills the subprocess AND signals
	// onResumeFailure so makeRestartFunc force-freshes the next session
	// regardless of how long claude took to emit the error.
	cmd.Stderr = wrapEnterStderrForResumeScan(logW, func() {
		if onResumeFailure != nil {
			onResumeFailure()
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

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

	// Wire the MCP server to the shared supervisor passed in by runEnter.
	// Creating a second supervisor here would give the sprawl_handoff MCP
	// tool its own HandoffRequested channel, which the TUI listener never
	// drains — the QUM-329 regression.
	mcpServer := sprawlmcp.New(sup)
	mcpBridge := host.NewMCPBridge()
	mcpBridge.Register("sprawl-ops", mcpServer)

	session := host.NewSession(transport, host.SessionConfig{
		MCPServerNames: []string{"sprawl-ops"},
		MCPBridge:      mcpBridge,
	})

	ctx := context.Background()
	bridge := tui.NewBridge(ctx, session)
	bridge.SetSessionID(prepared.SessionID)
	return bridge, prepared.Resume, nil
}

// wrapEnterStderrForResumeScan wraps the TUI-mode Claude subprocess stderr
// with a marker-aware writer. On the "No conversation found with session ID:"
// substring, kill is invoked so the subprocess exits fast enough for the
// restartFunc's resumeFailureWindow heuristic to force-fresh.
func wrapEnterStderrForResumeScan(underlying io.Writer, kill func()) io.Writer {
	return claude.NewMarkerWriter(underlying, claude.NoConversationMarker, claude.ResumeMarkerScanCap, kill)
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

	// resumeFailureCh is closed by the Claude subprocess's stderr marker
	// scanner when "No conversation found" fires. The onStart hook drains it
	// and posts RestartSessionMsg — the TUI cannot detect the dead subprocess
	// on its own because Bridge.WaitForEvent is only called after the user
	// submits a message, and Session.Initialize treats pre-init EOF as
	// success. Without this prod, the TUI would sit idle on a zombie session
	// until the user typed something. Buffered to size 1 so a second marker
	// trip in the same session is absorbed without blocking.
	resumeFailureCh := make(chan struct{}, 1)
	var resumeFailureOnce sync.Once
	onResumeFailure := func() {
		state.resumeMarkerTripped.Store(true)
		resumeFailureOnce.Do(func() { close(resumeFailureCh) })
	}

	// QUM-329: build the single supervisor BEFORE creating the session so
	// the same instance is wired into (a) the MCP server inside
	// newSession, (b) the tree-panel status poller (passed into AppModel
	// below), and (c) the HandoffRequested() listener goroutine in
	// onStart. A second supervisor would orphan the handoff signal.
	var sup supervisor.Supervisor
	if deps.newSupervisor != nil {
		sup = deps.newSupervisor(sprawlRoot)
	}

	var bridge *tui.Bridge
	if deps.newSession != nil {
		var err error
		var wasResume bool
		bridge, wasResume, err = deps.newSession(sprawlRoot, sup, false, onResumeFailure)
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		state.lastStartedAt = time.Now()
		state.lastWasResume = wasResume
	}

	var restartFunc func() (*tui.Bridge, error)
	if deps.newSession != nil {
		restartFunc = makeRestartFunc(deps.newSession, sup, deps.finalizeHandoff, sprawlRoot, state, &bridge, os.Stderr)
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

	// Shutdown signal for the forwarder goroutines; closed when the Bubble Tea
	// program returns so the goroutines can exit.
	handoffDone := make(chan struct{})
	onStart := func(send func(tea.Msg)) {
		// QUM-311: replace the process-level notifier (which in TUI mode is a
		// legacy-gated no-op — see cmd/messages_notify.go) with one that
		// dispatches an InboxArrivalMsg into the bubbletea program whenever a
		// message is delivered to weave's maildir. Every sprawl CLI invocation
		// in this process — children running MCP tools, `sprawl report done`,
		// `sprawl messages send`, and the in-process supervisor — goes through
		// messages.Send, so a single SetDefaultNotifier call covers all
		// origins of the notification.
		messages.SetDefaultNotifier(buildTUIRootNotifier("weave", send))

		// QUM-261: when the initial `--resume` subprocess trips the "No
		// conversation found" stderr marker, prod the TUI to tear down and
		// restart. Without this prod the TUI sits idle on a zombie session
		// because WaitForEvent is only dispatched after a user message.
		go func() {
			select {
			case <-handoffDone:
				return
			case <-resumeFailureCh:
				send(tui.SessionRestartingMsg{Reason: "resume failed — no conversation found"})
				send(tui.RestartSessionMsg{})
			}
		}()

		if sup == nil {
			return
		}
		ch := sup.HandoffRequested()
		if ch == nil {
			return
		}
		go func() {
			for {
				select {
				case <-handoffDone:
					return
				case _, ok := <-ch:
					if !ok {
						return
					}
					send(tui.HandoffRequestedMsg{})
				}
			}
		}()
	}

	// Redirect stderr to a log file for the lifetime of the TUI.
	// Any fmt.Fprintf(os.Stderr, ...) from Go code or stderr writes from
	// subprocesses that inherited FD 2 (notably the claude subprocess via
	// cmd.Stderr = os.Stderr) would otherwise corrupt the Bubble Tea
	// alt-screen render (QUM-304).
	var stderrRedirect *tui.StderrRedirect
	if deps.getenv("SPRAWL_TUI_NO_STDERR_REDIRECT") == "" {
		logDir := filepath.Join(sprawlRoot, ".sprawl", "logs")
		logPath := filepath.Join(logDir, fmt.Sprintf("tui-stderr-%s.log", time.Now().UTC().Format("20060102-150405")))
		var rerr error
		stderrRedirect, rerr = tui.RedirectStderr(logPath)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "[enter] warning: could not redirect stderr to %s: %v\n", logPath, rerr)
		}
		// Defensive restore: if a panic or early return skips the explicit
		// Restore below, the deferred call guarantees FD 2 is put back so the
		// user's shell isn't left with stderr pointing at a log file.
		// Restore is idempotent.
		if stderrRedirect != nil {
			defer func() { _ = stderrRedirect.Restore() }()
		}
	}

	// QUM-304 regression test hook: if the sentinel env var is set, emit it to
	// stderr after a brief delay so the TUI is fully rendered. The e2e harness
	// asserts this sentinel lands in the log file, not on the terminal.
	if sentinel := deps.getenv("SPRAWL_TUI_STDERR_LEAK_TEST"); sentinel != "" {
		go func() {
			time.Sleep(500 * time.Millisecond)
			fmt.Fprintln(os.Stderr, sentinel)
			// Also simulate subprocess inheritance of FD 2.
			c := exec.Command("sh", "-c", fmt.Sprintf("echo %s >&2", sentinel)) //nolint:gosec // sentinel comes from SPRAWL_TUI_STDERR_LEAK_TEST, a dev/test-only env var
			c.Stderr = os.Stderr
			_ = c.Run()
		}()
	}

	err = deps.runProgram(model, onStart)
	close(handoffDone)
	// QUM-311: the send function captured by the notifier closure becomes a
	// no-op once the tea.Program has exited, but be explicit and unregister
	// the TUI notifier so any lingering in-process messages.Send calls during
	// shutdown don't try to dispatch into a dead program.
	messages.SetDefaultNotifier(nil)

	// Ctrl+C / clean shutdown of the TUI does NOT run FinalizeHandoff and
	// does NOT kill child agents. Rationale:
	//   - FinalizeHandoff clears last-session-id when a handoff-signal file
	//     is present, which breaks resume-by-default (QUM-255) on the next
	//     `sprawl enter`. A stale/in-flight signal can linger across
	//     sessions; consuming it on Ctrl+C is the wrong trigger. The
	//     consolidate-then-fresh path in rootinit.Prepare will handle any
	//     pending handoff on the next launch.
	//   - Killing child agents surprises the user: they expect `sprawl
	//     enter` to be a detachable UI, not a process-group owner. The
	//     tmux/supervisor-managed agents should keep running and be
	//     reattachable via the next `sprawl enter`.
	//
	// The explicit /handoff path runs FinalizeHandoff via makeRestartFunc
	// before starting the next session — that path is unchanged.
	if sup != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sup.Shutdown(shutdownCtx)
	}

	if bridge != nil {
		_ = bridge.Close()
	}

	// Restore stderr after supervisor/bridge shutdown so any cleanup-time
	// stderr writes are still captured in the log. The trailing
	// "TUI session ended." line below then goes to the real terminal.
	if stderrRedirect != nil {
		_ = stderrRedirect.Restore()
	}

	if err != nil {
		return fmt.Errorf("TUI exited with error: %w", err)
	}

	fmt.Fprintln(os.Stderr, "TUI session ended.")
	return nil
}
