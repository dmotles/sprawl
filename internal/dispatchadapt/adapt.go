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

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
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
// InTurnObserved is FALSE for every entry, and that is a truthful answer rather
// than a limitation to work around: turn state lives in the supervisor's
// in-memory phase machine and has no on-disk representation, so a process
// reading .sprawl/agents genuinely does not know. Reporting InTurn: false with
// InTurnObserved: true would be a negative answer derived from an unavailable
// observation — the shape internal/supervisor/runtime.go's InTurnObserved
// already refuses — and it would make the sweeper poke every working agent.
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
			// Deliberately not observed. See the doc comment.
			InTurn:         false,
			InTurnObserved: false,
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
