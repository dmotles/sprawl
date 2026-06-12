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
	"time"

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

func (m *mockManagedTransport) Pid() int { return 0 }

type mockStarter struct {
	specs     []ExecSpec
	transport *mockManagedTransport
	startErr  error
}

func (s *mockStarter) Start(spec ExecSpec) (backendpkg.ManagedTransport, error) {
	s.specs = append(s.specs, spec)
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.transport, nil
}

func TestResolveHangTimeout(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		set     bool
		wantDur time.Duration
		wantOK  bool
	}{
		{name: "unset falls back to default", set: false, wantOK: false},
		{name: "empty falls back to default", env: "", set: true, wantOK: false},
		{name: "valid short duration", env: "20s", set: true, wantDur: 20 * time.Second, wantOK: true},
		{name: "negative disables watchdog", env: "-1s", set: true, wantDur: -1 * time.Second, wantOK: true},
		{name: "unparseable falls back to default", env: "not-a-duration", set: true, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("SPRAWL_BACKEND_HANG_TIMEOUT", tc.env)
			} else {
				// Ensure no ambient value leaks in from the host env.
				t.Setenv("SPRAWL_BACKEND_HANG_TIMEOUT", "")
			}
			gotDur, gotOK := resolveHangTimeout()
			if gotOK != tc.wantOK {
				t.Fatalf("resolveHangTimeout() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotDur != tc.wantDur {
				t.Errorf("resolveHangTimeout() dur = %v, want %v", gotDur, tc.wantDur)
			}
		})
	}
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

func TestAdapter_StartReplayUserMessagesPropagatesFlag(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	adapter := NewAdapter(Config{
		LookPath: func(string) (string, error) { return "/usr/bin/claude", nil },
		Starter:  starter,
	})

	if _, err := adapter.Start(context.Background(), backendpkg.SessionSpec{
		WorkDir:            "/repo",
		SessionID:          "sess-1",
		ReplayUserMessages: true,
	}); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if len(starter.specs) != 1 {
		t.Fatalf("starter specs = %d, want 1", len(starter.specs))
	}
	if !argsContain(starter.specs[0].Args, "--replay-user-messages") {
		t.Errorf("args missing --replay-user-messages: %v", starter.specs[0].Args)
	}
}

func TestAdapter_StartReplayUserMessagesDefaultOff(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	adapter := NewAdapter(Config{
		LookPath: func(string) (string, error) { return "/usr/bin/claude", nil },
		Starter:  starter,
	})

	if _, err := adapter.Start(context.Background(), backendpkg.SessionSpec{
		WorkDir:   "/repo",
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if len(starter.specs) != 1 {
		t.Fatalf("starter specs = %d, want 1", len(starter.specs))
	}
	if argsContain(starter.specs[0].Args, "--replay-user-messages") {
		t.Errorf("args should not contain --replay-user-messages by default: %v", starter.specs[0].Args)
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

func TestAdapter_StartResolvesWireLogPath(t *testing.T) {
	starter := &mockStarter{transport: &mockManagedTransport{}}
	adapter := NewAdapter(Config{
		Path:    "/opt/bin/claude",
		Starter: starter,
	})

	_, err := adapter.Start(context.Background(), backendpkg.SessionSpec{
		SprawlRoot: "/some/root",
		Identity:   "weave",
		SessionID:  "abc-123",
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	want := "/some/root/.sprawl/logs/sessions/weave/abc-123.ndjson"
	if got := starter.specs[0].WireLogPath; got != want {
		t.Errorf("WireLogPath = %q, want %q", got, want)
	}
}

func TestAdapter_StartLeavesWireLogPathEmptyWhenComponentMissing(t *testing.T) {
	cases := []struct {
		name string
		spec backendpkg.SessionSpec
	}{
		{
			name: "missing SprawlRoot",
			spec: backendpkg.SessionSpec{Identity: "weave", SessionID: "abc-123"},
		},
		{
			name: "missing Identity",
			spec: backendpkg.SessionSpec{SprawlRoot: "/some/root", SessionID: "abc-123"},
		},
		{
			name: "missing SessionID",
			spec: backendpkg.SessionSpec{SprawlRoot: "/some/root", Identity: "weave"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			starter := &mockStarter{transport: &mockManagedTransport{}}
			adapter := NewAdapter(Config{Path: "/opt/bin/claude", Starter: starter})
			if _, err := adapter.Start(context.Background(), tc.spec); err != nil {
				t.Fatalf("Start() error: %v", err)
			}
			if got := starter.specs[0].WireLogPath; got != "" {
				t.Errorf("WireLogPath = %q, want empty when %s", got, tc.name)
			}
		})
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

// blockingWriter is an io.Writer whose Write blocks until release is closed.
// Mirrors the kernel-pipe-full wedge that transport.Send hit prior to QUM-603.
type blockingWriter struct {
	release  chan struct{}
	released atomic.Bool
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{release: make(chan struct{})}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

func (b *blockingWriter) Close() error {
	if b.released.CompareAndSwap(false, true) {
		close(b.release)
	}
	return nil
}

// TestTransport_Send_HonorsCtxOnWedgedWrite proves QUM-603: when the underlying
// writer is wedged (kernel pipe full / consumer not draining), Send must
// return ctx.Err() promptly on ctx cancellation rather than blocking forever
// in WriteJSON's syscall.
func TestTransport_Send_HonorsCtxOnWedgedWrite(t *testing.T) {
	bw := newBlockingWriter()
	defer bw.Close()

	tr := &transport{
		writer: protocol.NewWriter(bw),
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- tr.Send(ctx, map[string]string{"type": "ping"})
	}()

	// Give Send a moment to enter WriteJSON and block in the wedged Write.
	// We don't need a precise sync — the select inside Send is what we're
	// testing, and it will exit on ctx.Done() regardless of where the
	// goroutine is when cancel() fires.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return within 2s of ctx cancellation — ctx not honored")
	}
}

// TestTransport_Send_NormalWriteSucceeds proves the goroutine+select wrapping
// doesn't break the happy path: a normal Send against a healthy writer
// returns nil and the bytes land on the wire.
func TestTransport_Send_NormalWriteSucceeds(t *testing.T) {
	var buf bytes.Buffer
	tr := &transport{
		writer: protocol.NewWriter(&buf),
	}

	if err := tr.Send(context.Background(), map[string]string{"type": "ping"}); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if !strings.Contains(buf.String(), `"type":"ping"`) {
		t.Errorf("buf = %q, want it to contain the marshaled message", buf.String())
	}
}

// TestTransport_Send_PrecancelledCtxReturnsImmediately ensures a caller that
// passes an already-cancelled ctx doesn't have to wait for the (possibly
// wedged) write to complete before getting ctx.Err() back. With a 1-buffer
// errCh on Send the goroutine doesn't leak on the happy path here either.
func TestTransport_Send_PrecancelledCtxReturnsImmediately(t *testing.T) {
	bw := newBlockingWriter()
	defer bw.Close()
	tr := &transport{writer: protocol.NewWriter(bw)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- tr.Send(ctx, map[string]string{"type": "ping"})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send returned %v, want context.Canceled", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Send did not return promptly when ctx was already cancelled")
	}
}
