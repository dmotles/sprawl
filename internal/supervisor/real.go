package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
	"github.com/dmotles/sprawl/internal/usage"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/gofrs/flock"
)

// mergeInflightInfo records the agent name and start time of an in-flight
// merge so concurrent Merge callers can capture contention metadata. See
// QUM-588.
type mergeInflightInfo struct {
	agentName string
	startedAt time.Time
}

// Config holds configuration for the real supervisor.
type Config struct {
	SprawlRoot        string
	CallerName        string
	ChildInitSpec     backendpkg.InitSpec
	ChildAllowedTools []string
}

// Real is the production implementation of Supervisor.
//
// Spawn/Merge/Retire/Kill delegate to internal/agentops, which contains the
// shared logic used by the spawn/merge/retire/kill MCP tools (and the
// `sprawl merge` CLI command, which is still surfaced standalone).
//
// The *Fn fields are test seams: tests can swap them to exercise Real's
// wiring without touching the underlying agentops machinery (which is
// already covered by cmd/*_test.go and internal/agentops tests).
type Real struct {
	sprawlRoot string
	callerName string

	runtimeRegistry *RuntimeRegistry
	runtimeStarter  RuntimeStarter

	spawnDeps  *agentops.SpawnDeps
	mergeDeps  *agentops.MergeDeps
	retireDeps *agentops.RetireDeps
	killDeps   *agentops.KillDeps

	spawnFn  func(*agentops.SpawnDeps, string, string, string, string, bool) (*state.AgentState, error)
	mergeFn  func(context.Context, *agentops.MergeDeps, string, string, bool, bool) (*agentops.MergeOutcome, error)
	retireFn func(context.Context, *agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error
	killFn   func(*agentops.KillDeps, string, bool) error

	// Handoff seams + signal channel. The channel is buffered (size 1) and
	// Handoff sends non-blocking so repeated calls never deadlock if no
	// listener drains.
	handoffCh                  chan struct{}
	handoffReadLastSessionID   func(sprawlRoot string) (string, error)
	handoffListAgents          func(sprawlRoot string) ([]*state.AgentState, error)
	handoffWriteSessionSummary func(sprawlRoot string, session memory.Session, body string) error
	handoffWriteSignalFile     func(sprawlRoot string) error
	handoffNow                 func() time.Time

	// mcpBridge is the host-scoped MCP tool bridge captured from the FIRST
	// SetChildMCPConfig call (set-once). Subsequent calls update the runtime
	// starter's allowed tools/InitSpec but do NOT replace the bridge that
	// children and weave-self share. See QUM-467: previously cmd/enter.go's
	// three weave-launch sites each constructed a fresh bridge, severing
	// children that had registered against the original on weave-claude
	// restart. Hoisting bridge identity onto the supervisor's lifetime fixes
	// that.
	mcpBridge backendpkg.ToolBridge

	// logger, when non-nil, is used to populate Checkpoint funcs on the
	// per-call agentops deps so merge/retire emit per-call observability
	// checkpoints into the JSONL call log. See QUM-494.
	logger *calllog.Logger

	// progressEmitter, when non-nil, is invoked for every per-call
	// agentops checkpoint with the active call_id, step name, and an
	// optional tail line (kv["line"] for merge.validate-line). The TUI
	// uses this to update the in-flight indicator with the current step
	// without re-rendering the whole call log. (QUM-497)
	progressEmitter func(callID, step, tail string)

	// validateEmitter is a richer, kv-preserving fan-out for the merge
	// validate sub-path. It receives every merge.* checkpoint with the
	// full kv slice decoded into a map. The TUI uses this to drive the
	// live validate-output popup (QUM-588). nil is allowed and disables.
	validateEmitter func(callID, step string, kv map[string]string)

	// faultEmitter, when non-nil, is invoked when a child runtime's
	// backend session fires a sticky terminal error (QUM-602). The TUI
	// installs this to surface a fault banner + tree-row indicator. The
	// emitter is propagated into newly-constructed runtime starters via
	// dispatchFault so the indirection survives SetChildMCPConfig
	// rebuilds.
	faultEmitter func(agent, class, reason, nextAction string)

	// wokenEmitter, when non-nil, is invoked after a successful Real.Wake
	// so the TUI can clear its per-agent fault sticker (QUM-601/QUM-724).
	// Mirrors faultEmitter contracts: nil-safe, idempotent install/clear.
	wokenEmitter func(agent string)

	// questions is the in-process question queue for ask_user_question
	// flows (QUM-527 slice 1). See question.go.
	questions *questionQueue

	// reportMu serializes ReportStatus calls. state.SaveAgent is not
	// atomic on disk (read-modify-write), so concurrent reporters racing
	// to update the same agent's state would corrupt or lose updates.
	// Status reports are low-frequency in practice; serialization is a
	// reasonable cost. See QUM-559 concurrency test.
	reportMu sync.Mutex

	// mergeSem serializes Real.Merge per-sprawl-root. Capacity 1: only one
	// merge runs at a time. mergeInflight records who currently holds the
	// sem so a queued caller can report "queued behind <name>" in its
	// outcome. See QUM-588.
	mergeSem        chan struct{}
	mergeInflightMu sync.Mutex
	mergeInflight   *mergeInflightInfo

	// gitRevParseHEAD is an injectable seam for resolving a worktree's
	// HEAD SHA. Unit tests inject a fake; production callers leave this
	// nil and the package-level realGitRevParseHEAD is used. See QUM-572.
	gitRevParseHEAD func(dir string) (string, error)

	// heartbeat is the QUM-730 supervisor liveness-check goroutine. Started
	// by NewReal when LivenessConfig.Enabled, stopped by Shutdown.
	heartbeat *heartbeat
}

// realGitRevParseHEAD shells out to `git -C <dir> rev-parse HEAD`. stdio is
// redirected to io.Discard (mirroring internal/worktree/worktree.go:branchExists)
// so a missing ref / not-a-repo error cannot inherit the parent's FD 2 in TUI
// mode. See QUM-330/QUM-304/QUM-342.
func realGitRevParseHEAD(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD") //nolint:gosec // arguments are not user-controlled
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SetProgressEmitter installs a fan-out hook invoked for every merge/retire
// agentops checkpoint with the active call_id (extracted from ctx), step
// name, and optional kv["line"] tail. The host TUI uses this to update its
// in-flight status-bar indicator with the latest step (QUM-497). nil is
// allowed and disables the fan-out.
func (r *Real) SetProgressEmitter(fn func(callID, step, tail string)) {
	r.progressEmitter = fn
}

// SetValidateEmitter installs a richer, kv-preserving fan-out hook invoked
// for every merge.* checkpoint (queued/starting/validate-started/-line/-ended)
// with the active call_id, step name, and a string-keyed kv map. The host
// TUI uses this to drive the live validate-output popup (QUM-588). nil is
// allowed and disables the fan-out.
func (r *Real) SetValidateEmitter(fn func(callID, step string, kv map[string]string)) {
	r.validateEmitter = fn
}

// SetBackendFaultEmitter installs a fan-out hook invoked whenever a child
// runtime's backend session fires a sticky terminal error (QUM-602). The
// host TUI uses this to render a fault banner and tag the agent's tree row.
// nil is allowed and clears the emitter; install + clear must both be
// idempotent and panic-free (mirrors SetProgressEmitter).
func (r *Real) SetBackendFaultEmitter(fn func(agent, class, reason, nextAction string)) {
	r.faultEmitter = fn
}

// dispatchFault reads the currently-installed faultEmitter and forwards
// the call. Indirection lets us survive SetChildMCPConfig rebuilds — the
// per-starter closure always reaches the live emitter, not a stale capture.
func (r *Real) dispatchFault(agent, class, reason, nextAction string) {
	if fn := r.faultEmitter; fn != nil {
		fn(agent, class, reason, nextAction)
	}
}

// SetBackendWokenEmitter installs a fan-out hook invoked whenever Real.Wake
// successfully brings an offline agent back online (QUM-601/QUM-724). The TUI
// uses this to clear the per-agent fault sticker and surface a "backend
// recovered on X" banner. nil is allowed and clears the emitter; install +
// clear must both be idempotent and panic-free.
func (r *Real) SetBackendWokenEmitter(fn func(agent string)) {
	r.wokenEmitter = fn
}

// SetCallLogger installs the per-MCP-call observability logger on this
// supervisor. After installation, Merge/Retire calls dispatched with a
// context carrying a call_id (placed there by Server.handleToolsCall) will
// emit checkpoints to the JSONL call log. nil is allowed and disables.
// See QUM-494.
func (r *Real) SetCallLogger(l *calllog.Logger) {
	r.logger = l
}

// NewReal creates a new real supervisor.
func NewReal(cfg Config) (*Real, error) {
	// supervisorGetenv injects the supervisor's identity/root into env
	// lookups that agentops performs. Everything else passes through to
	// the process environment.
	supervisorGetenv := func(key string) string {
		switch key {
		case "SPRAWL_AGENT_IDENTITY":
			return cfg.CallerName
		case "SPRAWL_ROOT":
			return cfg.SprawlRoot
		default:
			return os.Getenv(key)
		}
	}

	newMergeDeps := func() *merge.Deps {
		return &merge.Deps{
			LockAcquire:       merge.RealLockAcquire,
			GitMergeBase:      merge.RealGitMergeBase,
			GitRevParseHead:   merge.RealGitRevParseHead,
			GitResetSoft:      merge.RealGitResetSoft,
			GitCommit:         merge.RealGitCommit,
			GitRebase:         merge.RealGitRebase,
			GitRebaseAbort:    merge.RealGitRebaseAbort,
			GitFFMerge:        merge.RealGitFFMerge,
			GitResetHard:      merge.RealGitResetHard,
			RunTestsStreaming: merge.RealRunTestsStreaming,
			WritePoke:         merge.RealWritePoke,
			Stderr:            os.Stderr,
		}
	}

	r := &Real{
		sprawlRoot:      cfg.SprawlRoot,
		callerName:      cfg.CallerName,
		runtimeRegistry: NewRuntimeRegistry(),

		spawnDeps: &agentops.SpawnDeps{
			WorktreeCreator: &worktree.RealCreator{},
			Getenv:          supervisorGetenv,
			CurrentBranch:   agentops.GitCurrentBranch,
			NewSpawnLock: func(lockPath string) (func() error, func() error) {
				fl := flock.New(lockPath)
				return fl.Lock, fl.Unlock
			},
			LoadConfig:      config.Load,
			RunScript:       agentops.RunBashScript,
			WorktreeRemove:  agentops.RealWorktreeRemove,
			GitBranchDelete: agentops.RealGitBranchDelete,
		},
		mergeDeps: &agentops.MergeDeps{
			Getenv:        supervisorGetenv,
			LoadAgent:     state.LoadAgent,
			ListAgents:    state.ListAgents,
			GitStatus:     agentops.RealGitStatus,
			BranchExists:  agentops.RealBranchExists,
			CurrentBranch: agentops.GitCurrentBranch,
			LoadConfig:    config.Load,
			DoMerge:       merge.Merge,
			NewMergeDeps:  newMergeDeps,
			Stderr:        os.Stderr,
		},
		retireDeps: &agentops.RetireDeps{
			Getenv:              supervisorGetenv,
			WorktreeRemove:      agentops.RealWorktreeRemove,
			GitStatus:           agentops.RealGitStatus,
			RemoveAll:           os.RemoveAll,
			GitBranchDelete:     agentops.RealGitBranchDelete,
			GitBranchIsMerged:   agentops.RealGitBranchIsMerged,
			GitBranchSafeDelete: agentops.RealGitBranchSafeDelete,
			DoMerge:             merge.Merge,
			NewMergeDeps:        newMergeDeps,
			LoadAgent:           state.LoadAgent,
			CurrentBranch:       agentops.GitCurrentBranch,
			GitUnmergedCommits:  agentops.RealGitUnmergedCommits,
			LoadConfig:          config.Load,
			RunScript:           agentops.RunBashScript,
		},
		killDeps: &agentops.KillDeps{
			Getenv: supervisorGetenv,
		},

		spawnFn:  agentops.PrepareSpawn,
		mergeFn:  agentops.Merge,
		retireFn: agentops.Retire,
		killFn:   agentops.Kill,

		handoffCh:                  make(chan struct{}, 1),
		handoffReadLastSessionID:   memory.ReadLastSessionID,
		handoffListAgents:          state.ListAgents,
		handoffWriteSessionSummary: memory.WriteSessionSummary,
		handoffWriteSignalFile:     memory.WriteHandoffSignal,
		handoffNow:                 time.Now,

		questions: newQuestionQueue(),

		mergeSem: make(chan struct{}, 1),
	}
	starter := newInProcessUnifiedStarter(cfg.ChildInitSpec, cfg.ChildAllowedTools)
	if s, ok := starter.(*inProcessUnifiedStarter); ok {
		s.faultEmitter = r.dispatchFault
	}
	r.runtimeStarter = starter

	// QUM-730: install the heartbeat goroutine. Defaults enable it; the
	// project config can disable or tune via the `liveness:` YAML block.
	livenessCfg := ResolveLivenessConfig(loadRawLiveness(cfg.SprawlRoot))
	r.heartbeat = newHeartbeat(heartbeatDeps{
		Cfg:        livenessCfg,
		SprawlRoot: cfg.SprawlRoot,
		Registry:   &registryListerAdapter{reg: r.runtimeRegistry},
		SendMessage: func(ctx context.Context, to, body string, interrupt bool) (*SendMessageResult, error) {
			// Heartbeat liveness-checks never wake an offline target.
			return r.SendMessage(ctx, to, body, interrupt, false)
		},
		SendLivenessCheck: func(sprawlRoot, to string) (string, error) {
			return messages.SendLivenessCheck(sprawlRoot, r.callerName, to)
		},
		LoadAgent:        state.LoadAgent,
		ReadActivityTail: agentloop.ReadActivityTail,
		ActivityPath:     agentloop.ActivityPath,
		WakeForDelivery: func(rt *AgentRuntime) error {
			if rt == nil {
				return nil
			}
			return rt.WakeForDelivery()
		},
		Logger: slog.Default(),
	})
	r.heartbeat.Start()
	return r, nil
}

// loadRawLiveness reads `.sprawl/config.yaml`'s `liveness:` block. Errors
// (or absent file) yield nil so ResolveLivenessConfig applies defaults.
func loadRawLiveness(sprawlRoot string) *LivenessConfigRaw {
	if sprawlRoot == "" {
		return nil
	}
	c, err := config.Load(sprawlRoot)
	if err != nil || c == nil || c.Liveness == nil {
		return nil
	}
	raw := c.Liveness
	return &LivenessConfigRaw{
		Enabled:               raw.Enabled,
		HeartbeatInterval:     raw.HeartbeatInterval,
		IdleThreshold:         raw.IdleThreshold,
		Tier2ConsecutiveTicks: raw.Tier2ConsecutiveTicks,
		EscalationThreshold:   raw.EscalationThreshold,
	}
}

// RegisterRootRuntime attaches a pre-built RuntimeHandle to the runtime
// registry under the given name, marks it Started, and returns the
// AgentRuntime. See Supervisor.RegisterRootRuntime / QUM-399.
func (r *Real) RegisterRootRuntime(name string, handle RuntimeHandle, agentState *state.AgentState) (*AgentRuntime, error) {
	if name == "" {
		return nil, fmt.Errorf("RegisterRootRuntime: name must not be empty")
	}
	if handle == nil {
		return nil, fmt.Errorf("RegisterRootRuntime: handle must not be nil")
	}
	if agentState == nil {
		if loaded, err := state.LoadAgent(r.sprawlRoot, name); err == nil && loaded != nil {
			agentState = loaded
		} else {
			agentState = &state.AgentState{Name: name, Status: "running"}
		}
	}
	if agentState.Type == "" {
		agentState.Type = "root"
		// QUM-535: persist the type back to disk so the MCP eligibility
		// gate — which consults Supervisor.Status() / state.ListAgents —
		// observes the canonical "root" type. In-memory mutation alone
		// is invisible to disk-backed lookups, causing weave-as-caller
		// of ask_user_question to be rejected.
		if err := state.SaveAgent(r.sprawlRoot, agentState); err != nil {
			return nil, fmt.Errorf("persisting root runtime state for %q: %w", name, err)
		}
	}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: r.sprawlRoot,
		Agent:      agentState,
	})
	rt.AttachHandle(handle)
	return rt, nil
}

