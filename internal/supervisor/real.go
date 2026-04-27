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
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/gofrs/flock"
)

// Config holds configuration for the real supervisor.
type Config struct {
	SprawlRoot string
	CallerName string
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

// NewReal creates a new real supervisor. It returns an error if required
// tooling (tmux) is not available on PATH.
func NewReal(cfg Config) (*Real, error) {
	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}
	tmuxRunner := &tmux.RealRunner{TmuxPath: tmuxPath}

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
		sprawlRoot: cfg.SprawlRoot,
		callerName: cfg.CallerName,

		spawnDeps: &agentops.SpawnDeps{
			TmuxRunner:      tmuxRunner,
			WorktreeCreator: &worktree.RealCreator{},
			Getenv:          supervisorGetenv,
			CurrentBranch:   agentops.GitCurrentBranch,
			FindSprawl:      agentops.FindSprawlBin,
			NewSpawnLock: func(lockPath string) (func() error, func() error) {
				fl := flock.New(lockPath)
				return fl.Lock, fl.Unlock
			},
			LoadConfig:     config.Load,
			RunScript:      agentops.RunBashScript,
			WorktreeRemove: agentops.RealWorktreeRemove,
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
			TmuxRunner:          tmuxRunner,
			Getenv:              supervisorGetenv,
			WriteFile:           os.WriteFile,
			RemoveFile:          os.Remove,
			SleepFunc:           time.Sleep,
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
			TmuxRunner: tmuxRunner,
			Getenv:     supervisorGetenv,
			WriteFile:  os.WriteFile,
			RemoveFile: os.Remove,
			SleepFunc:  time.Sleep,
		},

		spawnFn:  agentops.Spawn,
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
	return r, nil
}

