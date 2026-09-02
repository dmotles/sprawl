// supervised.go — the seams only a process WITH a supervisor can supply
// (QUM-1252, AC5).
//
// adapt.go's header sets out what a standalone `sprawl store dispatch` cannot
// do, and one consequence stated there is that the stall sweeper is INERT by
// construction: DiskAgents answers TurnUnknown for every agent, and the
// sweeper's tri-state turn gate skips an unobserved turn state rather than
// guessing. Running the dispatcher inside `sprawl enter` made a better answer
// POSSIBLE; this file supplies the narrow part of it AC5 needs.
//
// THE BOUNDARY, which is the whole design and is easy to overshoot:
//
//	NO LIVE SUBPROCESS -> TurnIdle. This is a real observation. An agent with no
//	    process cannot be mid-turn, so nothing is being guessed at. It is also
//	    exactly the case AC5 is about — a researcher whose session died holding
//	    an open goal.
//	LIVE AND in_turn -> TurnInTurn. Also a real observation.
//	LIVE AND NOT in_turn -> STILL TurnUnknown. AgentInfo.InTurn is the ambiguous
//	    signal AgentRuntime.InTurnObserved exists to replace: its false means
//	    "between turns" and "nobody could tell" alike, and Status() does not carry
//	    the observed bit. Reading it as idle is the "poke every working agent"
//	    defect arriving through a field that looks authoritative.
//
// So a LIVE agent that is genuinely idle is still not poked. That remaining half
// needs the supervisor's phase machine plumbed through a new observation seam,
// which is QUM-1328 and deliberately not here: AC5 does not need it, and the
// cheap version of it is the defect above.
package dispatchadapt

import (
	"context"
	"fmt"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// TurnObserver is the slice of supervisor.Supervisor a turn observation needs.
//
// Declared at the consumer, like AgentSpawner: supervisor.Supervisor is two
// dozen methods and a double for it would be pages of nil stubs whose bulk hides
// the one that matters.
type TurnObserver interface {
	Status(ctx context.Context) ([]supervisor.AgentInfo, error)
}

// Waker is the slice of supervisor.Supervisor a revive needs.
type Waker interface {
	Wake(ctx context.Context, agentName string, reason agentpkg.WakeReason, injectedBody string) (*supervisor.WakeResult, error)
}

// SessionSupervisor is everything the dispatch layer wants from a live session's
// supervisor: it can launch an agent, observe turn state, and wake a crashed
// one. It is the ONE thing a `sprawl enter` dispatch stack has and a standalone
// `sprawl store dispatch` does not, which is why the two stacks differ by a
// single nil-able parameter rather than by three.
type SessionSupervisor interface {
	AgentSpawner
	TurnObserver
	Waker
}

// SupervisorAgents is a store.LocalAgents that overlays observed turn state onto
// the disk view.
//
// It OVERLAYS rather than replaces: the disk view still enumerates the agents and
// still owns Status, Branch, Worktree and SessionID. That is not tidiness — the
// on-disk AgentState is the sole wake arbiter, the supervisor's projection can
// disagree with it, and every gate reading this feeds a decision about whether to
// wake something.
type SupervisorAgents struct {
	Disk *DiskAgents
	Sup  TurnObserver
}

var _ store.LocalAgents = (*SupervisorAgents)(nil)

// Snapshot reports this host's agents with turn state filled in where it can be
// observed.
//
// Both reads must succeed. A supervisor that cannot be read does NOT degrade to
// the disk view: degrading looks like the old behaviour and is not the old
// behaviour in effect — it silently returns the sweeper to inert, on a timer,
// with nothing saying the observation stopped working. store.Sweep stops its
// pass when it cannot read local state, so the error lands somewhere that
// handles it.
func (s *SupervisorAgents) Snapshot(ctx context.Context) ([]store.LocalAgent, error) {
	if s.Disk == nil || s.Sup == nil {
		return nil, fmt.Errorf("dispatchadapt: a supervisor-backed agent view needs both a disk view and a supervisor; a silently disk-only view is the inert stall sweeper wearing the name of the effective one")
	}
	agents, err := s.Disk.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	infos, err := s.Sup.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("dispatchadapt: reading live turn state from the supervisor: %w", err)
	}
	turns := make(map[string]store.TurnState, len(infos))
	for _, i := range infos {
		turns[i.Name] = turnStateOf(i)
	}
	for idx := range agents {
		// A name the supervisor did not mention keeps the disk view's answer,
		// which is TurnUnknown.
		if t, ok := turns[agents[idx].Name]; ok {
			agents[idx].Turn = t
		}
	}
	return agents, nil
}