// RuntimeRegistry exposes the supervisor's in-memory runtime registry so
// process-level wiring can consult it.
func (r *Real) RuntimeRegistry() *RuntimeRegistry {
	return r.runtimeRegistry
}

// SetChildMCPConfig updates the child runtime starter with the given MCP
// init spec and allowed tools. Use this for two-phase init when the MCP
// server needs a reference to the supervisor itself.
//
// Set-once bridge semantics (QUM-467): the FIRST call's initSpec.ToolBridge
// is stashed into r.mcpBridge and is what MCPBridge() returns thereafter.
// Subsequent calls still update the runtime starter (so allowed-tools
// changes take effect for newly spawned children), but do NOT replace the
// authoritative bridge identity. This preserves children's MCP path across
// weave-claude restarts.
func (r *Real) SetChildMCPConfig(initSpec backendpkg.InitSpec, allowedTools []string) {
	if r.mcpBridge == nil && initSpec.ToolBridge != nil {
		r.mcpBridge = initSpec.ToolBridge
	}
	// Always re-flow the bridge field on the InitSpec to the canonical
	// supervisor-owned bridge so the runtime starter wires children against
	// it (the test-supplied subsequent bridge is intentionally ignored).
	if r.mcpBridge != nil {
		initSpec.ToolBridge = r.mcpBridge
	}
	starter := newInProcessUnifiedStarter(initSpec, allowedTools)
	if s, ok := starter.(*inProcessUnifiedStarter); ok {
		s.faultEmitter = r.dispatchFault
	}
	r.runtimeStarter = starter
}

// MCPBridge returns the host-scoped MCP tool bridge installed on this
// supervisor. The bridge is captured from the first SetChildMCPConfig call
// and reused across weave-claude restarts. Returns nil if no bridge has
// been installed yet. See QUM-467.
func (r *Real) MCPBridge() backendpkg.ToolBridge {
	return r.mcpBridge
}

func (r *Real) Status(_ context.Context) ([]AgentInfo, error) {
	agents, err := state.ListAgents(r.sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	processAliveByName := make(map[string]*bool)
	inAutonomousTurnByName := make(map[string]bool)
	lastActivityAtByName := make(map[string]time.Time)
	subprocessAliveByName := make(map[string]bool)
	eventbusSubCountByName := make(map[string]int)
	livenessByName := make(map[string]string)
	for _, runtime := range r.runtimeRegistry.List() {
		snap := runtime.Snapshot()
		subprocessAliveByName[snap.Name] = runtime.SubprocessAlive()
		eventbusSubCountByName[snap.Name] = runtime.EventBusSubscriberCount()
		// Derive process_alive from the AgentLiveness projection (QUM-615 M1)
		// so a terminally-faulted runtime whose handle is not yet torn down
		// reports alive=false instead of lying true (QUM-606, invariant 6). The
		// stored base liveness is rendered to From's lifecycle token via
		// livenessToLifecycleString(snap.Liveness) (QUM-627 M6). RuntimeState/
		// DiskStatus are intentionally left empty in M1; later slices (M4/M5) feed
		// them.
		st := liveness.From(liveness.Snapshot{
			Lifecycle:   livenessToLifecycleString(snap.Liveness),
			TerminalErr: runtime.IsTerminallyFaulted(),
			InTurn:      runtime.InTurn(),
			// QUM-722: feed DiskStatus into the projection so Paused/Died
			// resting states surface via Status, not just Unstarted.
			DiskStatus: snap.Status,
		})
		inAutonomousTurnByName[snap.Name] = runtime.InTurn()
		lastActivityAtByName[snap.Name] = runtime.LastActivityAt()
		// QUM-722: surface the unified projection token on the wire so callers
		// (TUI / MCP / tests) see Paused / Died / Stopping / Faulted distinctly.
		livenessByName[snap.Name] = st.String()
		if st.Liveness == liveness.Unstarted {
			continue // leave process_alive absent (nil) — preserves registered/unknown semantics
		}
		alive := liveness.ProcessAlive(st)
		processAliveByName[snap.Name] = &alive
	}

	result := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		lastActivity := lastActivityAtByName[a.Name]
		if lastActivity.IsZero() {
			if _, registered := r.runtimeRegistry.Get(a.Name); !registered {
				entries, err := agentloop.ReadActivityTail(agentloop.ActivityPath(r.sprawlRoot, a.Name), 1)
				if err == nil && len(entries) > 0 {
					lastActivity = entries[len(entries)-1].TS
				}
			}
		}
		liv := livenessByName[a.Name]
		if liv == "" {
			// QUM-722: disk-only agents (no registered runtime) — project
			// liveness from the durable disk Status alone.
			liv = liveness.From(liveness.Snapshot{DiskStatus: a.Status}).String()
		}
		info := AgentInfo{
			Name:               a.Name,
			Type:               a.Type,
			Family:             a.Family,
			Parent:             a.Parent,
			Status:             a.Status,
			Branch:             a.Branch,
			TreePath:           a.TreePath,
			LastReportType:     a.LastReportType,
			LastReportState:    a.LastReportState,
			LastReportMessage:  a.LastReportMessage,
			LastReportDetail:   a.LastReportDetail,
			TotalCostUsd:       sumUsageCostForAgent(r.sprawlRoot, a.Name),
			ProcessAlive:       processAliveByName[a.Name],
			SubprocessAlive:    subprocessAliveByName[a.Name],
			EventbusSubscribed: eventbusSubCountByName[a.Name] > 0,
			EventbusSubCount:   eventbusSubCountByName[a.Name],
			InTurn:             inAutonomousTurnByName[a.Name],
			LastActivityAt:     lastActivity,
			Liveness:           liv,
		}
		if a.Subagent {
			info.Subagent = true
			info.SharedWorktreeWith = a.Parent
		}
		result = append(result, info)
	}
	return result, nil
}

