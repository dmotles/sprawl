package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
	"github.com/dmotles/sprawl/internal/hub/store"
)

// newAuthedHubServer builds a server over a memStore seeded with one valid
// token and returns the client plus the token plaintext.
func newAuthedHubServer(t *testing.T, debug bool) (client hubv1connect.HubServiceClient, plaintext string, closeFn func()) {
	t.Helper()
	st := newMemStore(t)
	plaintext = seedToken(t, st)
	srv := NewServer(HubConfig{Store: st, DebugEndpoint: debug})
	ts := httptest.NewServer(srv.Handler())
	client = hubv1connect.NewHubServiceClient(ts.Client(), ts.URL)
	return client, plaintext, ts.Close
}

// bearer builds a RegisterInstance request carrying the given token plaintext.
func registerReq(hostID, runID, repo, userID, plaintext string) *connect.Request[hubv1.RegisterInstanceRequest] {
	req := connect.NewRequest(&hubv1.RegisterInstanceRequest{
		HostId: hostID, RunId: runID, RepoLabel: repo, UserId: userID,
	})
	if plaintext != "" {
		req.Header().Set("Authorization", "Bearer "+plaintext)
	}
	return req
}

// newCookieAuthedHubServer builds a server with BOTH host bearer auth and
// browser cookie login enabled over a shared memStore. It returns the client,
// a valid bearer plaintext, a helper to mint a live cookie value (backed by a
// login_sessions record), the store, and closeFn.
func newCookieAuthedHubServer(t *testing.T) (
	client hubv1connect.HubServiceClient,
	bearer string,
	mintCookie func(t *testing.T, expires time.Time) (value, id string),
	st store.Store,
	closeFn func(),
) {
	t.Helper()
	st = newMemStore(t)
	bearer = seedToken(t, st) // also EnsureUser(MVPUserID)
	ba := NewBrowserAuth(st, MVPUserID, testLoginToken, testCookieKey(), DefaultSessionTTL, nil)
	srv := NewServer(HubConfig{Store: st, Login: ba})
	ts := httptest.NewServer(srv.Handler())
	client = hubv1connect.NewHubServiceClient(ts.Client(), ts.URL)
	mintCookie = func(t *testing.T, expires time.Time) (string, string) {
		t.Helper()
		id, err := newSessionID()
		if err != nil {
			t.Fatalf("newSessionID: %v", err)
		}
		if err := st.CreateLoginSession(context.Background(), store.LoginSessionRecord{
			SessionID: store.LoginSessionID(id), UserID: MVPUserID, ExpiresAt: expires,
		}); err != nil {
			t.Fatalf("CreateLoginSession: %v", err)
		}
		return signSession(testCookieKey(), id), id
	}
	return client, bearer, mintCookie, st, ts.Close
}

func withCookie[T any](req *connect.Request[T], value string) *connect.Request[T] {
	req.Header().Set("Cookie", cookieName+"="+value)
	return req
}

func TestListInstances_CookieAuthPasses(t *testing.T) {
	client, bearer, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	ctx := context.Background()

	// Register an instance via the bearer path (a host action).
	if _, err := client.RegisterInstance(ctx, registerReq("host-c", "run", "repo", "", bearer)); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	// List it authenticated by the COOKIE ONLY (no bearer header).
	cookie, _ := mintCookie(t, time.Now().Add(time.Hour))
	resp, err := client.ListInstances(ctx, withCookie(connect.NewRequest(&hubv1.ListInstancesRequest{}), cookie))
	if err != nil {
		t.Fatalf("ListInstances via cookie: %v", err)
	}
	if len(resp.Msg.Instances) != 1 || resp.Msg.Instances[0].HostId != "host-c" {
		t.Fatalf("cookie-authed list = %+v, want [host-c]", resp.Msg.Instances)
	}
}

func TestRegisterInstance_CookieRejected(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	// A browser (cookie only, no bearer) must NOT be able to register a host.
	cookie, _ := mintCookie(t, time.Now().Add(time.Hour))
	_, err := client.RegisterInstance(context.Background(),
		withCookie(connect.NewRequest(&hubv1.RegisterInstanceRequest{HostId: "h"}), cookie))
	if err == nil {
		t.Fatal("RegisterInstance accepted a cookie — must be bearer-only")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestListInstances_ExpiredCookieRejected(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	cookie, _ := mintCookie(t, time.Now().Add(-time.Minute)) // already expired
	_, err := client.ListInstances(context.Background(),
		withCookie(connect.NewRequest(&hubv1.ListInstancesRequest{}), cookie))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated for an expired cookie", connect.CodeOf(err))
	}
}

func TestListInstances_DeletedSessionRejected(t *testing.T) {
	client, _, mintCookie, st, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	ctx := context.Background()
	cookie, id := mintCookie(t, time.Now().Add(time.Hour))

	// Valid before deletion.
	if _, err := client.ListInstances(ctx,
		withCookie(connect.NewRequest(&hubv1.ListInstancesRequest{}), cookie)); err != nil {
		t.Fatalf("pre-delete ListInstances: %v", err)
	}
	// Revoke server-side.
	if err := st.DeleteLoginSession(ctx, store.LoginSessionID(id)); err != nil {
		t.Fatalf("DeleteLoginSession: %v", err)
	}
	// The same cookie is now dead.
	_, err := client.ListInstances(ctx, withCookie(connect.NewRequest(&hubv1.ListInstancesRequest{}), cookie))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated after session delete", connect.CodeOf(err))
	}
}

