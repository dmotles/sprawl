package hub

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
)

func TestDeriveHostID_StableAndNonLeaking(t *testing.T) {
	const hint = "my-secret-hostname"
	a := DeriveHostID("/home/user/repo", hint)
	b := DeriveHostID("/home/user/repo", hint)
	if a == "" {
		t.Fatal("DeriveHostID returned empty")
	}
	if a != b {
		t.Errorf("DeriveHostID not stable: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "host_") {
		t.Errorf("host id %q lacks host_ prefix", a)
	}
	if strings.Contains(a, hint) {
		t.Errorf("host id %q leaks the raw machine hint", a)
	}
	if other := DeriveHostID("/home/user/other-repo", hint); other == a {
		t.Error("DeriveHostID should differ across sprawl roots")
	}
}

func TestResolveHostToken_EnvWins(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")
	if err := os.WriteFile(file, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == EnvHubToken {
			return "from-env"
		}
		return ""
	}
	got, err := ResolveHostToken(getenv, file)
	if err != nil {
		t.Fatalf("ResolveHostToken: %v", err)
	}
	if got != "from-env" {
		t.Errorf("token = %q, want from-env (env wins over file)", got)
	}
}

func TestResolveHostToken_ReadsFileWhenEnvEmpty(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")
	if err := os.WriteFile(file, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveHostToken(func(string) string { return "" }, file)
	if err != nil {
		t.Fatalf("ResolveHostToken: %v", err)
	}
	if got != "file-token" {
		t.Errorf("token = %q, want trimmed file-token", got)
	}
}

func TestResolveHostToken_RejectsWrongFileMode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveHostToken(func(string) string { return "" }, file)
	if err == nil {
		t.Fatal("expected an error for a non-0600 token file")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should mention 0600: %v", err)
	}
}

func TestResolveHostToken_MissingFileIsError(t *testing.T) {
	_, err := ResolveHostToken(func(string) string { return "" }, "/no/such/file")
	if err == nil {
		t.Fatal("expected an error for a missing token file")
	}
}

func TestResolveHostToken_NothingConfiguredIsEmptyNoError(t *testing.T) {
	got, err := ResolveHostToken(func(string) string { return "" }, "")
	if err != nil {
		t.Fatalf("no config should not error: %v", err)
	}
	if got != "" {
		t.Errorf("want empty token, got %q", got)
	}
}

func TestRegisterHost_ValidTokenRegistersAndAppears(t *testing.T) {
	st := newMemStore(t)
	plaintext := seedToken(t, st)
	srv := NewServer(HubConfig{Store: st})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	err := RegisterHost(context.Background(), ts.Client(), ts.URL, plaintext, HostIdentity{
		HostID: "host-abc", RunID: "run-1", RepoLabel: "repo",
	})
	if err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}

	client := hubv1connect.NewHubServiceClient(ts.Client(), ts.URL)
	req := connect.NewRequest(&hubv1.ListInstancesRequest{})
	req.Header().Set("Authorization", "Bearer "+plaintext)
	resp, err := client.ListInstances(context.Background(), req)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(resp.Msg.Instances) != 1 || resp.Msg.Instances[0].HostId != "host-abc" {
		t.Fatalf("registered host did not appear: %+v", resp.Msg.Instances)
	}
}

func TestRegisterHost_WrongTokenRejected(t *testing.T) {
	st := newMemStore(t)
	seedToken(t, st)
	srv := NewServer(HubConfig{Store: st})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	err := RegisterHost(context.Background(), ts.Client(), ts.URL, "sprawl_hub_bogus_bogus", HostIdentity{
		HostID: "host-abc", RunID: "run-1", RepoLabel: "repo",
	})
	if err == nil {
		t.Fatal("RegisterHost with a bogus token should error")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}
