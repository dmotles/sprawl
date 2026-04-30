package backend

import (
	"context"
	"testing"
)

func TestCallerIdentity_RoundTrip(t *testing.T) {
	ctx := WithCallerIdentity(context.Background(), "finn")
	got := CallerIdentity(ctx)
	if got != "finn" {
		t.Errorf("CallerIdentity() = %q, want %q", got, "finn")
	}
}

func TestCallerIdentity_EmptyContext(t *testing.T) {
	got := CallerIdentity(context.Background())
	if got != "" {
		t.Errorf("CallerIdentity() = %q, want empty string", got)
	}
}

func TestCallerIdentity_EmptyString(t *testing.T) {
	ctx := WithCallerIdentity(context.Background(), "")
	got := CallerIdentity(ctx)
	if got != "" {
		t.Errorf("CallerIdentity() = %q, want empty string", got)
	}
}
