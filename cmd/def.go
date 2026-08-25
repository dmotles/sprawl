// cmd/def.go — `sprawl def` command group (QUM-1251). The operator surface for
// agent cards as versioned data: list what is published, lint a card source,
// and publish a new immutable version.
//
// There is deliberately NO edit, delete or --force. agent_cards rows are
// immutable by design (UNIQUE (name, version), and the app role holds SELECT
// only), so the only way to change what an agent type spawns with is to publish
// a higher version. A bypass flag here would write an unlinted immutable row
// with no undo.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dmotles/sprawl/internal/card"
	"github.com/dmotles/sprawl/internal/store"
	"github.com/spf13/cobra"
)

// defDeps holds every external dependency, per the repo's DI convention, so each
// subcommand's output can be asserted without a database.
//
// ListCards and Publish are collapsed to functions rather than exposing a pool:
// *pgxpool.Pool is not constructible in a unit test, and a seam shaped like the
// database would make every assertion here an integration test.
type defDeps struct {
	ListCards  func(ctx context.Context) ([]store.PublishedCard, error)
	ResolveDSN func() (dsn string, source string, err error)
	// Publish takes the DSN rather than a pool because it needs the PRIVILEGED
	// one: migration 00002 grants sprawl_app SELECT only on agent_cards, and
	// that grant must not be widened to let a running agent rewrite the
	// definitions its siblings launch from.
	Publish  func(ctx context.Context, dsn string, c *card.Card) error
	ReadFile func(name string) ([]byte, error)
	// Lint is a seam so a test can observe the string that was scanned. What is
	// linted — the widest RENDER, never the raw body — is the property, and no
	// assertion on the command's output can distinguish the two.
	Lint   func(c *card.Card, rendered string) card.Report
	Stdout io.Writer
	Stderr io.Writer
}

var defaultDefDeps *defDeps

func resolveDefDeps() *defDeps {
	if defaultDefDeps != nil {
		return defaultDefDeps
	}
	return &defDeps{
		ListCards: func(ctx context.Context) ([]store.PublishedCard, error) {
			l, err := store.Process(ctx, os.Getenv("SPRAWL_ROOT"))
			if err != nil {
				return nil, err
			}
			return l.ListCards(ctx)
		},
		ResolveDSN: func() (string, string, error) {
			return store.ResolveDSN(os.Getenv, os.UserConfigDir)
		},
		Publish:  store.PublishCardDSN,
		ReadFile: os.ReadFile,
		Lint:     card.Lint,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}
}

var defCmd = &cobra.Command{
	Use:   "def",
	Short: "Inspect, lint and publish agent card definitions",
	Long: "Operate agent cards: the versioned, immutable definitions every " +
		"agent spawns from (QUM-1251). A published card can never be edited " +
		"or deleted — to change what an agent type launches with, publish a " +
		"higher version. Spawns pick the highest version up automatically.\n\n" +
		"There is no --force: publish refuses any card that fails card-lint, " +
		"and the row it would write cannot be taken back.",
}

var defListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every published agent card version",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		silenceUsagePastArgs(cmd)
		jsonOut, _ := cmd.Flags().GetBool("json")
		return runDefList(cmd.Context(), resolveDefDeps(), jsonOut)
	},
}

var defLintCmd = &cobra.Command{
	Use:   "lint <card.md>",
	Short: "Check a card source against the safety rules without publishing it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		silenceUsagePastArgs(cmd)
		return runDefLint(cmd.Context(), resolveDefDeps(), args[0])
	},
}

var defPublishCmd = &cobra.Command{
	Use:   "publish <card.md>",
	Short: "Publish a card source as a new immutable version",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		silenceUsagePastArgs(cmd)
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		return runDefPublish(cmd.Context(), resolveDefDeps(), args[0], dryRun)
	},
}

func init() {
	defListCmd.Flags().Bool("json", false, "emit the listing as a JSON array on stdout")
	defPublishCmd.Flags().Bool("dry-run", false, "parse, render and lint the card, then stop without writing")
	defCmd.AddCommand(defListCmd, defLintCmd, defPublishCmd)
	rootCmd.AddCommand(defCmd)
}

