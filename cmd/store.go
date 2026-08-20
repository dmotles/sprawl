// cmd/store.go — `sprawl store` command group (QUM-1249). Operator surface for
// the shared Postgres event log: apply migrations, report configuration, and
// diagnose a degraded connection.
//
// There is deliberately NO `sprawl store query`. Agents get narrow tools and
// never raw SQL, and an operator inspecting the log uses psql — adding a query
// surface here would grow into a second, worse query language.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/spf13/cobra"
)

// storeDeps holds every external dependency, per the repo's DI convention, so
// each subcommand's output can be asserted without a database.
type storeDeps struct {
	SprawlRoot string
	// OpenLedger returns (nil, nil) when the store is disabled, a degraded
	// Ledger when the database is unreachable, and an error only for a
	// misconfiguration.
	OpenLedger func(ctx context.Context, sprawlRoot string) (*store.Ledger, error)
	ResolveDSN func() (dsn string, source string, err error)
	Migrate    func(ctx context.Context, dsn string) error
	ReadDir    func(name string) ([]os.DirEntry, error)
	Stdout     io.Writer
	Stderr     io.Writer
}

var defaultStoreDeps *storeDeps

func resolveStoreDeps() *storeDeps {
	if defaultStoreDeps != nil {
		return defaultStoreDeps
	}
	return &storeDeps{
		SprawlRoot: os.Getenv("SPRAWL_ROOT"),
		OpenLedger: store.Process,
		ResolveDSN: func() (string, string, error) {
			return store.ResolveDSN(os.Getenv, os.UserConfigDir)
		},
		Migrate: store.Migrate,
		ReadDir: os.ReadDir,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
}

var storeCmd = &cobra.Command{
	Use:   "store",
	Short: "Inspect and migrate the shared Postgres event log",
	Long: "Operate the shared Postgres event log (QUM-1249). The DSN is read " +
		"from SPRAWL_DB_DSN or a 0600 ~/.config/sprawl/secrets.yaml and is " +
		"never stored in .sprawl/config.yaml, which is tracked in a public " +
		"repo. No subcommand ever prints the DSN.\n\n" +
		"Setup, including a local container, Neon's free tier, and the " +
		"least-privilege role that makes `events` append-only: " +
		"docs/event-log-setup.md",
}

var storeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report whether the event log is enabled and where its DSN comes from",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runStoreStatus(cmd.Context(), resolveStoreDeps())
	},
}

var storeDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose the event-log connection, append-only enforcement, and spill backlog",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runStoreDoctor(cmd.Context(), resolveStoreDeps())
	},
}

var storeMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply pending event-log migrations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runStoreMigrate(cmd.Context(), resolveStoreDeps())
	},
}

func init() {
	storeCmd.AddCommand(storeStatusCmd, storeDoctorCmd, storeMigrateCmd)
	rootCmd.AddCommand(storeCmd)
}

// requireSprawlRoot refuses an unset SPRAWL_ROOT.
//
// Every other command in cmd/ does this, and skipping it here was not merely
// inconsistent — it made the diagnostic LIE. With SPRAWL_ROOT unset,
// config.Load("") resolves a RELATIVE .sprawl/config.yaml, finds nothing, and
// returns a zero Config with no error, so `store status` printed
// "event log: disabled / enable it with: sprawl config set ..." and exited 0 on
// a host where the store was enabled and possibly spilling — a confident,
// exactly-wrong answer with a remedy that was already applied. reportSpill then
// looked at a relative spill dir and printed "nothing has ever spilled", which
// is the plausible zero this file goes out of its way to avoid elsewhere.
func requireSprawlRoot(deps *storeDeps) error {
	if deps.SprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set, so there is no project to report on\nnext: run this from inside a sprawl session, or set SPRAWL_ROOT to the repo root")
	}
	return nil
}

func runStoreStatus(ctx context.Context, deps *storeDeps) error {
	if err := requireSprawlRoot(deps); err != nil {
		return err
	}
	ledger, err := deps.OpenLedger(ctx, deps.SprawlRoot)
	if err != nil {
		// A misconfiguration, not an outage. Printed rather than returned so
		// status stays readable, and status still exits non-zero so a scripted
		// caller notices.
		fmt.Fprintf(deps.Stdout, "event log: MISCONFIGURED\n  %s\n", store.RedactError(err))
		return fmt.Errorf("the event log is enabled but not usable")
	}
	if !ledger.Enabled() {
		fmt.Fprintf(deps.Stdout,
			"event log: disabled\n"+
				"  enable it with: sprawl config set event_log.enabled true\n"+
				"  then supply a DSN via %s or a 0600 ~/.config/sprawl/%s\n"+
				"  setup guide: docs/event-log-setup.md\n",
			store.EnvDSN, store.SecretsFileName)
		return nil
	}
	defer ledger.Close()

	fmt.Fprintf(deps.Stdout, "event log: enabled\n")
	// The SOURCE, never the DSN.
	fmt.Fprintf(deps.Stdout, "  dsn source: %s\n", ledger.DSNSource())
	fmt.Fprintf(deps.Stdout, "  project id: %s\n", ledger.ProjectID())
	if degraded := ledger.DegradedError(); degraded != nil {
		fmt.Fprintf(deps.Stdout, "  state: DEGRADED — the database is unreachable, telemetry is spilling to %s\n",
			store.SpillDir(deps.SprawlRoot))
		fmt.Fprintf(deps.Stdout, "  cause: %s\n", store.RedactError(degraded))
		fmt.Fprintf(deps.Stdout, "  next: run `sprawl store doctor` for detail; goal open/close is refused until the database returns\n")
		return nil
	}
	fmt.Fprintf(deps.Stdout, "  state: connected\n")
	return nil
}

