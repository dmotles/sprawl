package hub

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// Health tracks the hub's readiness. Liveness is dependency-free and always
// reports up; readiness is a flag flipped true once the server is serving and
// false at the start of a graceful drain.
type Health struct {
	ready atomic.Bool
}

// SetReady sets the readiness flag.
func (h *Health) SetReady(v bool) { h.ready.Store(v) }

// Ready reports the current readiness flag.
func (h *Health) Ready() bool { return h.ready.Load() }

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
	return func(w http.ResponseWriter, _ *http.Request) {
		if h.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
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