func (r *Real) Delegate(ctx context.Context, agentName, task string, wakeIfOffline bool) error {
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	// QUM-789 lifecycle arc #2: retired/retiring is truly terminal — surface
	// the canonical "no longer running" error.
	if state.IsTerminal(agentState.Status) {
		if err := agentops.TerminalAgentError(r.sprawlRoot, agentName); err != nil {
			return err
		}
	}

	if task == "" {
		return fmt.Errorf("task prompt must not be empty")
	}

	// QUM-789 lifecycle arc #2: Status==complete is revivable via auto-wake
	// — NO wake_if_offline flag required. Wake first so the recipient's
	// first post-wake turn sees the delegate prompt, then continue to the
	// standard task enqueue below.
	if agentState.Status == state.StatusComplete {
		if _, wErr := r.Wake(ctx, agentName, agentpkg.WakeReasonDelegate, task); wErr != nil {
			return wErr
		}
	} else if lv, ok := r.livenessOf(agentName); ok {
		// QUM-726: offline-recoverable gate. For Paused/Killed/Died/Faulted/ResumeFailed, either
		// reject with the canonical error or wake-and-enqueue.
		if lv == liveness.Paused || lv == liveness.Killed || lv == liveness.Died || lv == liveness.Faulted || lv == liveness.ResumeFailed {
			if !wakeIfOffline {
				// QUM-726: canonical error string is byte-pinned in tests; do not reword.
				return fmt.Errorf("Delivery failed: agent %s is %s. Set wake_if_offline: true to wake and deliver.", agentName, lv.String()) //nolint:revive,staticcheck
			}
			if _, wErr := r.Wake(ctx, agentName, agentpkg.WakeReasonDelegate, task); wErr != nil {
				return wErr
			}
		}
	}

	if _, err := state.EnqueueTask(r.sprawlRoot, agentName, task); err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	if runtime, ok := r.runtimeRegistry.Get(agentName); ok {
		runtime.RecordQueuedTask()
		if runtime.Snapshot().Liveness == liveness.Running {
			_ = runtime.NotifyWake()
		}
	}
	return nil
}

func (r *Real) Spawn(ctx context.Context, req SpawnRequest) (*AgentInfo, error) {
	deps := r.spawnDepsForCaller(r.effectiveCaller(ctx))
	st, err := r.spawnFn(deps, req.Family, req.Type, req.Prompt, req.Branch, req.Subagent)
	if err != nil {
		return nil, err
	}
	runtime := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: r.sprawlRoot,
		Agent:      st,
		Starter:    r.runtimeStarter,
	})
	if err := runtime.Start(); err != nil {
		r.rollbackSpawnArtifacts(st.Name)
		return nil, fmt.Errorf("starting runtime for %s: %w", st.Name, err)
	}
	info := &AgentInfo{
		Name:   st.Name,
		Type:   st.Type,
		Family: st.Family,
		Parent: st.Parent,
		Status: st.Status,
		Branch: st.Branch,
	}
	if st.Subagent {
		info.Subagent = true
		info.SharedWorktreeWith = st.Parent
	}
	return info, nil
}

// Merge accepts a `caller` parameter so MCP-invoked merges run with the
// caller agent's identity (rather than the supervisor process's identity).
// Resolution order: explicit `caller` arg, then context CallerIdentity, then
// r.callerName fallback. The resolved identity is plumbed into a per-call
// MergeDeps whose Getenv reports it as SPRAWL_AGENT_IDENTITY so the parent-
// equality check inside agentops.Merge sees the correct caller. See QUM-487.
func (r *Real) Merge(ctx context.Context, caller, agentName, message string, noValidate bool) (*MergeOutcome, error) {
	effective := r.effectiveCallerOr(ctx, caller)

	// Detect contention BEFORE acquiring the sem so we capture who we'd
	// queue behind. See QUM-588.
	r.mergeInflightMu.Lock()
	var behind string
	if r.mergeInflight != nil {
		behind = r.mergeInflight.agentName
	}
	r.mergeInflightMu.Unlock()

	cp := r.composeCheckpoint(calllog.CallID(ctx))
	queueStart := time.Now()
	if behind != "" && cp != nil {
		cp("merge.queued", "line", fmt.Sprintf("behind=%s", behind))
	}

	select {
	case r.mergeSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.mergeSem }()

	queueWait := time.Since(queueStart)

	r.mergeInflightMu.Lock()
	r.mergeInflight = &mergeInflightInfo{agentName: agentName, startedAt: time.Now()}
	r.mergeInflightMu.Unlock()
	defer func() {
		r.mergeInflightMu.Lock()
		r.mergeInflight = nil
		r.mergeInflightMu.Unlock()
	}()

	if behind != "" && cp != nil {
		cp("merge.starting", "line", fmt.Sprintf("waited=%s behind=%s", queueWait.Round(time.Millisecond), behind))
	}

	outcome, err := r.mergeFn(ctx, r.mergeDepsForCaller(ctx, effective), agentName, message, noValidate, false)
	if err != nil {
		return nil, err
	}
	if outcome != nil && behind != "" {
		outcome.QueuedBehind = behind
		outcome.QueueWait = queueWait
	}
	return outcome, nil
}

// Retire accepts a `caller` parameter for the same reason as Merge — see
// QUM-487. Resolution order matches Merge; the resolved identity flows into
// retireDeps.Getenv and is also propagated through cascade recursion so every
// retireFn invocation in the tree runs under the caller's identity.
func (r *Real) Retire(ctx context.Context, caller string, agentName string, mergeFirst, abandon, cascade, noValidate bool) error {
	// Release any AskUserQuestion calls originating from this agent BEFORE
	// state mutation. Cascade recursion will naturally fire this for each
	// descendant as well. (QUM-527 slice 1.)
	r.questions.cancelByAgent(agentName, "agent retired")
	effective := r.effectiveCallerOr(ctx, caller)
	retireDeps := r.retireDepsForCaller(ctx, effective)
	if err := r.reconcileStateFromRegistry(agentName); err != nil {
		return err
	}
	if runtime, ok := r.startedRuntime(agentName); ok {
		children, err := r.listDirectChildren(agentName)
		if err != nil {
			return fmt.Errorf("checking children: %w", err)
		}
		if len(children) > 0 && !cascade {
			names := make([]string, len(children))
			for i, child := range children {
				names[i] = child.Name
			}
			sort.Strings(names)
			return fmt.Errorf("agent %s has %d active children: %s; use --cascade to retire %s and all descendants, or --force to retire %s only (children become orphans)",
				agentName, len(children), strings.Join(names, ", "), agentName, agentName)
		}
		if cascade {
			for _, child := range children {
				if err := r.Retire(ctx, effective, child.Name, false, false, true, noValidate); err != nil {
					return err
				}
			}
		}
		stopCtx, cancel := withRuntimeStopTimeout(ctx)
		defer cancel()
		cp := retireDeps.Checkpoint
		startLabel := "retire.runtime-stop-start"
		doneLabel := "retire.runtime-stop-done"
		if abandon {
			startLabel = "retire.runtime-stop-abandon-start"
			doneLabel = "retire.runtime-stop-abandon-done"
		}
		if cp != nil {
			cp(startLabel, "agent_name", agentName)
		}
		stopStart := time.Now()
		var stopErr error
		if abandon {
			// QUM-600: abandon path skips the polite Session.Interrupt
			// issued by Stop so a wedged stdin writer cannot stall retire.
			stopErr = runtime.StopAbandon(stopCtx)
		} else {
			stopErr = runtime.Stop(stopCtx)
		}
		if cp != nil {
			cp(doneLabel,
				"agent_name", agentName,
				"duration_ms", time.Since(stopStart).Milliseconds(),
				"wait_timeout", runtime.StopWaitTimedOut())
		}
		if stopErr != nil {
			return stopErr
		}
		if err := r.retireFn(ctx, retireDeps, agentName, false /* cascade already handled */, false /* force */, abandon, mergeFirst, true /* yes */, noValidate); err != nil {
			return err
		}
		r.runtimeRegistry.Remove(agentName)
		return nil
	}
	// QUM-739: retire is the legitimate cleanup path for terminal agents;
	// the TerminalAgentError gate that used to live here trapped zombies.
	// Keep the gate on send_message / peek (still callers of TerminalAgentError).
	if err := r.retireFn(ctx, retireDeps, agentName, cascade, false /* force */, abandon, mergeFirst, true /* yes */, noValidate); err != nil {
		if cascade {
			r.reconcileRuntimeTreeFromState(agentName)
		}
		return err
	}
	if cascade {
		r.runtimeRegistry.RemoveTree(agentName)
	} else {
		r.runtimeRegistry.Remove(agentName)
	}
	return nil
}

// Kill is idempotent: if the agent is already gone (state file missing) or
// was already killed, it returns nil. Enter.go's graceful shutdown iterates
// every agent and calls Kill, so transient absence must not fail.
func (r *Real) Kill(ctx context.Context, agentName string) error {
	if err := agentpkg.ValidateName(agentName); err != nil {
		return err
	}

	// Swallow "agent not found" — idempotent shutdown contract.
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// Unknown error reading state — propagate.
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}
	if agentState.Status == "killed" {
		return nil
	}

	// Release any AskUserQuestion calls originating from this agent.
	// (QUM-527 slice 1.)
	r.questions.cancelByAgent(agentName, "agent killed")

	if runtime, ok := r.startedRuntime(agentName); ok {
		stopCtx, cancel := withRuntimeStopTimeout(ctx)
		defer cancel()
		cp := r.composeCheckpoint(calllog.CallID(ctx))
		if cp != nil {
			cp("kill.runtime-stop-start", "agent_name", agentName)
		}
		stopStart := time.Now()
		// QUM-722: Kill is now HARD — flipped from polite Stop to StopAbandon.
		// The previous polite-Stop semantics moved to the new `pause` verb.
		stopErr := runtime.StopAbandon(stopCtx)
		if cp != nil {
			cp("kill.runtime-stop-done",
				"agent_name", agentName,
				"duration_ms", time.Since(stopStart).Milliseconds(),
				"wait_timeout", runtime.StopWaitTimedOut())
		}
		if stopErr != nil {
			return stopErr
		}
		updatedState, err := state.LoadAgent(r.sprawlRoot, agentName)
		if err != nil {
			return fmt.Errorf("agent %q not found: %w", agentName, err)
		}
		updatedState.Status = "killed"
		if err := state.SaveAgent(r.sprawlRoot, updatedState); err != nil {
			return fmt.Errorf("updating agent state: %w", err)
		}
		runtime.SyncAgentState(updatedState)
		return nil
	}
	if err := r.killFn(r.killDeps, agentName, false); err != nil {
		return err
	}
	r.syncRuntimeFromState(agentName)
	return nil
}

