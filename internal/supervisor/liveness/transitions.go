package liveness

import (
	"errors"
	"fmt"
)

// ErrIllegalTransition is the sentinel returned (wrapped) by Validate for
// disallowed state transitions.
var ErrIllegalTransition = errors.New("liveness: illegal transition")

// edge is a directed transition on the AgentLiveness axis (sub-state bit
// excluded).
type edge struct {
	from AgentLiveness
	to   AgentLiveness
}

// legalEdges enumerates the legal transitions T1–T19 on the liveness axis.
// Multi-source transitions (T12/T16/T17) are expanded into explicit pairs.
// Running→Running sub-state edges (T4/T5) are handled separately in
// CanTransition and are intentionally absent here.
var legalEdges = map[edge]bool{
	{Unstarted, Starting}: true, // T1
	{Starting, Running}:   true, // T2
	{Starting, Faulted}:   true, // T3
	{Running, Faulted}:    true, // T6
	{Running, Stopping}:   true, // T7
	{Stopping, Stopped}:   true, // T8
	{Faulted, Recovering}: true, // T9
	{Recovering, Running}: true, // T10
	{Recovering, Faulted}: true, // T11
	// T12: {Running,Stopped,Faulted} -> Suspended (authoritative trigger
	// table; deliberately excludes Unstarted).
	{Running, Suspended}:       true,
	{Stopped, Suspended}:       true,
	{Faulted, Suspended}:       true,
	{Suspended, Resuming}:      true, // T13
	{Resuming, Running}:        true, // T14
	{Resuming, ResumeFailed}:   true, // T15
	{ResumeFailed, Recovering}: true, // T19
	// T16: {Running,Faulted,Stopped,Suspended,ResumeFailed} -> Killed
	{Running, Killed}:      true,
	{Faulted, Killed}:      true,
	{Stopped, Killed}:      true,
	{Suspended, Killed}:    true,
	{ResumeFailed, Killed}: true,
	// T17: {Running,Faulted,Stopped,Suspended,ResumeFailed} -> Retiring
	{Running, Retiring}:      true,
	{Faulted, Retiring}:      true,
	{Stopped, Retiring}:      true,
	{Suspended, Retiring}:    true,
	{ResumeFailed, Retiring}: true,
	{Retiring, Retired}:      true, // T18
	// QUM-722: T20–T29 pause/death edges.
	{Running, Pausing}: true, // T20
	{Pausing, Paused}:  true, // T21
	{Pausing, Killed}:  true, // T22
	{Pausing, Faulted}: true, // T23
	{Paused, Killed}:   true, // T25
	{Paused, Retiring}: true, // T26
	// T27: Running/Recovering/Resuming → Died (unexpected exit)
	{Running, Died}:    true,
	{Recovering, Died}: true,
	{Resuming, Died}:   true,
	// T29: Died → Killed/Retiring (extends T16/T17)
	{Died, Killed}:   true,
	{Died, Retiring}: true,
}

// CanTransition reports whether moving from one state to another is legal.
func CanTransition(from, to State) bool {
	// Invariant 5: Killed and Retired are absorbing — no outgoing edges.
	if from.Liveness == Killed || from.Liveness == Retired {
		return false
	}

	// T4/T5: Running sub-state toggle. A Running→Running move is only a
	// transition when the autonomous-turn bit actually flips.
	if from.Liveness == Running && to.Liveness == Running {
		return from.InTurn != to.InTurn
	}

	// Guard: the autonomous-turn bit may only be set while Running.
	if to.InTurn && to.Liveness != Running {
		return false
	}

	// Invariant 4: an autonomous turn must resolve before leaving Running,
	// except for a fault. T5 (back to idle Running) is handled above, so the
	// only remaining legal target from Running·AutonomousTurn is Faulted.
	if from.Liveness == Running && from.InTurn {
		return to.Liveness == Faulted
	}

	if !legalEdges[edge{from.Liveness, to.Liveness}] {
		return false
	}

	// You always enter Running idle (T2/T10/T14): the autonomous-turn bit is
	// raised later via T4.
	if to.Liveness == Running && to.InTurn {
		return false
	}

	return true
}

// Validate returns nil for a legal transition, or an error wrapping
// ErrIllegalTransition otherwise.
func Validate(from, to State) error {
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from.String(), to.String())
}
