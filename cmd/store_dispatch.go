// cmd/store_dispatch.go — `sprawl store dispatch` (QUM-1250, M1b).
//
// The reactive half of the event log, as a foreground process: a seq-cursor
// consumer that notifies contract owners when their results land, acks those
// notifications at the recipient's next turn boundary, sweeps stalled goals, and
// reconciles spawn intents against local state at startup.
//
// ===========================================================================
// WHY THIS IS A SEPARATE COMMAND RATHER THAN WIRED INTO `sprawl enter`
// ===========================================================================
//
// Two reasons, recorded because the second is what makes it the right call
// rather than a deferral (approved as decision #3 on QUM-1250):
//
//  1. `cmd/enter.go` and `internal/supervisor/*.go` are named by the e2e matrix's
//     handoff glob row plus seven further per-file rows, so wiring a dispatcher
//     into the session lifecycle would owe ~8 live-claude rows for a component
//     with no product surface yet.
//  2. M3A NEEDS THAT WIRING ANYWAY, and is the first milestone with a reason for
//     it: until a workflow engine exists nothing appends the events this
//     consumes, so a dispatcher started by `sprawl enter` today would idle-poll
//     an empty tail. The obligation is recorded on QUM-1252.
//
// ===========================================================================
// WHAT THIS PROCESS CANNOT DO, AND WHY IT SAYS SO OUT LOUD
// ===========================================================================
//
// The supervisor, the runtime registry and the live Claude sessions all live
// inside a `sprawl enter` process. From out here:
//
//   - Delivery is DURABLE BUT NOT IMMEDIATE. Enqueueing writes the maildir
//     envelope and the queue entry; the recipient's own supervisor drains
//     `pending/` at its next turn boundary or redrain tick. Nothing is lost, and
//     the owner_notify contract stays open until acked, so a slow delivery and a
//     lost one are both visible.
//   - TURN STATE IS INVISIBLE. It lives only in the supervisor's in-memory phase
//     machine, so the sweeper's in-turn gate cannot be evaluated, so THE SWEEPER
//     IS INERT HERE: every candidate is skipped on the unobserved-turn-state
//     gate. That is deliberate and it is the safe direction — the alternative
//     reports "not in turn" for every working agent and pokes them all.
//   - THERE IS NO SPAWN HANDLER. Launching a session needs the supervisor, and
//     nothing emits spawn_requested in M1b anyway.
//
// The command PRINTS these limits at startup rather than leaving them to be
// discovered, because per /cli-ux-best-practices the primary consumer is an agent
// and a process that silently does less than its name implies is the worst
// possible output.
package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/dmotles/sprawl/internal/dispatchadapt"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// fallbackOwner is who inherits a contract whose owner is permanently gone.
//
// The ROOT agent, read from disk, which is what the plan means by "reassign to
// root/workflow engine" — the engine takes over in M3a. An empty result DISABLES
// reassignment rather than reassigning to "": a notification addressed to nobody
// is an owner_notify contract nothing can ever ack, which is strictly worse than
// leaving the original owner named and letting the sweeper re-deliver.
func fallbackOwner(sprawlRoot string) string {
	return state.ReadRootName(sprawlRoot)
}

var (
	dispatchHost      string
	dispatchOnce      bool
	dispatchNoSweeper bool
)

var storeDispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Run the event-log dispatch loop: owner notifications, acks, and the stall sweeper",
	Long: "Consume the shared event log and act on it (QUM-1250). Runs in the " +
		"foreground until interrupted.\n\n" +
		"Correctness is a seq cursor plus a poll; LISTEN/NOTIFY is not used and " +
		"is not required. Exactly-once is carried by event_claims, so running " +
		"this on several hosts at once is safe and expected — each event is " +
		"acted on once. The cursor is reconstructible: delete " +
		".sprawl/store/dispatch/ to re-scan, and claims make the repeat a no-op.\n\n" +
		"LIMITS OF A STANDALONE RUN: notifications are enqueued durably but " +
		"delivered when the recipient next drains, and the stall sweeper is " +
		"inert because turn state is only observable from inside a sprawl " +
		"session. Both are reported at startup.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runStoreDispatch(cmd.Context(), resolveStoreDeps())
	},
}

func init() {
	storeDispatchCmd.Flags().StringVar(&dispatchHost, "host", "",
		"Host identity for claims and host affinity (default: a stable id derived from this checkout)")
	storeDispatchCmd.Flags().BoolVar(&dispatchOnce, "once", false,
		"Run one catch-up pass and exit, instead of polling")
	storeDispatchCmd.Flags().BoolVar(&dispatchNoSweeper, "no-sweeper", false,
		"Skip the stall sweeper entirely")
	storeCmd.AddCommand(storeDispatchCmd)
}

