// cmd/operability.go — `sprawl goals` and `sprawl workflows`, the operator's
// read-only view of the event log (QUM-1252, M3a slice 9).
//
// Two commands in one file on purpose, against the repo's usual one-per-file
// rule. They share a deps struct and, more importantly, the three-state gate
// that must distinguish "the log is off" from "the log is unreachable" from
// "nothing is outstanding". That gate is the one thing here that must never
// drift between the two commands, and splitting the file is how it would.
//
// Everything here is a read. Nothing appends, claims, or acknowledges — an
// operator inspecting the fleet must not be able to change what it is looking
// at.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/store"
)

// operabilityView is the surface these commands need. Declared here because the
// consumer defines the interface; *store.Ledger satisfies it.
//
// Enabled and DegradedError are members for userInbox's reason: a nil
// *store.Ledger IS the disabled store, and a typed-nil in an interface is a
// NON-nil interface value, so a nil check here would read a switched-off store
// as a working one and print "nothing is outstanding".
type operabilityView interface {
	Enabled() bool
	DegradedError() error
	AllOpenGoals(ctx context.Context) ([]store.OpenGoal, error)
	OpenWorkflows(ctx context.Context) ([]store.WorkflowSummary, error)
	EventsByWorkflowInstance(ctx context.Context, workflowID uuid.UUID, afterSeq int64, limit int) ([]store.DispatchedEvent, error)
}

type operabilityDeps struct {
	SprawlRoot string
	Open       func(ctx context.Context, sprawlRoot string) (operabilityView, error)
	Stdout     io.Writer
	Stderr     io.Writer
}

var defaultOperabilityDeps *operabilityDeps

