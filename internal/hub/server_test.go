package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
)

func newHubTestServer(t *testing.T) (hubv1connect.HubServiceClient, func()) {
	t.Helper()
	srv := NewServer(HubConfig{})
	ts := httptest.NewServer(srv.Handler())
	client := hubv1connect.NewHubServiceClient(ts.Client(), ts.URL)
	return client, ts.Close
}

func TestRegisterInstance_Unimplemented(t *testing.T) {
	client, closeFn := newHubTestServer(t)
	defer closeFn()

	_, err := client.RegisterInstance(context.Background(),
		connect.NewRequest(&hubv1.RegisterInstanceRequest{HostId: "h1", RunId: "r1"}))
	if err == nil {
		t.Fatal("RegisterInstance: want error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Fatalf("RegisterInstance: want CodeUnimplemented, got %v (%v)", code, err)
	}
}

func TestListInstances_Unimplemented(t *testing.T) {
	client, closeFn := newHubTestServer(t)
	defer closeFn()

	_, err := client.ListInstances(context.Background(),
		connect.NewRequest(&hubv1.ListInstancesRequest{}))
	if err == nil {
		t.Fatal("ListInstances: want error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Fatalf("ListInstances: want CodeUnimplemented, got %v (%v)", code, err)
	}
}

func TestHandler_HealthEndpointsMounted(t *testing.T) {
	srv := NewServer(HubConfig{})
	srv.health.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, ep := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + ep)
		if err != nil {
			t.Fatalf("GET %s: %v", ep, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", ep, resp.StatusCode)
		}
	}
}

func TestHandler_DebugStateGated(t *testing.T) {
	// Default config has DebugEndpoint=false → /debug/state must 404.
	srv := NewServer(HubConfig{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/debug/state")
	if err != nil {
		t.Fatalf("GET /debug/state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("debug gated off: want 404, got %d", resp.StatusCode)
	}

	// With the endpoint enabled it responds 200 JSON with the component attr.
	srvOn := NewServer(HubConfig{DebugEndpoint: true})
	tsOn := httptest.NewServer(srvOn.Handler())
	defer tsOn.Close()
	resp, err = http.Get(tsOn.URL + "/debug/state")
	if err != nil {
		t.Fatalf("GET /debug/state (on): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("debug gated on: want 200, got %d", resp.StatusCode)
	}
}
