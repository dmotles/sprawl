package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/dmotles/sprawl/internal/hub/token"
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
	store  store.Store
	login  *BrowserAuth // browser login; nil disables it (host auth unaffected)
	fanout *Fanout      // in-memory wire-log backlog + browser fan-out (QUM-909)
}

// NewServer builds a Server from cfg. The readiness flag starts false; the
// serve loop flips it true once the listener is up. The Store is taken from
// cfg.Store, or defaults to an in-memory memStore when absent so dev and tests
// need no database. /readyz is gated on Store.Ping so it reflects backend
// reachability (memStore.Ping is always nil → dev stays ready).
func NewServer(cfg HubConfig) *Server {
	log := cfg.logger().With("component", "registry")
	st := cfg.Store
	if st == nil {
		mem, err := store.NewMemStore()
		if err != nil {
			// NewMemStore only fails if the OS RNG fails, which is effectively
			// never. Log and proceed with readiness gated on the flag alone.
			log.Error("memstore init failed; readiness gated on flag only", "error", err)
		} else {
			// Ensure the singleton user so the zero-config dev server can
			// accept registrations without a separate boot step. Idempotent;
			// memStore.EnsureUser ignores ctx.
			if err := mem.EnsureUser(context.Background(), MVPUserID); err != nil {
				log.Error("memstore ensure-user failed", "error", err)
			}
			st = mem
		}
	}

	health := &Health{}
	if st != nil {
		health.SetDBCheck(st.Ping)
	}

	fanout := NewFanout()
	fanout.log = log

	return &Server{
		log:    log,
		health: health,
		debug:  cfg.DebugEndpoint,
		spa:    cfg.SPA,
		store:  st,
		login:  cfg.Login,
		fanout: fanout,
	}
}

// RegisterInstance records a host's presence (idempotent upsert keyed by
// host_id). The caller is already authenticated by the auth interceptor. The
// client-supplied user_id is NOT trusted — the server always stamps MVPUserID.
func (s *Server) RegisterInstance(
	ctx context.Context, req *connect.Request[hubv1.RegisterInstanceRequest],
) (*connect.Response[hubv1.RegisterInstanceResponse], error) {
	if req.Msg.GetHostId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("host_id is required"))
	}
	if err := s.store.RegisterInstance(ctx, store.InstanceRegistration{
		HostID:    store.HostID(req.Msg.GetHostId()),
		RunID:     req.Msg.GetRunId(),
		RepoLabel: req.Msg.GetRepoLabel(),
		UserID:    MVPUserID,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("register instance: %w", err))
	}
	return connect.NewResponse(&hubv1.RegisterInstanceResponse{}), nil
}

// ListInstances returns the registered instances as metadata only — no secret
// material is ever included.
func (s *Server) ListInstances(
	ctx context.Context, _ *connect.Request[hubv1.ListInstancesRequest],
) (*connect.Response[hubv1.ListInstancesResponse], error) {
	recs, err := s.store.ListInstances(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("list instances: %w", err))
	}
	out := make([]*hubv1.Instance, 0, len(recs))
	for _, r := range recs {
		out = append(out, instanceToProto(r))
	}
	return connect.NewResponse(&hubv1.ListInstancesResponse{Instances: out}), nil
}

