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

	"github.com/dmotles/sprawl/internal/agent"
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

	spawnFn  func(*agentops.SpawnDeps, string, string, string, string) (*state.AgentState, error)
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

	// recoveredEmitter, when non-nil, is invoked after a successful
	// Real.Recover so the TUI can clear its per-agent fault sticker (QUM-601).
	// Mirrors faultEmitter contracts: nil-safe, idempotent install/clear.
	recoveredEmitter func(agent string)

	// questions is the in-process question queue for ask_user_question
	// flows (QUM-527 slice 1). See question.go.
	questions *questionQueue

	// statusNotifier is the in-process per-recipient ring populated by
	// ReportStatus and drained by the parent's drain pipeline
	// (peekAndDrainCmd / unifiedHandle.drainPendingToQueue). QUM-559:
	// status updates flow exclusively through this ring; no maildir
	// write, no harness-queue enqueue.
	statusNotifier *statusNotifier

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

// SetBackendRecoveredEmitter installs a fan-out hook invoked whenever
// Real.Recover successfully completes in-place recovery for an agent
// (QUM-601). The TUI uses this to clear the per-agent fault sticker and
// surface a "backend recovered on X" banner. nil is allowed and clears the
// emitter; install + clear must both be idempotent and panic-free.
func (r *Real) SetBackendRecoveredEmitter(fn func(agent string)) {
	r.recoveredEmitter = fn
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

		questions:      newQuestionQueue(),
		statusNotifier: newStatusNotifier(),

		mergeSem: make(chan struct{}, 1),
	}
	starter := newInProcessUnifiedStarter(cfg.ChildInitSpec, cfg.ChildAllowedTools)
	if s, ok := starter.(*inProcessUnifiedStarter); ok {
		s.statusDrainer = r.statusNotifier.Drain
		s.faultEmitter = r.dispatchFault
	}
	r.runtimeStarter = starter
	return r, nil
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
		if r.statusNotifier != nil {
			s.statusDrainer = r.statusNotifier.Drain
		}
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
	for _, runtime := range r.runtimeRegistry.List() {
		snap := runtime.Snapshot()
		switch snap.Lifecycle {
		case RuntimeLifecycleStarted:
			alive := true
			processAliveByName[snap.Name] = &alive
		case RuntimeLifecycleStopped, RuntimeLifecycleKilled, RuntimeLifecycleRetired:
			alive := false
			processAliveByName[snap.Name] = &alive
		}
	}

	result := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		result = append(result, AgentInfo{
			Name:              a.Name,
			Type:              a.Type,
			Family:            a.Family,
			Parent:            a.Parent,
			Status:            a.Status,
			Branch:            a.Branch,
			TreePath:          a.TreePath,
			LastReportType:    a.LastReportType,
			LastReportState:   a.LastReportState,
			LastReportMessage: a.LastReportMessage,
			LastReportDetail:  a.LastReportDetail,
			TotalCostUsd:      a.TotalCostUsd,
			ProcessAlive:      processAliveByName[a.Name],
		})
	}
	return result, nil
}

func (r *Real) Delegate(_ context.Context, agentName, task string) error {
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	switch agentState.Status {
	case "killed", "retired", "retiring":
		return fmt.Errorf("cannot delegate to agent %q: status is %q", agentName, agentState.Status)
	}

	if task == "" {
		return fmt.Errorf("task prompt must not be empty")
	}

	if _, err := state.EnqueueTask(r.sprawlRoot, agentName, task); err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	if runtime, ok := r.runtimeRegistry.Get(agentName); ok {
		runtime.RecordQueuedTask()
		if runtime.Snapshot().Lifecycle == RuntimeLifecycleStarted {
			_ = runtime.Wake()
		}
	}
	return nil
}

func (r *Real) Spawn(ctx context.Context, req SpawnRequest) (*AgentInfo, error) {
	deps := r.spawnDepsForCaller(r.effectiveCaller(ctx))
	st, err := r.spawnFn(deps, req.Family, req.Type, req.Prompt, req.Branch)
	if err != nil {
		return nil, err
	}
	runtime := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: r.sprawlRoot,
		Agent:      st,
		Starter:    r.runtimeStarter,
	})
	if err := runtime.Start(context.Background()); err != nil {
		r.rollbackSpawnArtifacts(st.Name)
		return nil, fmt.Errorf("starting runtime for %s: %w", st.Name, err)
	}
	return &AgentInfo{
		Name:   st.Name,
		Type:   st.Type,
		Family: st.Family,
		Parent: st.Parent,
		Status: st.Status,
		Branch: st.Branch,
	}, nil
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
	if err := agent.ValidateName(agentName); err != nil {
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
		stopErr := runtime.Stop(stopCtx)
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

// Recover dispatches in-place recovery on the named agent's runtime (QUM-601).
// On success, fires the BackendRecoveredEmitter (if installed) so the TUI can
// clear its per-agent fault sticker. ErrRecoverNotNeeded (session healthy) is
// propagated to the caller verbatim — callers (notably the MCP recover tool)
// treat it as a success-with-no-op.
func (r *Real) Recover(ctx context.Context, agentName string) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}
	if r.runtimeRegistry == nil {
		return fmt.Errorf("agent %q not found", agentName)
	}
	runtime, ok := r.runtimeRegistry.Get(agentName)
	if !ok {
		return fmt.Errorf("agent %q not found", agentName)
	}
	id := calllog.CallID(ctx)
	cp := r.composeCheckpoint(id)
	if cp != nil {
		cp("recover.start", "agent_name", agentName)
	}
	err := runtime.Recover(ctx)
	if cp != nil {
		var msg string
		if err != nil {
			msg = err.Error()
		}
		cp("recover.done", "agent_name", agentName, "err", msg)
	}
	if err != nil {
		return err
	}
	if emit := r.recoveredEmitter; emit != nil {
		emit(agentName)
	}
	return nil
}