// Pause politely stops the named agent at its next turn boundary, preserving
// the transcript so the agent can be `wake`d later. QUM-722.
//
// Outcome:
//   - "paused" — clean pause completed within the timeout.
//   - "escalated_to_kill" — wait timed out, runtime was hard-stopped.
//
// When opts.Cascade is true, descendants are paused first (children-before-
// parent) in parallel via errgroup. Per-child results are collected (no
// first-error-abort). When opts.Cascade is false but children exist, Pause
// returns an error.
//
// AskUserQuestion calls originating from the agent are cancelled at pause
// ENTRY so a parked modal cannot force the timeout escalation.
func (r *Real) Pause(ctx context.Context, agentName string, opts PauseOptions) (*PauseResult, error) {
	if err := agentpkg.ValidateName(agentName); err != nil {
		return nil, err
	}
	start := time.Now()

	// QUM-722 (code-review nit #3): refuse to pause an agent that is
	// currently waking — the wake path is mid-flight tearing down and
	// re-launching the runtime, and racing a Pause against that leaves disk
	// state in an undefined intermediate. Caller must retry after wake
	// completes (the Recovering liveness token name is unchanged — it's a
	// transient projection state).
	if r.runtimeRegistry != nil {
		if rt, ok := r.runtimeRegistry.Get(agentName); ok {
			if rt.Snapshot().Liveness == liveness.Recovering {
				return nil, fmt.Errorf("agent %q is waking; retry pause after wake completes", agentName)
			}
		}
	}

	// Cancel any parked AskUserQuestion calls originating from this agent
	// BEFORE we hit the runtime Pause path — otherwise a pending modal would
	// hold the in-flight turn open until pause_timeout fires.
	r.questions.cancelByAgent(agentName, "agent pausing")

	var cascaded []string
	if opts.Cascade {
		children, err := r.listDirectChildren(agentName)
		if err != nil {
			return nil, fmt.Errorf("checking children: %w", err)
		}
		// Children first — pause in parallel. Collect results; do NOT abort
		// siblings on first error.
		if len(children) > 0 {
			var wg sync.WaitGroup
			var mu sync.Mutex
			for _, child := range children {
				child := child
				wg.Add(1)
				go func() {
					defer wg.Done()
					sub, err := r.Pause(ctx, child.Name, opts)
					mu.Lock()
					defer mu.Unlock()
					cascaded = append(cascaded, child.Name)
					if sub != nil {
						cascaded = append(cascaded, sub.Cascade...)
					}
					if err != nil {
						slog.Warn("supervisor: cascade pause child", "child", child.Name, "err", err)
					}
				}()
			}
			wg.Wait()
		}
	} else {
		// Reject cascade=false when there are still children — orphaning a
		// running subtree under a paused parent is not allowed.
		children, err := r.listDirectChildren(agentName)
		if err != nil {
			return nil, fmt.Errorf("checking children: %w", err)
		}
		if len(children) > 0 {
			names := make([]string, len(children))
			for i, c := range children {
				names[i] = c.Name
			}
			sort.Strings(names)
			return nil, fmt.Errorf("agent %s has %d active children: %s; pass cascade=true to pause the subtree", agentName, len(children), strings.Join(names, ", "))
		}
	}

	outcome := "paused"
	if runtime, ok := r.startedRuntime(agentName); ok {
		stopCtx, cancel := withRuntimeStopTimeout(ctx)
		defer cancel()
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		clean, err := runtime.Pause(stopCtx, timeout)
		if err != nil {
			return nil, err
		}
		if !clean {
			outcome = "escalated_to_kill"
		}
		// Sync runtime snapshot from disk so a subsequent Status call reflects
		// the freshly stamped Paused/Killed token.
		if updated, lErr := state.LoadAgent(r.sprawlRoot, agentName); lErr == nil {
			runtime.SyncAgentState(updated)
		}
	} else {
		// No live runtime — best-effort durable mark. QUM-722
		// (code-review nit #6): do NOT clobber an already-terminal disk
		// status. Mirrors the defensive switch in stopWithFunc /
		// watchHandleExit — Killed/Retired/Died/Faulted carry distinct
		// resting semantics and must not be downgraded to Paused.
		if cur, lErr := state.LoadAgent(r.sprawlRoot, agentName); lErr == nil && cur != nil {
			switch cur.Status {
			case state.StatusKilled, state.StatusRetired, state.StatusRetiring, state.StatusFaulted, state.StatusDied:
				// leave as-is; terminal-ish state wins over Paused.
			default:
				cur.Status = state.StatusPaused
				if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
					return nil, fmt.Errorf("persisting paused state: %w", sErr)
				}
			}
		}
	}

	return &PauseResult{
		Outcome: outcome,
		WaitMs:  time.Since(start).Milliseconds(),
		Cascade: cascaded,
	}, nil
}

// Wake dispatches the named agent's runtime Wake path (QUM-724, renamed
// from Recover/QUM-601 with expanded scope). On success, fires the
// BackendWokenEmitter (if installed) so the TUI can clear its per-agent
// fault sticker. ErrWakeNotNeeded (session healthy) is propagated to the
// caller verbatim — callers (notably the MCP wake tool) treat it as a
// success-with-no-op.
func (r *Real) Wake(ctx context.Context, agentName string, reason agentpkg.WakeReason, injectedBody string) (*WakeResult, error) {
	if err := agentpkg.ValidateName(agentName); err != nil {
		return nil, err
	}
	if r.runtimeRegistry == nil {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}
	runtime, ok := r.runtimeRegistry.Get(agentName)
	if !ok {
		// QUM-818: registry miss. On restart, RecoverAgents intentionally does
		// not auto-resume (and therefore never Ensures) agents whose last
		// report was `complete` — and never re-registers other non-auto-resumed
		// offline classes either. Repopulate the runtime from on-disk state so
		// a parked agent can still be revived. Ensure is idempotent and does
		// not start a process. Terminal {retired, retiring} agents are not
		// revivable, so keep returning "not found" for them.
		st, lErr := state.LoadAgent(r.sprawlRoot, agentName)
		if lErr != nil || state.IsTerminal(st.Status) {
			return nil, fmt.Errorf("agent %q not found", agentName)
		}
		runtime = r.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: r.sprawlRoot,
			Agent:      st,
			Starter:    r.runtimeStarter,
		})
	}
	// QUM-726: capture the projected previous-state token BEFORE the wake
	// path tears the runtime down, so the wake-prompt template can interpolate
	// it. Falls back to the empty string when the projection is unavailable.
	previousState := ""
	if lv, ok := r.livenessOf(agentName); ok {
		previousState = lv.String()
	}
	// QUM-789: StatusComplete projects to liveness.Unstarted (intentional;
	// see runtime.go SyncAgentState). For the wake prompt, prefer the more
	// informative disk-status token so the recipient learns it was woken
	// from a `complete` resting state rather than the bland "unstarted".
	if st, lErr := state.LoadAgent(r.sprawlRoot, agentName); lErr == nil && st.Status == state.StatusComplete {
		previousState = state.StatusComplete
	}
	injection := agentpkg.BuildWakePrompt(reason, previousState, injectedBody)
	id := calllog.CallID(ctx)
	cp := r.composeCheckpoint(id)
	if cp != nil {
		cp("wake.start", "agent_name", agentName)
	}
	// QUM-611: proactively release any AskUserQuestion calls originating
	// from this agent BEFORE the runtime tears down its abandoned session.
	// Today the question cleanup is incidental via drainInflight on the
	// reader exit (cancels every inflight bridgeCtx, which the question
	// queue observes via ctx.Done()). That works but is fragile — a future
	// refactor moving teardown order around would leak the pending
	// question. Doing it explicitly here is cheap, idempotent
	// (cancelByAgent is a no-op on done entries), and removes the implicit
	// dependency on drainInflight ordering.
	r.questions.cancelByAgent(agentName, "agent waking")
	res, err := runtime.Wake(ctx, injection)
	if cp != nil {
		var msg string
		if err != nil {
			msg = err.Error()
		}
		cp("wake.done", "agent_name", agentName, "err", msg)
	}
	if err != nil {
		return nil, err
	}
	if emit := r.wokenEmitter; emit != nil {
		emit(agentName)
	}
	return res, nil
}

// InduceTerminalFault is the QUM-606 test-only seam: forces the named
// agent's backend session into the terminally-faulted state with the
// supplied error. Exposed via the build-tag-gated `_test_induce_wedge`
// MCP tool so the live-recover e2e harness can drive a deterministic
// SubscriberWedge / HangTimeout fault without inducing a real frame
// burst or stalled reader. Production callers MUST NOT invoke this.
func (r *Real) InduceTerminalFault(_ context.Context, agentName string, err error) error {
	if err := agentpkg.ValidateName(agentName); err != nil {
		return err
	}
	if r.runtimeRegistry == nil {
		return fmt.Errorf("agent %q not found", agentName)
	}
	runtime, ok := r.runtimeRegistry.Get(agentName)
	if !ok {
		return fmt.Errorf("agent %q not found", agentName)
	}
	return runtime.InduceTerminalFault(err)
}

// RecoverAgents iterates persisted agent state and resumes every non-terminal
// child whose worktree still exists. Skips the root caller. Walks the tree
// BFS-from-root so parents are resumed before children (defense in depth;
// maildir absorbs ordering gaps). Returns counts and per-agent errors. Does
// not abort the loop on per-agent failure. QUM-372.
//
// NOTE: RecoverAgents is sprawl-enter STARTUP auto-resume — distinct from the
// QUM-724 `wake` MCP verb (Real.Wake). The two share no code path; the rename
// of recover→wake (QUM-724) deliberately leaves this startup helper named
// RecoverAgents because it has nothing to do with the operator-facing
// lifecycle verb.
func (r *Real) RecoverAgents(_ context.Context) (resumed int, failed int, errs []error) {
	if r.runtimeRegistry == nil || r.runtimeStarter == nil {
		return 0, 0, nil
	}

	// QUM-668 Problem A: quarantine orphan agent dirs (dirs without a sibling
	// <name>.json). Safe, reversible — moves them under .sprawl/agents/_orphaned/<ts>/.
	r.quarantineOrphanAgentDirs()

	// QUM-668 Problem B: settle-pass. Belt-and-suspenders for any agent whose
	// LAST report was terminal (complete/failure) but whose persisted Status is
	// still "active" — flip to the terminal liveness on disk so the resume
	// filter and downstream MCP tools see a consistent picture. Only acts when
	// Status==active AND no runtime is registered for the agent (the latter is
	// trivially true at this point but stays defensive).
	if settleAgents, lerr := state.ListAgents(r.sprawlRoot); lerr == nil {
		for _, a := range settleAgents {
			if a == nil || a.Status != state.StatusActive {
				continue
			}
			var terminal string
			switch a.LastReportState {
			case agentops.ReportStateComplete:
				// QUM-787: an agent that reported complete settles to
				// StatusComplete (revivable). StatusStopped is no longer
				// a write target.
				terminal = state.StatusComplete
			case agentops.ReportStateFailure:
				terminal = state.StatusFaulted
			default:
				continue
			}
			if _, registered := r.runtimeRegistry.Get(a.Name); registered {
				continue
			}
			a.Status = terminal
			if sErr := state.SaveAgent(r.sprawlRoot, a); sErr != nil {
				slog.Warn("supervisor: RecoverAgents settle-pass save", "agent", a.Name, "err", sErr)
			}
		}
	}

	agents, err := state.ListAgents(r.sprawlRoot)
	if err != nil {
		return 0, 0, []error{fmt.Errorf("list agents: %w", err)}
	}
	var eligible []*state.AgentState
	for _, a := range agents {
		if a == nil || a.Name == r.callerName {
			continue
		}
		// QUM-625 (slice M4, Q2 ruling): resume-eligibility keys off the LIVENESS
		// projection, NEVER the raw Status string. Routing through
		// liveness.LivenessFromStatus keeps the two axes literally separate — a
		// future Status value or projection tweak can't silently re-conflate
		// liveness with outcome (the exact bug this refactor exists to kill).
		// Accept-set is {Suspended, Running}: Running covers disk "active"/"running"
		// crash-survivors (process died without a clean Shutdown→suspend), so they
		// still auto-resume. Faulted/Stopped/ResumeFailed/Killed/Retired/Retiring
		// (and any unrecognized status) are not auto-resumed. QUM-723: Paused is
		// also excluded — it is an explicit user-initiated rest state.
		lv, ok := liveness.LivenessFromStatus(a.Status)
		if !ok || (lv != liveness.Suspended && lv != liveness.Running) {
			continue
		}
		// QUM-723: `paused` is an explicit user-initiated rest state — paused agents
		// must NEVER auto-resume on sprawl-enter restart; they only revive via the
		// explicit `wake` verb (Sub-3 of QUM-708). The accept-set above
		// ({Suspended, Running}) already excludes Paused by construction, but we
		// add an explicit guard here so a future projection tweak can't silently
		// regress this contract.
		if lv == liveness.Paused {
			continue
		}
		// Terminal-outcome exclusion on the OUTCOME axis: a completed or
		// failed agent is not auto-resumed. This replaces the old implicit
		// done-exclusion (Status=="done" no longer occurs after the axis
		// split). QUM-668: failure is also terminal — extend exclusion.
		if a.LastReportState == agentops.ReportStateComplete || a.LastReportState == agentops.ReportStateFailure {
			continue
		}
		if a.Worktree == "" {
			continue
		}
		if _, statErr := os.Stat(a.Worktree); statErr != nil {
			continue
		}
		eligible = append(eligible, a)
	}
	ordered := bfsByParent(eligible, r.callerName)
	for _, a := range ordered {
		agent := a
		rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: r.sprawlRoot,
			Agent:      agent,
			Starter:    r.runtimeStarter,
		})
		onResumeFailure := func() {
			cur, lErr := state.LoadAgent(r.sprawlRoot, agent.Name)
			if lErr != nil {
				slog.Warn("supervisor: RecoverAgents OnResumeFailure load", "agent", agent.Name, "err", lErr)
				return
			}
			cur.Status = state.StatusResumeFailed
			if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
				slog.Warn("supervisor: RecoverAgents OnResumeFailure save", "agent", agent.Name, "err", sErr)
				return
			}
			rt.SyncAgentState(cur)
		}
		if err := rt.StartResume(agentpkg.RestartInjectionPrompt, onResumeFailure); err != nil {
			failed++
			errs = append(errs, fmt.Errorf("resume %q: %w", agent.Name, err))
			continue
		}
		// success — flip to active and persist (idempotent SessionID write).
		cur, lErr := state.LoadAgent(r.sprawlRoot, agent.Name)
		if lErr != nil {
			slog.Warn("supervisor: RecoverAgents post-start load", "agent", agent.Name, "err", lErr)
			resumed++
			continue
		}
		cur.Status = state.StatusActive
		if sid := rt.Snapshot().SessionID; sid != "" {
			cur.SessionID = sid
		}
		if sErr := state.SaveAgent(r.sprawlRoot, cur); sErr != nil {
			slog.Warn("supervisor: RecoverAgents post-start save", "agent", agent.Name, "err", sErr)
		}
		rt.SyncAgentState(cur)
		// QUM-605: drain any maildir entries that landed while the agent was
		// suspended so the resumed session's first turn picks them up. Without
		// this, async send_message deliveries sit forever until something else
		// wakes the agent.
		if wErr := rt.WakeForDelivery(); wErr != nil {
			slog.Warn("supervisor: RecoverAgents WakeForDelivery", "agent", agent.Name, "err", wErr)
		}
		resumed++
	}
	return resumed, failed, errs
}