// CreateHostToken mints a new host bearer token. This is a browser-only,
// cookie-authenticated operator action (the auth interceptor rejects host
// bearer callers). It mints via the mint → seal → persist path: the sealed
// hash is stored under MVPUserID and the plaintext is returned EXACTLY ONCE
// (never persisted, never logged).
func (s *Server) CreateHostToken(
	ctx context.Context, req *connect.Request[hubv1.CreateHostTokenRequest],
) (*connect.Response[hubv1.CreateHostTokenResponse], error) {
	m, err := token.Mint()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mint token: %w", err))
	}
	sealed, err := token.SealedHash(ctx, s.store.Secrets(), m.Secret)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("seal token hash: %w", err))
	}
	if err := s.store.CreateToken(ctx, store.TokenRecord{
		TokenID: store.TokenID(m.TokenID),
		UserID:  MVPUserID,
		Hash:    sealed,
		Label:   req.Msg.GetLabel(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist token: %w", err))
	}
	return connect.NewResponse(&hubv1.CreateHostTokenResponse{
		Token:   m.Plaintext,
		TokenId: m.TokenID,
	}), nil
}

// ListHostTokens returns the caller's host tokens as metadata only — never the
// secret, hash, or plaintext. Browser-only (cookie-authenticated).
func (s *Server) ListHostTokens(
	ctx context.Context, _ *connect.Request[hubv1.ListHostTokensRequest],
) (*connect.Response[hubv1.ListHostTokensResponse], error) {
	recs, err := s.store.ListTokens(ctx, MVPUserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list tokens: %w", err))
	}
	out := make([]*hubv1.HostToken, 0, len(recs))
	for _, r := range recs {
		out = append(out, hostTokenToProto(r))
	}
	return connect.NewResponse(&hubv1.ListHostTokensResponse{Tokens: out}), nil
}

// RevokeHostToken revokes a token by its non-secret token id. Browser-only
// (cookie-authenticated). An unknown id maps to NotFound.
func (s *Server) RevokeHostToken(
	ctx context.Context, req *connect.Request[hubv1.RevokeHostTokenRequest],
) (*connect.Response[hubv1.RevokeHostTokenResponse], error) {
	id := req.Msg.GetTokenId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("token_id is required"))
	}
	if err := s.store.RevokeToken(ctx, store.TokenID(id)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("no token with id %q", id))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke token: %w", err))
	}
	return connect.NewResponse(&hubv1.RevokeHostTokenResponse{}), nil
}

