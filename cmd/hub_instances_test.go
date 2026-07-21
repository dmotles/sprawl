package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/hub"
	"github.com/dmotles/sprawl/internal/hub/store"
)

func TestHubInstances_ListsRegisteredHosts(t *testing.T) {
	deps, out, _, st := newTestHubTokenDeps(t)
	ctx := context.Background()
	if err := st.EnsureUser(ctx, hub.MVPUserID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if err := st.RegisterInstance(ctx, store.InstanceRegistration{
		HostID: "host-xyz", RunID: "r", RepoLabel: "myrepo", UserID: hub.MVPUserID,
	}); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}

	if err := runHubInstances(ctx, deps, "postgres://x"); err != nil {
		t.Fatalf("instances: %v", err)
	}
	if !strings.Contains(out.String(), "host-xyz") || !strings.Contains(out.String(), "myrepo") {
		t.Fatalf("instances listing missing registered host: %q", out.String())
	}
}

func TestHubInstances_EmptyIsFriendly(t *testing.T) {
	deps, _, errOut, _ := newTestHubTokenDeps(t)
	if err := runHubInstances(context.Background(), deps, "postgres://x"); err != nil {
		t.Fatalf("instances: %v", err)
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "no instances") {
		t.Errorf("expected a friendly empty message, got: %q", errOut.String())
	}
}