// quarantineOrphanAgentDirs moves any .sprawl/agents/<name>/ directory that
// lacks a sibling <name>.json file to .sprawl/agents/_orphaned/<UTC-ts>/<name>/.
// One warning is logged per quarantined orphan. Errors are logged and
// swallowed — orphan quarantine must not block recovery. QUM-668 Problem A.
func (r *Real) quarantineOrphanAgentDirs() {
	orphans, err := agentops.ScanOrphans(agentops.GCDeps{
		SprawlRoot: r.sprawlRoot,
		Now:        time.Now,
	})
	if err != nil {
		slog.Warn("supervisor: RecoverAgents ScanOrphans", "err", err)
		return
	}
	if len(orphans) == 0 {
		return
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	quarantineRoot := filepath.Join(state.AgentsDir(r.sprawlRoot), "_orphaned", ts)
	if err := os.MkdirAll(quarantineRoot, 0o750); err != nil {
		slog.Warn("supervisor: orphan quarantine mkdir", "dir", quarantineRoot, "err", err)
		return
	}
	for _, o := range orphans {
		dst := filepath.Join(quarantineRoot, o.Name)
		if err := os.Rename(o.DirPath, dst); err != nil {
			slog.Warn("supervisor: orphan quarantine rename", "agent", o.Name, "from", o.DirPath, "to", dst, "err", err)
			continue
		}
		slog.Warn("supervisor: quarantined orphan agent dir", "agent", o.Name, "from", o.DirPath, "to", dst)
	}
}

// bfsByParent orders agents BFS-from-root, parents before children. Any
// agent whose parent isn't in the eligible set (orphaned or grandchild whose
// parent already terminal) lands at the tail in input order.
func bfsByParent(eligible []*state.AgentState, root string) []*state.AgentState {
	byParent := make(map[string][]*state.AgentState, len(eligible))
	for _, a := range eligible {
		byParent[a.Parent] = append(byParent[a.Parent], a)
	}
	visited := make(map[string]bool, len(eligible))
	var out []*state.AgentState
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		children := byParent[cur]
		sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
		for _, child := range children {
			if visited[child.Name] {
				continue
			}
			visited[child.Name] = true
			out = append(out, child)
			queue = append(queue, child.Name)
		}
	}
	for _, a := range eligible {
		if !visited[a.Name] {
			out = append(out, a)
		}
	}
	return out
}

func (r *Real) Shutdown(ctx context.Context) error {
	// QUM-730: stop the heartbeat first so it can't fire a stray nudge
	// against a runtime that's about to be torn down.
	if r.heartbeat != nil {
		r.heartbeat.Stop()
	}
	// Release any in-flight AskUserQuestion callers with OutcomeSessionEnded
	// BEFORE tearing down runtimes. (QUM-527 slice 1.)
	r.questions.closeAll(OutcomeSessionEnded, "supervisor shutdown")

	// QUM-722: graceful shutdown is pause-then-kill for in-turn agents,
	// preserving the legacy "auto-suspend on clean exit" contract for idle
	// agents (existing transition matrix). Idle agents flow through the
	// classic Stop → suspended path; in-turn agents flow through Pause with a
	// bounded budget that escalates to StopAbandon → killed if the turn
	// doesn't drain in time. Iteration is parallel so the wall-clock floor
	// is one budget, not N.
	const pauseBudget = 5 * time.Second
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for _, runtime := range r.runtimeRegistry.List() {
		snap := runtime.Snapshot()
		if snap.Liveness != liveness.Running {
			continue
		}
		runtime := runtime
		name := snap.Name
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopCtx, cancel := withRuntimeStopTimeout(ctx)
			defer cancel()
			// QUM-787: capture pre-Stop disk Status so the post-Stop
			// promotion-to-suspended can distinguish "agent was already
			// in a terminal-ish resting state on disk" (preserve) from
			// "Stop just landed StatusFaulted because there was no
			// complete report" (promote to suspended). Without this
			// snapshot the new stopWithFunc behavior would mask the
			// legacy active→suspended Shutdown contract.
			preStop, _ := state.LoadAgent(r.sprawlRoot, name)
			if runtime.InTurn() {
				// In-turn → use Pause (bounded escalation to killed).
				if _, pErr := runtime.Pause(stopCtx, pauseBudget); pErr != nil {
					_ = runtime.StopAbandon(stopCtx)
				}
			} else {
				// Idle → classic polite Stop (fast). Falls through to the
				// status-rewrite block below which stamps "suspended" so
				// auto-resume picks it up on next launch.
				if sErr := runtime.Stop(stopCtx); sErr != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = sErr
					}
					errMu.Unlock()
					return
				}
			}
			agentState, lErr := state.LoadAgent(r.sprawlRoot, name)
			if lErr != nil {
				return
			}
			preserve := false
			switch agentState.Status {
			case state.StatusKilled, state.StatusRetired, state.StatusRetiring, state.StatusPaused, state.StatusDied, state.StatusComplete:
				// QUM-625 (slice M4): leave terminal-ish states as-is.
				// QUM-722: also leave Paused / Died as-is — they carry
				// distinct resting semantics. QUM-787: leave Complete
				// as-is — an agent that reported state=complete finished
				// its work and should not be auto-resumed.
				preserve = true
			case state.StatusFaulted:
				// QUM-787: preserve only if disk was ALREADY faulted
				// pre-Stop (genuine fault). If Stop is what landed the
				// faulted Status (clean idle-Stop with no complete
				// report under the new stopWithFunc semantics), promote
				// to suspended so the active→suspended Shutdown
				// contract still holds.
				if preStop != nil && preStop.Status == state.StatusFaulted {
					preserve = true
				}
			}
			if !preserve {
				agentState.Status = state.StatusSuspended
				if sErr := state.SaveAgent(r.sprawlRoot, agentState); sErr != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("updating agent state: %w", sErr)
					}
					errMu.Unlock()
					return
				}
			}
			runtime.SyncAgentState(agentState)
		}()
	}
	wg.Wait()
	return firstErr
}

// Handoff persists the given summary as a session memory file (with
// Handoff=true) and writes the handoff-signal file. On success it fires
// HandoffRequested via a non-blocking send; consumers teardown+restart
// asynchronously so the caller (the MCP tool) returns immediately.
//
// Mirrors the logic in cmd/handoff.go:runHandoff, minus stdin parsing and
// the root-agent env check (the MCP tool is only wired into the weave root's
// allowlist, which is the only caller).
func (r *Real) Handoff(_ context.Context, summary string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("handoff summary must not be empty")
	}

	sessionID, err := r.handoffReadLastSessionID(r.sprawlRoot)
	if err != nil {
		return fmt.Errorf("reading session ID: %w", err)
	}
	if sessionID == "" {
		return fmt.Errorf("no session ID found; .sprawl/memory/last-session-id is missing or empty")
	}

	agents, err := r.handoffListAgents(r.sprawlRoot)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}
	var agentNames []string
	for _, a := range agents {
		agentNames = append(agentNames, a.Name)
	}

	session := memory.Session{
		SessionID:    sessionID,
		Timestamp:    r.handoffNow().UTC(),
		Handoff:      true,
		AgentsActive: agentNames,
	}
	if err := r.handoffWriteSessionSummary(r.sprawlRoot, session, summary); err != nil {
		return fmt.Errorf("writing session summary: %w", err)
	}

	if err := r.handoffWriteSignalFile(r.sprawlRoot); err != nil {
		return fmt.Errorf("writing handoff signal: %w", err)
	}

	select {
	case r.handoffCh <- struct{}{}:
	default:
		// Channel already has a pending signal — that's fine. The consumer
		// will pick up the restart on its next drain.
	}
	return nil
}

// HandoffRequested returns the signal channel. See Handoff.
func (r *Real) HandoffRequested() <-chan struct{} {
	return r.handoffCh
}

// CallerName returns the identity this supervisor stamps into children's
// Parent field on Spawn and uses as the "From" in send-async/send-interrupt
// deliveries. Exposed for tests (see QUM-333 regression guard).
func (r *Real) CallerName() string {
	return r.callerName
}