// PushWireLog is the host->hub uplink for batches of seq'd wire-log frames. It
// stores frames in the in-memory per-session backlog, idempotent by seq (a
// frame whose seq is <= the highest already stored is dropped, so a re-uplink
// after a browser reconnect is a no-op). The durable audit sink is deferred
// (QUM-909): nothing is written to the store here.
func (s *Server) PushWireLog(
	_ context.Context, req *connect.Request[hubv1.PushWireLogRequest],
) (*connect.Response[hubv1.PushWireLogResponse], error) {
	key, err := wireSessionKey(req.Msg.GetHostId(), req.Msg.GetRunId(), req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	s.fanout.Push(key, req.Msg.GetFrames())
	return connect.NewResponse(&hubv1.PushWireLogResponse{}), nil
}

// SubscribeWireLog is the browser->hub server-stream downlink. It replays the
// in-memory backlog (full when from_seq=0, delta for from_seq>0), then
// live-tails new frames and emits a periodic on-stream HEARTBEAT frame to keep
// the connection alive across L7 idle timeouts. It returns when the client
// disconnects (ctx cancelled) or a send fails.
//
// Restart behavior (QUM-909, in-mem only): a hubd restart drops all streams and
// clears the backlog. Browsers reconnect and pass the last seq they saw as
// from_seq; the backlog is rebuilt as the host re-uplinks, so replay resumes
// from the earliest re-uplinked seq.
func (s *Server) SubscribeWireLog(
	ctx context.Context,
	req *connect.Request[hubv1.SubscribeWireLogRequest],
	stream *connect.ServerStream[hubv1.SubscribeWireLogResponse],
) error {
	key, err := wireSessionKey(req.Msg.GetHostId(), req.Msg.GetRunId(), req.Msg.GetSessionId())
	if err != nil {
		return err
	}
	return s.fanout.Subscribe(ctx, key, req.Msg.GetFromSeq(), stream)
}

// wireSessionKey validates and builds a sessionKey from the wire-log request
// triple. All three components are required; a session is uniquely identified
// by (host_id, run_id, session_id).
func wireSessionKey(hostID, runID, sessionID string) (sessionKey, error) {
	if hostID == "" || runID == "" || sessionID == "" {
		return sessionKey{}, connect.NewError(connect.CodeInvalidArgument,
			errors.New("host_id, run_id, and session_id are all required"))
	}
	return sessionKey{hostID: hostID, runID: runID, sessionID: sessionID}, nil
}

// hostTokenToProto maps a store TokenRecord to the wire HostToken (metadata
// only — no secret material). A nil RevokedAt maps to the 0 sentinel (active).
func hostTokenToProto(r store.TokenRecord) *hubv1.HostToken {
	ht := &hubv1.HostToken{
		TokenId:         string(r.TokenID),
		Label:           r.Label,
		CreatedAtUnixMs: r.CreatedAt.UnixMilli(),
	}
	if r.RevokedAt != nil {
		ht.RevokedAtUnixMs = r.RevokedAt.UnixMilli()
	}
	return ht
}

// instanceToProto maps a store InstanceRecord to the wire Instance shape.
func instanceToProto(r store.InstanceRecord) *hubv1.Instance {
	return &hubv1.Instance{
		HostId:           string(r.HostID),
		RepoLabel:        r.RepoLabel,
		Active:           r.Active,
		ClientsConnected: r.ClientsConnected,
		LastSeenUnixMs:   r.LastSeenUnixMs,
	}
}

// debugSnapshot returns the read-only state snapshot served at /debug/state.
// It reflects only state the server already holds: process health plus the
// instance registry and the advisory active-host markers (the host ids that
// currently hold a marker for any project — advisory only, no fence/lease).
func (s *Server) debugSnapshot() any {
	streams, connections := s.fanout.DebugSnapshot()
	snap := map[string]any{
		"component":   "hubd",
		"now":         time.Now().UTC().Format(time.RFC3339),
		"uptime_ms":   time.Since(startedAt).Milliseconds(),
		"ready":       s.health.Ready(),
		"streams":     streams,
		"connections": connections,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	recs, err := s.store.ListInstances(ctx)
	if err != nil {
		snap["instances_error"] = err.Error()
		return snap
	}
	instances := make([]map[string]any, 0, len(recs))
	activeHosts := make([]string, 0)
	for _, r := range recs {
		instances = append(instances, map[string]any{
			"host_id":           string(r.HostID),
			"repo_label":        r.RepoLabel,
			"active":            r.Active,
			"clients_connected": r.ClientsConnected,
			"last_seen_unix_ms": r.LastSeenUnixMs,
		})
		if r.Active {
			activeHosts = append(activeHosts, string(r.HostID))
		}
	}
	snap["instances"] = instances
	snap["active_hosts"] = activeHosts
	return snap
}

// Handler builds the mux: the Connect HubService route, health/readiness
// probes, the gated /debug/state endpoint, and the embedded SPA seam.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Auth is always on for the HubService route: every RPC must present a valid
	// bearer token (hosts) or, for read RPCs, a valid browser session cookie
	// (s.login, when enabled). Health/readiness/debug are separate mux routes and
	// intentionally stay open.
	path, handler := hubv1connect.NewHubServiceHandler(s,
		connect.WithInterceptors(NewAuthInterceptor(s.store, MVPUserID, s.login, s.log)))
	mux.Handle(path, handler)

	mux.Handle("/healthz", s.health.LivenessHandler())
	mux.Handle("/readyz", s.health.ReadinessHandler())
	mux.Handle("/debug/state", DebugStateHandler(s.debug, s.debugSnapshot))

	// Browser login (docs 04 §1/§6). Routes are mounted unconditionally so a
	// build without browser login configured returns a clear 503 rather than a
	// confusing 404. When enabled, POST /login trades the login token for a
	// signed session cookie and /logout revokes it.
	mux.Handle("/login", s.browserLoginRoute(func(login *BrowserAuth) http.Handler {
		return login.LoginHandler(s.spa)
	}))
	mux.Handle("/logout", s.browserLoginRoute(func(login *BrowserAuth) http.Handler {
		return login.LogoutHandler()
	}))

	// SPA seam: serve embedded assets under /app/ when present. An empty embed
	// is fine this slice (the real SPA lands later); the path stays reserved and
	// CDN-friendly so a later split is a deploy change, not a code change.
	if s.spa != nil {
		mux.Handle("/app/", http.StripPrefix("/app/", http.FileServerFS(s.spa)))
	}

	return mux
}

// browserLoginRoute returns the given browser-login handler when browser login
// is enabled, or a handler that returns 503 "browser login not configured"
// when it is disabled (s.login == nil). This keeps /login and /logout mounted
// in both modes so a disabled deploy gives a clear signal, not a 404.
func (s *Server) browserLoginRoute(build func(*BrowserAuth) http.Handler) http.Handler {
	if s.login == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "browser login not configured", http.StatusServiceUnavailable)
		})
	}
	return build(s.login)
}