// Reclaim delegates to the disk view, which refuses unless a remover was
// explicitly wired. Being in-process does not change that judgement: tearing
// down a worktree needs the retire path in internal/agentops, and the reconciler
// is the only caller.
func (s *SupervisorAgents) Reclaim(ctx context.Context, name string) error {
	if s.Disk == nil {
		return fmt.Errorf("dispatchadapt: no disk view, so %q cannot be reclaimed", name)
	}
	return s.Disk.Reclaim(ctx, name)
}

// turnStateOf maps one AgentInfo onto the tri-state. See the file header for why
// the live-but-not-in-turn case stays unknown.
func turnStateOf(i supervisor.AgentInfo) store.TurnState {
	if !i.SubprocessAlive {
		return store.TurnIdle
	}
	if i.InTurn {
		return store.TurnInTurn
	}
	return store.TurnUnknown
}

// WakeInjector is a store.Injector that REVIVES a crashed recipient.
//
// QueueInjector's delivery is loss-free but not immediate: the entry sits in
// pending/ until the recipient next drains, and an agent with no live session
// gets it only when one starts. For a notification that is fine — the contract
// stays open and the notify sweeper re-delivers. For a stall poke it is the whole
// problem: the agent AC5 describes has no session, so nothing will ever start one
// and the poke is delivered to a corpse.
//
// WHAT IS AND IS NOT REVIVED. Only the crash-and-finished classes — died,
// faulted, resume_failed, complete. Nobody chose those states.
//
// `killed` and `paused` are deliberately NOT in the set, and the reasoning is
// the sweeper's own: an operator-paused agent is excluded from auto-resume
// because a human decided it, and `sprawl kill` is a human decision in exactly
// the same way. Reviving either on a sweep timer overrides an operator on a
// machine they may not be watching. Those recipients get the durable queue
// instead, so the poke is waiting if a human brings the agent back — the poke is
// not dropped, only the revival is refused.
//
// Adding `killed` to the set later is one line. Shipping auto-revival of
// operator-killed agents and finding out in the field is not, which is why the
// default falls this way.
type WakeInjector struct {
	Queue *QueueInjector
	Sup   Waker
	Disk  *DiskAgents
}

var _ store.Injector = (*WakeInjector)(nil)

// revivable is the set of liveness states a stall poke may wake.
func revivable(status string) bool {
	switch status {
	case state.StatusDied, state.StatusFaulted, state.StatusResumeFailed, state.StatusComplete:
		return true
	}
	return false
}

func (w *WakeInjector) Inject(ctx context.Context, recipient, body string) error {
	if w.Queue == nil || w.Sup == nil || w.Disk == nil {
		return fmt.Errorf("dispatchadapt: a waking injector needs a queue, a supervisor and a disk view")
	}
	agents, err := w.Disk.Snapshot(ctx)
	if err != nil {
		return err
	}
	// An agent this host has no state file for is not classified and not woken.
	// The durable queue is the answer that does not act on a guess.
	for _, a := range agents {
		if a.Name != recipient || !revivable(a.Status) {
			continue
		}
		// WakeReasonSendMessage rather than a new reason: its template carries the
		// body verbatim, which is what a poke needs, and the wake-prompt literals
		// are byte-pinned by a test in internal/agent — a new one is a contract
		// change this slice does not need.
		if _, err := w.Sup.Wake(ctx, recipient, agentpkg.WakeReasonSendMessage, body); err != nil {
			return fmt.Errorf("dispatchadapt: waking %q to deliver a poke (it is %s and its goal is still open): %w", recipient, a.Status, err)
		}
		// No enqueue. The wake already delivered the body, and doing both is the
		// same poke twice.
		return nil
	}
	return w.Queue.Inject(ctx, recipient, body)
}
