package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/gofrs/flock"
)

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
// same logic used by the CLI `sprawl spawn|merge|retire|kill` commands.
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
	mergeFn  func(*agentops.MergeDeps, string, string, bool, bool) error
	retireFn func(*agentops.RetireDeps, string, bool, bool, bool, bool, bool, bool) error
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
			LockAcquire:     merge.RealLockAcquire,
			GitMergeBase:    merge.RealGitMergeBase,
			GitRevParseHead: merge.RealGitRevParseHead,
			GitResetSoft:    merge.RealGitResetSoft,
			GitCommit:       merge.RealGitCommit,
			GitRebase:       merge.RealGitRebase,
			GitRebaseAbort:  merge.RealGitRebaseAbort,
			GitFFMerge:      merge.RealGitFFMerge,
			GitResetHard:    merge.RealGitResetHard,
			RunTests:        merge.RealRunTests,
			WritePoke:       merge.RealWritePoke,
			Stderr:          os.Stderr,
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
	}
	r.runtimeStarter = newRuntimeStarter(cfg.ChildInitSpec, cfg.ChildAllowedTools)
	return r, nil
}

// RuntimeRegistry exposes the supervisor's in-memory runtime registry so
// process-level wiring (e.g. messages.RecipientResolver) can consult it.
func (r *Real) RuntimeRegistry() *RuntimeRegistry {
	return r.runtimeRegistry
}

// SetChildMCPConfig updates the child runtime starter with the given MCP
// init spec and allowed tools. Use this for two-phase init when the MCP
// server needs a reference to the supervisor itself.
func (r *Real) SetChildMCPConfig(initSpec backendpkg.InitSpec, allowedTools []string) {
	r.runtimeStarter = newRuntimeStarter(initSpec, allowedTools)
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

	enqueuedTask, err := state.EnqueueTask(r.sprawlRoot, agentName, task)
	if err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	if runtime, ok := r.runtimeRegistry.Get(agentName); ok {
		runtime.RecordQueuedTask(enqueuedTask)
		if runtime.Snapshot().Lifecycle == RuntimeLifecycleStarted {
			_ = runtime.Wake()
		}
	}
	return nil
}