func resolveOperabilityDeps() *operabilityDeps {
	if defaultOperabilityDeps != nil {
		return defaultOperabilityDeps
	}
	return &operabilityDeps{
		SprawlRoot: os.Getenv("SPRAWL_ROOT"),
		Open: func(ctx context.Context, sprawlRoot string) (operabilityView, error) {
			return store.Process(ctx, sprawlRoot)
		},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

// openOperability runs the shared gate: root, then off, then unreachable.
//
// The three are separate refusals rather than one empty listing because they
// call for different actions, and the empty listing is the answer an operator
// acts on by walking away.
func openOperability(ctx context.Context, deps *operabilityDeps) (operabilityView, error) {
	if deps.SprawlRoot == "" {
		return nil, fmt.Errorf("SPRAWL_ROOT environment variable is not set, so there is no project whose work to list\nnext: run this from inside a sprawl session, or set SPRAWL_ROOT to the repo root")
	}
	view, err := deps.Open(ctx, deps.SprawlRoot)
	if err != nil {
		return nil, err
	}
	if !view.Enabled() {
		return nil, fmt.Errorf("the event log is disabled on this host, so no goal or workflow is being recorded\nnext: enable it with `sprawl config set event_log.enabled true`, then see docs/event-log-setup.md")
	}
	if derr := view.DegradedError(); derr != nil {
		return nil, fmt.Errorf("the event log is unreachable, so outstanding work cannot be listed (`sprawl store doctor` diagnoses the connection): %w", derr)
	}
	return view, nil
}

func runGoals(ctx context.Context, deps *operabilityDeps) error {
	view, err := openOperability(ctx, deps)
	if err != nil {
		return err
	}
	goals, err := view.AllOpenGoals(ctx)
	if err != nil {
		return fmt.Errorf("reading outstanding goals: %w", err)
	}
	if len(goals) == 0 {
		fmt.Fprintln(deps.Stdout, "no goals are outstanding.")
		// Said even on the empty listing, because the empty listing is exactly
		// where it misleads: only engine-driven work opens a goal contract.
		fmt.Fprintln(deps.Stdout, "note: prose-spawned agents do not open goals yet — run `sprawl status` for those.")
		return nil
	}

	fmt.Fprintf(deps.Stdout, "%d goal(s) outstanding, oldest first:\n\n", len(goals))
	for _, g := range goals {
		fmt.Fprintf(deps.Stdout, "  %s\n", g.GoalEventID)
		fmt.Fprintf(deps.Stdout, "    type:     %s\n", g.GoalType)
		owner := g.Owner
		if owner == "" {
			// Not silently blanked: an unowned goal is nobody's, so the sweeper
			// has no one to poke about it, and that is worth seeing in the list.
			owner = "(unowned)"
		}
		if g.Legacy {
			owner += " (legacy spawn)"
		}
		fmt.Fprintf(deps.Stdout, "    owner:    %s\n", owner)
		fmt.Fprintf(deps.Stdout, "    open:     %s\n", humaneAge(time.Since(g.OpenedAt)))
		fmt.Fprintf(deps.Stdout, "    workflow: %s\n\n", g.WorkflowID)
	}
	fmt.Fprintln(deps.Stdout, "read one workflow's log with: sprawl workflows <workflow-id>")
	return nil
}

func runWorkflowsList(ctx context.Context, deps *operabilityDeps) error {
	view, err := openOperability(ctx, deps)
	if err != nil {
		return err
	}
	flows, err := view.OpenWorkflows(ctx)
	if err != nil {
		return fmt.Errorf("reading workflow instances: %w", err)
	}
	if len(flows) == 0 {
		fmt.Fprintln(deps.Stdout, "no workflow instances are in flight.")
		return nil
	}

	fmt.Fprintf(deps.Stdout, "%d workflow instance(s) in flight, oldest first:\n\n", len(flows))
	for _, w := range flows {
		fmt.Fprintf(deps.Stdout, "  %s\n", w.WorkflowID)
		fmt.Fprintf(deps.Stdout, "    events:   %d\n", w.Events)
		fmt.Fprintf(deps.Stdout, "    open:     %d contract(s)\n", w.OpenContracts)
		fmt.Fprintf(deps.Stdout, "    started:  %s ago\n", humaneAge(time.Since(w.StartedAt)))
		fmt.Fprintf(deps.Stdout, "    last:     %s ago\n\n", humaneAge(time.Since(w.LastEventAt)))
	}
	fmt.Fprintln(deps.Stdout, "read one with: sprawl workflows <workflow-id>")
	return nil
}

// workflowLogLimit bounds one page of the detail view. An instance's log is
// unbounded, and a command that printed all of it would be unusable exactly on
// the long-running goal an operator is most likely investigating.
var workflowLogLimit int

func runWorkflowLog(ctx context.Context, deps *operabilityDeps, id string, limit int) error {
	// Parsed before the store is opened, so a typo fails fast and locally.
	workflowID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("%q is not a uuid; it is the workflow id `sprawl workflows` and `sprawl goals` print: %w", id, err)
	}
	view, err := openOperability(ctx, deps)
	if err != nil {
		return err
	}
	events, err := view.EventsByWorkflowInstance(ctx, workflowID, 0, limit)
	if err != nil {
		return fmt.Errorf("reading workflow instance %s: %w", workflowID, err)
	}
	if len(events) == 0 {
		// Not an error: an id that names nothing and an instance that has yet to
		// record anything are the same observation from here, and claiming
		// either one specifically would be a guess.
		fmt.Fprintf(deps.Stdout, "no events on workflow instance %s.\n", workflowID)
		return nil
	}

	fmt.Fprintf(deps.Stdout, "workflow instance %s — %d event(s):\n\n", workflowID, len(events))
	for _, e := range events {
		fmt.Fprintf(deps.Stdout, "  #%d  %s  %s v%d\n", e.Seq, e.At.Format(time.RFC3339), e.SchemaName, e.SchemaVersion)
		if e.ClosesEventID != nil {
			fmt.Fprintf(deps.Stdout, "      closes:  %s\n", *e.ClosesEventID)
		}
		if e.FollowsEventID != nil {
			fmt.Fprintf(deps.Stdout, "      follows: %s\n", *e.FollowsEventID)
		}
		fmt.Fprintf(deps.Stdout, "      %s\n", e.Payload)
	}
	if len(events) == limit {
		// The page being full is not proof there is more, but it is the only
		// state in which there might be — and a truncated log presented as a
		// whole one is how an operator concludes a goal never progressed.
		fmt.Fprintf(deps.Stdout, "\nthis page is full (%d) — there may be more; raise it with --limit\n", limit)
	}
	return nil
}

var goalsCmd = &cobra.Command{
	Use:   "goals",
	Short: "List the goals the fleet still has outstanding",
	Long: "List every open goal contract in this project, across all agents, oldest first.\n\n" +
		"A goal is outstanding until a close event references it; the log is monotone, so a " +
		"goal that has left this list cannot come back — rework opens a new linked goal.\n\n" +
		"Only engine-driven work opens goal contracts today. Agents started by the older prose " +
		"`spawn` path do not appear here; `sprawl status` lists those.\n\n" +
		"Requires the event log (`event_log.enabled`).",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Past arg validation every error here is about the STORE, and a flag
		// reference under "the event log is unreachable" is noise that pushes
		// the remedy off the top of the screen.
		silenceUsagePastArgs(cmd)
		return runGoals(cmd.Context(), resolveOperabilityDeps())
	},
}

var workflowsCmd = &cobra.Command{
	Use:   "workflows [workflow-id]",
	Short: "List workflow instances in flight, or read one instance's log",
	Long: "With no argument, list the workflow instances that still have at least one open " +
		"contract, oldest first. An instance whose contracts are all closed is finished and is " +
		"deliberately not listed — pass its id to read its log anyway.\n\n" +
		"With an id, print that instance's events in log order. This is the raw log: it is what " +
		"the engine derives its cursor from, so it is also the answer to \"why did this goal not " +
		"advance\".\n\n" +
		"Requires the event log (`event_log.enabled`).",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		silenceUsagePastArgs(cmd)
		deps := resolveOperabilityDeps()
		if len(args) == 0 {
			return runWorkflowsList(cmd.Context(), deps)
		}
		return runWorkflowLog(cmd.Context(), deps, args[0], workflowLogLimit)
	},
}

func init() {
	workflowsCmd.Flags().IntVar(&workflowLogLimit, "limit", 200, "Maximum events to print when reading one instance's log")
	rootCmd.AddCommand(goalsCmd)
	rootCmd.AddCommand(workflowsCmd)
}
