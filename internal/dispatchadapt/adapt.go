// Package dispatchadapt implements the dispatch layer's local seams
// (QUM-1250, M1b).
//
// WHY IT IS A SEPARATE PACKAGE. internal/store defines Injector, LocalAgents and
// Spawner; the things that satisfy them live in internal/state,
// internal/messages and internal/agentloop. Putting the implementations here
// rather than in internal/store keeps the import direction one-way
// (supervisor -> store, never the reverse) and — the practical reason — keeps the
// diff out of internal/supervisor, which the e2e matrix names in a glob row plus
// seven per-file rows.
//
// ===========================================================================
// WHAT A PROCESS OUTSIDE THE SUPERVISOR CAN AND CANNOT DO
// ===========================================================================
//
// This is the constraint that shapes every type here, and it was not obvious
// until the adapters were written. `sprawl store dispatch` is a STANDALONE
// process. The supervisor, the runtime registry and the live Claude sessions all
// live inside a `sprawl enter` process, so from out here:
//
//	CAN   read the event log, take claims, run the reconciler, run the
//	      sweeper's candidate query.
//	CAN   ENQUEUE durably. messages.Send plus agentloop.Enqueue write the
//	      maildir envelope and the queue entry to disk, and the recipient's own
//	      supervisor drains them at its next turn boundary or redrain tick. The
//	      delivery is therefore real and loss-free — just not immediate.
//	CANNOT poke synchronously. Real.SendMessage's WakeForDelivery reaches into
//	      the in-process runtime registry, which does not exist here.
//	CANNOT observe turn state. It lives only in the supervisor's in-memory phase
//	      machine and has NO on-disk form, which is why store.LocalAgent carries
//	      InTurnObserved and why this adapter reports false for it.
//	CANNOT launch a session, so there is no Spawner here. Nothing emits
//	      spawn_requested in M1b either — the producer is M3a's engine — so a
//	      standalone spawner would be untestable machinery for an event that
//	      never arrives.
//
// The consequence to be honest about: a sweeper running out here is INERT by
// construction, because every candidate is skipped on the unobserved-turn-state
// gate. That is the safe direction and it is deliberate — the alternative is a
// sweeper that reports "not in turn" for every working agent and pokes them all.
// It becomes effective when M3a runs the dispatcher inside the supervisor
// process and supplies a LocalAgents view that can see turn state.
package dispatchadapt

import (
	"context"
	"fmt"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/sysframe"
)

// DiskAgents is a store.LocalAgents backed by the on-disk agent state.
//
// The on-disk AgentState is the sole wake arbiter and this reads it directly
// rather than through any projection — agent_sessions is explicitly forbidden
// from being the source of a wake decision, and this feeds gates that decide
// whether to wake something.
type DiskAgents struct {
	SprawlRoot string
	// List is injectable for tests; nil means state.ListAgents.
	List func(sprawlRoot string) ([]*state.AgentState, error)
	// Remove is injectable for tests; nil means a REFUSAL rather than a
	// deletion. See Reclaim.
	Remove func(ctx context.Context, sprawlRoot, name string) error
}

var _ store.LocalAgents = (*DiskAgents)(nil)

func (d *DiskAgents) list() func(string) ([]*state.AgentState, error) {
	if d.List != nil {
		return d.List
	}
	return state.ListAgents
}

// Snapshot reports this host's agents.
//
// Turn is TurnUnknown for every entry, and that is a truthful answer rather than
// a limitation to work around: turn state lives in the supervisor's in-memory
// phase machine and has no on-disk representation, so a process reading
// .sprawl/agents genuinely does not know. Answering TurnIdle would be a negative
// answer derived from an unavailable observation — the shape
// internal/supervisor/runtime.go's InTurnObserved already refuses — and it would
// make the sweeper poke every working agent.
func (d *DiskAgents) Snapshot(context.Context) ([]store.LocalAgent, error) {
	agents, err := d.list()(d.SprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("dispatchadapt: listing local agents: %w", err)
	}
	out := make([]store.LocalAgent, 0, len(agents))
	for _, a := range agents {
		out = append(out, store.LocalAgent{
			Name:      a.Name,
			Status:    a.Status,
			Branch:    a.Branch,
			Worktree:  a.Worktree,
			SessionID: a.SessionID,
			// Turn is left at its ZERO VALUE, which is TurnUnknown. That is a
			// truthful answer rather than a limitation to work around: turn state
			// lives in the supervisor's in-memory phase machine and has no
			// on-disk representation, so a process reading .sprawl/agents
			// genuinely does not know. Claiming TurnIdle would be a negative
			// answer derived from an unavailable observation, and it would make
			// the sweeper poke every working agent.
			//
			// Not spelled explicitly, deliberately: the type's zero value is the
			// safe one, so the safe answer is what a forgotten field gives.
		})
	}
	return out, nil
}

