// cmd/store_dispatch.go — `sprawl store dispatch` (QUM-1250, M1b).
//
// The reactive half of the event log, as a foreground process: a seq-cursor
// consumer that notifies contract owners when their results land, acks those
// notifications at the recipient's next turn boundary, sweeps stalled goals, and
// reconciles spawn intents against local state at startup.
//
// ===========================================================================
// THIS IS NO LONGER THE ONLY WAY THE LOOP RUNS
// ===========================================================================
//
// QUM-1250 deliberately shipped this command unwired and recorded the wiring as
// an obligation on QUM-1252. That obligation is now discharged: under Option A
// `sprawl enter` starts the same dispatcher and sweepers for the life of the
// session, gated on `event_log.enabled` — see cmd/enter_dispatch.go. Both paths
// assemble through buildDispatchStack below, so the handler set cannot drift
// between them.
//
// The command remains, and is not redundant: it is how a dispatcher is run on a
// host with no TUI session, how `--once` drives a single deterministic catch-up
// pass in the e2e rows, and the only path that reconciles spawn intents at
// startup. Two hosts running it concurrently with a session is safe and expected
// — event_claims is what makes each event acted on once.
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
//   - THERE IS NO SPAWN HANDLER. Launching a session needs the supervisor. As of
//     QUM-1252 something DOES emit spawn_requested — the goal_opened handler
//     registered below turns an engine-driven goal into a request — and a
//     `sprawl enter` dispatcher consumes it. So a request appended here waits for
//     a session, and is UNCLAIMED while it waits: registering the handler out
//     here would take the claim and then fail, which is worse than not
//     registering, because the claim is what stops the session from running it.
//     That is stated at startup rather than left to be discovered as "the goal
//     never started".
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
	"os"
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
		"session. Both are reported at startup. A notification whose injection " +
		"fails is NOT lost: the contract stays open and each sweep re-delivers " +
		"it on a widening backoff, until a cap past which it is recorded " +
		"undelivered and never retried again.",
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

// defaultHostIdentity is this process's host identity for claims, affinity and
// reconciliation.
//
// IT MUST BE UNIQUE PER MACHINE, and the first version was not: it used
// store.ProvisionalProjectID, i.e. `"local:" + sprawlRoot` — a FILESYSTEM PATH.
// Two machines with the same checkout path (the normal case for a container
// image, and exactly the deployment this milestone exists to serve) reported the
// same host, and code review traced what that breaks:
//
//   - AFFINITY: both machines match `affinity == d.host`, so a worktree-bound
//     event is claimable where the worktree does not exist.
//   - RECONCILE: machine B reads machine A's intents as its own, finds no local
//     trace, and past the grace period emits spawn_failed for an agent that is
//     alive and well on A.
//
// Hostname AND root, because neither alone is enough: two checkouts on one
// machine are two hosts for affinity purposes (they have different worktrees),
// and two machines sharing a path are two hosts for every purpose.
//
// A hostname that cannot be read is a REFUSAL rather than a fallback to the path
// alone: silently returning a non-unique identity is how the original defect
// looked from the outside.
func defaultHostIdentity(deps *storeDeps) string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return ""
	}
	return name + ":" + deps.SprawlRoot
}

// dispatchSweepInterval is how often the sweeper runs.
//
// Far longer than the dispatch poll: the dispatcher is chasing latency on newly
// appended events, while the sweeper is looking for things that have not happened
// for tens of minutes. Sweeping at the dispatch interval would run the candidate
// query hundreds of times per stall threshold to reach the same conclusion.
const dispatchSweepInterval = 2 * time.Minute

// dispatchDegradedRefusal is the refusal returned when the ledger is degraded.
//
// A FUNCTION so the refusal is reachable from a test: everything above its call
// site needs a live Postgres. It keeps %w — the cause must stay in the chain for
// errors.Is, and the redaction happens at the print in cmd/root.go.
func dispatchDegradedRefusal(derr error) error {
	return fmt.Errorf("the event log is unreachable, so dispatch cannot start: %w\nnext: run `sprawl store doctor` to diagnose the connection; running agents are unaffected and their telemetry is spilling locally", derr)
}