// RecoverAgents iterates persisted agent state and resumes every non-terminal
// child whose worktree still exists. Skips the root caller. Walks the tree
// BFS-from-root so parents are resumed before children (defense in depth;
// maildir absorbs ordering gaps). Returns counts and per-agent errors. Does
// not abort the loop on per-agent failure. QUM-372.
func (r *Real) RecoverAgents(ctx context.Context) (resumed int, failed int, errs []error) {
	if r.runtimeRegistry == nil || r.runtimeStarter == nil {
		return 0, 0, nil
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
		switch a.Status {
		case state.StatusSuspended, state.StatusActive, state.StatusRunning:
		default:
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
		if err := rt.StartResume(ctx, onResumeFailure); err != nil {
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
	// Release any in-flight AskUserQuestion callers with OutcomeSessionEnded
	// BEFORE tearing down runtimes. (QUM-527 slice 1.)
	r.questions.closeAll(OutcomeSessionEnded, "supervisor shutdown")
	for _, runtime := range r.runtimeRegistry.List() {
		snap := runtime.Snapshot()
		if snap.Lifecycle != RuntimeLifecycleStarted {
			continue
		}
		stopCtx, cancel := withRuntimeStopTimeout(ctx)
		if err := runtime.Stop(stopCtx); err != nil {
			cancel()
			return err
		}
		cancel()
		agentState, err := state.LoadAgent(r.sprawlRoot, snap.Name)
		if err != nil {
			continue
		}
		switch agentState.Status {
		case state.StatusKilled, state.StatusRetired, state.StatusRetiring, state.StatusDone:
			// leave terminal-ish states as-is
		default:
			agentState.Status = state.StatusSuspended
		}
		if err := state.SaveAgent(r.sprawlRoot, agentState); err != nil {
			return fmt.Errorf("updating agent state: %w", err)
		}
		runtime.SyncAgentState(agentState)
	}
	return nil
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
	if err := agent.ValidateName(agentName); err != nil {
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
func (r *Real) SendMessage(ctx context.Context, to, body string, interrupt bool) (*SendMessageResult, error) {
	if err := agent.ValidateName(to); err != nil {
		return nil, err
	}
	if _, err := state.LoadAgent(r.sprawlRoot, to); err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", to, err)
	}

	caller := r.effectiveCaller(ctx)
	if interrupt {
		if caller == "" {
			return nil, fmt.Errorf("send_message: caller identity unknown; refusing to send")
		}
		if caller == to {
			return nil, fmt.Errorf("send_message: cannot interrupt self")
		}
		ok, err := isAncestor(r.sprawlRoot, caller, to)
		if err != nil {
			return nil, fmt.Errorf("checking ancestry: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("send_message: %q is not an ancestor of %q (parent→descendants only per §8.5)", caller, to)
		}
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
	if runtimeBacked {
		if interrupt {
			_ = runtime.ForceInterruptDelivery()
		} else {
			_ = runtime.WakeForDelivery()
		}
	}

	return &SendMessageResult{
		MessageID:   entry.ID,
		QueuedAt:    entry.EnqueuedAt,
		Interrupted: interrupt,
	}, nil
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
	if err := agent.ValidateName(agentName); err != nil {
		return nil, err
	}
	st, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", agentName, err)
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
	if rt, ok := r.runtimeRegistry.Get(agentName); ok {
		inAutonomousTurn = rt.InAutonomousTurn()
	}

	return &PeekResult{
		Status: st.Status,
		LastReport: LastReport{
			Type:    st.LastReportType,
			Message: st.LastReportMessage,
			At:      st.LastReportAt,
			State:   st.LastReportState,
			Detail:  st.LastReportDetail,
		},
		Activity:         activity,
		InAutonomousTurn: inAutonomousTurn,
	}, nil
}

// ReportStatus delegates to agentops.Report, which is the single persistence
// path used by the `report_status` MCP tool. See
// docs/designs/messaging-overhaul.md §4.2.3 / §4.7.
//
// An empty agentName defaults to r.callerName — the MCP tool invokes this
// method with an empty name so child agents can report without passing their
// own identity as a parameter.
func (r *Real) ReportStatus(_ context.Context, agentName, reportState, summary string) (*ReportStatusResult, error) {
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
	r.syncRuntimeFromState(agentName)

	// QUM-559: push the ephemeral notification onto the parent's ring and
	// cooperatively wake the parent runtime. Status updates are never
	// interrupt-class — children that genuinely need to preempt should use
	// send_message(interrupt=true).
	if parent != "" {
		line := inboxprompt.BuildStatusNotification(agentName, reportState, summary)
		r.statusNotifier.Enqueue(parent, line)
		if parentRuntime, ok := r.startedRuntime(parent); ok {
			_ = parentRuntime.WakeForDelivery()
		}
	}
	return &ReportStatusResult{ReportedAt: res.ReportedAt}, nil
}

// DrainStatusNotifications returns and clears the per-recipient ephemeral
// status-notification ring. See QUM-559.
func (r *Real) DrainStatusNotifications(recipient string) []string {
	return r.statusNotifier.Drain(recipient)
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
	if runtime.Snapshot().Lifecycle != RuntimeLifecycleStarted {
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
	if err == nil {
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
		if agentState.Parent == parentName {
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
		return nil, fmt.Errorf("invalid filter %q: must be one of all, unread, read, archived", filter)
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