// composeCheckpoint builds the Checkpoint hook installed on per-call merge
// and retire deps. It fans out into BOTH the JSONL call log (when a callLog
// logger and a non-empty call_id are present) AND the host TUI progress
// emitter (QUM-497). The combined closure is safe with id=="" — it just
// drops the call-log half. Returns nil only when there's nothing to do, so
// callers can install the result unconditionally.
func (r *Real) composeCheckpoint(id string) func(step string, kv ...any) {
	var logFn func(step string, kv ...any)
	if r.logger != nil && id != "" {
		logFn = r.logger.CheckpointFn(id)
	}
	emit := r.progressEmitter
	vemit := r.validateEmitter
	if logFn == nil && emit == nil && vemit == nil {
		return nil
	}
	return func(step string, kv ...any) {
		if logFn != nil {
			logFn(step, kv...)
		}
		if emit != nil && id != "" {
			emit(id, step, extractKVLine(kv))
		}
		if vemit != nil && id != "" && strings.HasPrefix(step, "merge.") {
			vemit(id, step, kvToMap(kv))
		}
	}
}

// kvToMap decodes the flat ...any kv slice (k1, v1, k2, v2, ...) into a
// string-keyed map. Non-string keys and values are skipped. Used by the
// validateEmitter fan-out path (QUM-588).
func kvToMap(kv []any) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	out := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		v, ok := kv[i+1].(string)
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// extractKVLine returns the value of the "line" key from a flat kv slice,
// or empty string if absent / malformed. Used to pull the per-line tail
// out of merge's "merge.validate-line" checkpoint payload. (QUM-497)
func extractKVLine(kv []any) string {
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok || k != "line" {
			continue
		}
		if v, ok := kv[i+1].(string); ok {
			return v
		}
	}
	return ""
}

// effectiveCaller returns the caller identity from context if set (child agent
// MCP calls), otherwise falls back to the static r.callerName. This enables
// the shared supervisor to act on behalf of the correct child agent when
// processing MCP tool calls (QUM-387).
func (r *Real) effectiveCaller(ctx context.Context) string {
	if override := backendpkg.CallerIdentity(ctx); override != "" {
		return override
	}
	return r.callerName
}

// effectiveCallerOr resolves the effective caller identity for Merge/Retire,
// preferring an explicit `caller` argument over the context override and
// finally falling back to r.callerName. See QUM-487.
func (r *Real) effectiveCallerOr(ctx context.Context, caller string) string {
	if caller != "" {
		return caller
	}
	if override := backendpkg.CallerIdentity(ctx); override != "" {
		return override
	}
	return r.callerName
}

// mergeDepsForCaller returns a copy of r.mergeDeps with Getenv overridden so
// SPRAWL_AGENT_IDENTITY reflects the effective caller. Mirrors the pattern
// used by spawnDepsForCaller (QUM-384). See QUM-487.
func (r *Real) mergeDepsForCaller(ctx context.Context, caller string) *agentops.MergeDeps {
	deps := *r.mergeDeps // shallow copy
	origGetenv := r.mergeDeps.Getenv
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_AGENT_IDENTITY" {
			return caller
		}
		if origGetenv != nil {
			return origGetenv(key)
		}
		return os.Getenv(key)
	}
	id := calllog.CallID(ctx)
	deps.Checkpoint = r.composeCheckpoint(id)
	return &deps
}

// retireDepsForCaller returns a copy of r.retireDeps with Getenv overridden so
// SPRAWL_AGENT_IDENTITY reflects the effective caller. See QUM-487.
func (r *Real) retireDepsForCaller(ctx context.Context, caller string) *agentops.RetireDeps {
	deps := *r.retireDeps // shallow copy
	origGetenv := r.retireDeps.Getenv
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_AGENT_IDENTITY" {
			return caller
		}
		if origGetenv != nil {
			return origGetenv(key)
		}
		return os.Getenv(key)
	}
	id := calllog.CallID(ctx)
	deps.Checkpoint = r.composeCheckpoint(id)
	return &deps
}

// spawnDepsForCaller returns a copy of r.spawnDeps with Getenv overridden so
// that SPRAWL_AGENT_IDENTITY reflects the effective caller (the agent whose
// MCP tool call triggered the spawn), making child spawns get the correct
// Parent linkage. See QUM-384. Also overrides SPRAWL_TREE_PATH using the
// caller's persisted state.TreePath so grandchild spawns get a tree path
// rooted at the actual caller rather than the supervisor's process env
// (QUM-416).
func (r *Real) spawnDepsForCaller(caller string) *agentops.SpawnDeps {
	deps := *r.spawnDeps // shallow copy
	gitFn := r.gitRevParseHEAD
	if gitFn == nil {
		gitFn = realGitRevParseHEAD
	}
	var callerTreePath string
	var callerWorktree string
	if st, err := state.LoadAgent(r.sprawlRoot, caller); err == nil && st != nil {
		callerTreePath = st.TreePath
		callerWorktree = st.Worktree
	}
	deps.ResolveBase = func(_, _ string) (string, error) {
		if callerWorktree == "" {
			return "", nil
		}
		return gitFn(callerWorktree)
	}
	deps.Getenv = func(key string) string {
		switch key {
		case "SPRAWL_AGENT_IDENTITY":
			return caller
		case "SPRAWL_ROOT":
			return r.sprawlRoot
		case "SPRAWL_TREE_PATH":
			if callerTreePath != "" {
				return callerTreePath
			}
			return os.Getenv(key)
		default:
			return os.Getenv(key)
		}
	}
	return &deps
}

// PeekActivity reads the tail of the agent's activity.ndjson file and
// returns the last `tail` entries. A missing file yields an empty slice.
// tail ≤ 0 returns all available entries.
//
// QUM-331: entries with TS earlier than the agent's CreatedAt are filtered
// out before tail truncation. The on-disk activity.ndjson is append-only and
// shared across spawns that reuse a name, so without this filter a respawned
// agent's panel would render tool calls from the prior incarnation. Missing
// state file or unparseable CreatedAt → no filter (fail-open: better to show
// stale entries than to hide a live agent's activity).
func (r *Real) PeekActivity(_ context.Context, agentName string, tail int) ([]agentloop.ActivityEntry, error) {
	if err := agentpkg.ValidateName(agentName); err != nil {
		return nil, err
	}
	path := agentloop.ActivityPath(r.sprawlRoot, agentName)
	// Read all entries; we apply tail AFTER the CreatedAt filter so a tail
	// window isn't consumed by stale pre-incarnation entries.
	entries, err := agentloop.ReadActivityFile(path, 0)
	if err != nil {
		return nil, fmt.Errorf("reading activity for %q: %w", agentName, err)
	}

	if createdAt, ok := agentCreatedAt(r.sprawlRoot, agentName); ok {
		filtered := entries[:0]
		for _, e := range entries {
			if !e.TS.Before(createdAt) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if tail > 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
	}
	return entries, nil
}

// agentCreatedAt loads the agent's persisted CreatedAt and returns it parsed
// as RFC3339. Returns ok=false when the state file is missing or the
// timestamp is unparseable, so callers can fall back to no filtering.
func agentCreatedAt(sprawlRoot, agentName string) (time.Time, bool) {
	st, err := state.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return time.Time{}, false
	}
	if st.CreatedAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, st.CreatedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// SendMessage is the canonical messaging tool (QUM-550) that replaces
// send_async + send_interrupt. interrupt=false: cooperative wake, ClassAsync,
// no Session.Interrupt. interrupt=true: force preempt, ClassInterrupt, gated
// to ancestor-only per §8.5.
func (r *Real) SendMessage(ctx context.Context, to, body string, interrupt, wakeIfOffline bool) (*SendMessageResult, error) {
	if err := agentpkg.ValidateName(to); err != nil {
		return nil, err
	}
	agentState, err := state.LoadAgent(r.sprawlRoot, to)
	if err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", to, err)
	}
	caller := r.effectiveCaller(ctx)
	// currentLv/lvOK are captured ONCE here pre-Wake; the deadFallback block
	// below intentionally consults this pre-Wake snapshot (a post-Wake agent
	// is no longer Died, so the route-up walk would skip otherwise).
	currentLv, lvOK := r.livenessOf(to)
	switch {
	// QUM-789 lifecycle arc #2: Status==complete is revivable via auto-wake
	// — NO wake_if_offline flag required. Wake first so the recipient's
	// first post-wake turn sees the send_message prompt, then continue to
	// the interrupt gate + persistence below. Bypasses the offline-gate /
	// TerminalAgentError branches since complete is not terminal.
	case agentState.Status == state.StatusComplete:
		if _, wErr := r.Wake(ctx, to, agentpkg.WakeReasonSendMessage, body); wErr != nil {
			return nil, wErr
		}
	// QUM-726: offline-recoverable gate. For Paused/Killed/Faulted/ResumeFailed, either
	// reject with the canonical error or wake-and-deliver. For Died, defer to
	// the QUM-725 route-up walk below; only if that walk fails (no live
	// ancestor reachable) does the canonical error / wake fallback fire.
	case lvOK && (currentLv == liveness.Paused || currentLv == liveness.Killed || currentLv == liveness.Faulted || currentLv == liveness.ResumeFailed):
		if !wakeIfOffline {
			// QUM-726: canonical error string is byte-pinned in tests; do not reword.
			return nil, fmt.Errorf("Delivery failed: agent %s is %s. Set wake_if_offline: true to wake and deliver.", to, currentLv.String()) //nolint:revive,staticcheck
		}
		if _, wErr := r.Wake(ctx, to, agentpkg.WakeReasonSendMessage, body); wErr != nil {
			return nil, wErr
		}
	default:
		// QUM-789: TerminalAgentError now fires only for retired/retiring.
		if _, ok := r.startedRuntime(to); !ok {
			if err := agentops.TerminalAgentError(r.sprawlRoot, to); err != nil {
				return nil, err
			}
		}
	}
	if interrupt {
		if caller == "" {
			return nil, fmt.Errorf("send_message: caller identity unknown; refusing to send")
		}
		if caller == to {
			return nil, fmt.Errorf("send_message: cannot interrupt self")
		}
		// QUM-725: the §8.5 ancestor gate fires against the ORIGINAL `to`
		// (caller intent), NOT the route-up target. Otherwise a sibling that
		// fails the gate would be silently rerouted up to a shared ancestor.
		ok, err := isAncestor(r.sprawlRoot, caller, to)
		if err != nil {
			return nil, fmt.Errorf("checking ancestry: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("send_message: %q is not an ancestor of %q (parent→descendants only per §8.5)", caller, to)
		}
	}

	// QUM-725: if the target is Died, walk up to the first live ancestor and
	// wrap the body so the recipient can distinguish a routed-up notification
	// from a direct message. The §8.5 gate above intentionally still checks
	// the ORIGINAL `to`.
	originalTo := to
	liveAncestor, deadChain, walkErr := WalkDeadAncestors(to, r.livenessOf, r.parentOf)
	// QUM-726: Died fallback. When route-up cannot resolve to a genuinely-
	// known-live ancestor (walkErr, OR liveAncestor unknown to the probe), the
	// offline-gate semantics fire: canonical error or wake-the-original.
	deadFallback := false
	if lvOK && currentLv == liveness.Died {
		if walkErr != nil {
			deadFallback = true
		} else if deadChain != nil {
			if lv, ok := r.livenessOf(liveAncestor); !ok || lv == liveness.Died {
				deadFallback = true
			}
		}
	}
	if deadFallback {
		if !wakeIfOffline {
			// QUM-726: canonical error string is byte-pinned in tests; do not reword.
			return nil, fmt.Errorf("Delivery failed: agent %s is %s. Set wake_if_offline: true to wake and deliver.", originalTo, currentLv.String()) //nolint:revive,staticcheck
		}
		if _, wErr := r.Wake(ctx, originalTo, agentpkg.WakeReasonSendMessage, body); wErr != nil {
			return nil, wErr
		}
		// Wake succeeded — fall through to normal persistence against the
		// original recipient.
		to = originalTo
		deadChain = nil
	} else if walkErr != nil {
		return nil, walkErr
	}
	if deadChain != nil {
		to = liveAncestor
		body = inboxprompt.WrapForDeadTarget(caller, originalTo, deadChain, body)
	}

	runtime, runtimeBacked := r.startedRuntime(to)

	shortID, err := messages.Send(r.sprawlRoot, caller, to, "", body)
	if err != nil {
		return nil, err
	}

	class := agentloop.ClassAsync
	if interrupt {
		class = agentloop.ClassInterrupt
	}
	entry, err := agentloop.Enqueue(r.sprawlRoot, to, agentloop.Entry{
		ShortID: shortID,
		Class:   class,
		From:    caller,
		Subject: "",
		Body:    body,
	})
	if err != nil {
		return nil, fmt.Errorf("enqueuing message: %w", err)
	}
	// QUM-821: both interrupt=true and interrupt=false deliver via the same
	// cooperative wake. Urgency for interrupt=true is carried by the enqueued
	// ClassInterrupt entry, which drainPendingToStdin writes at priority `now`
	// (cancel-and-replace). The bare interrupt frame is reserved for Esc-abort.
	if runtimeBacked {
		_ = runtime.WakeForDelivery()
	}

	return &SendMessageResult{
		MessageID:   entry.ID,
		QueuedAt:    entry.EnqueuedAt,
		Interrupted: interrupt,
	}, nil
}

// livenessOf is the QUM-725 LivenessProbe used by WalkDeadAncestors. It
// resolves liveness from the runtime registry first (briefly acquires registry
// mu via Get, then runtime mu via Snapshot — never holding both); falls back
// to disk-only projection via state.LoadAgent when no runtime is registered.
// Returns ok=false when the agent is unknown to both sources.
//
// Lock-order (QUM-615 R5): registry.Get → snapshot read; no other locks
// crossed; no wake/interrupt invoked under any acquired lock.
func (r *Real) livenessOf(name string) (liveness.AgentLiveness, bool) {
	if r.runtimeRegistry != nil {
		if rt, ok := r.runtimeRegistry.Get(name); ok {
			snap := rt.Snapshot()
			st := liveness.From(liveness.Snapshot{
				Lifecycle:   livenessToLifecycleString(snap.Liveness),
				TerminalErr: rt.IsTerminallyFaulted(),
				InTurn:      rt.InTurn(),
				DiskStatus:  snap.Status,
			})
			return st.Liveness, true
		}
	}
	st, err := state.LoadAgent(r.sprawlRoot, name)
	if err != nil {
		return 0, false
	}
	return liveness.From(liveness.Snapshot{DiskStatus: st.Status}).Liveness, true
}

// parentOf is the QUM-725 ParentLookup used by WalkDeadAncestors. Reads the
// persisted Parent field. An unknown agent surfaces as a propagated error.
func (r *Real) parentOf(name string) (string, error) {
	st, err := state.LoadAgent(r.sprawlRoot, name)
	if err != nil {
		return "", fmt.Errorf("loading agent %q: %w", name, err)
	}
	return st.Parent, nil
}

// isAncestor reports whether `maybeAncestor` appears anywhere in `agent`'s
// parent chain. Returns true iff maybeAncestor == agent.Parent, or a parent
// of that parent, and so on up to the root. The root agent (empty Parent)
// terminates the walk. A depth cap (16) guards against accidental cycles in
// state files.
func isAncestor(sprawlRoot, maybeAncestor, agentName string) (bool, error) {
	if maybeAncestor == "" || agentName == "" {
		return false, nil
	}
	current := agentName
	for depth := 0; depth < 16; depth++ {
		st, err := state.LoadAgent(sprawlRoot, current)
		if err != nil {
			return false, err
		}
		if st.Parent == "" {
			return false, nil
		}
		if st.Parent == maybeAncestor {
			return true, nil
		}
		current = st.Parent
	}
	return false, fmt.Errorf("parent chain exceeds 16 levels starting from %q", agentName)
}

// Peek loads the agent's state plus the tail of its activity ring.
// A tail ≤ 0 defaults to 20; the caller should clamp the upper bound.
func (r *Real) Peek(ctx context.Context, agentName string, tail int) (*PeekResult, error) {
	if err := agentpkg.ValidateName(agentName); err != nil {
		return nil, err
	}
	st, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", agentName, err)
	}
	if _, ok := r.startedRuntime(agentName); !ok {
		if err := agentops.TerminalAgentError(r.sprawlRoot, agentName); err != nil {
			return nil, err
		}
	}
	if tail <= 0 {
		tail = 20
	}
	activity, err := r.PeekActivity(ctx, agentName, tail)
	if err != nil {
		return nil, err
	}
	if activity == nil {
		activity = []agentloop.ActivityEntry{}
	}
	// QUM-585: query the live runtime (if any) for whether its backend
	// Session is currently in an autonomous turn. Defaults to false when no
	// runtime is registered.
	inAutonomousTurn := false
	var livenessTok string
	if rt, ok := r.runtimeRegistry.Get(agentName); ok {
		inAutonomousTurn = rt.InTurn()
		snap := rt.Snapshot()
		livenessTok = liveness.From(liveness.Snapshot{
			Lifecycle:   livenessToLifecycleString(snap.Liveness),
			TerminalErr: rt.IsTerminallyFaulted(),
			InTurn:      rt.InTurn(),
			DiskStatus:  st.Status,
		}).String()
	} else {
		// QUM-722: disk-only — project from durable Status.
		livenessTok = liveness.From(liveness.Snapshot{DiskStatus: st.Status}).String()
	}

	pr := &PeekResult{
		Status: st.Status,
		LastReport: LastReport{
			Type:    st.LastReportType,
			Message: st.LastReportMessage,
			At:      st.LastReportAt,
			State:   st.LastReportState,
			Detail:  st.LastReportDetail,
		},
		Activity: activity,
		InTurn:   inAutonomousTurn,
		Liveness: livenessTok,
	}
	if st.Subagent {
		pr.Subagent = true
		pr.SharedWorktreeWith = st.Parent
	}
	return pr, nil
}