// dispatchLogger builds the dispatch logger, REDACTING (QUM-1280).
//
// The store's dispatcher, sweeper and notify handlers log their failures with
// the raw error attached, and "dispatch pass failed, retrying" fires on a loop
// during a database outage — the highest-volume DSN carrier in the tree. The
// wrapper is at the handler rather than at those ~17 call sites so a log line
// added later cannot bypass it; see internal/store/redactslog.go.
func dispatchLogger(w io.Writer) *slog.Logger {
	return slog.New(store.RedactingHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

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
		return dispatchDegradedRefusal(derr)
	}
	pool := ledger.Pool()
	if pool == nil {
		return fmt.Errorf("the event log has no connection pool, so dispatch cannot start\nnext: run `sprawl store doctor`")
	}

	host := dispatchHost
	if host == "" {
		host = defaultHostIdentity(deps)
	}
	if host == "" {
		return fmt.Errorf("could not determine a host identity for this machine (os.Hostname failed)\nnext: pass --host <stable-unique-id>; two machines sharing a host identity is a DATA-LOSS configuration — each reads the other's spawn intents as its own and can declare a live agent failed")
	}
	out := deps.Stdout
	errOut := deps.Stderr

	// A LOGGER, WIRED EXPLICITLY. Every deps struct in internal/store defaults a
	// nil Logger to slog.DiscardHandler — which is right for a library and wrong
	// for this process: without one, Dispatcher.Run's "dispatch pass failed,
	// retrying" WARN goes nowhere, so a database outage looks exactly like an
	// idle log. Found by the dispatch-db-outage e2e row, which could not find
	// any evidence of the outage it had just caused.
	logger := dispatchLogger(deps.Stderr)

	reportDispatchLimits(out, host)

	// No Sup and so no StallAfter: without a supervisor the stall sweeper is inert
	// by construction (see buildDispatchStack), so a threshold here would be a
	// config key that changes nothing. The knob is read on the session path, which
	// is the one that can act on it.
	stack, err := buildDispatchStack(dispatchStackOpts{
		Ledger:     ledger,
		SprawlRoot: deps.SprawlRoot,
		Host:       host,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	// Startup reconciliation, BEFORE the loop. An orphan whose intent is still
	// open must be adopted before anything else acts on the log, or the sweeper
	// spends its first pass chasing a contract that is about to be closed.
	// A RECONCILE FAILURE DOES NOT STOP THE PROCESS.
	//
	// It used to. Combined with DiskAgents.Reclaim refusing without a wired
	// remover, ONE stray — a failed intent plus a local agent of that name —
	// permanently prevented `store dispatch` from starting on that host, which
	// stops notifications and acks: work with nothing to do with the stray. A
	// transient database error inside reconcile had the same effect.
	//
	// Reconciliation is a best-effort tidy-up of a previous run. Refusing to
	// dispatch because it could not be completed is the wrong blast radius, so it
	// is reported loudly and the loop starts anyway.
	if err := reportReconcile(ctx, out, stack.reconcile); err != nil {
		reportReconcileFailure(errOut, err)
	}

	if dispatchOnce {
		res, err := stack.dispatcher.Step(ctx)
		fmt.Fprintf(out, "dispatch pass: scanned %d, handled %d, skipped %d, cursor at %d\n",
			res.Scanned, res.Handled, res.Skipped, res.AdvancedTo)
		if err != nil {
			return err
		}
		if !dispatchNoSweeper {
			reportSweep(ctx, out, errOut, stack.sweeper)
			reportNotifySweep(ctx, out, errOut, stack.notifySweeper)
		}
		return nil
	}

	if !dispatchNoSweeper {
		go runSweepTicker(ctx, out, errOut, stack.sweeper, stack.notifySweeper)
	}
	fmt.Fprintf(out, "dispatching (Ctrl-C to stop)\n")
	return stack.dispatcher.Run(ctx)
}

// dispatchStack is everything a dispatch loop needs, already wired.
//
// It exists because `sprawl enter` must run the SAME loop this command runs
// (QUM-1252, Option A): the dispatcher shipped unwired, and a second
// hand-assembled copy of this wiring in the session path is how the two silently
// diverge — a handler registered here and forgotten there means an event type
// that is dispatched from the CLI and inert in every real session, which reads
// as "the log is quiet" rather than as a missing handler.
type dispatchStack struct {
	dispatcher    *store.Dispatcher
	reconcile     store.ReconcileDeps
	sweeper       store.SweeperDeps
	notifySweeper store.NotifySweeperDeps
}

// dispatchStackOpts is buildDispatchStack's input.
//
// A struct rather than a parameter list because the two optional members are
// both zero-valued on the standalone path, and `buildDispatchStack(ledger, root,
// host, nil, 0, logger)` is a call nobody can read.
type dispatchStackOpts struct {
	Ledger     *store.Ledger
	SprawlRoot string
	Host       string
	// Sup is nil outside a `sprawl enter` session. See buildDispatchStack.
	Sup dispatchadapt.SessionSupervisor
	// StallAfter is zero when the caller has no config to read it from, which
	// leaves store.DefaultStallAfter in force.
	StallAfter time.Duration
	Logger     *slog.Logger
}

// buildDispatchStack wires the dispatcher, the reconciler and both sweepers.
//
// It takes an ALREADY-VALIDATED ledger: enabled, non-degraded, with a pool. The
// two callers reach that state differently — the CLI refuses with an actionable
// next-step, the session simply declines to start — so the checks stay with the
// callers and this function stays a pure assembly step.
//
// opts.Sup is nil from `sprawl store dispatch` and the live supervisor from
// `sprawl enter`. It is the ONLY difference between the two stacks — it decides
// the spawn handler, the turn observation and whether a poke can revive a
// crashed owner — and it is a parameter rather than something derived here
// because this function cannot see whether a supervisor exists; that is a
// property of the process, not the ledger.
func buildDispatchStack(o dispatchStackOpts) (*dispatchStack, error) {
	ledger, sprawlRoot, host, logger := o.Ledger, o.SprawlRoot, o.Host, o.Logger
	pool := ledger.Pool()
	registry := ledger.Registry()
	emitter := store.LedgerEmitter{Ledger: ledger}
	reader := &store.PgEventReader{Pool: pool, Registry: registry}
	notifies := &store.PgNotifyReader{Pool: pool, Registry: registry}
	disk := &dispatchadapt.DiskAgents{SprawlRoot: sprawlRoot}
	queue := &dispatchadapt.QueueInjector{SprawlRoot: sprawlRoot}

	// THE TWO SEAMS THAT DECIDE WHETHER THE STALL SWEEPER DOES ANYTHING.
	//
	// With no supervisor both fall back to the disk-only pair, and the sweeper is
	// inert by construction: DiskAgents cannot observe turn state, so the
	// tri-state gate skips every candidate. With one, turn state is observable
	// for an agent with no live subprocess and a poke can wake a crashed owner —
	// which together are AC5. The fallback is deliberate rather than a
	// degradation to be fixed: the alternative for a process that cannot see turn
	// state is to poke every working agent in the fleet.
	var local store.LocalAgents = disk
	var injector store.Injector = queue
	if o.Sup != nil {
		local = &dispatchadapt.SupervisorAgents{Disk: disk, Sup: o.Sup}
		injector = &dispatchadapt.WakeInjector{Queue: queue, Sup: o.Sup, Disk: disk}
	}
	spawner := dispatchSpawner(o.Sup)

	notify, err := store.NewNotifyHandler(store.NotifyHandlerDeps{
		Emitter:  emitter,
		Injector: injector,
		Lookup:   reader,
		Notifies: notifies,
		Local:    local,
		// Ownership falls back to the root agent, which is what the plan means by
		// "reassign to root/workflow engine". The engine takes over in M3a.
		FallbackOwner: fallbackOwner(sprawlRoot),
		Host:          host,
		Consumer:      dispatchConsumer,
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}
	// goal_opened -> spawn_requested (QUM-1252). Registered in BOTH dispatch
	// paths, because it needs no supervisor: it only appends. What consumes
	// spawn_requested and actually launches a session is the separate handler
	// below, which does need one.
	goalSpawn, err := store.NewGoalSpawnHandler(store.GoalSpawnHandlerDeps{
		Emitter: emitter,
		Names:   &dispatchadapt.PoolNamer{SprawlRoot: sprawlRoot},
		Logger:  logger,
	})
	if err != nil {
		return nil, err
	}
	// rework_requested -> the next spawn_requested (QUM-1252, AC3). Registered
	// alongside goalSpawn and for the same reason: it only appends, so it needs
	// no supervisor and belongs in both dispatch paths. Unregistered, an owner's
	// rejection would be recorded and then acted on by nobody, which reads
	// exactly like a rework that was never asked for.
	rework, err := store.NewReworkHandler(store.ReworkHandlerDeps{
		Emitter: emitter,
		Names:   &dispatchadapt.PoolNamer{SprawlRoot: sprawlRoot},
		Lookup:  &store.PgEventReader{Pool: pool, Registry: registry},
		Logger:  logger,
	})
	if err != nil {
		return nil, err
	}
	// spawn_requested -> an actual agent, write-ahead first (QUM-1252). Declared
	// as the interface so that with no spawner it stays a TRUE nil rather than a
	// non-nil interface holding a nil pointer, which dispatchHandlerSet's guard
	// could not tell from a real handler.
	var spawn store.Handler
	if spawner != nil {
		h, err := store.NewSpawnHandler(store.SpawnHandlerDeps{
			Emitter: emitter,
			Spawner: spawner,
			Host:    host,
			Logger:  logger,
		})
		if err != nil {
			return nil, err
		}
		spawn = h
	}

	ack, err := store.NewNotifyAckHandler(store.NotifyAckHandlerDeps{
		Emitter:  emitter,
		Notifies: notifies,
		Host:     host,
		Logger:   logger,
	})
	if err != nil {
		return nil, err
	}

	dispatcher, err := store.NewDispatcher(store.DispatcherDeps{
		Events:    reader,
		Claims:    &store.PgClaimStore{Pool: pool},
		Cursor:    &store.FileCursorStore{Root: sprawlRoot},
		Registry:  registry,
		ProjectID: ledger.ProjectID(),
		Host:      host,
		Consumer:  dispatchConsumer,
		// Per-path, because the handler tables differ — see
		// dispatchCursorConsumer.
		CursorConsumer: dispatchCursorConsumer(spawner != nil),
		Handlers:       dispatchHandlerSet(notify, ack, goalSpawn, rework, spawn),
		Logger:         logger,
		// Doorbell deliberately nil: correctness is the poll, and a standalone
		// process holding a LISTEN connection open buys latency this process does
		// not need — its deliveries are already asynchronous.
	})
	if err != nil {
		return nil, err
	}

	return &dispatchStack{
		dispatcher: dispatcher,
		reconcile: store.ReconcileDeps{
			Intents:   &store.PgIntentReader{Pool: pool, Registry: registry},
			Local:     local,
			Emitter:   emitter,
			ProjectID: ledger.ProjectID(),
			Host:      host,
			Logger:    logger,
		},
		sweeper:       sweeperDeps(pool, registry, local, emitter, injector, ledger.ProjectID(), host, o.StallAfter, logger),
		notifySweeper: notifySweeperDeps(pool, registry, emitter, injector, ledger.ProjectID(), host, logger),
	}, nil
}

// dispatchHandlerSet is the event-type -> handler table.
//
// A function rather than a literal inside buildDispatchStack so the table can be
// asserted without a live Postgres: everything else in that function needs a
// pool, and an unregistered handler is invisible from outside — the event is
// scanned, skipped, and the loop reports itself healthy.
//
// Kept explicit rather than "anything with closes_event_id": a handler
// registered by name is a decision, and a catch-all would silently start
// notifying on event types nobody has thought about.
// spawn is nil on the standalone path and only there — see the comment on the
// registration below for why that is a deliberate hole rather than a gap.
func dispatchHandlerSet(notify, ack, goalSpawn, rework, spawn store.Handler) map[string]store.Handler {
	set := map[string]store.Handler{
		// Every close-typed event that can land a result for an owner.
		"goal_closed": notify,
		// The ack, from the log rather than a runtime hook.
		"turn_finished": ack,
		// The engine's start leg: a goal becomes a spawn request.
		"goal_opened": goalSpawn,
		// The rework leg: a rejected result becomes the next spawn request.
		"rework_requested": rework,
	}
	// CONDITIONAL, unlike every other row. Launching a session needs the
	// supervisor, which exists only inside `sprawl enter`. A standalone
	// `sprawl store dispatch` that registered this anyway would CLAIM each
	// spawn_requested and then fail it — and the claim is precisely what stops
	// the session's dispatcher, which could have run it, from taking the event.
	// Leaving it unregistered means the event is skipped without a claim, so the
	// session's dispatcher still scans it and acts on it.
	//
	// That last sentence is only true because the two loops have SEPARATE
	// cursors (dispatchCursorConsumer below). Sharing one would make the
	// standalone loop's skip advance the position the session loop reads, and the
	// event would be lost rather than deferred — silently, and only for the
	// types the two tables disagree about.
	if spawn != nil {
		set["spawn_requested"] = spawn
	}
	return set
}

// dispatchConsumer is the event_claims consumer name for this loop.
//
// A CONSTANT, not derived from the host: two hosts must COMPETE for an event, and
// competing is what the shared consumer name expresses. A per-host consumer name
// would give every host its own claim key, so every host would act on every
// event — which is the exactly-once failure, arriving through a naming decision.
const dispatchConsumer = "dispatcher"

// dispatchCursorConsumer names a loop's SCAN POSITION, which — unlike the claims
// consumer above — must NOT be shared between the two dispatch paths.
//
// The paths run different handler tables (only the session has a supervisor, so
// only it handles spawn_requested), and an unhandled type advances the cursor
// without claiming. One shared cursor therefore lets the standalone loop advance
// past a spawn_requested the session loop would have handled, leaving no claim
// row, no lease, and nothing for TakeoverExpired or the reconciler to recover.
// Two names, so each loop's progress is its own.
func dispatchCursorConsumer(session bool) string {
	if session {
		return dispatchConsumer + "-session"
	}
	return dispatchConsumer + "-standalone"
}

// stallAfter of zero is PASSED THROUGH rather than replaced here, because
// store.Sweep already reads a zero as "use DefaultStallAfter". Substituting the
// default at this seam would put a second copy of that decision in the tree, and
// the two would disagree the first time one of them changed.
func sweeperDeps(pool *pgxpool.Pool, registry *store.Registry, local store.LocalAgents,
	emitter store.EventEmitter, injector store.Injector,
	projectID uuid.UUID, host string, stallAfter time.Duration, sweepLogger *slog.Logger,
) store.SweeperDeps {
	return store.SweeperDeps{
		Goals:      &store.PgSweepReader{Pool: pool, Registry: registry},
		Local:      local,
		Emitter:    emitter,
		Injector:   injector,
		ProjectID:  projectID,
		Host:       host,
		StallAfter: stallAfter,
		Logger:     sweepLogger,
	}
}

func notifySweeperDeps(pool *pgxpool.Pool, registry *store.Registry,
	emitter store.EventEmitter, injector store.Injector,
	projectID uuid.UUID, host string, sweepLogger *slog.Logger,
) store.NotifySweeperDeps {
	return store.NotifySweeperDeps{
		Notifications: &store.PgNotifySweepReader{Pool: pool, Registry: registry},
		Emitter:       emitter,
		Injector:      injector,
		ProjectID:     projectID,
		Host:          host,
		Logger:        sweepLogger,
	}
}

func runSweepTicker(ctx context.Context, out, errOut io.Writer, deps store.SweeperDeps, notifyDeps store.NotifySweeperDeps) {
	t := time.NewTicker(dispatchSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reportSweep(ctx, out, errOut, deps)
			reportNotifySweep(ctx, out, errOut, notifyDeps)
		}
	}
}

// reportNotifySweep runs the notification re-delivery pass.
//
// A SEPARATE PASS from the goal sweep rather than a stage inside it, because the
// two answer different questions about different subjects and a failure in
// either must not stop the other: a store that cannot record a poke can still be
// reachable enough to say a notification was already delivered, and a fleet with
// no goals at all can still have an undelivered result.
func reportNotifySweep(ctx context.Context, out, errOut io.Writer, deps store.NotifySweeperDeps) { //nolint:revive // out is the success surface, errOut the failure surface
	res, err := store.SweepNotifications(ctx, deps)
	if err != nil {
		fmt.Fprintf(errOut, "notification sweep failed: %s\n", store.RedactError(err))
		return
	}
	if res.Considered > 0 {
		fmt.Fprintf(out, "notify sweep: considered %d, delivered %d, quarantined %d, skipped %d\n",
			res.Considered, res.Delivered, res.Quarantined, res.Skipped)
	}
}

func reportSweep(ctx context.Context, out, errOut io.Writer, deps store.SweeperDeps) { //nolint:revive // out is the success surface, errOut the failure surface
	res, err := store.Sweep(ctx, deps)
	if err != nil {
		// STDERR. Every other failure in this file goes there, and a failure on
		// stdout is invisible to a caller that separates the streams.
		fmt.Fprintf(errOut, "sweep failed: %s\n", store.RedactError(err))
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

// reportReconcileFailure reports a startup reconciliation that did not complete.
//
// A FUNCTION RATHER THAN THREE INLINE Fprintfs so the failure surface is
// reachable without a live Postgres: everything above its call site in
// runStoreDispatch needs a real pool, so inline this and the redaction below is
// testable only from the dispatch e2e rows.
func reportReconcileFailure(errOut io.Writer, err error) {
	fmt.Fprintf(errOut, "startup reconciliation did not complete: %s\n", store.RedactError(err))
	fmt.Fprintf(errOut, "  continuing anyway: notifications and acks do not depend on it\n")
	fmt.Fprintf(errOut, "  next: `sprawl store doctor`, and check for a stray agent named by a failed spawn intent\n")
}

func reportReconcile(ctx context.Context, out io.Writer, deps store.ReconcileDeps) error {
	res, err := store.Reconcile(ctx, deps)
	if err != nil {
		return err
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
	fmt.Fprintf(out, "  a failed injection is retried by each sweep on a widening backoff, then recorded undelivered at the cap and never retried again\n")
	fmt.Fprintf(out, "  the stall sweeper is INERT here: turn state is only observable inside a sprawl session, and an unobserved turn state is never poked\n")
	fmt.Fprintf(out, "  goal_opened IS handled here — it appends spawn_requested — but spawn_requested is NOT handled here: launching a session needs the supervisor, so a goal opened against this host gets a request and waits for a `sprawl enter` dispatcher to turn it into an agent\n")
	fmt.Fprintf(out, "  cursor: %s (this loop's own scan position; the `sprawl enter` dispatcher keeps a separate one, so the spawn_requested skipped above is still scanned there)\n", dispatchCursorConsumer(false))
}
