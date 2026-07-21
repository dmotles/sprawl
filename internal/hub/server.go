package hub

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
)

// startedAt records process start for the /debug/state uptime field.
var startedAt = time.Now()

// Server holds the hub's HTTP surface: the Connect HubService handlers (stubbed
// this slice), health/readiness probes, and the gated /debug/state endpoint.
type Server struct {
	log    *slog.Logger
	health *Health
	debug  bool
	spa    fs.FS // embedded SPA assets; may be nil/empty this slice
}

// NewServer builds a Server from cfg. The readiness flag starts false; the
// serve loop flips it true once the listener is up.
func NewServer(cfg HubConfig) *Server {
	return &Server{
		log:    cfg.logger().With("component", "registry"),
		health: &Health{},
		debug:  cfg.DebugEndpoint,
		spa:    cfg.SPA,
	}
}

// RegisterInstance is stubbed this slice; the real registry lands in P0-3.
func (s *Server) RegisterInstance(
	context.Context, *connect.Request[hubv1.RegisterInstanceRequest],
) (*connect.Response[hubv1.RegisterInstanceResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented,
		errors.New("RegisterInstance not implemented (QUM-875 spine)"))
}

// ListInstances is stubbed this slice; the real registry lands in P0-3.
func (s *Server) ListInstances(
	context.Context, *connect.Request[hubv1.ListInstancesRequest],
) (*connect.Response[hubv1.ListInstancesResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented,
		errors.New("ListInstances not implemented (QUM-875 spine)"))
}

// debugSnapshot returns the read-only state snapshot served at /debug/state.
// It reflects only state the server already holds; near-empty this slice.
func (s *Server) debugSnapshot() any {
	return map[string]any{
		"component":   "hubd",
		"now":         time.Now().UTC().Format(time.RFC3339),
		"uptime_ms":   time.Since(startedAt).Milliseconds(),
		"ready":       s.health.Ready(),
		"streams":     []any{},
		"connections": []any{},
	}
}

// Handler builds the mux: the Connect HubService route, health/readiness
// probes, the gated /debug/state endpoint, and the embedded SPA seam.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	path, handler := hubv1connect.NewHubServiceHandler(s)
	mux.Handle(path, handler)

	mux.Handle("/healthz", s.health.LivenessHandler())
	mux.Handle("/readyz", s.health.ReadinessHandler())
	mux.Handle("/debug/state", DebugStateHandler(s.debug, s.debugSnapshot))

	// SPA seam: serve embedded assets under /app/ when present. An empty embed
	// is fine this slice (the real SPA lands later); the path stays reserved and
	// CDN-friendly so a later split is a deploy change, not a code change.
	if s.spa != nil {
		mux.Handle("/app/", http.StripPrefix("/app/", http.FileServerFS(s.spa)))
	}

	return mux
}