// silenceUsagePastArgs suppresses cobra's usage block for the errors raised from
// here on.
//
// Called INSIDE RunE rather than set as a field on the command, which is the
// whole point: by the time RunE runs, cobra has already validated the arguments,
// so a wrong-arity invocation still gets its usage block. What it stops is a
// card-lint verdict — several findings, each naming a rule and a remedy — being
// followed by a flag reference that has nothing to do with the failure and
// pushes the findings off the top of the operator's screen.
func silenceUsagePastArgs(cmd *cobra.Command) { cmd.SilenceUsage = true }

// defListRow is one row of `def list --json`.
type defListRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   int    `json:"version"`
	AgentType string `json:"agent_type"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	// RenderErr is why this row's render options could not be decoded, or "" if
	// they decoded. Reported rather than dropped: `def list` is the diagnostic
	// an operator reaches for BECAUSE something is wrong, and a broken row that
	// prints like every other row conceals the one thing they came for.
	RenderErr string `json:"render_err,omitempty"`
}

func runDefList(ctx context.Context, deps *defDeps, jsonOut bool) error {
	cards, err := deps.ListCards(ctx)
	if err != nil {
		return err
	}
	if len(cards) == 0 {
		// Refused rather than printed as an empty listing. Zero rows is a
		// PLAUSIBLE ZERO: "nothing published" and "the migrations never ran"
		// look identical, and the second is far likelier — the seeds are
		// written by every migration run, so an empty table means the schema is
		// not there.
		return fmt.Errorf("no agent cards are published, which on a migrated database is impossible — the seeds are written by the migrations\nnext: sprawl store migrate, then sprawl def list")
	}

	rows := make([]defListRow, 0, len(cards))
	for _, c := range cards {
		rows = append(rows, defListRow{
			ID: c.ID().String(), Name: c.Name, Version: c.Version,
			AgentType: c.AgentType, Model: c.Model, Effort: c.Effort,
			RenderErr: c.RenderErr,
		})
	}

	if jsonOut {
		enc := json.NewEncoder(deps.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	tw := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT TYPE\tCARD\tMODEL\tEFFORT\tID")
	for _, r := range rows {
		note := ""
		if r.RenderErr != "" {
			note = "  !! " + r.RenderErr
		}
		fmt.Fprintf(tw, "%s\t%s@%d\t%s\t%s\t%s%s\n",
			r.AgentType, r.Name, r.Version, r.Model, r.Effort, r.ID, note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(deps.Stderr, "%d published card(s). Every version is immutable; a spawn uses the highest version for its agent type.\n", len(rows))
	return nil
}

// prepublish is the ONE path from card source to a lint verdict, shared by lint,
// publish and --dry-run.
//
// Shared deliberately. A separate --dry-run path is a named landmine: the
// operator's evidence would come from the code that did not run, and the two
// would drift in the direction that matters least visibly — the preview passing
// what the real publish refuses, or worse, the reverse.
func prepublish(deps *defDeps, path string) (*card.Card, error) {
	src, err := deps.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the card source %s: %w", path, err)
	}
	c, err := card.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid card: %w", path, err)
	}

	// Rendered under card.LintInput() — the WIDEST render, sub-agent banner and
	// sandbox warning spliced in — because a section-scoped rule evaluated
	// pre-render is answering a question no agent's prompt ever poses, and
	// linting the widest form means no spliced block can hide a finding.
	rendered, err := c.Render(card.LintInput())
	if err != nil {
		return nil, fmt.Errorf("%s cannot be rendered, so it cannot be checked or spawned: %w", path, err)
	}

	rep := deps.Lint(c, rendered)
	reportLint(deps.Stderr, path, c, rep)
	if len(rep.Applied) == 0 {
		// The card-lint equivalent of a `0 passed / 0 failed` green run. Zero
		// findings out of zero rules is not evidence of anything, and a card
		// published this way WOULD spawn: cardresolve looks the agent type up by
		// name before it ever reaches the unknown-type fallback.
		return nil, fmt.Errorf("%s declares agent_type %q, which card-lint has no rules for, so publishing it would ship a card nothing checked\nnext: use one of %s, or add a rule set for %q in internal/card/lint.go",
			path, c.AgentType, strings.Join(card.LintableAgentTypes, ", "), c.AgentType)
	}
	if len(rep.Findings) > 0 {
		var names []string
		for _, f := range rep.Findings {
			names = append(names, f.Rule)
		}
		return nil, fmt.Errorf("%s fails %d card-lint rule(s) (%s); see the findings above\nnext: fix the card and re-run: sprawl def lint %s",
			path, len(rep.Findings), strings.Join(names, ", "), path)
	}
	return c, nil
}

// reportLint prints the verdict, naming what ran, what was skipped and why.
//
// Skipped rules are reported for the same reason card.Report carries them: a
// clean verdict that does not say what went unasked is unfalsifiable, and the
// reader cannot tell a card that passed from a card nothing applied to.
func reportLint(w io.Writer, path string, c *card.Card, rep card.Report) {
	fmt.Fprintf(w, "card-lint %s (%s@%d, agent_type %s), against the fully rendered sub-agent prompt:\n",
		path, c.Name, c.Version, c.AgentType)
	for _, rule := range rep.Applied {
		fmt.Fprintf(w, "  applied: %s\n", rule)
	}
	skipped := make([]string, 0, len(rep.Skipped))
	for rule := range rep.Skipped {
		skipped = append(skipped, rule)
	}
	sort.Strings(skipped)
	for _, rule := range skipped {
		fmt.Fprintf(w, "  skipped: %s — %s\n", rule, rep.Skipped[rule])
	}
	for _, f := range rep.Findings {
		fmt.Fprintf(w, "  FINDING [%s]: %s\n", f.Rule, f.Message)
	}
}

func runDefLint(_ context.Context, deps *defDeps, path string) error {
	c, err := prepublish(deps, path)
	if err != nil {
		return err
	}
	fmt.Fprintf(deps.Stderr, "%s@%d passes every card-lint rule that applies to it.\nnext: sprawl def publish %s\n", c.Name, c.Version, path)
	return nil
}

func runDefPublish(ctx context.Context, deps *defDeps, path string, dryRun bool) error {
	// Linted BEFORE the DSN is resolved: a card that will be refused should be
	// refused without a database round trip, and --dry-run must work on a
	// machine with no credential at all.
	c, err := prepublish(deps, path)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(deps.Stderr, "dry run: %s@%d would publish as %s. Nothing was written.\nnext: sprawl def publish %s\n",
			c.Name, c.Version, c.ID(), path)
		return nil
	}

	dsn, source, err := deps.ResolveDSN()
	if err != nil {
		return err
	}
	if dsn == "" {
		return fmt.Errorf("no event-log DSN is configured, so there is nowhere to publish %s@%d\nnext: set %s, or put `db_dsn: <dsn>` in a 0600 ~/.config/sprawl/%s",
			c.Name, c.Version, store.EnvDSN, store.SecretsFileName)
	}

	if err := deps.Publish(ctx, dsn, c); err != nil {
		// Redacted, always. This is the one card subcommand holding an admin
		// credential, the repo is public, and a driver connect failure re-renders
		// the DSN — password included — into its own message.
		if errors.Is(err, store.ErrCardAlreadyPublished) {
			// The sentinel is re-wrapped rather than folded into the redacted
			// text: RedactError returns a STRING, so formatting it with %s
			// silently drops the error chain and no caller can branch on the
			// one publish failure that has a specific remedy.
			return fmt.Errorf("%s@%d is already published, and published cards are immutable (%s)\nnext: bump the version in %s and publish again — nothing can edit the existing row: %w",
				c.Name, c.Version, store.RedactSecrets(err.Error()), path, store.ErrCardAlreadyPublished)
		}
		return fmt.Errorf("publishing %s@%d failed: %s", c.Name, c.Version, store.RedactError(err))
	}

	// stdout is the return value a caller captures: the bare id, nothing else.
	fmt.Fprintf(deps.Stdout, "%s\n", c.ID())
	fmt.Fprintf(deps.Stderr, "Published %s@%d for agent_type %s (model %s, effort %s), dsn from %s.\n",
		c.Name, c.Version, c.AgentType, c.Model, c.Effort, source)
	fmt.Fprintf(deps.Stderr, "  This row is immutable — it can never be edited or deleted. To change it, publish a higher version.\n")
	fmt.Fprintf(deps.Stderr, "  New spawns of type %s pick this up automatically; nothing needs restarting.\n", c.AgentType)
	fmt.Fprintf(deps.Stderr, "Verify with: sprawl def list\n")
	return nil
}
