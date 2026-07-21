// Command hubd is the sprawl hub server: one Connect listener serving the
// (stubbed) HubService RPCs, health/readiness probes, a gated /debug/state
// endpoint, and a graceful SIGTERM drain. It is a separate deployable process,
// not a `sprawl` subcommand. See docs/design/hub/.
package main

import (
	"context"
	"embed"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/hub"
)

// spaFS is the embedded SPA seam. It is an empty placeholder this slice (the
// real SPA lands later); a build with an empty embed is intentional and fine.
//
//go:embed all:web/dist
var spaFS embed.FS

// serveFn is indirected so tests can drive run() without binding a socket.
var serveFn = hub.Serve

func main() {
	if err := main1(); err != nil {
		os.Exit(1)
	}
}

// main1 runs the server with signal wiring, returning an error instead of
// calling os.Exit so deferred cleanup (signal.stop) always runs.
func main1() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return run(ctx, os.Args[1:], os.Getenv, os.Stderr)
}

// run parses flags, resolves configuration, and serves until ctx is cancelled.
// It is separated from main for testability (inject args, getenv, and w).
func run(ctx context.Context, args []string, getenv func(string) string, w io.Writer) error {
	fs := flag.NewFlagSet("hubd", flag.ContinueOnError)
	fs.SetOutput(w)
	addr := fs.String("addr", hub.DefaultAddr, "listen address")
	hubURLFlag := fs.String("hub-url", "", "hub uplink endpoint (default empty; no baked-in endpoint)")
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

	return serveFn(ctx, hub.HubConfig{
		Addr:          *addr,
		HubURL:        hubURL,
		Grace:         *grace,
		DebugEndpoint: truthy(getenv("SPRAWL_HUB_DEBUG_ENDPOINT")),
		Logger:        logger,
		SPA:           spaAssets(),
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
