package hub

import (
	"io"
	"io/fs"
	"log/slog"
	"time"
)

// DefaultGrace is the graceful-shutdown drain window if none is configured.
const DefaultGrace = 15 * time.Second

// DefaultAddr is the default listen address for hubd.
const DefaultAddr = ":8080"

// HubConfig configures a hubd server instance. All environment-specific values
// are injected; nothing is baked in (public-repo hygiene).
type HubConfig struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// HubURL is the resolved uplink endpoint. May be empty (inert) this slice;
	// it is logged host-only via RedactHubURL, never with credentials.
	HubURL string
	// Grace bounds how long in-flight requests may drain on shutdown.
	Grace time.Duration
	// DebugEndpoint gates /debug/state (SPRAWL_HUB_DEBUG_ENDPOINT).
	DebugEndpoint bool
	// Logger is the structured JSON logger; components attach a `component`
	// attr. If nil, a discard logger is used.
	Logger *slog.Logger
	// SPA is the embedded SPA asset filesystem served under /app/. May be nil
	// or empty this slice; the real SPA lands later.
	SPA fs.FS
}

// logger returns the configured logger or a discard logger.
func (c HubConfig) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// grace returns the configured grace window or DefaultGrace.
func (c HubConfig) grace() time.Duration {
	if c.Grace > 0 {
		return c.Grace
	}
	return DefaultGrace
}

// addr returns the configured listen address or DefaultAddr.
func (c HubConfig) addr() string {
	if c.Addr != "" {
		return c.Addr
	}
	return DefaultAddr
}
