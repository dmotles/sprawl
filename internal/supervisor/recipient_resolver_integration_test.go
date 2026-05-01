package supervisor

// Integration-style end-to-end test for QUM-438. This exercises the FULL
// in-process path: a real *RuntimeRegistry seeded with a started runtime
// whose handle is either unified or legacy, wired to the process via
// messages.SetRecipientResolver(NewRecipientResolver(reg)), then driving
// messages.Send against an on-disk sprawl root and asserting on the
// presence/absence of the legacy `.wake` sentinel.
//
// This stands in for the bash sandbox script the issue's acceptance
// criterion describes. The pure bash path requires driving a live `claude`
// TTY to invoke the `mcp__sprawl__send_async` tool from inside `sprawl
// enter`; that is too brittle to script reliably. This Go test reproduces
// the same end-state assertions (wake-file absent for unified recipient,
// present for legacy/unknown) against the same production code paths.

import (
	"os"
	"path/filepath"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
)

// Helper: start a fake-handle-backed runtime in reg and return the agents
// dir under sprawlRoot for wake-file assertions.
func sandboxedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	return root
}

func TestE2E_QUM438_UnifiedRecipientSkipsWakeFile(t *testing.T) {
	// Belt-and-braces: clear any process-level resolver from prior tests.
	messages.SetRecipientResolver(nil)
	t.Cleanup(func() { messages.SetRecipientResolver(nil) })

	root := sandboxedRoot(t)

	reg := NewRuntimeRegistry()
	handle := &fakeUnifiedHandle{runtimeTestSession: runtimeTestSession{
		sessionID: "sess-childu",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
	}}
	ensureStartedRuntime(t, reg, "childu", handle)

	messages.SetRecipientResolver(NewRecipientResolver(reg))

	if _, err := messages.Send(root, "weave", "childu", "subj", "body"); err != nil {
		t.Fatalf("messages.Send: %v", err)
	}

	wakePath := filepath.Join(root, ".sprawl", "agents", "childu.wake")
	if _, err := os.Stat(wakePath); !os.IsNotExist(err) {
		t.Fatalf("wake file MUST NOT exist for unified recipient; stat err = %v", err)
	}

	// Message MUST still land in childu/new for the next turn to consume.
	newDir := filepath.Join(messages.MessagesDir(root), "childu", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("read new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 message in %s, got %d", newDir, len(entries))
	}
}

func TestE2E_QUM438_LegacyRecipientWritesWakeFile(t *testing.T) {
	messages.SetRecipientResolver(nil)
	t.Cleanup(func() { messages.SetRecipientResolver(nil) })

	root := sandboxedRoot(t)

	reg := NewRuntimeRegistry()
	handle := &fakeLegacyHandle{runtimeTestSession: runtimeTestSession{
		sessionID: "sess-childl",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
	}}
	ensureStartedRuntime(t, reg, "childl", handle)

	messages.SetRecipientResolver(NewRecipientResolver(reg))

	if _, err := messages.Send(root, "weave", "childl", "subj", "body"); err != nil {
		t.Fatalf("messages.Send: %v", err)
	}

	wakePath := filepath.Join(root, ".sprawl", "agents", "childl.wake")
	if _, err := os.Stat(wakePath); err != nil {
		t.Fatalf("wake file MUST exist for legacy recipient: %v", err)
	}
}

func TestE2E_QUM438_NoResolverWritesWakeFile(t *testing.T) {
	// Mirrors out-of-process / CLI fall-through where no resolver was
	// installed. Covers the "default runtime, no regression" criterion.
	messages.SetRecipientResolver(nil)
	t.Cleanup(func() { messages.SetRecipientResolver(nil) })

	root := sandboxedRoot(t)

	if _, err := messages.Send(root, "weave", "child0", "subj", "body"); err != nil {
		t.Fatalf("messages.Send: %v", err)
	}

	wakePath := filepath.Join(root, ".sprawl", "agents", "child0.wake")
	if _, err := os.Stat(wakePath); err != nil {
		t.Fatalf("wake file MUST exist when no resolver is installed: %v", err)
	}
}
