package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLiveness_Always200(t *testing.T) {
	h := &Health{}
	// Liveness is dependency-free: it reports 200 whether or not the server is
	// ready, so a slow/unready dependency never triggers a restart loop.
	for _, ready := range []bool{false, true} {
		h.SetReady(ready)
		rec := httptest.NewRecorder()
		h.LivenessHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("liveness with ready=%v: want 200, got %d", ready, rec.Code)
		}
	}
}

func TestReadiness_FlipsWithAtomic(t *testing.T) {
	h := &Health{}

	rec := httptest.NewRecorder()
	h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness before SetReady(true): want 503, got %d", rec.Code)
	}

	h.SetReady(true)
	rec = httptest.NewRecorder()
	h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness after SetReady(true): want 200, got %d", rec.Code)
	}

	h.SetReady(false)
	rec = httptest.NewRecorder()
	h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness after SetReady(false): want 503, got %d", rec.Code)
	}
}

// TestReadiness_ConcurrentAccess exercises the atomic backing under concurrent
// flips and reads; meaningful under `go test -race`.
func TestReadiness_ConcurrentAccess(t *testing.T) {
	h := &Health{}
	handler := h.ReadinessHandler()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 100 {
				h.SetReady((i+j)%2 == 0)
				rec := httptest.NewRecorder()
				handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
				if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
					t.Errorf("unexpected readiness status: %d", rec.Code)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestDebugState_GatedOff(t *testing.T) {
	// When the debug endpoint is disabled it must return 404 AND withhold state:
	// the snapshot function must never be invoked, and no state bytes may reach
	// the body. This is the security property of the gate.
	handler := DebugStateHandler(false, func() any {
		t.Error("snapshot function must not be invoked when the debug endpoint is gated off")
		return map[string]any{"secret": "leaked-topology"}
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/debug/state", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("debug gated off: want 404, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "leaked-topology") {
		t.Fatalf("debug gated off leaked state into body: %q", rec.Body.String())
	}
}

func TestDebugState_GatedOn(t *testing.T) {
	handler := DebugStateHandler(true, func() any {
		return map[string]any{"component": "hubd", "connections": 0}
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/debug/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("debug gated on: want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("debug content-type: want application/json, got %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("debug body is not valid JSON: %v", err)
	}
	if body["component"] != "hubd" {
		t.Fatalf("debug body missing component=hubd: %v", body)
	}
}
