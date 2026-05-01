package supervisor

// Tests for QUM-438: NewRecipientResolver bridges the supervisor's
// RuntimeRegistry to messages.RecipientResolver, classifying recipients as
// Unified vs Legacy vs Unknown based on whether their started runtime
// handle implements the unifiedRuntimeHandle marker.
//
// Expected production-code shape (referenced from these tests):
//
//   // recipient_resolver.go
//   type unifiedRuntimeHandle interface { isUnifiedHandle() }
//   func NewRecipientResolver(reg *RuntimeRegistry) messages.RecipientResolver
//
//   // runtime_launcher_unified.go (one-line addition)
//   func (h *unifiedHandle) isUnifiedHandle() {}
//
//   // runtime.go (unexported accessor used by the resolver)
//   func (r *AgentRuntime) currentHandle() RuntimeHandle
//
// These tests assume those exist; they will fail to compile until the
// implementation lands. That compile failure IS the red phase.

import (
	"context"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
)

// fakeUnifiedHandle implements RuntimeHandle and the unifiedRuntimeHandle
// marker, simulating *unifiedHandle for resolver tests.
type fakeUnifiedHandle struct{ runtimeTestSession }

func (h *fakeUnifiedHandle) isUnifiedHandle() {}

// fakeLegacyHandle implements RuntimeHandle but NOT unifiedRuntimeHandle,
// simulating a legacy runner-style handle.
type fakeLegacyHandle struct{ runtimeTestSession }

// ensureStartedRuntime registers an agent in reg with a fake starter that
// returns the given handle, then drives Start so the runtime ends up in the
// "Started" lifecycle with handle attached. This avoids reaching into the
// registry's unexported fields.
func ensureStartedRuntime(t *testing.T, reg *RuntimeRegistry, name string, handle RuntimeHandle) *AgentRuntime {
	t.Helper()
	starter := &runtimeTestStarter{session: handle}
	rt := reg.Ensure(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState(name),
		Starter:    starter,
	})
	if rt == nil {
		t.Fatalf("Ensure(%q) returned nil", name)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	return rt
}

func TestNewRecipientResolver_UnifiedHandleReturnsUnified(t *testing.T) {
	reg := NewRuntimeRegistry()
	handle := &fakeUnifiedHandle{runtimeTestSession: runtimeTestSession{
		sessionID: "sess-bob",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
	}}
	ensureStartedRuntime(t, reg, "bob", handle)

	resolver := NewRecipientResolver(reg)
	if resolver == nil {
		t.Fatal("NewRecipientResolver returned nil")
	}
	if got := resolver("bob"); got != messages.RecipientUnified {
		t.Fatalf("resolver(bob) = %v, want RecipientUnified", got)
	}
}

func TestNewRecipientResolver_LegacyHandleReturnsLegacy(t *testing.T) {
	reg := NewRuntimeRegistry()
	handle := &fakeLegacyHandle{runtimeTestSession: runtimeTestSession{
		sessionID: "sess-carol",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
	}}
	ensureStartedRuntime(t, reg, "carol", handle)

	resolver := NewRecipientResolver(reg)
	if got := resolver("carol"); got != messages.RecipientLegacy {
		t.Fatalf("resolver(carol) = %v, want RecipientLegacy", got)
	}
}

func TestNewRecipientResolver_NotStartedReturnsUnknown(t *testing.T) {
	reg := NewRuntimeRegistry()
	reg.Ensure(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("dave"),
	})
	// dave is registered but not started — lifecycle = Registered, no handle.

	resolver := NewRecipientResolver(reg)
	if got := resolver("dave"); got != messages.RecipientUnknown {
		t.Fatalf("resolver(dave) = %v, want RecipientUnknown (not started)", got)
	}
}

func TestNewRecipientResolver_AbsentReturnsUnknown(t *testing.T) {
	reg := NewRuntimeRegistry()

	resolver := NewRecipientResolver(reg)
	if got := resolver("nobody"); got != messages.RecipientUnknown {
		t.Fatalf("resolver(nobody) = %v, want RecipientUnknown (absent)", got)
	}
}

func TestNewRecipientResolver_NilRegistryReturnsUnknown(t *testing.T) {
	resolver := NewRecipientResolver(nil)
	if resolver == nil {
		t.Fatal("NewRecipientResolver(nil) returned nil; expected fail-open resolver")
	}
	if got := resolver("anyone"); got != messages.RecipientUnknown {
		t.Fatalf("resolver(anyone) with nil registry = %v, want RecipientUnknown", got)
	}
}
