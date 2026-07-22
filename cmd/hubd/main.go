// Command hubd is the sprawl hub server: one Connect listener serving the
// (stubbed) HubService RPCs, health/readiness probes, a gated /debug/state
// endpoint, and a graceful SIGTERM drain. It is a separate deployable process,
// not a `sprawl` subcommand. See docs/design/hub/.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/hub"
	"github.com/dmotles/sprawl/internal/hub/store"
)

// spaFS is the embedded SPA seam. It is an empty placeholder this slice (the
// real SPA lands later); a build with an empty embed is intentional and fine.
//
//go:embed all:web/dist
var spaFS embed.FS

// serveFn is indirected so tests can drive run() without binding a socket.
var serveFn = hub.Serve

// buildStoreFn is indirected so tests can drive DSN plumbing without a real
// database. It opens a pgStore and applies pending migrations at boot.
var buildStoreFn = defaultBuildStore

// memStoreFn is indirected so tests can substitute the no-DSN store. It builds
// the in-memory dev store.
var memStoreFn = store.NewMemStore

// pgConfig assembles the PGConfig from the environment, keeping the SecretURL /
// BlobURL env reads in one testable place. Empty values are passed through
// verbatim so NewPGStore applies its own defaults (a per-process random keeper
// for SecretURL, "mem://" for BlobURL).
func pgConfig(dsn string) store.PGConfig {
	return store.PGConfig{
		DSN:       dsn,
		SecretURL: os.Getenv(hub.EnvHubSecretURL),
		BlobURL:   os.Getenv(hub.EnvHubBlobURL),
	}
}

// defaultBuildStore opens a Postgres-backed Store and migrates it to head.
// The token-sealing keeper (per-deploy pepper) is resolved from
// SPRAWL_HUB_SECRET_URL — it MUST match the one `sprawl hub token create`
// used, or hubd cannot verify minted tokens (and a restart would invalidate
// every token). The blob bucket is resolved from SPRAWL_HUB_BLOB_URL (empty →
// "mem://"). Both come from the secrets/config path, never compiled in.
func defaultBuildStore(ctx context.Context, dsn string) (store.Store, error) {
	st, err := store.NewPGStore(ctx, pgConfig(dsn))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return st, nil
}

func main() {
	if err := main1(os.Args[1:], os.Getenv, os.Stderr); err != nil {
		os.Exit(1)
	}
}

// main1 runs the server with signal wiring, returning an error instead of
// calling os.Exit so deferred cleanup (signal.stop) always runs. Args/getenv/w
// are injected so the boot-error-logging path is testable.
func main1(args []string, getenv func(string) string, w io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, args, getenv, w); err != nil {
		fmt.Fprintln(w, err)
		return err
	}
	return nil
}

// run parses flags, resolves configuration, and serves until ctx is cancelled.
// It is separated from main for testability (inject args, getenv, and w).
func run(ctx context.Context, args []string, getenv func(string) string, w io.Writer) error {
	fs := flag.NewFlagSet("hubd", flag.ContinueOnError)
	fs.SetOutput(w)
	addr := fs.String("addr", hub.DefaultAddr, "listen address")
	hubURLFlag := fs.String("hub-url", "", "hub uplink endpoint (default empty; no baked-in endpoint)")
	dsnFlag := fs.String("dsn", "", "Postgres DSN (or set SPRAWL_HUB_DSN). Empty uses an in-memory store.")
	grace := fs.Duration("grace", hub.DefaultGrace, "graceful shutdown drain window")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := newLogger(w, getenv)

	// Config file is the lowest-precedence hub-url source; best-effort only —
	// hubd runs fine without a .sprawl/config.yaml.
	var configHubURL string
	if root := getenv("SPRAWL_ROOT"); root != "" {
		if cfg, err := config.Load(root); err == nil {
			configHubURL = cfg.HubURL
		}
	}

	hubURL := hub.ResolveHubURL(*hubURLFlag, getenv, configHubURL)
	logger.Info("hub endpoint resolved",
		"component", "hubd",
		"hub_url", hub.RedactHubURL(hubURL),
		"configured", hubURL != "",
	)

	// DSN comes from --dsn (highest precedence) or SPRAWL_HUB_DSN. When set, we
	// open a Postgres store and apply migrations at boot; otherwise we build an
	// in-memory memStore (dev default). Either way boot OWNS store creation so
	// it can EnsureUser the singleton before any FK-dependent write.
	dsn := *dsnFlag
	if dsn == "" {
		dsn = getenv("SPRAWL_HUB_DSN")
	}
	var st store.Store
	if dsn != "" {
		var err error
		st, err = buildStoreFn(ctx, dsn)
		if err != nil {
			return fmt.Errorf("hubd: initialize store: %w", err)
		}
		defer func() { _ = st.Close() }()
		logger.Info("store initialized", "component", "hubd", "backend", "postgres", "migrated", true)
	} else {
		var err error
		st, err = memStoreFn()
		if err != nil {
			return fmt.Errorf("hubd: initialize memstore: %w", err)
		}
		defer func() { _ = st.Close() }()
		logger.Info("store initialized", "component", "hubd", "backend", "memory")
	}

	// EnsureUser the single MVP principal before serving so the first
	// RegisterInstance does not fail the users FK (docs 04 §3).
	if err := st.EnsureUser(ctx, hub.MVPUserID); err != nil {
		return fmt.Errorf("hubd: ensure singleton user: %w", err)
	}

	// Browser login (docs 04 §1/§6) resolves from SPRAWL_HUB_LOGIN_TOKEN +
	// SPRAWL_HUB_COOKIE_KEY at boot. When unset, login is nil — browser login is
	// cleanly disabled and host bearer auth is unaffected. Never logs the secret.
	login := hub.ResolveBrowserAuth(getenv, st, hub.MVPUserID, logger)

	return serveFn(ctx, hub.HubConfig{
		Addr:          *addr,
		HubURL:        hubURL,
		Grace:         *grace,
		DebugEndpoint: truthy(getenv("SPRAWL_HUB_DEBUG_ENDPOINT")),
		Logger:        logger,
		SPA:           spaAssets(),
		Store:         st,
		Login:         login,
	})
}

// spaAssets returns the embedded SPA sub-filesystem, or nil if empty/absent.
func spaAssets() fs.FS {
	sub, err := fs.Sub(spaFS, "web/dist")
	if err != nil {
		return nil
	}
	return sub
}

// newLogger builds the structured logger. JSON in deployed mode; text for a
// TTY. Level from SPRAWL_HUB_LOG_LEVEL (default info).
func newLogger(w io.Writer, getenv func(string) string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(getenv("SPRAWL_HUB_LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.ToLower(strings.TrimSpace(getenv("SPRAWL_HUB_LOG_FORMAT"))) == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// truthy reports whether an env value means "on".
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
