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
// Do NOT create a second supervisor inside defaultNewSession or anywhere
// else in this package. If two supervisors exist, the handoff MCP
// tool fires on one channel while the TUI listens on the other, the
// teardown/restart never runs, and the user is stuck in a stale session.
// See QUM-329 postmortem for the full failure mode and tests that guard
// against regression.
package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/agentloop"
	backend "github.com/dmotles/sprawl/internal/backend"
	backendclaude "github.com/dmotles/sprawl/internal/backend/claude"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/host"
	"github.com/dmotles/sprawl/internal/inputcoalesce"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/observe/sigdump"
	"github.com/dmotles/sprawl/internal/procutil"
	"github.com/dmotles/sprawl/internal/rootinit"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/runtimecfg"
	"github.com/dmotles/sprawl/internal/sprawlmcp"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/tui"
	"github.com/dmotles/sprawl/internal/tuiruntime"
	"github.com/spf13/cobra"
)

// resumeFailureWindow is the max runtime of a --resume invocation before we
// stop treating its exit as evidence the resume cookie is invalid. If claude
// exits within this window the next launch is force-fresh; outside it, normal
// restart logic applies.
const resumeFailureWindow = 5 * time.Second

// shutdownWatchdogTimeout bounds how long `sprawl enter` teardown may run
// before the shutdown watchdog force-dumps goroutines, releases the weave
// lock, and force-exits. Package-level var so it can be overridden later.
var shutdownWatchdogTimeout = 30 * time.Second