// ReportStatus delegates to agentops.Report, which is the single persistence
// path used by the `report_status` MCP tool. See
// docs/designs/messaging-overhaul.md §4.2.3 / §4.7.
//
// An empty agentName defaults to r.callerName — the MCP tool invokes this
// method with an empty name so child agents can report without passing their
// own identity as a parameter.
func (r *Real) ReportStatus(ctx context.Context, agentName, reportState, summary string) (*ReportStatusResult, error) {
	_ = ctx // QUM-727: teardown uses a fresh background ctx since the goroutine outlives this request.
	if agentName == "" {
		agentName = r.callerName
	}
	if agentName == "" {
		return nil, fmt.Errorf("reporter identity not set (callerName is empty)")
	}

	// Serialize concurrent reporters — state.SaveAgent is read-modify-write.
	r.reportMu.Lock()
	defer r.reportMu.Unlock()

	// Load reporter state to resolve parent. A load failure (e.g. orphan
	// reporter) is non-fatal — agentops.Report below will surface a clear
	// error if the agent truly doesn't exist.
	parent := ""
	if agentState, err := state.LoadAgent(r.sprawlRoot, agentName); err == nil && agentState != nil {
		parent = agentState.Parent
	}

	// State-only persistence (QUM-559): no maildir, no harness-queue enqueue.
	res, err := agentops.Report(&agentops.ReportDeps{}, r.sprawlRoot, agentName, reportState, summary)
	if err != nil {
		return nil, err
	}

	// QUM-727: terminal-outcome reports (complete/failure) must release the
	// live runtime — subprocess + EventBus subscribers — to prevent stopped
	// agents from pinning ~280 MB RSS each and inflating goroutine fan-out.
	// Run runtime.Stop in a goroutine so this method (and the MCP reply path)
	// returns immediately: the JSON result frame must flush to claude's stdin
	// before the subprocess transport closes (design §7). Use a fresh
	// background ctx because the original MCP-request ctx is cancelled when
	// the request returns — the goroutine outlives the request.
	teardown := reportState == agentops.ReportStateComplete || reportState == agentops.ReportStateFailure
	if teardown {
		if runtime, ok := r.startedRuntime(agentName); ok {
			go func(rt *AgentRuntime) {
				stopCtx, cancel := context.WithTimeout(context.Background(), runtimeStopTimeout)
				defer cancel()
				if err := rt.Stop(stopCtx); err != nil {
					slog.Default().Warn("supervisor: ReportStatus runtime.Stop failed",
						slog.String("agent", agentName),
						slog.String("state", reportState),
						slog.Any("err", err))
				}
				// QUM-727: for the failure path, stopWithFunc clobbers the
				// in-memory snapshot.Status to "stopped" (it cannot
				// distinguish a polite Stop from a Stop driven by a
				// failure-report). The durable on-disk Status is "faulted"
				// (agentops.Report wrote it, stopWithFunc's preservation
				// switch kept it). Re-sync the snapshot from disk so the
				// projection sees DiskStatus=Faulted and QUM-606 Recover
				// accepts the agent as a legal source. The complete path
				// keeps Liveness=Stopped (post-stopWithFunc) so callers see
				// a deliberate-stop projection.
				if reportState == agentops.ReportStateFailure {
					r.syncRuntimeFromState(agentName)
				}
			}(runtime)
		}
	} else {
		// Non-terminal report (working/blocked): the live handle stays
		// attached; just mirror persisted state into the snapshot. For
		// terminal reports we skip this sync to avoid racing the async
		// Stop above — Stop owns the final snapshot mutation.
		r.syncRuntimeFromState(agentName)
	}

	// QUM-614: write the status_change envelope into the parent's maildir
	// (via messages.SendStatusChange — which deliberately bypasses the
	// process-level defaultNotifier so this does NOT raise the inbox banner
	// / unread badge / drain-row prompt-inject) and cooperatively wake the
	// parent runtime. Status updates are never interrupt-class — children
	// that genuinely need to preempt should use send_message(interrupt=true).
	if parent != "" {
		// QUM-725: if the parent is dead, route the status_change envelope up
		// to the first live ancestor with the summary wrapped so the live
		// recipient can tell the routed-up notification apart from a normal
		// status_change.
		originalParent := parent
		effectiveSummary := summary
		liveAncestor, deadChain, walkErr := WalkDeadAncestors(parent, r.livenessOf, r.parentOf)
		if walkErr != nil {
			// ReportStatus is best-effort (we already warn-and-continue on
			// SendStatusChange errors below); log walkErr at warn and fall
			// through to direct delivery against the original parent rather
			// than failing the report.
			slog.Default().Warn(
				"supervisor: ReportStatus dead-parent walk failed",
				slog.String("from", agentName),
				slog.String("parent", parent),
				slog.Any("err", walkErr),
			)
		}
		if walkErr == nil && deadChain != nil {
			parent = liveAncestor
			effectiveSummary = inboxprompt.WrapForDeadTarget(agentName, originalParent, deadChain, summary)
		}
		payload := messages.StatusChangePayload{
			State:     reportState,
			Summary:   effectiveSummary,
			Timestamp: res.ReportedAt,
		}
		if _, sendErr := messages.SendStatusChange(r.sprawlRoot, agentName, parent, payload); sendErr != nil {
			slog.Default().Warn(
				"supervisor: SendStatusChange failed",
				slog.String("from", agentName),
				slog.String("to", parent),
				slog.Any("err", sendErr),
			)
		}
		if parentRuntime, ok := r.startedRuntime(parent); ok {
			_ = parentRuntime.WakeForDelivery()
		}
	}
	return &ReportStatusResult{ReportedAt: res.ReportedAt}, nil
}