// dispatchSweepInterval is how often the sweeper runs.
//
// Far longer than the dispatch poll: the dispatcher is chasing latency on newly
// appended events, while the sweeper is looking for things that have not happened
// for tens of minutes. Sweeping at the dispatch interval would run the candidate
// query hundreds of times per stall threshold to reach the same conclusion.
const dispatchSweepInterval = 2 * time.Minute

func runStoreDispatch(ctx context.Context, deps *storeDeps) error {
	if err := requireSprawlRoot(deps); err != nil {
		return err
	}
	ledger, err := deps.OpenLedger(ctx, deps.SprawlRoot)
	if err != nil {
		return err
	}
	if !ledger.Enabled() {
		return fmt.Errorf("the event log is disabled, so there is nothing to dispatch\nnext: enable it with `sprawl config set event_log.enabled true`, then see docs/event-log-setup.md for the DSN and `sprawl store migrate`")
	}
	// A DEGRADED ledger is refused rather than run. Every consumer here either
	// appends a contract event or takes a claim, and both need the database —
	// spawn_intent, owner_notify and goal_poke are all non-spillable by design,
	// so a degraded run would fail on every event while looking busy.
	if derr := ledger.DegradedError(); derr != nil {
		return fmt.Errorf("the event log is unreachable, so dispatch cannot start: %w\nnext: run `sprawl store doctor` to diagnose the connection; running agents are unaffected and their telemetry is spilling locally", derr)
	}
	pool := ledger.Pool()
	if pool == nil {
		return fmt.Errorf("the event log has no connection pool, so dispatch cannot start\nnext: run `sprawl store doctor`")
	}

	host := dispatchHost
	if host == "" {
		host = store.ProvisionalProjectID(deps.SprawlRoot)
	}
	out := deps.Stdout

	// A LOGGER, WIRED EXPLICITLY. Every deps struct in internal/store defaults a
	// nil Logger to slog.DiscardHandler — which is right for a library and wrong
	// for this process: without one, Dispatcher.Run's "dispatch pass failed,
	// retrying" WARN goes nowhere, so a database outage looks exactly like an
	// idle log. Found by the dispatch-db-outage e2e row, which could not find
	// any evidence of the outage it had just caused.
	logger := slog.New(slog.NewTextHandler(deps.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	emitter := store.LedgerEmitter{Ledger: ledger}
	registry := ledger.Registry()
	reader := &store.PgEventReader{Pool: pool, Registry: registry}
	claims := &store.PgClaimStore{Pool: pool}
	local := &dispatchadapt.DiskAgents{SprawlRoot: deps.SprawlRoot}
	injector := &dispatchadapt.QueueInjector{SprawlRoot: deps.SprawlRoot}

	reportDispatchLimits(out, host)

	// Startup reconciliation, BEFORE the loop. An orphan whose intent is still
	// open must be adopted before anything else acts on the log, or the sweeper
	// spends its first pass chasing a contract that is about to be closed.
	if err := reportReconcile(ctx, out, store.ReconcileDeps{
		Intents:   &store.PgIntentReader{Pool: pool, Registry: registry},
		Local:     local,
		Emitter:   emitter,
		ProjectID: ledger.ProjectID(),
		Host:      host,
		Logger:    logger,
	}); err != nil {
		return err
	}

	notify, err := store.NewNotifyHandler(store.NotifyHandlerDeps{
		Emitter:  emitter,
		Injector: injector,
		Claims:   claims,
		Lookup:   reader,
		Local:    local,
		// Ownership falls back to the root agent, which is what the plan means by
		// "reassign to root/workflow engine". The engine takes over in M3a.
		FallbackOwner: fallbackOwner(deps.SprawlRoot),
		Host:          host,
		Consumer:      dispatchConsumer,
		Logger:        logger,
	})
	if err != nil {
		return err
	}
	ack, err := store.NewNotifyAckHandler(store.NotifyAckHandlerDeps{
		Emitter:  emitter,
		Notifies: &store.PgNotifyReader{Pool: pool, Registry: registry},
		Host:     host,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	dispatcher, err := store.NewDispatcher(store.DispatcherDeps{
		Events:    reader,
		Claims:    claims,
		Cursor:    &store.FileCursorStore{Root: deps.SprawlRoot},
		Registry:  registry,
		ProjectID: ledger.ProjectID(),
		Host:      host,
		Consumer:  dispatchConsumer,
		Handlers: map[string]store.Handler{
			// Every close-typed event that can land a result for an owner. Kept
			// explicit rather than "anything with closes_event_id": a handler
			// registered by name is a decision, and a catch-all would silently
			// start notifying on event types nobody has thought about.
			"goal_closed": notify,
			// The ack, from the log rather than a runtime hook.
			"turn_finished": ack,
		},
		Logger: logger,
		// Doorbell deliberately nil: correctness is the poll, and a standalone
		// process holding a LISTEN connection open buys latency this process does
		// not need — its deliveries are already asynchronous.
	})
	if err != nil {
		return err
	}

	if dispatchOnce {
		res, err := dispatcher.Step(ctx)
		fmt.Fprintf(out, "dispatch pass: scanned %d, handled %d, skipped %d, cursor at %d\n",
			res.Scanned, res.Handled, res.Skipped, res.AdvancedTo)
		if err != nil {
			return err
		}
		if !dispatchNoSweeper {
			reportSweep(ctx, out, sweeperDeps(pool, registry, local, claims, emitter, injector, ledger.ProjectID(), host, logger))
		}
		return nil
	}

	if !dispatchNoSweeper {
		go runSweepTicker(ctx, out, sweeperDeps(pool, registry, local, claims, emitter, injector, ledger.ProjectID(), host, logger))
	}
	fmt.Fprintf(out, "dispatching (Ctrl-C to stop)\n")
	return dispatcher.Run(ctx)
}

// dispatchConsumer is the event_claims consumer name for this loop.
//
// A CONSTANT, not derived from the host: two hosts must COMPETE for an event, and
// competing is what the shared consumer name expresses. A per-host consumer name
// would give every host its own claim key, so every host would act on every
// event — which is the exactly-once failure, arriving through a naming decision.
const dispatchConsumer = "dispatcher"

func sweeperDeps(pool *pgxpool.Pool, registry *store.Registry, local store.LocalAgents,
	claims store.ClaimStore, emitter store.EventEmitter, injector store.Injector,
	projectID uuid.UUID, host string, sweepLogger *slog.Logger,
) store.SweeperDeps {
	return store.SweeperDeps{
		Goals:     &store.PgSweepReader{Pool: pool, Registry: registry},
		Local:     local,
		Claims:    claims,
		Emitter:   emitter,
		Injector:  injector,
		ProjectID: projectID,
		Host:      host,
		Elect:     store.PgSweepElection(pool, projectID),
		Logger:    sweepLogger,
	}
}

func runSweepTicker(ctx context.Context, out io.Writer, deps store.SweeperDeps) {
	t := time.NewTicker(dispatchSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reportSweep(ctx, out, deps)
		}
	}
}

func reportSweep(ctx context.Context, out io.Writer, deps store.SweeperDeps) {
	res, err := store.Sweep(ctx, deps)
	if err != nil {
		fmt.Fprintf(out, "sweep failed: %v\n", err)
		return
	}
	if !res.Elected {
		return
	}
	// Reported even when nothing happened, and Skipped is reported alongside
	// Poked deliberately: "considered 12, poked 0, skipped 12" is a very
	// different fact from "considered 0", and a surface that printed only the
	// poke count would render them identically.
	if res.Considered > 0 {
		fmt.Fprintf(out, "sweep: considered %d, poked %d, quarantined %d, skipped %d\n",
			res.Considered, res.Poked, res.Quarantined, res.Skipped)
	}
}

func reportReconcile(ctx context.Context, out io.Writer, deps store.ReconcileDeps) error {
	res, err := store.Reconcile(ctx, deps)
	if err != nil {
		return fmt.Errorf("startup reconciliation: %w", err)
	}
	fmt.Fprintf(out, "reconcile: adopted %d, failed %d, reclaimed %d, in flight %d, unattributed %d\n",
		res.Adopted, res.Failed, res.Reclaimed, res.InFlight, res.Unattributed)
	if res.Unattributed > 0 {
		fmt.Fprintf(out, "  note: %d local agent(s) are not mentioned by any spawn intent and were left strictly alone (expected: agents created outside the dispatcher have no intent)\n",
			res.Unattributed)
	}
	return nil
}

// reportDispatchLimits states what this process cannot do, before it starts.
//
// Per /cli-ux-best-practices the primary consumer is an agent, and a process that
// silently does less than its name implies is the worst available output. Both
// limits below are structural rather than transient, so they are printed
// unconditionally rather than only when they bite.
func reportDispatchLimits(out io.Writer, host string) {
	fmt.Fprintf(out, "host: %s\n", host)
	fmt.Fprintf(out, "consumer: %s (shared across hosts; event_claims makes each event act-once)\n", dispatchConsumer)
	fmt.Fprintf(out, "limits of a standalone run:\n")
	fmt.Fprintf(out, "  notifications are enqueued durably and delivered when the recipient next drains, not immediately\n")
	fmt.Fprintf(out, "  the stall sweeper is INERT here: turn state is only observable inside a sprawl session, and an unobserved turn state is never poked\n")
	fmt.Fprintf(out, "  no spawn handler: launching a session needs the supervisor (M3a)\n")
}