// enterDeps holds dependencies for the enter command, enabling testability.
type enterDeps struct {
	getenv     func(string) string
	getwd      func() (string, error)
	runProgram func(model tea.Model, onStart func(sender func(tea.Msg))) error
	// newSession launches a Claude Code subprocess wrapped in a UnifiedRuntime
	// and returns a tui.SessionBackend for AppModel to drive.
	// onResumeFailure, if non-nil, is invoked from the subprocess's stderr
	// scanner when the "No conversation found" marker trips — used by the
	// restart loop to force-fresh the next session regardless of elapsed time.
	// sup is the runEnter-scoped supervisor (see architectural contract at
	// top of this file). It is passed to defaultNewSession so the handoff
	// MCP tool and the TUI HandoffRequested() listener observe the same
	// channel. QUM-329.
	newSession      func(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (tui.SessionBackend, bool, error)
	newSupervisor   func(sprawlRoot string, logger *calllog.Logger) (supervisor.Supervisor, *sprawlmcp.Server)
	finalizeHandoff func(ctx context.Context, sprawlRoot string, stdout io.Writer, events chan<- rootinit.ConsolidationEvent) error
	// noResume, when true, skips the QUM-372 child-agent auto-resume scan.
	// Set via the `--no-resume` flag on the enter cobra command. Useful when
	// the operator suspects a poison-pill persisted child whose resume would
	// re-crash the supervisor, or when running disposable test sessions.
	noResume bool
	// noCoalesce, when true, skips wrapping os.Stdin with the QUM-608
	// paste coalescer. Set via the `--no-coalesce` flag. Emergency opt-
	// out — the coalescer is ON by default and is what makes TUI paste
	// instant on tmux <3.4.
	noCoalesce bool
	// pprofAddr, when non-empty, causes runEnter to start a net/http/pprof
	// listener on the given address in a background goroutine. Set via
	// the `--pprof` flag (wins) or the SPRAWL_PPROF_ADDR env var. QUM-678.
	pprofAddr string
	// pickAccent returns a randomly-picked accent color name (e.g.
	// "colour39"). Used by resolveAccentColor on first-run when no accent
	// has been persisted yet. Injected for deterministic tests. QUM-704.
	pickAccent func() string
}

// resolveAccentColor returns the persisted accent color, seeding a randomly-
// picked one on first run (QUM-704). On empty read, it calls pick(), persists
// the result via state.WriteAccentColor, and returns the picked value. Persist
// errors are logged to stderr but do not block startup — the in-memory value
// is still used for the current session so the TUI never silently falls back
// to the default cyan. A non-empty persisted color is returned as-is.
func resolveAccentColor(sprawlRoot string, pick func() string) string {
	if c := state.ReadAccentColor(sprawlRoot); c != "" {
		return c
	}
	c := pick()
	if err := state.WriteAccentColor(sprawlRoot, c); err != nil {
		fmt.Fprintf(os.Stderr, "[enter] warning: persisting seeded accent color failed: %v\n", err)
	}
	return c
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
	newSession func(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (tui.SessionBackend, bool, error),
	sup supervisor.Supervisor,
	finalize func(ctx context.Context, sprawlRoot string, stdout io.Writer, events chan<- rootinit.ConsolidationEvent) error,
	sprawlRoot string,
	consolidationCh chan<- rootinit.ConsolidationEvent,
	state *restartState,
	bridgeOut *tui.SessionBackend,
	logW io.Writer,
) func() (tui.SessionBackend, error) {
	return func() (tui.SessionBackend, error) {
		markerTripped := state.resumeMarkerTripped.Swap(false)
		forceFresh := markerTripped || (state.lastWasResume && time.Since(state.lastStartedAt) < resumeFailureWindow)
		switch {
		case markerTripped:
			fmt.Fprintf(logW, "[enter] resume failed (no conversation found) — falling back to fresh session\n")
		case forceFresh:
			fmt.Fprintf(logW, "[enter] resume died fast — falling back to fresh session\n")
		}
		// QUM-399: in unified mode, the bridge's Close() only cancels the
		// TUIAdapter's EventBus subscription — it does NOT kill the backing
		// claude subprocess (the runtime owns the session). If we leave the
		// old subprocess alive while consolidation runs, the new claude -p
		// memory worker contends with it for the .sprawl/memory/weave.lock
		// file (QUM-300), causing FinalizeHandoff to hang.
		//
		// Stop the registered weave runtime here, BEFORE finalize, so its
		// session is closed and waited and the lock is released.
		if sup != nil {
			if reg := sup.RuntimeRegistry(); reg != nil {
				if existing, ok := reg.Get("weave"); ok {
					stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					_ = existing.Stop(stopCtx)
					cancel()
					reg.Remove("weave")
				}
			}
		}

		// Phase D: run post-session housekeeping (consolidation when a handoff
		// signal is present, otherwise a noop). Errors are logged and do not
		// block the restart — matches cmd/rootloop.go's tmux-mode behavior.
		if finalize != nil {
			if err := finalize(context.Background(), sprawlRoot, logW, consolidationCh); err != nil {
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

func drainResumeFailureSignal(ch <-chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

var defaultEnterDeps *enterDeps

func init() {
	rootCmd.AddCommand(enterCmd)
	// QUM-372: --no-resume disables the startup child-agent auto-resume scan.
	enterCmd.Flags().Bool("no-resume", false, "skip auto-resuming suspended child agents (QUM-372)")
	// QUM-608: --no-coalesce disables the stdin paste coalescer that wraps
	// os.Stdin to synthesize bracketed-paste markers around detected
	// bursts. Emergency opt-out; the coalescer is ON by default.
	enterCmd.Flags().Bool("no-coalesce", false, "disable the stdin paste coalescer (QUM-608); use if it misbehaves in your terminal")
	// QUM-678: --pprof <addr> exposes net/http/pprof on the given address.
	// Also accepts SPRAWL_PPROF_ADDR; flag wins. Suggest a loopback bind so
	// users don't accidentally expose pprof to other hosts.
	enterCmd.Flags().String("pprof", "", "expose net/http/pprof on this address (e.g., 127.0.0.1:6060); also reads SPRAWL_PPROF_ADDR (QUM-678)")
}

var enterCmd = &cobra.Command{
	Use:   "enter",
	Short: "Launch the TUI dashboard",
	Long:  "Launch a fullscreen terminal UI for monitoring and interacting with agents. Works in any terminal — no tmux required.\n\nFlags:\n  --no-resume     skip auto-resuming suspended child agents (QUM-372)\n  --no-coalesce   disable the stdin paste coalescer (QUM-608); pastes will animate one char at a time on tmux <3.4",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps := resolveEnterDeps()
		// QUM-372: thread --no-resume into the resolved deps (the default
		// deps share a singleton, so we update it on each invocation).
		if v, err := cmd.Flags().GetBool("no-resume"); err == nil {
			deps.noResume = v
		}
		if v, err := cmd.Flags().GetBool("no-coalesce"); err == nil {
			deps.noCoalesce = v
		}
		// QUM-678: resolve pprof bind addr from --pprof (wins) or env var.
		pprofFlag, _ := cmd.Flags().GetString("pprof")
		deps.pprofAddr = resolvePprofAddr(pprofFlag, os.Getenv("SPRAWL_PPROF_ADDR"))
		return runEnter(deps)
	},
}

func resolveEnterDeps() *enterDeps {
	if defaultEnterDeps != nil {
		return defaultEnterDeps
	}

	deps := &enterDeps{
		getenv:     os.Getenv,
		getwd:      os.Getwd,
		newSession: defaultNewSession,
		pickAccent: runtimecfg.PickAccentColor,
		finalizeHandoff: func(ctx context.Context, sprawlRoot string, stdout io.Writer, events chan<- rootinit.ConsolidationEvent) error {
			deps := rootinit.DefaultDeps()
			deps.LogPrefix = "[enter]"
			return rootinit.FinalizeHandoff(ctx, deps, sprawlRoot, stdout, events)
		},
		newSupervisor: func(sprawlRoot string, logger *calllog.Logger) (supervisor.Supervisor, *sprawlmcp.Server) {
			// CallerName is what gets stamped into Parent when this
			// supervisor's Spawn() creates a child (cmd/enter.go's supervisor
			// now serves the MCP spawn tool since QUM-329 unified it).
			// Must be "weave" — the root agent's identity — so child agents'
			// report/message deliveries route to weave's maildir + harness
			// queue, not a phantom "enter" recipient.
			//
			// Before QUM-329 this field was "enter" because two separate
			// supervisors coexisted: one here (tree polling — CallerName
			// didn't matter) and one inside the legacy session factory
			// with CallerName:rootName. QUM-329 merged them into this one,
			// and kept the wrong CallerName — regression filed as QUM-333.
			sup, err := supervisor.NewReal(supervisor.Config{
				SprawlRoot: sprawlRoot,
				CallerName: "weave",
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "[enter] supervisor unavailable: %v\n", err)
				return nil, nil
			}
			// QUM-494: install the per-MCP-call observability logger on the
			// supervisor and wire it into the MCP server so checkpoints emit
			// during real tool calls.
			if logger != nil {
				sup.SetCallLogger(logger)
			}
			// Two-phase init: the child MCP server needs a reference to the
			// supervisor, so we create it after construction and wire it in.
			mcpServer := sprawlmcp.New(sup).WithCallLog(logger)
			childBridge := host.NewMCPBridge()
			childBridge.Register("sprawl", mcpServer)
			sup.SetChildMCPConfig(
				backend.InitSpec{
					MCPServerNames: []string{"sprawl"},
					ToolBridge:     childBridge,
				},
				sprawlmcp.MCPToolNames(),
			)
			return sup, mcpServer
		},
	}
	deps.runProgram = func(model tea.Model, onStart func(sender func(tea.Msg))) error {
		var opts []tea.ProgramOption
		// QUM-608: wrap os.Stdin with the paste coalescer unless
		// --no-coalesce is set. The coalescer synthesizes bracketed-
		// paste markers around detected bursts so Bubble Tea emits a
		// single tea.PasteMsg instead of one KeyPressMsg per character
		// — fixes the typewriter animation on tmux <3.4. Only wrap a
		// TTY stdin; piped stdin (tests, scripts) bypasses coalescing.
		var coal *inputcoalesce.Coalescer
		if !deps.noCoalesce && isStdinTTY() {
			coal = inputcoalesce.New(os.Stdin, inputcoalesce.DefaultWindow, nil)
			defer coal.Close()
			opts = append(opts, tea.WithInput(coal))
		}
		p := tea.NewProgram(model, opts...)

		// Catch SIGTERM/SIGHUP and forward as quit to the Bubble Tea program.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			if _, ok := <-sigCh; ok {
				p.Quit()
				// QUM-636: a quit signal means the user/parent wants out. If
				// the bubbletea program (or downstream teardown) wedges — e.g.
				// Program.Run won't drain its command goroutines while a
				// question modal is parked — force-exit within the bound so the
				// process never leaks and weave.lock is released (the kernel
				// drops the flock on exit). KillChildProcessGroups first so the
				// claude subprocess does not orphan to init. No disarm: a
				// healthy exit terminates the process (and this goroutine) well
				// before the bound, so the timer never fires.
				_ = installShutdownWatchdog(
					func() (<-chan time.Time, func()) {
						tm := time.NewTimer(shutdownWatchdogTimeout)
						return tm.C, func() { tm.Stop() }
					},
					defaultGoroutineDump,
					os.Stderr,
					procutil.KillChildProcessGroups,
					os.Exit,
				)
			}
		}()
		defer signal.Stop(sigCh)

		// QUM-458 layer 3: arm the orphan watchdog when running under a
		// sandbox/test context. installOrphanWatchdog gates internally on
		// shouldEnableOrphanWatchdog and returns a no-op stop in
		// production.
		stopWatchdog := installOrphanWatchdog(
			os.Getenv,
			os.Getenv("SPRAWL_ROOT"),
			syscall.Getppid,
			func() error {
				root := os.Getenv("SPRAWL_ROOT")
				if root == "" {
					return nil
				}
				_, err := os.Stat(root)
				return err
			},
			p.Quit,
			func() (<-chan time.Time, func()) {
				t := time.NewTicker(2 * time.Second)
				return t.C, t.Stop
			},
		)
		defer stopWatchdog()

		if onStart != nil {
			onStart(func(msg tea.Msg) { p.Send(msg) })
		}

		_, err := p.Run()
		return err
	}
	return deps
}

// enterAllowedTools returns the root tool allowlist (root-loop tools plus the
// sprawl MCP tool names) used by both the TUI launch path and the supervised
// session spec. Centralized so the two callsites can't drift.
func enterAllowedTools(prepared *rootinit.PreparedSession) []string {
	mcpNames := sprawlmcp.MCPToolNames()
	allowed := make([]string, 0, len(prepared.RootTools)+len(mcpNames))
	allowed = append(allowed, prepared.RootTools...)
	allowed = append(allowed, mcpNames...)
	return allowed
}

func buildEnterSessionSpec(sprawlRoot string, prepared *rootinit.PreparedSession, logW io.Writer, onResumeFailure func()) backend.SessionSpec {
	allowed := enterAllowedTools(prepared)

	spec := backend.SessionSpec{
		WorkDir:         sprawlRoot,
		Identity:        "weave",
		SprawlRoot:      sprawlRoot,
		SessionID:       prepared.SessionID,
		Model:           prepared.Model,
		PermissionMode:  "bypassPermissions",
		AllowedTools:    allowed,
		DisallowedTools: prepared.Disallowed,
		Stderr:          logW,
		OnResumeFailure: onResumeFailure,
	}
	if prepared.Resume {
		spec.Resume = true
	}
	spec.PromptFile = prepared.PromptPath
	return spec
}

func buildEnterInitSpec(bridge backend.ToolBridge) backend.InitSpec {
	return backend.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge,
	}
}

// supervisorMCPBridge returns the host-scoped MCP bridge owned by sup.
// If the supervisor exposes an MCPBridge() accessor (the production
// *supervisor.Real does after QUM-467) and it returns non-nil, that
// instance is reused. Otherwise we fall back to building a fresh bridge
// (legacy/test-double path). Reusing the supervisor-owned bridge is the
// fix for child agents losing MCP after a weave-claude restart: the bridge
// must outlive the weave-claude session.
func supervisorMCPBridge(sup supervisor.Supervisor) backend.ToolBridge {
	type mcpBridgeAccessor interface {
		MCPBridge() backend.ToolBridge
	}
	if acc, ok := sup.(mcpBridgeAccessor); ok {
		if b := acc.MCPBridge(); b != nil {
			return b
		}
	}
	// Fallback: construct a fresh bridge with the sprawl server registered.
	// Hit only when sup is a test double or the supervisor was constructed
	// without going through newSupervisor's wiring.
	mcpServer := sprawlmcp.New(sup)
	bridge := host.NewMCPBridge()
	bridge.Register("sprawl", mcpServer)
	return bridge
}

// buildSessionEnv returns the environment variables for the Claude Code subprocess.
func buildSessionEnv() []string {
	return append(os.Environ(),
		"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
		"SPRAWL_AGENT_IDENTITY=weave",
	)
}

// defaultNewSession launches a Claude Code subprocess wrapped in a
// UnifiedRuntime + TUIAdapter and returns it as a tui.SessionBackend.
//
// When forceFresh is true, Prepare's resume decision is bypassed and a fresh
// session is built. Callers use this for resume-failure fallback — the TUI's
// restartFunc forces fresh if the previous subprocess was a resume attempt
// that exited within resumeFailureWindow.
//
// Initialize timing: the backend session's Initialize is called here
// (synchronously) to register MCP tools BEFORE the runtime loop starts —
// matching the children's path in inProcessUnifiedStarter.Start. The
// adapter's Initialize() tea.Cmd then starts the runtime (rt.Start) when
// AppModel.Init dispatches it.
//
// Returns (backend, wasResume, error). wasResume indicates whether Prepare
// took the resume path; callers use it to decide if a fast exit warrants a
// force-fresh retry.
func defaultNewSession(sprawlRoot string, sup supervisor.Supervisor, forceFresh bool, onResumeFailure func()) (tui.SessionBackend, bool, error) {
	rinitDeps := rootinit.DefaultDeps()
	rinitDeps.LogPrefix = "[enter]"
	logW := os.Stderr
	if sup == nil {
		return nil, false, fmt.Errorf("defaultNewSession: supervisor must be non-nil (see QUM-329 architectural contract)")
	}

	const rootName = "weave"

	// Self-cleaning restart: if a prior weave runtime is still registered
	// (from a previous session in this process), stop it and remove from the
	// registry before building a new one.
	if reg := sup.RuntimeRegistry(); reg != nil {
		if existing, ok := reg.Get(rootName); ok {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = existing.Stop(stopCtx)
			cancel()
			reg.Remove(rootName)
		}
	}

	var prepared *rootinit.PreparedSession
	var err error
	if forceFresh {
		prepared, err = rootinit.PrepareFresh(context.Background(), rinitDeps, sprawlRoot, rootName, logW)
	} else {
		prepared, err = rootinit.Prepare(context.Background(), rinitDeps, sprawlRoot, rootName, logW)
	}
	if err != nil {
		return nil, false, fmt.Errorf("preparing session: %w", err)
	}
	if prepared.Resume {
		fmt.Fprintf(logW, "[enter] resuming session %s (unified)\n", prepared.SessionID)
	} else {
		fmt.Fprintf(logW, "[enter] starting session %s (unified)\n", prepared.SessionID)
	}

	// QUM-467: reuse the supervisor's host-scoped MCP bridge instead of
	// constructing a fresh one here. Each weave-claude restart routes back
	// through this function; constructing a new bridge per invocation
	// severed any child agent already registered against the prior bridge.
	mcpBridge := supervisorMCPBridge(sup)

	adapter := backendclaude.NewAdapter(backendclaude.Config{})
	session, err := adapter.Start(context.Background(), buildEnterSessionSpec(sprawlRoot, prepared, logW, onResumeFailure))
	if err != nil {
		return nil, false, err
	}
	if err := session.Start(context.Background()); err != nil {
		_ = session.Close()
		_ = session.Wait()
		return nil, false, fmt.Errorf("starting session reader: %w", err)
	}
	if err := session.Initialize(context.Background(), buildEnterInitSpec(mcpBridge)); err != nil {
		_ = session.Close()
		_ = session.Wait()
		return nil, false, fmt.Errorf("initializing session: %w", err)
	}

	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:         rootName,
		SprawlRoot:   sprawlRoot,
		Session:      session,
		IsRoot:       true,
		Capabilities: session.Capabilities(),
		// Defends against wedged-SDK hangs (QUM-578/QUM-581). 30m is long
		// enough for long autonomous turns but bounded so an SDK that opens
		// system:init and never closes doesn't permanently freeze the agent.
		TurnTimeout: 30 * time.Minute,
		OnQueueItemDelivered: func(it runtimepkg.QueueItem) {
			for _, id := range it.EntryIDs {
				if err := agentloop.MarkDelivered(sprawlRoot, rootName, id); err != nil {
					fmt.Fprintf(logW, "[enter] mark delivered %s: %v\n", id, err)
				}
			}
		},
	})

	handle, err := supervisor.NewWeaveRuntimeHandle(rt, session, sprawlRoot, rootName)
	if err != nil {
		_ = session.Close()
		_ = session.Wait()
		return nil, false, fmt.Errorf("building weave runtime handle: %w", err)
	}

	weaveAgentState, _ := state.LoadAgent(sprawlRoot, rootName)
	if _, err := sup.RegisterRootRuntime(rootName, handle, weaveAgentState); err != nil {
		_ = handle.Stop(context.Background())
		return nil, false, fmt.Errorf("registering root runtime: %w", err)
	}

	tuiAdapter := tuiruntime.NewTUIAdapter(rt)
	return tuiAdapter, prepared.Resume, nil
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

	// QUM-678: start the opt-in pprof listener as early as possible so it's
	// up before heavy init in case the operator wants to profile startup.
	// No-op when neither --pprof nor SPRAWL_PPROF_ADDR is set.
	startPprof(deps.pprofAddr, os.Stderr)

	// Single-weave invariant: acquire the flock before any init work, and
	// hold it for the lifetime of the sprawl enter process (including
	// across TUI-driven session restarts). Released on return.
	lock, err := rootinit.AcquireWeaveLock(sprawlRoot)
	if err != nil {
		printWeaveLockError(os.Stderr, err, sprawlRoot)
		return err
	}
	defer func() { _ = lock.Release() }()

	// QUM-522: best-effort cleanup of a stale .consolidating lockfile
	// orphaned by a crashed/SIGKILLed prior weave. Runs after the weave
	// flock is held so we know no other weave is launching this pipeline.
	_, _ = rootinit.JanitorStaleLock(sprawlRoot, os.Stderr, time.Now)

	// QUM-495: install SIGUSR1 handler that dumps goroutine stacks + open
	// file descriptors to $SPRAWL_ROOT/.sprawl/runtime/. Process-wide and
	// active for the lifetime of `sprawl enter` regardless of TUI mode.
	stopSigDump := sigdump.Install(context.Background(), sprawlRoot, log.New(os.Stderr, "[sigdump] ", log.LstdFlags))
	defer stopSigDump()

	pickAccent := deps.pickAccent
	if pickAccent == nil {
		pickAccent = runtimecfg.PickAccentColor
	}
	accentColor := resolveAccentColor(sprawlRoot, pickAccent)
	repoName := filepath.Base(sprawlRoot)

	// Track last session's start time + whether it was a resume attempt so
	// that the TUI's restartFunc can fall back to a fresh session if a
	// resume attempt died within resumeFailureWindow.
	state := &restartState{}

	// resumeFailureCh is signaled by the Claude subprocess's stderr marker
	// scanner when "No conversation found" fires. The onStart hook drains it
	// and posts RestartSessionMsg — the TUI cannot detect the dead subprocess
	// on its own because Bridge.WaitForEvent is only called after the user
	// submits a message, and Session.Initialize treats pre-init EOF as
	// success. Without this prod, the TUI would sit idle on a zombie session
	// until the user typed something. Buffered to size 1 so a second marker
	// trip in the same session is absorbed without blocking.
	resumeFailureCh := make(chan struct{}, 1)
	onResumeFailure := func() {
		state.resumeMarkerTripped.Store(true)
		select {
		case resumeFailureCh <- struct{}{}:
		default:
		}
	}

	// QUM-494: open the per-MCP-call observability logger. A failure to
	// open the call log file should not break enter — fall back to a no-op
	// logger and continue. The logger is closed at the end of runEnter.
	callLogger, callLogErr := calllog.Open(sprawlRoot)
	if callLogErr != nil {
		fmt.Fprintf(os.Stderr, "[enter] warning: opening MCP call log failed: %v (continuing without per-call observability)\n", callLogErr)
		callLogger = calllog.NewNoop()
	}
	defer func() { _ = callLogger.Close() }()

	// QUM-329: build the single supervisor BEFORE creating the session so
	// the same instance is wired into (a) the MCP server inside
	// newSession, (b) the tree-panel status poller (passed into AppModel
	// below), and (c) the HandoffRequested() listener goroutine in
	// onStart. A second supervisor would orphan the handoff signal.
	var sup supervisor.Supervisor
	var mcpServer *sprawlmcp.Server
	if deps.newSupervisor != nil {
		sup, mcpServer = deps.newSupervisor(sprawlRoot, callLogger)
	}

	// QUM-372: walk persisted child agents and resume any that were in a
	// non-terminal state when the prior `sprawl enter` exited. Counts feed
	// the AgentsResumedMsg banner emitted from onStart below. Skipped when
	// --no-resume is set (operator wants a disposable session).
	var pendingResume struct{ resumed, failed int }
	if sup != nil && !deps.noResume {
		r, f, errs := sup.RecoverAgents(context.Background())
		pendingResume.resumed = r
		pendingResume.failed = f
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "[enter] resume error: %v\n", e)
		}
	}

	var bridge tui.SessionBackend
	if deps.newSession != nil {
		var err error
		var wasResume bool
		bridge, wasResume, err = deps.newSession(sprawlRoot, sup, false, onResumeFailure)
		if err != nil && state.resumeMarkerTripped.Swap(false) {
			drainResumeFailureSignal(resumeFailureCh)
			fmt.Fprintf(os.Stderr, "[enter] initial resume failed (no conversation found) — falling back to fresh session\n")
			bridge, wasResume, err = deps.newSession(sprawlRoot, sup, true, onResumeFailure)
		}
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		state.lastStartedAt = time.Now()
		state.lastWasResume = wasResume
	}

	// QUM-391: consolidation events channel — long-lived, shared between
	// makeRestartFunc (writer via finalize) and onStart (reader that forwards
	// events to the TUI as tea.Msgs).
	consolidationCh := make(chan rootinit.ConsolidationEvent, 16)

	var restartFunc func() (tui.SessionBackend, error)
	if deps.newSession != nil {
		restartFunc = makeRestartFunc(deps.newSession, sup, deps.finalizeHandoff, sprawlRoot, consolidationCh, state, &bridge, os.Stderr)
	}
	model := tui.NewAppModel(accentColor, repoName, buildVersion, bridge, sup, sprawlRoot, restartFunc)
	// QUM-588: read .sprawl/config.yaml's validate_popup_after_seconds and
	// install on the AppModel. Failure to load is non-fatal — defaults apply.
	if cfg, cErr := config.Load(sprawlRoot); cErr == nil && cfg != nil {
		model.SetValidatePopupAfter(cfg.ValidatePopupAfter())
	}
	if homeDir, hErr := os.UserHomeDir(); hErr == nil {
		// QUM-332: child-agent transcript tailing resolves Claude session
		// log paths via memory.SessionLogPath(homeDir, worktree, sessionID).
		model.SetHomeDir(homeDir)
	}

	if bridge != nil && state.lastWasResume {
		if sessionID, sErr := memory.ReadLastSessionID(sprawlRoot); sErr == nil && sessionID != "" {
			if homeDir, hErr := os.UserHomeDir(); hErr == nil {
				path := memory.SessionLogPath(homeDir, sprawlRoot, sessionID)
				entries, lErr := tui.LoadTranscript(path, tui.ReplayMaxMessages)
				if lErr != nil {
					fmt.Fprintf(os.Stderr, "[enter] transcript replay failed: %v (continuing with empty viewport)\n", lErr)
				} else if len(entries) > 0 {
					// QUM-676: the legacy "Resumed from prior session" + "earlier
					// messages truncated" MessageStatus markers are gone with the
					// ChatList contract-violator routing. Surface the resume hint
					// via the status-bar transient label instead.
					model.PreloadTranscript(entries)
					model.SetTransientStatus("Resumed from prior session")
				}
			}
		}
	}

	// Shutdown signal for the forwarder goroutines; closed when the Bubble Tea
	// program returns so the goroutines can exit.
	handoffDone := make(chan struct{})
	// qConsumer holds the registered TUI question consumer (QUM-527 slice 2c)
	// so the shutdown path can unregister it. Written once in onStart, read
	// once after runProgram returns — single-threaded by construction.
	var qConsumer *tui.QuestionConsumer
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

		// QUM-497: install the TUI message sender on the in-process MCP
		// server and the supervisor so MCPCallStartedMsg / *ProgressMsg /
		// *EndedMsg events surface in the host status bar / viewport. The
		// sender is cleared on TUI shutdown below (sibling of the notifier
		// teardown).
		if mcpServer != nil {
			mcpServer.SetMsgSender(func(msg any) { send(msg.(tea.Msg)) })
		}
		if r, ok := sup.(*supervisor.Real); ok {
			r.SetProgressEmitter(func(callID, step, tail string) {
				send(tui.MCPCallProgressMsg{CallID: callID, Step: step, Tail: tail})
			})
			// QUM-588: richer kv-preserving fan-out for the validate popup.
			// Separate from progressEmitter so the QUM-497 status-bar surface
			// is unaffected.
			r.SetValidateEmitter(func(callID, step string, kv map[string]string) {
				send(tui.ValidateEventMsg{CallID: callID, Step: step, KV: kv})
			})
			// QUM-602: backend-fault fan-out. The TUI renders a viewport
			// banner + tree-row indicator when a child runtime's backend
			// session faults terminally (ErrHangTimeout /
			// ErrSubscriberWedged).
			r.SetBackendFaultEmitter(func(agent, class, reason, nextAction string) {
				send(tui.BackendFaultMsg{
					Agent:      agent,
					Class:      class,
					Reason:     reason,
					NextAction: nextAction,
				})
			})
			// QUM-601: backend-recovered fan-out. The TUI clears the
			// per-agent fault sticker, rebuilds the tree to drop the FAULT
			// badge, and surfaces a "backend recovered on X" banner.
			r.SetBackendRecoveredEmitter(func(agent string) {
				send(tui.BackendFaultClearedMsg{Agent: agent})
			})
		}

		// QUM-527 slice 2c: register the TUI question consumer so the
		// supervisor's question queue dispatches OnEnqueue / OnCancel into
		// the bubbletea program. The forwarder goroutine below additionally
		// subscribes to QuestionsChanged() so depth updates (e.g. resolves
		// from other consumers) refresh the status bar.
		if sup != nil {
			c := tui.NewQuestionConsumer(send)
			if err := sup.RegisterQuestionConsumer(c); err != nil {
				fmt.Fprintf(os.Stderr, "[enter] register question consumer: %v\n", err)
			} else {
				qConsumer = c
			}
			if qch := sup.QuestionsChanged(); qch != nil {
				go func() {
					for {
						select {
						case <-handoffDone:
							return
						case _, ok := <-qch:
							if !ok {
								return
							}
							depth, head := sup.PeekQuestions()
							send(tui.QuestionsAvailableMsg{Depth: depth, Head: head})
						}
					}
				}()
			}
		}

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

		// QUM-391: forward consolidation events from the background goroutine
		// to the TUI as tea.Msgs. The consolidationCh is written to by
		// makeRestartFunc → finalize → StartBackgroundConsolidation.
		go func() {
			for {
				select {
				case <-handoffDone:
					return
				case ev, ok := <-consolidationCh:
					if !ok {
						return
					}
					if ev.Done {
						send(tui.ConsolidationCompleteMsg{Err: ev.Err, Duration: ev.Duration})
					} else {
						send(tui.ConsolidationPhaseMsg{Phase: ev.Phase})
					}
				}
			}
		}()

		// QUM-372: surface the resume-scan outcome as a viewport banner.
		// Silent when both counts are zero (fresh session / nothing to resume).
		// Dispatched on a goroutine because onStart runs before p.Run(): a
		// synchronous p.Send here deadlocks the main goroutine until the
		// Bubble Tea message loop starts, which never happens because we
		// haven't returned from onStart yet.
		if pendingResume.resumed > 0 || pendingResume.failed > 0 {
			resumed, failed := pendingResume.resumed, pendingResume.failed
			go send(tui.AgentsResumedMsg{Resumed: resumed, Failed: failed})
		}

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
	// QUM-636: arm a shutdown watchdog over the teardown block. If teardown
	// wedges (e.g. drainInflight blocks), this force-dumps goroutines into the
	// still-redirected tui-stderr log, releases the weave flock, and exits.
	stopShutdownWatchdog := installShutdownWatchdog(
		func() (<-chan time.Time, func()) {
			tm := time.NewTimer(shutdownWatchdogTimeout)
			return tm.C, func() { tm.Stop() }
		},
		defaultGoroutineDump,
		os.Stderr,
		func() { procutil.KillChildProcessGroups(); _ = lock.Release() },
		os.Exit,
	)
	defer stopShutdownWatchdog()
	close(handoffDone)
	// QUM-311: the send function captured by the notifier closure becomes a
	// no-op once the tea.Program has exited, but be explicit and unregister
	// the TUI notifier so any lingering in-process messages.Send calls during
	// shutdown don't try to dispatch into a dead program.
	messages.SetDefaultNotifier(nil)
	// QUM-497: tear down the TUI msg sender on shutdown so any in-flight MCP
	// dispatch goroutines that emit Started/Ended events on their way out
	// don't try to push into a dead bubbletea program.
	if mcpServer != nil {
		mcpServer.SetMsgSender(nil)
	}
	if r, ok := sup.(*supervisor.Real); ok {
		r.SetProgressEmitter(nil)
		r.SetValidateEmitter(nil)
		r.SetBackendFaultEmitter(nil)
		r.SetBackendRecoveredEmitter(nil)
	}
	// QUM-527 slice 2c: unregister the TUI question consumer so the
	// supervisor's queue stops fanning out OnEnqueue / OnCancel to a dead
	// program. Idempotent on the supervisor side.
	if sup != nil && qConsumer != nil {
		sup.UnregisterQuestionConsumer(qConsumer.Name())
	}

	// Ctrl+C / clean shutdown of the TUI does NOT run FinalizeHandoff. It
	// does stop runtime-backed children because this weave process owns them.
	// Rationale:
	//   - FinalizeHandoff clears last-session-id when a handoff-signal file
	//     is present, which breaks resume-by-default (QUM-255) on the next
	//     `sprawl enter`. A stale/in-flight signal can linger across
	//     sessions; consuming it on Ctrl+C is the wrong trigger. The
	//     consolidate-then-fresh path in rootinit.Prepare will handle any
	//     pending handoff on the next launch.
	//   - The same-process runtime is not detachable. When weave exits, its
	//     in-memory child runtimes must exit with it; persisted state remains
	//     for inspection or follow-up cleanup.
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
	// QUM-636: disarm the shutdown watchdog before restoring stderr, so a
	// force-dump (if it ever fired) lands in the still-redirected tui-stderr
	// log rather than on the real terminal. Idempotent with the defer above.
	stopShutdownWatchdog()
	if stderrRedirect != nil {
		_ = stderrRedirect.Restore()
	}

	if err != nil {
		return fmt.Errorf("TUI exited with error: %w", err)
	}

	fmt.Fprintln(os.Stderr, "TUI session ended.")
	return nil
}

// isStdinTTY reports whether os.Stdin is a terminal. False when stdin is
// a pipe.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
