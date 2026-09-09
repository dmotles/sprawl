// Command uiapi is the read-only web UI API: one net/http listener serving
// plain JSON over the shared Postgres event log, a liveness probe, and a
// graceful SIGTERM drain. It is a separate deployable process, not a `sprawl`
// subcommand, and it is NOT hubd — hubd serves a different database entirely.
//
// Two properties are load-bearing and are asserted rather than intended:
//
//   - It is configured by the discrete libpq variables PGHOST / PGDATABASE /
//     PGUSER / PGSSLMODE, with the password injected separately as PGPASSWORD.
//     Deliberately NOT SPRAWL_DB_DSN: that is the appender's DSN and carries an
//     INSERT-capable role, and a read-only service picking it up by accident is
//     precisely the mistake the separate role exists to prevent. See
//     uiapi.PGEnvVars.
//   - It RUNS NO MIGRATIONS. It connects as a login user inheriting sprawl_ro
//     (SELECT-only), so an unmigrated database is a boot failure that names the
//     command to fix it, never something this process repairs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dmotles/sprawl/internal/uiapi"
)

// Env names for logging, namespaced to this binary so a hubd setting cannot
// silently reconfigure it.
const (
	EnvLogLevel  = "SPRAWL_UIAPI_LOG_LEVEL"
	EnvLogFormat = "SPRAWL_UIAPI_LOG_FORMAT"
)

// Seams, indirected so run() is testable without a database or a socket.
var (
	openPoolFn     = openPool
	verifySchemaFn = uiapi.VerifySchema
	serveFn        = uiapi.Serve
)

// closerFunc adapts a close function to io.Closer.
type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }

// openPool deliberately takes no getenv. libpq resolves PG* from the AMBIENT
// process environment inside pgxpool, so the connection settings cannot be
// threaded through run's injected getenv — run validates os.Getenv's view and
// the pool reads the same one. A test that stubs getenv therefore proves things
// about the refusal, never about the connection.
func openPool(ctx context.Context) (uiapi.Pool, io.Closer, error) {
	pool, err := uiapi.OpenPool(ctx)
	if err != nil {
		return nil, nil, err
	}
	return pool, closerFunc(pool.Close), nil
}

func main() {
	if err := main1(os.Args[1:], os.Getenv, os.Stderr); err != nil {
		os.Exit(1)
	}
}

// main1 runs the server with signal wiring, returning an error instead of
// calling os.Exit so deferred cleanup always runs. Mirrors cmd/hubd.
func main1(args []string, getenv func(string) string, w io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, args, getenv, w); err != nil {
		fmt.Fprintln(w, err)
		return err
	}
	return nil
}

func run(ctx context.Context, args []string, getenv func(string) string, w io.Writer) error {
	fs := flag.NewFlagSet("uiapi", flag.ContinueOnError)
	fs.SetOutput(w)
	addr := fs.String("addr", uiapi.DefaultAddr, "listen address")
	grace := fs.Duration("grace", uiapi.DefaultGrace, "graceful shutdown drain window")
	if err := fs.Parse(args); err != nil {
		// -h/--help is a request that was SERVED, not a failure: Parse has
		// already written the usage to w. Returning the sentinel would make
		// main1 print `flag: help requested` and exit 1 at an operator who
		// followed the documented `exec uiapi /uiapi --help`.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	logger := newLogger(w, getenv)

	// REFUSED rather than defaulted. hubd falls back to an in-memory store
	// when its DSN is absent, which is right for a dev server with a schema it
	// owns; there is no equivalent here — an empty read API is not a degraded
	// mode, it is a process that looks healthy and shows the operator nothing.
	// libpq's own defaults are worse than absent: they are plausible, so an
	// unset PGHOST connects to a local socket instead of failing.
	if missing := uiapi.MissingPGEnv(getenv); len(missing) > 0 {
		return fmt.Errorf("the read database is not configured: %s unset\nnext: set %s (and PGPASSWORD, injected separately) for a login user that inherits sprawl_ro",
			strings.Join(missing, ", "),
			strings.Join(uiapi.RequiredPGEnvVars, ", "))
	}

	pool, closer, err := openPoolFn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	// Boot-time precondition, not a repair. See uiapi.VerifySchema.
	if err := verifySchemaFn(ctx, pool); err != nil {
		return err
	}
	logger.Info("read database ready", "component", "uiapi", "migrated_by", "sprawl store migrate")

	return serveFn(ctx, uiapi.Config{
		Addr:   *addr,
		Grace:  *grace,
		Logger: logger,
		Events: uiapi.PgEventReader{Pool: pool},
		Goals:  uiapi.PgGoalReader{Pool: pool},
		Health: pool,
	})
}

// newLogger builds the structured logger. JSON by default (deployed in a
// container), text on request. Mirrors cmd/hubd's newLogger.
func newLogger(w io.Writer, getenv func(string) string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(getenv(EnvLogLevel))) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.ToLower(strings.TrimSpace(getenv(EnvLogFormat))) == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