func (r *Real) Status(_ context.Context) ([]AgentInfo, error) {
	agents, err := state.ListAgents(r.sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
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
			LastReportState:   a.LastReportState,
			LastReportSummary: a.LastReportMessage,
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

	_, err = state.EnqueueTask(r.sprawlRoot, agentName, task)
	if err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	return nil
}

func (r *Real) Message(_ context.Context, agentName, subject, body string) error {
	_, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	return messages.Send(r.sprawlRoot, r.callerName, agentName, subject, body)
}

func (r *Real) Spawn(_ context.Context, req SpawnRequest) (*AgentInfo, error) {
	st, err := r.spawnFn(r.spawnDeps, req.Family, req.Type, req.Prompt, req.Branch)
	if err != nil {
		return nil, err
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

func (r *Real) Retire(_ context.Context, agentName string, mergeFirst, abandon bool) error {
	return r.retireFn(r.retireDeps, agentName, false /* cascade */, false /* force */, abandon, mergeFirst, true /* yes */, false /* noValidate */)
}

// Kill is idempotent: if the agent is already gone (state file missing) or
// was already killed, it returns nil. Enter.go's graceful shutdown iterates
// every agent and calls Kill, so transient absence must not fail.
func (r *Real) Kill(_ context.Context, agentName string) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	// Swallow "agent not found" — idempotent shutdown contract.
	if _, err := state.LoadAgent(r.sprawlRoot, agentName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// Unknown error reading state — propagate.
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	return r.killFn(r.killDeps, agentName, false)
}

func (r *Real) Shutdown(_ context.Context) error {
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

// PeekActivity reads the tail of the agent's activity.ndjson file and
// returns the last `tail` entries. A missing file yields an empty slice.
// tail ≤ 0 returns all available entries.
func (r *Real) PeekActivity(_ context.Context, agentName string, tail int) ([]agentloop.ActivityEntry, error) {
	if err := agent.ValidateName(agentName); err != nil {
		return nil, err
	}
	path := agentloop.ActivityPath(r.sprawlRoot, agentName)
	entries, err := agentloop.ReadActivityFile(path, tail)
	if err != nil {
		return nil, fmt.Errorf("reading activity for %q: %w", agentName, err)
	}
	return entries, nil
}

// SendAsync persists the message to Maildir and appends a harness queue
// entry (class=async) for the recipient. See
// docs/designs/messaging-overhaul.md §4.2.1.
func (r *Real) SendAsync(_ context.Context, to, subject, body, replyTo string, tags []string) (*SendAsyncResult, error) {
	if err := agent.ValidateName(to); err != nil {
		return nil, err
	}
	if _, err := state.LoadAgent(r.sprawlRoot, to); err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", to, err)
	}

	if err := messages.Send(r.sprawlRoot, r.callerName, to, subject, body); err != nil {
		return nil, err
	}

	entry, err := agentloop.Enqueue(r.sprawlRoot, to, agentloop.Entry{
		Class:   agentloop.ClassAsync,
		From:    r.callerName,
		Subject: subject,
		Body:    body,
		ReplyTo: replyTo,
		Tags:    tags,
	})
	if err != nil {
		return nil, fmt.Errorf("enqueuing async message: %w", err)
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
func (r *Real) SendInterrupt(_ context.Context, to, subject, body, resumeHint string) (*SendInterruptResult, error) {
	if err := agent.ValidateName(to); err != nil {
		return nil, err
	}
	if _, err := state.LoadAgent(r.sprawlRoot, to); err != nil {
		return nil, fmt.Errorf("agent %q not found: %w", to, err)
	}

	// Parent→descendants gate: the caller must be an ancestor of `to`.
	// An empty callerName (e.g. when invoked from unidentified contexts)
	// is rejected to avoid accidental self-or-upward interrupts.
	if r.callerName == "" {
		return nil, fmt.Errorf("send_interrupt: caller identity unknown; refusing to send")
	}
	if r.callerName == to {
		return nil, fmt.Errorf("send_interrupt: cannot interrupt self")
	}
	ok, err := isAncestor(r.sprawlRoot, r.callerName, to)
	if err != nil {
		return nil, fmt.Errorf("checking ancestry: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("send_interrupt: %q is not an ancestor of %q (parent→descendants only per §8.5)", r.callerName, to)
	}

	// Assemble the enqueue body. We preserve the resume_hint separately in
	// Tags so the agent-loop harness can render the §4.5.2 frame without
	// re-parsing the body. Tag key pattern: "resume_hint:<value>".
	var tags []string
	if resumeHint != "" {
		tags = append(tags, "resume_hint:"+resumeHint)
	}

	if err := messages.Send(r.sprawlRoot, r.callerName, to, subject, body); err != nil {
		return nil, err
	}

	entry, err := agentloop.Enqueue(r.sprawlRoot, to, agentloop.Entry{
		Class:   agentloop.ClassInterrupt,
		From:    r.callerName,
		Subject: subject,
		Body:    body,
		Tags:    tags,
	})
	if err != nil {
		return nil, fmt.Errorf("enqueuing interrupt message: %w", err)
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
	res, err := agentops.Report(&agentops.ReportDeps{}, r.sprawlRoot, agentName, reportState, summary, detail)
	if err != nil {
		return nil, err
	}
	return &ReportStatusResult{ReportedAt: res.ReportedAt}, nil
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

func (r *Real) requireCallerIdentity() error {
	if r.callerName == "" {
		return fmt.Errorf("caller identity not set (SPRAWL_AGENT_IDENTITY unset); refusing mailbox operation")
	}
	return nil
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

func (r *Real) MessagesList(_ context.Context, filter string, limit int) (*MessagesListResult, error) {
	if err := r.requireCallerIdentity(); err != nil {
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
	msgs, err := messages.List(r.sprawlRoot, r.callerName, effective)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	summaries := toSummaries(msgs, limit)
	return &MessagesListResult{
		Agent:    r.callerName,
		Filter:   effective,
		Count:    len(summaries),
		Messages: summaries,
	}, nil
}

// isInNewDir reports whether the message with the given full ID is sitting in
// the caller's new/ directory (i.e. unread). Used to report WasUnread without
// perturbing the library's ReadMessage auto-mark behavior.
func (r *Real) isInNewDir(msgID string) bool {
	path := filepath.Join(messages.MessagesDir(r.sprawlRoot), r.callerName, "new", msgID+".json")
	_, err := os.Stat(path)
	return err == nil
}

func (r *Real) MessagesRead(_ context.Context, msgID string) (*MessagesReadResult, error) {
	if err := r.requireCallerIdentity(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(msgID) == "" {
		return nil, fmt.Errorf("message id must not be empty")
	}
	full, err := messages.ResolvePrefix(r.sprawlRoot, r.callerName, msgID)
	if err != nil {
		return nil, err
	}
	wasUnread := r.isInNewDir(full)
	msg, err := messages.ReadMessage(r.sprawlRoot, r.callerName, full)
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

func (r *Real) MessagesArchive(_ context.Context, msgID string) (*MessagesArchiveResult, error) {
	if err := r.requireCallerIdentity(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(msgID) == "" {
		return nil, fmt.Errorf("message id must not be empty")
	}
	full, err := messages.ResolvePrefix(r.sprawlRoot, r.callerName, msgID)
	if err != nil {
		return nil, err
	}
	if err := messages.Archive(r.sprawlRoot, r.callerName, full); err != nil {
		return nil, err
	}
	short := msgID
	if len(full) >= len(msgID) {
		// Prefer the short ID when we can retrieve it from the archived file.
		archived, rerr := messages.ReadMessage(r.sprawlRoot, r.callerName, full)
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

func (r *Real) MessagesPeek(_ context.Context) (*MessagesPeekResult, error) {
	if err := r.requireCallerIdentity(); err != nil {
		return nil, err
	}
	msgs, err := messages.List(r.sprawlRoot, r.callerName, "unread")
	if err != nil {
		return nil, fmt.Errorf("listing unread: %w", err)
	}
	preview := toSummaries(msgs, messagesPeekPreviewCap)
	return &MessagesPeekResult{
		Agent:       r.callerName,
		UnreadCount: len(msgs),
		Preview:     preview,
	}, nil
}