// runStoreDoctor reports on the event log. It exits 0 for every state it can
// describe, INCLUDING a broken one — deliberately, and asymmetrically with
// `store status`, which exits non-zero on a misconfiguration.
//
// The asymmetry is the point, and it is documented because an agent caller (the
// stated primary consumer) otherwise cannot know which command's exit status
// means what. `status` answers "is the store healthy?", so a bad configuration
// is a failure of the question. `doctor` answers "what is wrong with the
// store?", and it having successfully found the answer is not a failure — a
// doctor that exited non-zero whenever it had something to report could not be
// distinguished from a doctor that failed to run. Callers wanting a pass/fail
// signal should use `status`; callers wanting detail should read `doctor`'s
// output, not its status.
func runStoreDoctor(ctx context.Context, deps *storeDeps) error {
	if err := requireSprawlRoot(deps); err != nil {
		// The one exception: an unset SPRAWL_ROOT means doctor cannot examine
		// anything at all, so there is no diagnosis to have succeeded at.
		return err
	}
	ledger, err := deps.OpenLedger(ctx, deps.SprawlRoot)
	if err != nil {
		fmt.Fprintf(deps.Stdout, "connection: FAILED\n  %s\n", store.RedactError(err))
		return nil
	}
	if !ledger.Enabled() {
		fmt.Fprintf(deps.Stdout,
			"event log: disabled\n  enable it with: sprawl config set event_log.enabled true\n")
		reportSpill(deps)
		return nil
	}
	defer ledger.Close()

	fmt.Fprintf(deps.Stdout, "dsn source:   %s\n", ledger.DSNSource())
	if degraded := ledger.DegradedError(); degraded != nil {
		fmt.Fprintf(deps.Stdout, "connection:   DEGRADED (unreachable)\n  %s\n", store.RedactError(degraded))
		fmt.Fprintf(deps.Stdout, "append-only:  not measured (no connection)\n")
		reportSpill(deps)
		fmt.Fprintf(deps.Stdout, "next: fix the DSN or the network, then restart the session — a degraded store does not reconnect in place\n")
		return nil
	}
	fmt.Fprintf(deps.Stdout, "connection:   ok\n")
	fmt.Fprintf(deps.Stdout, "project id:   %s\n", ledger.ProjectID())

	// Append-only is a property of the CONNECTION IN USE, not of the migration:
	// an owner or superuser DSN bypasses every REVOKE while the schema is
	// perfectly correct. This is the only place that can observe it.
	if err := store.VerifyAppendOnly(ctx, ledger.Pool()); err != nil {
		fmt.Fprintf(deps.Stdout, "append-only:  NOT ENFORCED\n  %s\n", store.RedactError(err))
	} else {
		fmt.Fprintf(deps.Stdout, "append-only:  enforced by grants\n")
	}
	reportSpill(deps)
	return nil
}

// reportSpill prints the degraded-mode backlog.
//
// It reports "not measured" rather than 0 when the directory cannot be read.
// Those are different facts, and a plausible zero is worse than a blank: a
// reader reasons from "0 spilled events" and stops investigating, whereas
// "not measured" prompts the next question. (This repo's diagnostic rule: never
// render a number you did not measure.)
func reportSpill(deps *storeDeps) {
	dir := store.SpillDir(deps.SprawlRoot)
	readDir := deps.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	entries, err := readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(deps.Stdout, "spill:        none — %s does not exist (nothing has ever spilled)\n", dir)
			return
		}
		fmt.Fprintf(deps.Stdout, "spill:        not measured — could not read %s: %s\n", dir, store.RedactError(err))
		return
	}
	var files int
	var dead bool
	for _, e := range entries {
		if e.IsDir() {
			dead = true
			continue
		}
		files++
	}
	fmt.Fprintf(deps.Stdout, "spill:        %d file(s) in %s\n", files, dir)
	if dead {
		fmt.Fprintf(deps.Stdout, "dead letters: present in %s — these could NOT be replayed and need triage\n",
			filepath.Join(dir, "dead-letter"))
	}
}

func runStoreMigrate(ctx context.Context, deps *storeDeps) error {
	resolve := deps.ResolveDSN
	if resolve == nil {
		resolve = func() (string, string, error) {
			return store.ResolveDSN(os.Getenv, os.UserConfigDir)
		}
	}
	dsn, source, err := resolve()
	if err != nil {
		return err
	}
	if dsn == "" {
		// Refuses rather than silently succeeding: an operator who runs migrate,
		// sees no error and believes the schema exists is worse off than one who
		// gets told the DSN is missing.
		return fmt.Errorf("no event-log DSN is configured, so there is nothing to migrate\nnext: set %s, or put `db_dsn: <dsn>` in a 0600 ~/.config/sprawl/%s",
			store.EnvDSN, store.SecretsFileName)
	}
	migrate := deps.Migrate
	if migrate == nil {
		migrate = store.Migrate
	}
	if err := migrate(ctx, dsn); err != nil {
		return err
	}
	fmt.Fprintf(deps.Stdout, "event-log migrations applied (dsn from %s)\n", source)
	return nil
}
