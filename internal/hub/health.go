package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// dbCheckTimeout bounds the readiness DB probe so a hung backend cannot wedge
// /readyz.
const dbCheckTimeout = 2 * time.Second

// Health tracks the hub's readiness. Liveness is dependency-free and always
// reports up; readiness is a flag flipped true once the server is serving and
// false at the start of a graceful drain. An optional dbCheck adds backend
// reachability on top of the flag: once serving, /readyz reports 200 only if
// the flag is set AND the backend is reachable.
type Health struct {
	ready   atomic.Bool
	dbCheck func(context.Context) error // optional; nil = ready-flag only
}

// SetReady sets the readiness flag.
func (h *Health) SetReady(v bool) { h.ready.Store(v) }

// Ready reports the current readiness flag.
func (h *Health) Ready() bool { return h.ready.Load() }

// SetDBCheck installs a backend-reachability probe consulted by /readyz. It is
// set once at server construction, before serving begins. Passing nil (the
// zero value) means readiness is gated on the flag alone (dev/memStore).
func (h *Health) SetDBCheck(fn func(context.Context) error) { h.dbCheck = fn }

// LivenessHandler answers /healthz. It is dependency-free and always returns
// 200 so a slow or unready dependency never causes a container restart loop —
// that is what readiness is for.
func (h *Health) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}

// ReadinessHandler answers /readyz: 200 when ready, 503 otherwise. The platform
// uses this to gate routing and rollout without restarting the container.
func (h *Health) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		if h.dbCheck != nil {
			ctx, cancel := context.WithTimeout(r.Context(), dbCheckTimeout)
			defer cancel()
			if err := h.dbCheck(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("database unreachable\n"))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}

// DebugStateHandler answers /debug/state with a read-only JSON snapshot of the
// hub's internal state. It is gated: when enabled is false it returns 404 and
// no body (the endpoint reveals topology, so it is opt-in via
// SPRAWL_HUB_DEBUG_ENDPOINT). snap supplies the snapshot value to serialize.
func DebugStateHandler(enabled bool, snap func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !enabled {
			http.Error(w, "not found\n", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap())
	}
}