// Reclaim REFUSES unless a remover is explicitly wired.
//
// This is the one destructive operation in the dispatch layer, and a standalone
// process is the worst place to perform it: removing a worktree needs the git
// machinery and the retire bookkeeping that internal/agentops owns, and doing it
// by hand from here would leave a half-torn-down agent — a state nothing in the
// system knows how to recover from.
//
// Refusing is not a gap in AC4. The reconciler only ever reclaims a resource
// whose spawn_intent was already closed spawn_failed, and nothing in M1b creates
// spawn intents (the dispatcher ships unwired, and no producer emits
// spawn_requested), so this cannot be reached in an M1b deployment. When M3a runs
// the dispatcher in-process it wires a remover that goes through the real retire
// path. Until then, an unattributable deletion is strictly worse than a loud
// refusal — and the refusal names what to do about it.
func (d *DiskAgents) Reclaim(ctx context.Context, name string) error {
	if d.Remove == nil {
		return fmt.Errorf("dispatchadapt: refusing to reclaim the local resources of %q from outside the supervisor process: tearing down a worktree needs the retire path in internal/agentops, and doing it by hand here would leave a half-torn-down agent; run the reconciler from inside a sprawl session, or retire the agent explicitly", name)
	}
	return d.Remove(ctx, d.SprawlRoot, name)
}

// QueueInjector is a store.Injector that enqueues DURABLY.
//
// It writes the same two artifacts Real.SendMessage writes on its way through —
// the maildir envelope (messages.Send) and the queue entry
// (agentloop.Enqueue) — and stops there. The recipient's own supervisor drains
// `pending/` at its next turn boundary or redrain tick, which is the hardened
// injection leg this issue asks to reuse: runDrain, the QUM-1066 in-flight
// duplicate filter, the QUM-1072 bounded per-frame write, the idempotent ack.
//
// WHAT IT DOES NOT DO, stated because commit 5's header said delivery reuses
// Real.SendMessage and that is only true in-process: it does not POKE. Poking
// is WakeForDelivery on the in-process runtime registry, which does not exist in
// a standalone process. So delivery here is LOSS-FREE BUT NOT IMMEDIATE — the
// entry sits in pending/ until the recipient next drains, and an agent with no
// live session gets it when one starts.
//
// That is why the notification is a CONTRACT: an owner_notify stays open until
// the recipient's turn boundary acks it, so a delivery that is merely slow and
// one that never arrives are both visible, and the sweeper re-delivers.
type QueueInjector struct {
	SprawlRoot string
	// From is the sender recorded on the envelope. It is the dispatcher's own
	// identity, not the agent that produced the result: the recipient needs to
	// know this arrived from the coordination layer rather than from a peer.
	From string
	// Send and Enqueue are injectable for tests.
	Send    func(sprawlRoot, from, to, subject, body string) (string, error)
	Enqueue func(sprawlRoot, agentName string, e agentloop.Entry) (agentloop.Entry, error)
}

var _ store.Injector = (*QueueInjector)(nil)

// DispatcherIdentity is the sender name the dispatch layer uses.
const DispatcherIdentity = "sprawl-dispatch"

func (q *QueueInjector) Inject(_ context.Context, recipient, body string) error {
	if recipient == "" {
		return fmt.Errorf("dispatchadapt: refusing to enqueue for an empty recipient")
	}
	from := q.From
	if from == "" {
		from = DispatcherIdentity
	}
	send := q.Send
	if send == nil {
		send = func(root, f, to, subject, b string) (string, error) {
			return messages.Send(root, f, to, subject, b)
		}
	}
	enqueue := q.Enqueue
	if enqueue == nil {
		enqueue = agentloop.Enqueue
	}

	// The envelope FIRST, so the queue entry can reference it by short id. The
	// reverse order would enqueue an entry whose id resolves to nothing, and the
	// drain renders that id into the recipient's prompt as the thing to read.
	shortID, err := send(q.SprawlRoot, from, recipient, "", body)
	if err != nil {
		return fmt.Errorf("dispatchadapt: writing the message envelope for %q: %w", recipient, err)
	}
	if _, err := enqueue(q.SprawlRoot, recipient, agentloop.Entry{
		ShortID: shortID,
		// ClassAsync, never ClassInterrupt. A coordination nudge must not
		// preempt a turn: the sweeper's whole purpose is to help an agent that
		// is NOT working, and an interrupt-class entry to one that is would be
		// the preemption the in-turn gate exists to prevent, arriving by a
		// different route.
		Class: agentloop.ClassAsync,
		From:  from,
		Body:  body,
	}); err != nil {
		return fmt.Errorf("dispatchadapt: enqueueing for %q: %w", recipient, err)
	}
	return nil
}

