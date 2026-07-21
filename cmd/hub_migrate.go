package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/spf13/cobra"
)

// hubMigrateDeps injects the migration runner + IO so the command is testable
// without a real database.
type hubMigrateDeps struct {
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
	Apply  func(ctx context.Context, dsn string) error
	Status func(ctx context.Context, dsn string) ([]store.MigrationStatus, error)
}

func resolveHubMigrateDeps() *hubMigrateDeps {
	return &hubMigrateDeps{
		Getenv: os.Getenv,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Apply:  store.Migrate,
		Status: store.MigrateStatus,
	}
}

var hubMigrateDSN string

func init() {
	hubMigrateCmd.PersistentFlags().StringVar(&hubMigrateDSN, "dsn", "",
		"Postgres DSN (or set SPRAWL_HUB_DSN). Injected at runtime; never committed.")
	hubMigrateCmd.AddCommand(hubMigrateStatusCmd)
	hubCmd.AddCommand(hubMigrateCmd)
}

var hubMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply pending hub schema migrations",
	Long: "Apply all pending goose migrations to the hub's Postgres database. " +
		"Idempotent: re-running applies nothing once up to date. The DSN comes " +
		"from --dsn or the SPRAWL_HUB_DSN environment variable.",
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		return runHubMigrate(c.Context(), resolveHubMigrateDeps(), hubMigrateDSN)
	},
}

var hubMigrateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show applied and pending hub schema migrations",
	Long: "List every known migration with its version and whether it has been " +
		"applied to the database named by --dsn / SPRAWL_HUB_DSN.",
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		return runHubMigrateStatus(c.Context(), resolveHubMigrateDeps(), hubMigrateDSN)
	},
}

// resolveHubDSN picks the DSN from the flag (highest precedence) or the
// SPRAWL_HUB_DSN environment variable, erroring with an actionable hint when
// neither is set.
func resolveHubDSN(getenv func(string) string, dsnFlag string) (string, error) {
	if dsnFlag != "" {
		return dsnFlag, nil
	}
	if env := getenv("SPRAWL_HUB_DSN"); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("no Postgres DSN provided; pass --dsn or set SPRAWL_HUB_DSN " +
		"(e.g. --dsn 'postgres://user:pass@host:5432/hub?sslmode=disable')")
}

func runHubMigrate(ctx context.Context, deps *hubMigrateDeps, dsnFlag string) error {
	dsn, err := resolveHubDSN(deps.Getenv, dsnFlag)
	if err != nil {
		return err
	}
	if err := deps.Apply(ctx, dsn); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	fmt.Fprintln(deps.Stderr, "Migrations applied (up to date).")
	fmt.Fprintln(deps.Stderr, "Verify with: sprawl hub migrate status --dsn <dsn>")
	return nil
}

func runHubMigrateStatus(ctx context.Context, deps *hubMigrateDeps, dsnFlag string) error {
	dsn, err := resolveHubDSN(deps.Getenv, dsnFlag)
	if err != nil {
		return err
	}
	statuses, err := deps.Status(ctx, dsn)
	if err != nil {
		return fmt.Errorf("reading migration status: %w", err)
	}
	for _, s := range statuses {
		state := "pending"
		if s.Applied {
			state = "applied"
		}
		fmt.Fprintf(deps.Stdout, "%05d  %-8s  %s\n", s.Version, state, s.Source)
	}
	return nil
}