func TestListInstances_TamperedCookieRejected(t *testing.T) {
	client, _, mintCookie, _, closeFn := newCookieAuthedHubServer(t)
	defer closeFn()
	cookie, _ := mintCookie(t, time.Now().Add(time.Hour))
	// Corrupt the first MAC char (right after the '.'): those are all data bits,
	// so the flip reliably changes the decoded MAC (the trailing base64 char
	// carries pad bits and can decode unchanged — see cookie_test).
	dot := strings.IndexByte(cookie, '.')
	b := []byte(cookie)
	b[dot+1] ^= 0x01
	_, err := client.ListInstances(context.Background(),
		withCookie(connect.NewRequest(&hubv1.ListInstancesRequest{}), string(b)))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated for a tampered cookie", connect.CodeOf(err))
	}
}

func TestRegisterInstance_RoundTripThroughList(t *testing.T) {
	client, plaintext, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	ctx := context.Background()

	if _, err := client.RegisterInstance(ctx, registerReq("host-1", "run-1", "myrepo", "", plaintext)); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	resp, err := client.ListInstances(ctx,
		withBearer(connect.NewRequest(&hubv1.ListInstancesRequest{}), plaintext))
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	insts := resp.Msg.Instances
	if len(insts) != 1 {
		t.Fatalf("want 1 instance, got %d", len(insts))
	}
	got := insts[0]
	if got.HostId != "host-1" {
		t.Errorf("host_id = %q, want host-1", got.HostId)
	}
	if got.RepoLabel != "myrepo" {
		t.Errorf("repo_label = %q, want myrepo", got.RepoLabel)
	}
	if got.Active {
		t.Errorf("active = true, want false (no marker set)")
	}
	if got.LastSeenUnixMs <= 0 {
		t.Errorf("last_seen_unix_ms = %d, want > 0", got.LastSeenUnixMs)
	}
}

func withBearer[T any](req *connect.Request[T], plaintext string) *connect.Request[T] {
	if plaintext != "" {
		req.Header().Set("Authorization", "Bearer "+plaintext)
	}
	return req
}

func TestRegisterInstance_RejectsMissingToken(t *testing.T) {
	client, _, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	_, err := client.RegisterInstance(context.Background(),
		registerReq("host-1", "run-1", "myrepo", "", "")) // no token
	if err == nil {
		t.Fatal("want error for missing token")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestListInstances_RejectsMissingToken(t *testing.T) {
	client, _, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	_, err := client.ListInstances(context.Background(),
		connect.NewRequest(&hubv1.ListInstancesRequest{}))
	if err == nil {
		t.Fatal("want error for missing token")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

// The server must NOT trust a client-supplied user_id; it stamps MVPUserID.
// memStore.RegisterInstance rejects an un-ensured user with ErrNotFound, so a
// bogus user_id would fail if the server forwarded it verbatim.
func TestRegisterInstance_IgnoresClientUserID(t *testing.T) {
	client, plaintext, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	if _, err := client.RegisterInstance(context.Background(),
		registerReq("host-1", "run-1", "myrepo", "attacker-user", plaintext)); err != nil {
		t.Fatalf("RegisterInstance with bogus user_id should succeed under MVPUserID: %v", err)
	}
}

func TestRegisterInstance_RequiresHostID(t *testing.T) {
	client, plaintext, closeFn := newAuthedHubServer(t, false)
	defer closeFn()
	_, err := client.RegisterInstance(context.Background(),
		registerReq("", "run-1", "myrepo", "", plaintext))
	if err == nil {
		t.Fatal("want error for empty host_id")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
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

// /debug/state surfaces the instance registry + advisory active-host markers.
func TestDebugState_ShowsInstancesAndMarker(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	ctx := context.Background()

	// Register a host, then make it the advisory active host for a project.
	if err := st.RegisterInstance(ctx, store.InstanceRegistration{
		HostID: "host-9", RunID: "r", RepoLabel: "repo9", UserID: MVPUserID,
	}); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	if err := st.UpsertProject(ctx, store.ProjectRecord{ProjectID: "proj-1", UserID: MVPUserID, RepoLabel: "repo9"}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if err := st.SetActiveHost(ctx, "proj-1", "host-9"); err != nil {
		t.Fatalf("SetActiveHost: %v", err)
	}
	_ = plaintext

	srv := NewServer(HubConfig{Store: st, DebugEndpoint: true})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/debug/state")
	if err != nil {
		t.Fatalf("GET /debug/state: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var snap map[string]any
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v (body=%s)", err, body)
	}
	instances, ok := snap["instances"].([]any)
	if !ok || len(instances) != 1 {
		t.Fatalf("snapshot missing instances array: %s", body)
	}
	inst := instances[0].(map[string]any)
	if inst["host_id"] != "host-9" {
		t.Errorf("instance host_id = %v, want host-9", inst["host_id"])
	}
	if inst["active"] != true {
		t.Errorf("instance active = %v, want true (holds marker)", inst["active"])
	}
	// An explicit advisory-marker view listing active host ids.
	markers, ok := snap["active_hosts"].([]any)
	if !ok || len(markers) != 1 || markers[0] != "host-9" {
		t.Errorf("snapshot active_hosts = %v, want [host-9]", snap["active_hosts"])
	}
}