func (r *Real) Message(ctx context.Context, agentName, subject, body string) error {
	_, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	_, err = messages.Send(r.sprawlRoot, r.effectiveCaller(ctx), agentName, subject, body)
	return err
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

func (r *Real) Merge(_ context.Context, agentName, message string, noValidate bool) error {
	return r.mergeFn(r.mergeDeps, agentName, message, noValidate, false)
}

func (r *Real) Retire(ctx context.Context, agentName string, mergeFirst, abandon, cascade, noValidate bool) error {
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
				if err := r.Retire(ctx, child.Name, false, false, true, noValidate); err != nil {
					return err
				}
			}
		}
		stopCtx, cancel := withRuntimeStopTimeout(ctx)
		defer cancel()
		if err := runtime.Stop(stopCtx); err != nil {
			return err
		}
		if err := r.retireFn(r.retireDeps, agentName, false /* cascade already handled */, false /* force */, abandon, mergeFirst, true /* yes */, noValidate); err != nil {
			return err
		}
		r.runtimeRegistry.Remove(agentName)
		return nil
	}
	if err := r.retireFn(r.retireDeps, agentName, cascade, false /* force */, abandon, mergeFirst, true /* yes */, noValidate); err != nil {
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

	if runtime, ok := r.startedRuntime(agentName); ok {
		stopCtx, cancel := withRuntimeStopTimeout(ctx)
		defer cancel()
		if err := runtime.Stop(stopCtx); err != nil {
			return err
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

func (r *Real) Shutdown(ctx context.Context) error {
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
		agentState.Status = "killed"
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

// spawnDepsForCaller returns a copy of r.spawnDeps with Getenv overridden so
// that SPRAWL_AGENT_IDENTITY reflects the effective caller (the agent whose
// MCP tool call triggered the spawn), making child spawns get the correct
// Parent linkage. See QUM-384. Also overrides SPRAWL_TREE_PATH using the
// caller's persisted state.TreePath so grandchild spawns get a tree path
// rooted at the actual caller rather than the supervisor's process env
// (QUM-416).
func (r *Real) spawnDepsForCaller(caller string) *agentops.SpawnDeps {
	deps := *r.spawnDeps // shallow copy
	var callerTreePath string
	if st, err := state.LoadAgent(r.sprawlRoot, caller); err == nil && st != nil {
		callerTreePath = st.TreePath
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

// SendAsync persists the message to Maildir and appends a harness queue
// entry (class=async) for the recipient. See
// docs/designs/messaging-overhaul.md §4.2.1.
func (r *Real) SendAsync(ctx context.Context, to, subject, body, replyTo string, tags []string) (*SendAsyncResult, error) {
	if err := agent.ValidateName(to); err != nil {
		return nil, err
	}
	if _, err := state.LoadAgent(r.sprawlRoot, to); err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", to, err)
	}

	runtime, runtimeBacked := r.startedRuntime(to)

	var sendOpts []messages.SendOption
	if runtimeBacked {
		sendOpts = append(sendOpts, messages.WithoutWakeFile())
	}

	caller := r.effectiveCaller(ctx)
	shortID, err := messages.Send(r.sprawlRoot, caller, to, subject, body, sendOpts...)
	if err != nil {
		return nil, err
	}

	entry, err := agentloop.Enqueue(r.sprawlRoot, to, agentloop.Entry{
		ShortID: shortID,
		Class:   agentloop.ClassAsync,
		From:    caller,
		Subject: subject,
		Body:    body,
		ReplyTo: replyTo,
		Tags:    tags,
	})
	if err != nil {
		return nil, fmt.Errorf("enqueuing async message: %w", err)
	}
	if runtimeBacked {
		_ = runtime.InterruptDelivery()
	}

	return &SendAsyncResult{
		MessageID: entry.ID,
		QueuedAt:  entry.EnqueuedAt,
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

// SendInterrupt persists the message to Maildir and appends an
// interrupt-class queue entry for the recipient. Gated to parent→descendants
// per §8.5: the caller must be an ancestor of `to`. See
// docs/designs/messaging-overhaul.md §4.2.2 and §4.5.2.
func (r *Real) SendInterrupt(ctx context.Context, to, subject, body, resumeHint string) (*SendInterruptResult, error) {
	if err := agent.ValidateName(to); err != nil {
		return nil, err
	}
	if _, err := state.LoadAgent(r.sprawlRoot, to); err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", to, err)
	}

	// Parent→descendants gate: the caller must be an ancestor of `to`.
	// An empty callerName (e.g. when invoked from unidentified contexts)
	// is rejected to avoid accidental self-or-upward interrupts.
	caller := r.effectiveCaller(ctx)
	if caller == "" {
		return nil, fmt.Errorf("send_interrupt: caller identity unknown; refusing to send")
	}
	if caller == to {
		return nil, fmt.Errorf("send_interrupt: cannot interrupt self")
	}
	ok, err := isAncestor(r.sprawlRoot, caller, to)
	if err != nil {
		return nil, fmt.Errorf("checking ancestry: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("send_interrupt: %q is not an ancestor of %q (parent→descendants only per §8.5)", caller, to)
	}

	// Assemble the enqueue body. We preserve the resume_hint separately in
	// Tags so the child runtime can render the §4.5.2 frame without
	// re-parsing the body. Tag key pattern: "resume_hint:<value>".
	var tags []string
	if resumeHint != "" {
		tags = append(tags, "resume_hint:"+resumeHint)
	}

	runtime, runtimeBacked := r.startedRuntime(to)

	var sendOpts []messages.SendOption
	if runtimeBacked {
		sendOpts = append(sendOpts, messages.WithoutWakeFile())
	}

	shortID, err := messages.Send(r.sprawlRoot, caller, to, subject, body, sendOpts...)
	if err != nil {
		return nil, err
	}

	entry, err := agentloop.Enqueue(r.sprawlRoot, to, agentloop.Entry{
		ShortID: shortID,
		Class:   agentloop.ClassInterrupt,
		From:    caller,
		Subject: subject,
		Body:    body,
		Tags:    tags,
	})
	if err != nil {
		return nil, fmt.Errorf("enqueuing interrupt message: %w", err)
	}
	if runtimeBacked {
		_ = runtime.InterruptDelivery()
	}

	return &SendInterruptResult{
		MessageID:   entry.ID,
		DeliveredAt: entry.EnqueuedAt,
		// Best-effort advisory: we report interrupted=true whenever the
		// target exists and the entry was enqueued. The harness decides
		// mid-turn preemption asynchronously; callers shouldn't rely on
		// this for strict invariants. See §4.2.2.
		Interrupted: true,
	}, nil
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
	return &PeekResult{
		Status: st.Status,
		LastReport: LastReport{
			Type:    st.LastReportType,
			Message: st.LastReportMessage,
			At:      st.LastReportAt,
			State:   st.LastReportState,
			Detail:  st.LastReportDetail,
		},
		Activity: activity,
	}, nil
}

// ReportStatus delegates to agentops.Report, which is the single persistence
// path shared by the `sprawl report` CLI. See
// docs/designs/messaging-overhaul.md §4.2.3 / §4.7.
//
// An empty agentName defaults to r.callerName — the MCP tool invokes this
// method with an empty name so child agents can report without passing their
// own identity as a parameter.
func (r *Real) ReportStatus(_ context.Context, agentName, reportState, summary, detail string) (*ReportStatusResult, error) {
	if agentName == "" {
		agentName = r.callerName
	}
	if agentName == "" {
		return nil, fmt.Errorf("reporter identity not set (callerName is empty)")
	}

	var parentRuntime *AgentRuntime
	if agentState, err := state.LoadAgent(r.sprawlRoot, agentName); err == nil && agentState.Parent != "" {
		parentRuntime, _ = r.startedRuntime(agentState.Parent)
	}

	reportDeps := &agentops.ReportDeps{
		SendMessage: func(sprawlRoot, from, to, subject, body string, opts ...messages.SendOption) (string, error) {
			if parentRuntime != nil {
				opts = append(opts, messages.WithoutWakeFile())
			}
			return messages.Send(sprawlRoot, from, to, subject, body, opts...)
		},
	}
	res, err := agentops.Report(reportDeps, r.sprawlRoot, agentName, reportState, summary, detail)
	if err != nil {
		return nil, err
	}
	r.syncRuntimeFromState(agentName)
	if parentRuntime != nil && res.MessageID != "" {
		_ = parentRuntime.InterruptDelivery()
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
