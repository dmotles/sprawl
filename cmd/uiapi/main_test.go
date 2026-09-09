package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/uiapi"
	"github.com/jackc/pgx/v5"
)

// envMap turns a map into a getenv, so a test states its whole environment
// rather than mutating the process's.
func envMap(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func fullEnv() map[string]string {
	return map[string]string{
		"PGHOST":     "db.test.invalid",
		"PGDATABASE": "sprawl",
		"PGUSER":     "sprawl_ui_login",
		"PGPASSWORD": "hunter2",
	}
}

// stubPool satisfies uiapi.Pool without a database.
type stubPool struct{}

func (stubPool) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (stubPool) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (stubPool) Ping(context.Context) error                              { return nil }

// withSeams swaps the three indirected seams for the duration of a test and
// restores them, so the package's globals cannot leak between cases.
func withSeams(t *testing.T, open func(context.Context) (uiapi.Pool, io.Closer, error), verify func(context.Context, uiapi.Pool) error, serve func(context.Context, uiapi.Config) error) {
	t.Helper()
	oldOpen, oldVerify, oldServe := openPoolFn, verifySchemaFn, serveFn
	t.Cleanup(func() { openPoolFn, verifySchemaFn, serveFn = oldOpen, oldVerify, oldServe })
	openPoolFn, verifySchemaFn, serveFn = open, verify, serve
}

func okOpen(context.Context) (uiapi.Pool, io.Closer, error) {
	return stubPool{}, closerFunc(func() {}), nil
}

func okVerify(context.Context, uiapi.Pool) error { return nil }

// TestRun_RefusesAnIncompleteEnvironment. libpq's defaults for these three are
// silently PLAUSIBLE — an unset PGHOST is a unix socket, an unset PGUSER and
// PGDATABASE are the OS user's name — so a half-configured container would not
// fail, it would connect somewhere else or produce an error naming a socket
// path rather than the variable the operator forgot.
func TestRun_RefusesAnIncompleteEnvironment(t *testing.T) {
	for _, missing := range uiapi.RequiredPGEnvVars {
		t.Run("without "+missing, func(t *testing.T) {
			opened := false
			withSeams(t, func(context.Context) (uiapi.Pool, io.Closer, error) {
				opened = true
				return okOpen(context.Background())
			}, okVerify, func(context.Context, uiapi.Config) error { return nil })

			env := fullEnv()
			delete(env, missing)

			err := run(context.Background(), nil, envMap(env), io.Discard)
			if err == nil {
				t.Fatalf("run() succeeded with %s unset — libpq would default it to something plausible and connect to the wrong place", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q does not name the variable that is missing, which is the only thing the operator needs from it", err)
			}
			if opened {
				t.Error("run() tried to connect despite an incomplete environment — the refusal must come first, or the diagnostic is a connection error instead of a config one")
			}
		})
	}
}

// TestRun_AcceptsAConfiguredEnvironment is the negative control for the table
// above: without it, a run() that refused unconditionally would pass every case.
func TestRun_AcceptsAConfiguredEnvironment(t *testing.T) {
	served := false
	withSeams(t, okOpen, okVerify, func(context.Context, uiapi.Config) error {
		served = true
		return nil
	})
	if err := run(context.Background(), nil, envMap(fullEnv()), io.Discard); err != nil {
		t.Fatalf("run() with a complete environment = %v, want nil", err)
	}
	if !served {
		t.Error("run() returned without serving")
	}
}

// TestRun_DoesNotRequireAPasswordOrSSLMode: a trust/peer-auth local database
// needs no password, and PGSSLMODE has a defensible libpq default. Requiring
// them would make the compose stack unbootable for no security gain.
func TestRun_DoesNotRequireAPasswordOrSSLMode(t *testing.T) {
	withSeams(t, okOpen, okVerify, func(context.Context, uiapi.Config) error { return nil })
	env := fullEnv()
	delete(env, "PGPASSWORD")
	if err := run(context.Background(), nil, envMap(env), io.Discard); err != nil {
		t.Errorf("run() without PGPASSWORD = %v, want nil", err)
	}
}

// TestRun_NeverPrintsThePassword. main1 writes the error to stderr, and the
// container's stderr is shipped to a log aggregator.
func TestRun_NeverPrintsThePassword(t *testing.T) {
	withSeams(t, func(context.Context) (uiapi.Pool, io.Closer, error) {
		return nil, nil, errors.New("uiapi: the read database is unreachable")
	}, okVerify, func(context.Context, uiapi.Config) error { return nil })

	var out strings.Builder
	env := fullEnv()
	env["PGPASSWORD"] = "s3cr3t-do-not-log"
	// The log level is turned all the way up, so a debug-level config dump
	// would be captured too.
	env[EnvLogLevel] = "debug"

	if err := run(context.Background(), nil, envMap(env), &out); err == nil {
		t.Fatal("expected the seeded connection failure")
	}
	if strings.Contains(out.String(), "s3cr3t-do-not-log") {
		t.Errorf("the password reached the process's output: %s", out.String())
	}
}

// TestRun_FailsLoudlyAgainstAnUnmigratedDatabase is the boot precondition:
// this binary must never repair a schema, so an unmigrated database is a
// refusal that names the command to fix it.
func TestRun_FailsLoudlyAgainstAnUnmigratedDatabase(t *testing.T) {
	unmigrated := &store.HintError{
		Err:  uiapi.ErrSchemaNotMigrated,
		Hint: "migrate the database from an ADMIN dsn first — `SPRAWL_DB_DSN=<admin dsn> sprawl store migrate`",
	}
	served := false
	withSeams(t, okOpen, func(context.Context, uiapi.Pool) error { return unmigrated },
		func(context.Context, uiapi.Config) error {
			served = true
			return nil
		})

	err := run(context.Background(), nil, envMap(fullEnv()), io.Discard)
	if err == nil {
		t.Fatal("run() booted against an unmigrated database — it would report healthy and 500 on every request")
	}
	if !errors.Is(err, uiapi.ErrSchemaNotMigrated) {
		t.Errorf("error %v does not wrap ErrSchemaNotMigrated", err)
	}
	if !strings.Contains(err.Error(), "sprawl store migrate") {
		t.Errorf("error %q does not name the command that fixes this", err)
	}
	if served {
		t.Error("run() served anyway after the schema check failed — the check is then decorative")
	}
}

// TestRun_ClosesThePoolOnEveryExit. A boot that fails after connecting must
// still hand the connection back; a container that crash-loops on an unmigrated
// schema would otherwise exhaust the database's connection slots.
func TestRun_ClosesThePoolOnEveryExit(t *testing.T) {
	tests := map[string]func(context.Context, uiapi.Pool) error{
		"after a successful boot": okVerify,
		"after a failed schema check": func(context.Context, uiapi.Pool) error {
			return errors.New("not migrated")
		},
	}
	for name, verify := range tests {
		t.Run(name, func(t *testing.T) {
			closed := false
			withSeams(t, func(context.Context) (uiapi.Pool, io.Closer, error) {
				return stubPool{}, closerFunc(func() { closed = true }), nil
			}, verify, func(context.Context, uiapi.Config) error { return nil })

			_ = run(context.Background(), nil, envMap(fullEnv()), io.Discard)
			if !closed {
				t.Error("the pool was not closed")
			}
		})
	}
}

// TestRun_PassesTheFlagsThrough pins --addr and --grace onto the served Config,
// since nothing else observes them.
func TestRun_PassesTheFlagsThrough(t *testing.T) {
	var got uiapi.Config
	withSeams(t, okOpen, okVerify, func(_ context.Context, cfg uiapi.Config) error {
		got = cfg
		return nil
	})

	if err := run(context.Background(), []string{"--addr", "127.0.0.1:9999", "--grace", "3s"}, envMap(fullEnv()), io.Discard); err != nil {
		t.Fatalf("run(): %v", err)
	}
	if got.Addr != "127.0.0.1:9999" {
		t.Errorf("Addr = %q, want the flag's value", got.Addr)
	}
	if got.Grace != 3*time.Second {
		t.Errorf("Grace = %v, want 3s", got.Grace)
	}
	if got.Events == nil {
		t.Error("Config.Events is nil, so /api/events would panic on the first request")
	}
}

// TestNewLogger_DefaultsToJSON. The binary runs in a container whose stdout is
// scraped, and a text handler there produces unparseable lines rather than an
// obvious failure.
func TestNewLogger_DefaultsToJSON(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   string
	}{
		{format: "", want: `"msg":"probe"`},
		{format: "json", want: `"msg":"probe"`},
		{format: "text", want: `msg=probe`},
	} {
		var out strings.Builder
		newLogger(&out, envMap(map[string]string{EnvLogFormat: tc.format})).Info("probe")
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("format %q produced %q, want it to contain %q", tc.format, out.String(), tc.want)
		}
	}
}

// TestNewLogger_HonoursTheLevel, in both directions: a level that filters and
// one that does not. Only the pair distinguishes a working level from a handler
// that drops (or keeps) everything.
func TestNewLogger_HonoursTheLevel(t *testing.T) {
	var quiet, loud strings.Builder
	newLogger(&quiet, envMap(map[string]string{EnvLogLevel: "error"})).Info("probe")
	newLogger(&loud, envMap(map[string]string{EnvLogLevel: "debug"})).Debug("probe")

	if strings.Contains(quiet.String(), "probe") {
		t.Errorf("an info line survived SPRAWL_UIAPI_LOG_LEVEL=error: %q", quiet.String())
	}
	if !strings.Contains(loud.String(), "probe") {
		t.Errorf("a debug line was dropped at SPRAWL_UIAPI_LOG_LEVEL=debug: %q", loud.String())
	}
}
