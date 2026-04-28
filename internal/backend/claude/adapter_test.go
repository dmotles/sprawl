package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	claudecli "github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/protocol"
)

type mockManagedTransport struct {
	mu          sync.Mutex
	closeCalled bool
	waitCalled  bool
	killCalled  bool
}

func (m *mockManagedTransport) Send(context.Context, any) error { return nil }
func (m *mockManagedTransport) Recv(context.Context) (*protocol.Message, error) {
	return nil, io.EOF
}

func (m *mockManagedTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalled = true
	return nil
}

func (m *mockManagedTransport) Wait() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waitCalled = true
	return nil
}

func (m *mockManagedTransport) Kill() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killCalled = true
	return nil
}

type mockStarter struct {
	specs     []ExecSpec
	transport *mockManagedTransport
	startErr  error
}

func (s *mockStarter) Start(_ context.Context, spec ExecSpec) (backendpkg.ManagedTransport, error) {
	s.specs = append(s.specs, spec)
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.transport, nil
}

func TestAdapter_StartBuildsStreamJSONExecSpecFromSessionSpec(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	var lookedUp string
	adapter := NewAdapter(Config{
		LookPath: func(name string) (string, error) {
			lookedUp = name
			return "/usr/bin/claude", nil
		},
		Starter: starter,
	})

	session, err := adapter.Start(context.Background(), backendpkg.SessionSpec{
		WorkDir:         "/repo",
		Identity:        "weave",
		SprawlRoot:      "/repo",
		SessionID:       "sess-1",
		PromptFile:      "/repo/.sprawl/agents/weave/SYSTEM.md",
		Model:           "sonnet",
		Effort:          "medium",
		PermissionMode:  "bypassPermissions",
		AllowedTools:    []string{"Read"},
		DisallowedTools: []string{"Edit"},
		AdditionalEnv:   map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if session == nil {
		t.Fatal("Start() returned nil session")
	}

	if lookedUp != "claude" {
		t.Errorf("LookPath called with %q, want claude", lookedUp)
	}
	if len(starter.specs) != 1 {
		t.Fatalf("starter specs = %d, want 1", len(starter.specs))
	}

	spec := starter.specs[0]
	if spec.Path != "/usr/bin/claude" {
		t.Errorf("spec.Path = %q, want /usr/bin/claude", spec.Path)
	}
	if spec.Dir != "/repo" {
		t.Errorf("spec.Dir = %q, want /repo", spec.Dir)
	}
	if !argsContain(spec.Args, "-p") {
		t.Errorf("args missing -p: %v", spec.Args)
	}
	if !argsContainPair(spec.Args, "--input-format", "stream-json") {
		t.Errorf("args missing --input-format stream-json: %v", spec.Args)
	}
	if !argsContainPair(spec.Args, "--output-format", "stream-json") {
		t.Errorf("args missing --output-format stream-json: %v", spec.Args)
	}
	if !argsContain(spec.Args, "--verbose") {
		t.Errorf("args missing --verbose: %v", spec.Args)
	}
	if !argsContainPair(spec.Args, "--session-id", "sess-1") {
		t.Errorf("args missing --session-id sess-1: %v", spec.Args)
	}
	if !argsContainPair(spec.Args, "--system-prompt-file", "/repo/.sprawl/agents/weave/SYSTEM.md") {
		t.Errorf("args missing --system-prompt-file: %v", spec.Args)
	}
	if !envContains(spec.Env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1") {
		t.Errorf("env missing session-state-events flag: %v", spec.Env)
	}
	if !envContains(spec.Env, "SPRAWL_AGENT_IDENTITY=weave") {
		t.Errorf("env missing agent identity: %v", spec.Env)
	}
	if !envContains(spec.Env, "SPRAWL_ROOT=/repo") {
		t.Errorf("env missing sprawl root: %v", spec.Env)
	}
	if !envContains(spec.Env, "FOO=bar") {
		t.Errorf("env missing extra env override: %v", spec.Env)
	}
}

func TestAdapter_StartUsesConfiguredBinaryPathWithoutLookup(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	lookupCalled := false
	adapter := NewAdapter(Config{
		Path: "/opt/bin/claude",
		LookPath: func(string) (string, error) {
			lookupCalled = true
			return "", errors.New("should not be called")
		},
		Starter: starter,
	})

	if _, err := adapter.Start(context.Background(), backendpkg.SessionSpec{SessionID: "sess-1"}); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if lookupCalled {
		t.Fatal("LookPath should not be called when Config.Path is set")
	}
	if starter.specs[0].Path != "/opt/bin/claude" {
		t.Errorf("spec.Path = %q, want /opt/bin/claude", starter.specs[0].Path)
	}
}

func TestAdapter_StartWrapsResumeWatchWhenHandlerProvided(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	var stderr bytes.Buffer
	var tripped int32
	adapter := NewAdapter(Config{
		Path:    "/opt/bin/claude",
		Starter: starter,
	})

	_, err := adapter.Start(context.Background(), backendpkg.SessionSpec{
		SessionID:       "sess-1",
		Resume:          true,
		Stderr:          &stderr,
		OnResumeFailure: func() { atomic.AddInt32(&tripped, 1) },
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if _, err := starter.specs[0].Stderr.Write([]byte(claudecli.NoConversationMarker + " dead-session\n")); err != nil {
		t.Fatalf("writing watched stderr: %v", err)
	}
	if atomic.LoadInt32(&tripped) != 1 {
		t.Fatalf("resume failure callback count = %d, want 1", tripped)
	}
	if !starter.transport.killCalled {
		t.Fatal("resume failure marker should trigger transport.Kill")
	}
	if !strings.Contains(stderr.String(), claudecli.NoConversationMarker) {
		t.Fatalf("wrapped stderr should preserve output, got %q", stderr.String())
	}
}

func TestAdapter_StartDoesNotInstallResumeWatchWhenHandlerMissing(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	var stderr bytes.Buffer
	adapter := NewAdapter(Config{
		Path:    "/opt/bin/claude",
		Starter: starter,
	})

	_, err := adapter.Start(context.Background(), backendpkg.SessionSpec{
		SessionID: "sess-1",
		Resume:    true,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if _, err := starter.specs[0].Stderr.Write([]byte(claudecli.NoConversationMarker + " dead-session\n")); err != nil {
		t.Fatalf("writing stderr: %v", err)
	}
	if starter.transport.killCalled {
		t.Fatal("resume watch should be opt-in; transport.Kill should not be called when handler is nil")
	}
	if !strings.Contains(stderr.String(), claudecli.NoConversationMarker) {
		t.Fatalf("stderr should still receive the marker text, got %q", stderr.String())
	}
}

func TestAdapter_StartReturnsSessionThatForwardsLifecycle(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	adapter := NewAdapter(Config{
		Path:    "/opt/bin/claude",
		Starter: starter,
	})

	session, err := adapter.Start(context.Background(), backendpkg.SessionSpec{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if err := session.Kill(); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if !starter.transport.closeCalled {
		t.Fatal("Close() should forward to transport.Close")
	}
	if !starter.transport.waitCalled {
		t.Fatal("Wait() should forward to transport.Wait")
	}
	if !starter.transport.killCalled {
		t.Fatal("Kill() should forward to transport.Kill")
	}
}

func argsContain(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func argsContainPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func envContains(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