// PoolNamer is a store.NameAllocator over the agent name pools (QUM-1252).
//
// It reads the SAME .sprawl/agents directory the legacy `spawn` path allocates
// from, which is the entire point: an engine-driven goal and a prose-driven
// spawn must not be able to hand out the same name, and the on-disk directory is
// the only thing both paths can see.
//
// The allocation is not transactional against a concurrent spawn — two
// allocations racing on one host can pick the same name. That race predates this
// type (agent.AllocateName has always had it) and is not made worse here; the
// spawn write-ahead in internal/store/spawn.go is what catches the collision,
// because the local spawn fails and the intent stays open for the reconciler.
type PoolNamer struct {
	SprawlRoot string
}

var _ store.NameAllocator = (*PoolNamer)(nil)

func (p *PoolNamer) AllocateName(_ context.Context, agentType string) (string, error) {
	name, err := agent.AllocateName(state.AgentsDir(p.SprawlRoot), agentType)
	if err != nil {
		return "", fmt.Errorf("dispatchadapt: allocating a name for a %s: %w", agentType, err)
	}
	return name, nil
}

// AgentSpawner is the slice of supervisor.Supervisor a spawn needs (QUM-1252).
//
// Declared here, at the consumer, rather than taking supervisor.Supervisor
// whole: the full interface is two dozen methods, so a test double for it would
// be pages of nil stubs whose bulk hides the one method that matters.
type AgentSpawner interface {
	Spawn(ctx context.Context, req supervisor.SpawnRequest) (*supervisor.AgentInfo, error)
}

// SupervisorSpawner is a store.Spawner over a live supervisor (QUM-1252).
//
// It is the piece that makes a spawn_requested event become an actual agent, and
// it exists only inside a `sprawl enter` process — a standalone
// `sprawl store dispatch` has no supervisor, so it registers no spawn handler at
// all rather than one that fails on every event.
type SupervisorSpawner struct {
	Sup AgentSpawner
}

var _ store.Spawner = (*SupervisorSpawner)(nil)

func (s *SupervisorSpawner) Spawn(ctx context.Context, req store.SpawnRequest) error {
	// The parent is checked HERE, at the seam that depends on it, rather than
	// trusted from the event. An empty one is not inert: the supervisor reads ""
	// as "no caller" and falls back to whichever identity runs the dispatcher, so
	// the mis-parenting the next comment describes would arrive silently. And the
	// parent is joined into a state-file path and exported to the child as its
	// identity, so it earns the same name validation agent_name gets.
	// GoalSpawnHandler rejects an empty owner today; a hand-appended or replayed
	// spawn_requested bypasses it entirely.
	if req.Parent == "" {
		return fmt.Errorf("dispatchadapt: the spawn_requested for %s names no parent, so the agent would be parented to whichever identity runs the dispatcher and its result would go to the wrong agent", req.AgentName)
	}
	if err := agent.ValidateName(req.Parent); err != nil {
		return fmt.Errorf("dispatchadapt: the spawn_requested for %s names parent %q: %w", req.AgentName, req.Parent, err)
	}
	// The goal's owner becomes the spawned agent's parent. The supervisor derives
	// the parent from the CALLER IDENTITY, not from a field on the request, so
	// this context value is the only way to say it — without it the agent is
	// parented to whichever identity happens to be running the dispatcher, and
	// its result notification goes to the wrong agent.
	ctx = backend.WithCallerIdentity(ctx, req.Parent)
	if _, err := s.Sup.Spawn(ctx, supervisor.SpawnRequest{
		// Name, not an allocation: the log named this agent before anything
		// existed locally and the reconciler matches spawn_intent BY NAME.
		Name:   req.AgentName,
		Type:   req.AgentType,
		Family: req.Family,
		// FRAMED HERE, AT THE DELIVERY SEAM (QUM-1348). Every event-log spawn
		// delivers its brief as a marked first message rather than as a prompt
		// file, because the log is the durable copy of the task and a prompt
		// file is a per-host second copy that no replay can reconstruct. The
		// envelope is built here and not in the emitter because the log is
		// append-only: markup stored in a payload would pin the tag name and
		// the renderer's vocabulary permanently.
		//
		// UNCONDITIONAL, deliberately. This wrap was once guarded by a
		// "already framed? pass it through" check for idempotence against a
		// replayed dispatch. That guard was a hole and is gone: the brief is
		// operator-authored goal text, the log stores it as PROSE
		// (goalspawn.go's goalPrompt, pinned by
		// TestGoalSpawnHandler_StoresProseNotMarkup), so a re-handled
		// spawn_requested presents prose every time and the guard could only
		// ever fire on a brief SHAPED like a frame — the one input crafted to
		// exploit the envelope, handed through unneutralized. The scenario it
		// protected against cannot arise; the one it enabled was a forged
		// notification class.
		Prompt:   sysframe.Goal(req.Prompt),
		Branch:   req.Branch,
		Model:    req.Model,
		Subagent: req.Subagent,
	}); err != nil {
		return fmt.Errorf("dispatchadapt: spawning %s for the event log: %w", req.AgentName, err)
	}
	return nil
}
