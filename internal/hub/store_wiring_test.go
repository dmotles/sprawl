package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmotles/sprawl/internal/hub/store"
)

// fakeStore is a minimal Store whose Ping is scripted, for readiness wiring.
type fakeStore struct {
	store.Store
	pingErr error
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func TestNewServer_NilStoreDefaultsMem_Ready(t *testing.T) {
	srv := NewServer(HubConfig{})
	srv.health.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz with default memStore: want 200, got %d", resp.StatusCode)
	}
}

func TestNewServer_InjectedStore_ReadyzReflectsPing(t *testing.T) {
	fs := &fakeStore{pingErr: errors.New("connection refused")}
	srv := NewServer(HubConfig{Store: fs})
	srv.health.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with failing Ping: want 503, got %d", resp.StatusCode)
	}

	// Recover: Ping succeeds → 200.
	fs.pingErr = nil
	resp2, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz (recover): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/readyz after Ping recovers: want 200, got %d", resp2.StatusCode)
	}
}