func (r *Real) syncRuntimeFromState(agentName string) {
	runtime, ok := r.runtimeRegistry.Get(agentName)
	if !ok {
		return
	}
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return
	}
	runtime.SyncAgentState(agentState)
}

const runtimeStopTimeout = 10 * time.Second

func withRuntimeStopTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), runtimeStopTimeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, runtimeStopTimeout)
}

func (r *Real) startedRuntime(agentName string) (*AgentRuntime, bool) {
	runtime, ok := r.runtimeRegistry.Get(agentName)
	if !ok {
		return nil, false
	}
	if runtime.Snapshot().Liveness != liveness.Running {
		return nil, false
	}
	return runtime, true
}

// reconcileStateFromRegistry handles the QUM-404 divergence case: when the
// supervisor's runtime registry knows the agent but the on-disk JSON has
// gone missing, retire (and other state-driven flows) cannot proceed because
// agentops.Retire calls state.LoadAgent which fails with ENOENT. We
// synthesize an AgentState from the runtime snapshot and persist it so the
// downstream retireFn sees a valid state file. If neither source has the
// agent, return a clear "not found" error rather than silently no-oping.
func (r *Real) reconcileStateFromRegistry(agentName string) error {
	if _, err := state.LoadAgent(r.sprawlRoot, agentName); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	runtime, ok := r.runtimeRegistry.Get(agentName)
	if !ok {
		return fmt.Errorf("agent %q not found: no on-disk state and no runtime registry entry", agentName)
	}
	snap := runtime.Snapshot()
	synth := &state.AgentState{
		Name:              snap.Name,
		Type:              snap.Type,
		Family:            snap.Family,
		Parent:            snap.Parent,
		Branch:            snap.Branch,
		Worktree:          snap.Worktree,
		Status:            snap.Status,
		SessionID:         snap.SessionID,
		TreePath:          snap.TreePath,
		CreatedAt:         snap.CreatedAt,
		LastReportType:    snap.LastReport.Type,
		LastReportMessage: snap.LastReport.Message,
		LastReportAt:      snap.LastReport.At,
		LastReportState:   snap.LastReport.State,
		LastReportDetail:  snap.LastReport.Detail,
	}
	if synth.Status == "" {
		synth.Status = "active"
	}
	if err := state.SaveAgent(r.sprawlRoot, synth); err != nil {
		return fmt.Errorf("reconciling state for %q from registry: %w", agentName, err)
	}
	return nil
}

func (r *Real) rollbackSpawnArtifacts(agentName string) {
	r.runtimeRegistry.Remove(agentName)
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err == nil && !agentState.Subagent {
		if agentState.Worktree != "" && r.spawnDeps != nil && r.spawnDeps.WorktreeRemove != nil {
			_ = r.spawnDeps.WorktreeRemove(r.sprawlRoot, agentState.Worktree, true)
		}
		if agentState.Branch != "" && r.spawnDeps != nil && r.spawnDeps.GitBranchDelete != nil {
			_ = r.spawnDeps.GitBranchDelete(r.sprawlRoot, agentState.Branch)
		}
	}
	// state.DeleteAgent now removes the per-agent directory as well (QUM-404).
	_ = state.DeleteAgent(r.sprawlRoot, agentName)
}

func (r *Real) listDirectChildren(parentName string) ([]*state.AgentState, error) {
	agents, err := state.ListAgents(r.sprawlRoot)
	if err != nil {
		return nil, err
	}
	var children []*state.AgentState
	for _, agentState := range agents {
		// QUM-739 / QUM-787: resolved-orphan children don't block parent
		// retire/merge and shouldn't be cascaded. Uses IsResolvedOrphan
		// because QUM-787 narrowed IsTerminal to {retired, retiring}.
		if agentState.Parent == parentName && !state.IsResolvedOrphan(agentState.Status) {
			children = append(children, agentState)
		}
	}
	return children, nil
}

func (r *Real) reconcileRuntimeTreeFromState(rootName string) {
	parentByName := make(map[string]string)
	for _, runtime := range r.runtimeRegistry.List() {
		snap := runtime.Snapshot()
		parentByName[snap.Name] = snap.Parent
	}

	for name := range parentByName {
		current := name
		for current != "" {
			if current == rootName {
				if _, err := state.LoadAgent(r.sprawlRoot, name); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						r.runtimeRegistry.Remove(name)
					}
				}
				break
			}
			current = parentByName[current]
		}
	}
}

// --- QUM-316: mailbox read/list/archive tools ---

// validFilters enumerates filter values the MCP mailbox tools accept. The
// messages library also supports "sent", but that's out of scope for the MCP
// surface per QUM-316 (non-goal).
var validMessagesListFilters = map[string]bool{
	"":         true,
	"all":      true,
	"unread":   true,
	"read":     true,
	"archived": true,
	"status":   true,
}

const (
	defaultMessagesListLimit = 0 // 0 = no limit
	maxMessagesListLimit     = 500
	messagesPeekPreviewCap   = 5
)

func (r *Real) requireEffectiveCaller(ctx context.Context) (string, error) {
	caller := r.effectiveCaller(ctx)
	if caller == "" {
		return "", fmt.Errorf("caller identity not set (SPRAWL_AGENT_IDENTITY unset); refusing mailbox operation")
	}
	return caller, nil
}

// toSummaries maps *messages.Message slices to MessageSummary slices and
// sorts them newest-first by timestamp. Truncated to `limit` (limit ≤ 0
// returns all).
func toSummaries(msgs []*messages.Message, limit int) []MessageSummary {
	out := make([]MessageSummary, 0, len(msgs))
	for _, m := range msgs {
		id := m.ShortID
		if id == "" {
			id = m.ID
		}
		read := m.Dir != "new"
		out = append(out, MessageSummary{
			ID:        id,
			FullID:    m.ID,
			From:      m.From,
			Subject:   m.Subject,
			Timestamp: m.Timestamp,
			Read:      read,
			Dir:       m.Dir,
		})
	}
	// Newest-first.
	sort.SliceStable(out, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, out[i].Timestamp)
		tj, _ := time.Parse(time.RFC3339, out[j].Timestamp)
		return ti.After(tj)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (r *Real) MessagesList(ctx context.Context, filter string, limit int) (*MessagesListResult, error) {
	caller, err := r.requireEffectiveCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !validMessagesListFilters[filter] {
		return nil, fmt.Errorf("invalid filter %q: must be one of all, unread, read, archived, status", filter)
	}
	effective := filter
	if effective == "" {
		effective = "all"
	}
	if limit < 0 {
		limit = 0
	}
	if limit > maxMessagesListLimit {
		limit = maxMessagesListLimit
	}
	msgs, err := messages.List(r.sprawlRoot, caller, effective)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	summaries := toSummaries(msgs, limit)
	return &MessagesListResult{
		Agent:    caller,
		Filter:   effective,
		Count:    len(summaries),
		Messages: summaries,
	}, nil
}

// isInNewDir reports whether the message with the given full ID is sitting in
// the caller's new/ directory (i.e. unread). Used to report WasUnread without
// perturbing the library's ReadMessage auto-mark behavior.
func (r *Real) isInNewDir(caller, msgID string) bool {
	path := filepath.Join(messages.MessagesDir(r.sprawlRoot), caller, "new", msgID+".json")
	_, err := os.Stat(path)
	return err == nil
}

func (r *Real) MessagesRead(ctx context.Context, msgID string) (*MessagesReadResult, error) {
	caller, err := r.requireEffectiveCaller(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(msgID) == "" {
		return nil, fmt.Errorf("message id must not be empty")
	}
	full, err := messages.ResolvePrefix(r.sprawlRoot, caller, msgID)
	if err != nil {
		return nil, err
	}
	wasUnread := r.isInNewDir(caller, full)
	msg, err := messages.ReadMessage(r.sprawlRoot, caller, full)
	if err != nil {
		return nil, err
	}
	short := msg.ShortID
	if short == "" {
		short = msg.ID
	}
	return &MessagesReadResult{
		ID:        short,
		FullID:    msg.ID,
		From:      msg.From,
		To:        msg.To,
		Subject:   msg.Subject,
		Body:      msg.Body,
		Timestamp: msg.Timestamp,
		Dir:       msg.Dir,
		WasUnread: wasUnread,
	}, nil
}

func (r *Real) MessagesArchive(ctx context.Context, msgID string) (*MessagesArchiveResult, error) {
	caller, err := r.requireEffectiveCaller(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(msgID) == "" {
		return nil, fmt.Errorf("message id must not be empty")
	}
	full, err := messages.ResolvePrefix(r.sprawlRoot, caller, msgID)
	if err != nil {
		return nil, err
	}
	if err := messages.Archive(r.sprawlRoot, caller, full); err != nil {
		return nil, err
	}
	short := msgID
	if len(full) >= len(msgID) {
		// Prefer the short ID when we can retrieve it from the archived file.
		archived, rerr := messages.ReadMessage(r.sprawlRoot, caller, full)
		if rerr == nil && archived.ShortID != "" {
			short = archived.ShortID
		}
	}
	return &MessagesArchiveResult{
		ID:       short,
		FullID:   full,
		Archived: true,
	}, nil
}

func (r *Real) MessagesArchiveAll(ctx context.Context, mode string) (*MessagesArchiveAllResult, error) {
	caller, err := r.requireEffectiveCaller(ctx)
	if err != nil {
		return nil, err
	}
	var count int
	switch mode {
	case "all":
		count, err = messages.ArchiveAll(r.sprawlRoot, caller)
	case "read":
		count, err = messages.ArchiveRead(r.sprawlRoot, caller)
	default:
		return nil, fmt.Errorf("invalid archive mode %q: must be \"all\" or \"read\"", mode)
	}
	if err != nil {
		return nil, err
	}
	return &MessagesArchiveAllResult{
		ArchivedCount: count,
		Archived:      true,
	}, nil
}

func (r *Real) MessagesPeek(ctx context.Context) (*MessagesPeekResult, error) {
	caller, err := r.requireEffectiveCaller(ctx)
	if err != nil {
		return nil, err
	}
	msgs, err := messages.List(r.sprawlRoot, caller, "unread")
	if err != nil {
		return nil, fmt.Errorf("listing unread: %w", err)
	}
	preview := toSummaries(msgs, messagesPeekPreviewCap)
	return &MessagesPeekResult{
		Agent:       caller,
		UnreadCount: len(msgs),
		Preview:     preview,
	}, nil
}

// sumUsageCostForAgent returns the aggregated total_cost_usd for the named
// agent across all per-session usage NDJSON logs under
// .sprawl/logs/usage/<agent>/*.ndjson. Errors are swallowed (returns 0) so
// the Status build path is robust against transient I/O issues — usage data
// is informational, not load-bearing for control flow.
func sumUsageCostForAgent(sprawlRoot, name string) float64 {
	t, err := usage.SumForAgent(sprawlRoot, name)
	if err != nil {
		return 0
	}
	return t.TotalCostUsd
}
