package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/rootinit"
)

// syncBuffer is a mutex-guarded log sink: the controller's serve goroutine
// writes to it while the test goroutine reads, so an unguarded bytes.Buffer
// would be a data race rather than a real finding.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestPprofController builds a controller logging into a guarded buffer,
// with unconditional teardown so no serve goroutine or bound socket outlives
// the test.
func newTestPprofController(t *testing.T) (*pprofController, *syncBuffer) {
	t.Helper()
	return newTestPprofControllerFor(t, "")
}

// newTestPprofControllerFor builds a controller with a preferred address, as
// runEnter does from the resolved --pprof/SPRAWL_PPROF_ADDR value.
func newTestPprofControllerFor(t *testing.T, preferred string) (*pprofController, *syncBuffer) {
	t.Helper()
	return newTestPprofControllerWith(t, pprofOptions{Preferred: preferred})
}

// newTestPprofControllerWith builds a controller from full options, with
// unconditional teardown so no serve goroutine or bound socket outlives the
// test.
func newTestPprofControllerWith(t *testing.T, opts pprofOptions) (*pprofController, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	c := newPprofController(buf, opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = c.Stop(ctx)
	})
	return c, buf
}

// occupyAddr binds a real ephemeral loopback socket and holds it for the whole
// test, returning the address it occupies. Tests use this instead of assuming a
// fixed port is taken: the occupancy is real, so the EADDRINUSE the controller
// sees comes from the kernel rather than from a fabricated error.
//
// It deliberately never closes-then-reuses the port number, which would race
// anything else on the host for it.
func occupyAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupying a loopback port: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// pprofListenFunc is the shape of the pprofListen seam.
type pprofListenFunc = func(network, addr string) (net.Listener, error)

// swapPprofListen installs a pprofListen seam built from the real one for the
// duration of the test. Every listen helper in this file is built on it so the
// save/restore discipline lives in exactly one place.
func swapPprofListen(t *testing.T, build func(orig pprofListenFunc) pprofListenFunc) {
	t.Helper()
	orig := pprofListen
	pprofListen = build(orig)
	t.Cleanup(func() { pprofListen = orig })
}

// flakyPprofListen installs a pprofListen seam that fails the first bind with
// firstErr and delegates every later bind to a real ephemeral loopback socket,
// so a test can pin exactly which addresses a retry asks for.
func flakyPprofListen(t *testing.T, firstErr error) *pprofListenRecorder {
	t.Helper()
	rec := &pprofListenRecorder{}
	swapPprofListen(t, func(orig pprofListenFunc) pprofListenFunc {
		return func(network, addr string) (net.Listener, error) {
			rec.record(addr)
			if rec.count() == 1 {
				return nil, firstErr
			}
			return orig(network, "127.0.0.1:0")
		}
	})
	return rec
}

// recordingPprofListen records every requested address and performs the REAL
// bind on that exact address. Unlike fakePprofListen it does not rewrite the
// address to an ephemeral port, so a test can prove both what was attempted and
// that a genuine kernel error came back.
func recordingPprofListen(t *testing.T) *pprofListenRecorder {
	t.Helper()
	rec := &pprofListenRecorder{}
	swapPprofListen(t, func(orig pprofListenFunc) pprofListenFunc {
		return func(network, addr string) (net.Listener, error) {
			rec.record(addr)
			return orig(network, addr)
		}
	})
	return rec
}

// stallPprofShutdown makes the graceful drain report err without draining, so a
// drain-timeout test is deterministic. A real http.Server.Shutdown cannot be
// driven to a timeout by a slow listener: listenerGroup.Wait() is not
// ctx-aware, and with no in-flight connections closeIdleConns() succeeds on the
// first loop iteration, so Shutdown returns nil however short the deadline is.
// Stop still closes the server and joins the serve goroutine on this path, so
// nothing leaks.
func stallPprofShutdown(t *testing.T, err error) {
	t.Helper()
	orig := pprofShutdown
	pprofShutdown = func(context.Context, *http.Server) error { return err }
	t.Cleanup(func() { pprofShutdown = orig })
}

// eaddrinuseErr builds the error shape net.Listen actually returns for an
// occupied port, so a test of the non-fallback path cannot pass by accident on
// a bare syscall.Errno the production code would never see.
func eaddrinuseErr() error {
	return &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: syscall.EADDRINUSE}}
}

// addrFileUnderNewDir returns a pprof-addr path whose parent directory does not
// exist yet, proving the controller creates it.
func addrFileUnderNewDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "runtime", "pprof-addr")
}

// readAddrFile returns the file's contents, failing the test if it is absent.
func readAddrFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("reading pprof addr file %s: %v", path, err)
	}
	return string(b)
}

// requireNoAddrFile asserts the addr file is absent.
func requireNoAddrFile(t *testing.T, path, when string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pprof addr file %s still exists %s (stat err = %v), want removed", path, when, err)
	}
}

// pprofListenRecorder records what addresses the controller asked to bind.
type pprofListenRecorder struct {
	mu    sync.Mutex
	addrs []string
}

func (r *pprofListenRecorder) requested() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.addrs...)
}

func (r *pprofListenRecorder) count() int {
	return len(r.requested())
}

func (r *pprofListenRecorder) record(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = append(r.addrs, addr)
}

// fakePprofListen installs a pprofListen seam that records every requested
// address and delegates to a real ephemeral loopback bind, so no test ever
// depends on a fixed port being free.
//
// pprofListen / pprofSignalNotify / pprofSignalStop are process globals that
// these helpers swap per test: never add t.Parallel() to a test in this file.
func fakePprofListen(t *testing.T) *pprofListenRecorder {
	t.Helper()
	rec := &pprofListenRecorder{}
	swapPprofListen(t, func(orig pprofListenFunc) pprofListenFunc {
		return func(network, addr string) (net.Listener, error) {
			rec.record(addr)
			return orig(network, "127.0.0.1:0")
		}
	})
	return rec
}

// failingPprofListen installs a pprofListen seam that always fails to bind.
func failingPprofListen(t *testing.T, err error) *pprofListenRecorder {
	t.Helper()
	rec := &pprofListenRecorder{}
	swapPprofListen(t, func(pprofListenFunc) pprofListenFunc {
		return func(_, addr string) (net.Listener, error) {
			rec.record(addr)
			return nil, err
		}
	})
	return rec
}

// badAcceptListener binds for real but fails every Accept, so http.Serve
// returns a non-ErrServerClosed error.
type badAcceptListener struct {
	net.Listener
	err error
}

func (b badAcceptListener) Accept() (net.Conn, error) { return nil, b.err }

// slowUnwindListener makes the serve goroutine's exit observably slow: Accept
// parks until Close, then lingers before returning, so a Stop that does not
// wait for the goroutine leaves a wide, reliably observable window. (The error
// value is irrelevant — net/http rewrites any post-Shutdown Accept error to
// http.ErrServerClosed.)
// accepting is closed once Accept has been entered. Tests must wait for it
// before calling Stop: http.Server.Serve returns ErrServerClosed straight out
// of trackListener if Shutdown already began, so a Stop that outruns the serve
// goroutine would skip the linger entirely and make the test vacuous.
type slowUnwindListener struct {
	net.Listener
	closed      chan struct{}
	accepting   chan struct{}
	closeOnce   sync.Once
	acceptsOnce sync.Once
}

func newSlowUnwindListener(ln net.Listener) *slowUnwindListener {
	return &slowUnwindListener{
		Listener:  ln,
		closed:    make(chan struct{}),
		accepting: make(chan struct{}),
	}
}

func (l *slowUnwindListener) Accept() (net.Conn, error) {
	l.acceptsOnce.Do(func() { close(l.accepting) })
	<-l.closed
	time.Sleep(100 * time.Millisecond)
	return nil, net.ErrClosed
}

// installSlowUnwindListen makes every pprof bind produce a slowUnwindListener
// on an ephemeral loopback port, and returns a func that blocks until the
// serve goroutine is parked in Accept.
func installSlowUnwindListen(t *testing.T) (waitAccepting func()) {
	t.Helper()
	var (
		mu  sync.Mutex
		cur *slowUnwindListener
	)
	swapPprofListen(t, func(orig pprofListenFunc) pprofListenFunc {
		return func(network, _ string) (net.Listener, error) {
			ln, err := orig(network, "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			slow := newSlowUnwindListener(ln)
			mu.Lock()
			cur = slow
			mu.Unlock()
			return slow, nil
		}
	})
	return func() {
		t.Helper()
		mu.Lock()
		slow := cur
		mu.Unlock()
		if slow == nil {
			t.Fatal("no listener was created")
		}
		select {
		case <-slow.accepting:
		case <-time.After(2 * time.Second):
			t.Fatal("serve goroutine never reached Accept")
		}
	}
}

func (l *slowUnwindListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

// mustGet issues a short-timeout GET against a bound pprof address.
func mustGet(t *testing.T, boundAddr, path string) int {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + boundAddr + path)
	if err != nil {
		t.Fatalf("GET %s%s: %v", boundAddr, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestResolvePprofAddr(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{"both empty", "", "", ""},
		{"only flag", "127.0.0.1:6060", "", "127.0.0.1:6060"},
		{"only env", "", "127.0.0.1:7070", "127.0.0.1:7070"},
		{"flag wins over env", "127.0.0.1:6060", "127.0.0.1:7070", "127.0.0.1:6060"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePprofAddr(tt.flag, tt.env)
			if got != tt.want {
				t.Errorf("resolvePprofAddr(%q, %q) = %q, want %q", tt.flag, tt.env, got, tt.want)
			}
		})
	}
}

// TestPprofToggleSignalIsSIGUSR2 pins the documented operator contract: every
// other signal in the repo is claimed (SIGUSR1 sigdump, SIGTERM/SIGHUP quit,
// SIGINT). Without this the constant could drift while the tests still pass.
func TestPprofToggleSignalIsSIGUSR2(t *testing.T) {
	if pprofToggleSignal != syscall.SIGUSR2 {
		t.Errorf("pprofToggleSignal = %v, want SIGUSR2", pprofToggleSignal)
	}
}

// TestPprofController_StartResolvesActualBoundAddr is the :0 requirement: the
// reported address must come from the listener, not from the request.
func TestPprofController_StartResolvesActualBoundAddr(t *testing.T) {
	c, _ := newTestPprofController(t)

	bound, already, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if already {
		t.Error("Start on a fresh controller reported already-running")
	}
	if bound == "127.0.0.1:0" || bound == "" {
		t.Fatalf("bound addr not resolved from listener: %q", bound)
	}
	host, port, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", bound, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("bound host = %q, want 127.0.0.1", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p == 0 {
		t.Errorf("bound port = %q, want a non-zero number", port)
	}
	if got := c.Status(); got != (pprofStatus{Addr: bound, Running: true}) {
		t.Errorf("Status() = %+v, want {%s true}", got, bound)
	}
}

func TestPprofController_ServesPprofIndexOnBoundAddr(t *testing.T) {
	c, _ := newTestPprofController(t)

	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if code := mustGet(t, bound, "/debug/pprof/"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ = %d, want 200", code)
	}
}

func TestPprofController_StartTwiceIsIdempotent(t *testing.T) {
	rec := fakePprofListen(t)
	c, _ := newTestPprofController(t)

	first, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	second, already, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if !already {
		t.Error("second Start should report already-running")
	}
	if second != first {
		t.Errorf("second Start addr = %q, want the running addr %q", second, first)
	}
	if got := rec.count(); got != 1 {
		t.Errorf("bind attempts = %d, want 1", got)
	}
}

// TestPprofController_StartWithDifferentAddrKeepsRunningListener pins the
// already-running precedence: a second Start never silently relocates the
// endpoint out from under whoever is scraping it.
func TestPprofController_StartWithDifferentAddrKeepsRunningListener(t *testing.T) {
	rec := fakePprofListen(t)
	c, _ := newTestPprofController(t)

	first, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	second, already, err := c.Start("127.0.0.1:7071")
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if !already {
		t.Error("Start with a different addr while running should report already-running")
	}
	if second != first {
		t.Errorf("Start with a different addr = %q, want the running addr %q", second, first)
	}
	if got := rec.count(); got != 1 {
		t.Errorf("bind attempts = %d, want 1 (no rebind)", got)
	}
}

func TestPprofController_StopReleasesSocket(t *testing.T) {
	c, _ := newTestPprofController(t)

	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	was, err := c.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !was {
		t.Error("Stop on a running controller should report was-running")
	}
	if got := c.Status(); got != (pprofStatus{}) {
		t.Errorf("Status() after Stop = %+v, want zero value", got)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://" + bound + "/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Errorf("GET on %s still succeeded after Stop; socket not released", bound)
	}
}

// TestPprofController_StopWaitsForServeGoroutineExit pins the requirement that
// Stop does not report stopped until the serve goroutine has actually
// returned — otherwise a stop→start cycle on a fixed port can race into
// "address already in use". Shutdown closes the listener before it returns, so
// a plain port-rebind test would pass without the wait; the done channel is
// closed by the serve goroutine itself, and slowUnwindListener's 100ms linger
// makes an early-returning Stop reliably observable. The waitAccepting()
// barrier is load-bearing: without it Stop can outrun the serve goroutine, the
// linger never happens, and the test passes vacuously.
func TestPprofController_StopWaitsForServeGoroutineExit(t *testing.T) {
	waitAccepting := installSlowUnwindListen(t)

	c, _ := newTestPprofController(t)
	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitAccepting()
	done := c.serveDone()
	if done == nil {
		t.Fatal("serveDone() = nil while running; want the run's done channel")
	}
	select {
	case <-done:
		t.Fatal("serve goroutine already exited while the controller is running")
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-done:
	default:
		t.Error("Stop returned before the serve goroutine's done channel closed")
	}
}

func TestPprofController_StopWhenNotRunningIsNoop(t *testing.T) {
	fakePprofListen(t)
	c, buf := newTestPprofController(t)
	ctx := context.Background()

	was, err := c.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop on fresh controller: %v", err)
	}
	if was {
		t.Error("Stop on a fresh controller should report was-running=false")
	}

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	was, err = c.Stop(ctx)
	if err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if was {
		t.Error("second Stop should report was-running=false")
	}
	// Stop waits for the serve goroutine, so http.ErrServerClosed — the
	// normal stop path — must not have produced a scary log line.
	if out := buf.String(); out != "" {
		t.Errorf("clean start/stop should log nothing (ErrServerClosed is not a failure), got %q", out)
	}
}

// TestPprofController_LogsRealServeError is the counterpart to the silence
// above: a serve error that is NOT ErrServerClosed must be reported.
func TestPprofController_LogsRealServeError(t *testing.T) {
	acceptErr := errors.New("accept exploded")
	orig := pprofListen
	pprofListen = func(network, _ string) (net.Listener, error) {
		ln, err := orig(network, "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		return badAcceptListener{Listener: ln, err: acceptErr}, nil
	}
	t.Cleanup(func() { pprofListen = orig })

	c, buf := newTestPprofController(t)
	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the serve error to be logged", func() bool {
		return strings.Contains(buf.String(), "accept exploded")
	})
}

// TestPprofController_RestartAfterStopBindsFreshServer guards the single-use
// *http.Server trap: reusing the server after Shutdown makes Serve return
// ErrServerClosed immediately and the restarted endpoint answers nothing.
func TestPprofController_RestartAfterStopBindsFreshServer(t *testing.T) {
	c, _ := newTestPprofController(t)
	ctx := context.Background()

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	bound, already, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("restart Start: %v", err)
	}
	if already {
		t.Error("restart after Stop should not report already-running")
	}
	if bound == "" {
		t.Fatal("restart returned an empty bound addr")
	}
	if code := mustGet(t, bound, "/debug/pprof/"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ after restart = %d, want 200", code)
	}
}

func TestPprofController_StartBindErrorLeavesControllerStopped(t *testing.T) {
	bindErr := errors.New("bind refused")
	failingPprofListen(t, bindErr)
	c, _ := newTestPprofController(t)

	if _, _, err := c.Start("127.0.0.1:0"); !errors.Is(err, bindErr) {
		t.Fatalf("Start error = %v, want %v", err, bindErr)
	}
	if got := c.Status(); got.Running {
		t.Errorf("Status() after failed Start = %+v, want not running", got)
	}
}

func TestPprofController_DefaultsToLoopbackWhenAddrEmpty(t *testing.T) {
	rec := fakePprofListen(t)
	c, _ := newTestPprofController(t)

	if _, _, err := c.Start(""); err != nil {
		t.Fatalf("Start(\"\"): %v", err)
	}
	requested := rec.requested()
	if len(requested) != 1 {
		t.Fatalf("bind requests = %v, want exactly one", requested)
	}
	if requested[0] != defaultPprofAddr {
		t.Errorf("Start(\"\") bound %q, want the default %q", requested[0], defaultPprofAddr)
	}
	host, _, err := net.SplitHostPort(defaultPprofAddr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", defaultPprofAddr, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("defaultPprofAddr host = %q, want loopback 127.0.0.1", host)
	}
}

// --- QUM-934 follow-up: explicit-vs-default address provenance ---------------

// TestPprofController_ResolveTargetProvenance pins the whole point of the
// pprofTarget type: an ephemeral fallback is permitted ONLY when nobody named an
// address and the controller fell back to its own default. Every other row is a
// promise to an operator who named a port.
func TestPprofController_ResolveTargetProvenance(t *testing.T) {
	tests := []struct {
		name         string
		explicit     string
		preferred    string
		wantAddr     string
		wantFallback bool
	}{
		{
			name:     "explicit only",
			explicit: "127.0.0.1:7001",
			wantAddr: "127.0.0.1:7001",
		},
		{
			name:      "preferred only",
			preferred: "127.0.0.1:7002",
			wantAddr:  "127.0.0.1:7002",
		},
		{
			name:      "explicit beats preferred",
			explicit:  "127.0.0.1:7003",
			preferred: "127.0.0.1:7004",
			wantAddr:  "127.0.0.1:7003",
		},
		{
			// The default addr is the one printed in --pprof's own help text,
			// so an operator naming it explicitly is the likeliest collision.
			// Provenance, not the address value, decides the policy.
			name:     "explicitly naming our default is still explicit",
			explicit: defaultPprofAddr,
			wantAddr: defaultPprofAddr,
		},
		{
			name:      "configuring our default is still explicit",
			preferred: defaultPprofAddr,
			wantAddr:  defaultPprofAddr,
		},
		{
			name:         "neither configured falls back to our own default",
			wantAddr:     defaultPprofAddr,
			wantFallback: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestPprofControllerFor(t, tc.preferred)
			got := c.resolveTarget(tc.explicit)
			if got.addr != tc.wantAddr {
				t.Errorf("resolveTarget(%q).addr = %q, want %q", tc.explicit, got.addr, tc.wantAddr)
			}
			if got.allowEphemeralFallback != tc.wantFallback {
				t.Errorf("resolveTarget(%q).allowEphemeralFallback = %v, want %v",
					tc.explicit, got.allowEphemeralFallback, tc.wantFallback)
			}
		})
	}
}

// TestPprofController_DefaultAddrOccupiedFallsBackToEphemeral is the scenario
// QUM-934 B exists for: a live session that was NOT launched with --pprof, on a
// host where our own default port is already taken by something unrelated.
// Nobody asked for that port, so a bind failure must relocate rather than
// dead-end. Uses a really-held socket and a real kernel EADDRINUSE.
func TestPprofController_DefaultAddrOccupiedFallsBackToEphemeral(t *testing.T) {
	occupied := occupyAddr(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{DefaultAddrForTest: occupied})

	bound, already, err := c.Start("")
	if err != nil {
		t.Fatalf("Start(\"\") with an occupied default: %v, want a fallback bind", err)
	}
	if already {
		t.Error("Start reported already-running on a fresh controller")
	}
	if bound == occupied {
		t.Fatalf("Start bound the occupied address %q", bound)
	}
	host, port, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", bound, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("fallback bound host = %q, want loopback only", host)
	}
	if p, convErr := strconv.Atoi(port); convErr != nil || p == 0 {
		t.Errorf("fallback bound port = %q, want a real kernel-assigned port", port)
	}
	if got := c.Status(); !got.Running || got.Addr != bound {
		t.Errorf("Status() = %+v, want running on the fallback addr %q", got, bound)
	}
	// Reporting a port is worthless if nothing is served there.
	if code := mustGet(t, bound, "/debug/pprof/"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ on the fallback addr = %d, want 200", code)
	}
}

// TestPprofController_ExplicitAddrOccupiedFailsAndDoesNotFallBack is the key
// regression guard. An operator who passed an address will curl that address, so
// a silent relocation is worse than a loud failure. This must never become a
// fallback.
func TestPprofController_ExplicitAddrOccupiedFailsAndDoesNotFallBack(t *testing.T) {
	occupied := occupyAddr(t)
	rec := recordingPprofListen(t)
	c, _ := newTestPprofController(t)

	bound, already, err := c.Start(occupied)
	if err == nil {
		t.Fatalf("Start(%q) on an occupied explicit address succeeded (bound %q), want a loud failure", occupied, bound)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Start error = %v, want it to wrap syscall.EADDRINUSE", err)
	}
	if already {
		t.Error("Start reported already-running after a bind failure")
	}
	if got := c.Status(); got.Running {
		t.Errorf("Status() after a failed explicit bind = %+v, want not running", got)
	}
	if c.serveDone() != nil {
		t.Error("a failed explicit bind left a run behind")
	}
	// The point of the test: exactly one attempt, on the named address. A
	// second attempt would mean a silent relocation.
	if got := rec.requested(); len(got) != 1 || got[0] != occupied {
		t.Errorf("bind requests = %v, want exactly [%s] with no fallback attempt", got, occupied)
	}
}

// TestPprofController_PreferredAddrOccupiedFailsAndDoesNotFallBack covers the
// other half of "explicitly configured": the launch flag. Without this test an
// implementation keyed only on `addr == ""` passes the explicit-arg guard above
// and still silently relocates a --pprof/SPRAWL_PPROF_ADDR session.
func TestPprofController_PreferredAddrOccupiedFailsAndDoesNotFallBack(t *testing.T) {
	occupied := occupyAddr(t)
	rec := recordingPprofListen(t)
	c, _ := newTestPprofControllerFor(t, occupied)

	bound, _, err := c.Start("")
	if err == nil {
		t.Fatalf("Start(\"\") with an occupied configured addr succeeded (bound %q), want a loud failure", bound)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Start error = %v, want it to wrap syscall.EADDRINUSE", err)
	}
	if got := c.Status(); got.Running {
		t.Errorf("Status() = %+v, want not running", got)
	}
	if got := rec.requested(); len(got) != 1 || got[0] != occupied {
		t.Errorf("bind requests = %v, want exactly [%s] with no fallback attempt", got, occupied)
	}
}

// TestPprofController_ExplicitDefaultAddrOccupiedFailsAndDoesNotFallBack: the
// policy keys on PROVENANCE, not on the address value. Naming our own default
// explicitly is still a promise, so it must fail loudly even though the very
// same address would have been relocated had nobody named it. This is the pair
// to TestPprofController_DefaultAddrOccupiedFallsBackToEphemeral: same occupied
// address, opposite outcome, and explicitness is the only differing input.
func TestPprofController_ExplicitDefaultAddrOccupiedFailsAndDoesNotFallBack(t *testing.T) {
	occupied := occupyAddr(t)
	rec := recordingPprofListen(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{DefaultAddrForTest: occupied})

	if _, _, err := c.Start(occupied); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Start(%q) = %v, want a loud EADDRINUSE failure", occupied, err)
	}
	if got := c.Status(); got.Running {
		t.Errorf("Status() = %+v, want not running", got)
	}
	if got := rec.requested(); len(got) != 1 || got[0] != occupied {
		t.Errorf("bind requests = %v, want exactly [%s]", got, occupied)
	}
}

// TestPprofController_NonEADDRINUSEBindErrorDoesNotFallBack: only an occupied
// port is a relocatable condition. EACCES/EADDRNOTAVAIL mean the configuration
// or the environment is wrong, and relocating would hide the diagnosis.
func TestPprofController_NonEADDRINUSEBindErrorDoesNotFallBack(t *testing.T) {
	permErr := &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: syscall.EACCES}}
	rec := failingPprofListen(t, permErr)
	c, _ := newTestPprofController(t)

	if _, _, err := c.Start(""); err == nil {
		t.Fatal("Start(\"\") succeeded despite a permission error")
	}
	if got := rec.count(); got != 1 {
		t.Errorf("bind attempts = %d, want exactly 1 (no retry on a non-EADDRINUSE error)", got)
	}
}

// TestPprofController_EphemeralFallbackTargetsLoopbackZero pins the retry target
// itself: the fallback must be loopback port 0, never a wildcard host.
func TestPprofController_EphemeralFallbackTargetsLoopbackZero(t *testing.T) {
	rec := flakyPprofListen(t, eaddrinuseErr())
	c, _ := newTestPprofControllerWith(t, pprofOptions{DefaultAddrForTest: "127.0.0.1:7005"})

	if _, _, err := c.Start(""); err != nil {
		t.Fatalf("Start(\"\"): %v", err)
	}
	want := []string{"127.0.0.1:7005", pprofEphemeralAddr}
	got := rec.requested()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("bind requests = %v, want %v", got, want)
	}
	host, port, err := net.SplitHostPort(pprofEphemeralAddr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", pprofEphemeralAddr, err)
	}
	if host != "127.0.0.1" || port != "0" {
		t.Errorf("pprofEphemeralAddr = %q, want loopback with port 0", pprofEphemeralAddr)
	}
}

// TestPprofController_FallbackFailureStillNamesRequestedAddr: when the default
// is occupied AND the ephemeral fallback also fails, the operator must still be
// able to tell what was originally attempted. Reporting only "127.0.0.1:0" hides
// the real request.
func TestPprofController_FallbackFailureStillNamesRequestedAddr(t *testing.T) {
	const wanted = "127.0.0.1:7006"
	rec := failingPprofListen(t, eaddrinuseErr())
	c, _ := newTestPprofControllerWith(t, pprofOptions{DefaultAddrForTest: wanted})

	_, _, err := c.Start("")
	if err == nil {
		t.Fatal("Start(\"\") succeeded although every bind failed")
	}
	if !strings.Contains(err.Error(), wanted) {
		t.Errorf("error = %q, want it to name the originally requested %q", err, wanted)
	}
	if got := rec.count(); got != 2 {
		t.Errorf("bind attempts = %d, want 2 (default then ephemeral fallback)", got)
	}
}

// --- QUM-934 follow-up: addr-file discoverability ----------------------------

// TestPprofController_WritesAddrFileOnStartRemovesOnStop: the toggle's log line
// only reaches .sprawl/logs/tui-stderr-*.log, so the bound address must also
// land somewhere an operator (or a future TUI reader) can find without knowing
// the log exists.
func TestPprofController_WritesAddrFileOnStartRemovesOnStop(t *testing.T) {
	path := addrFileUnderNewDir(t)
	c, buf := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	requireNoAddrFile(t, path, "before Start")
	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := readAddrFile(t, path), bound+"\n"; got != want {
		t.Errorf("addr file = %q, want %q", got, want)
	}

	if _, err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	requireNoAddrFile(t, path, "after Stop")
	// A successful start/stop advertises via the file, not via chatter — and a
	// silent happy path is what gives the failure-path log assertions meaning.
	if got := buf.String(); got != "" {
		t.Errorf("log output = %q, want empty on a clean start/stop", got)
	}
}

// TestPprofController_StartOverwritesStaleAddrFile: a SIGKILLed session leaves
// the file behind holding a dead port. Because the write is deliberately
// best-effort, an implementation that refuses to clobber it (O_EXCL, or treating
// the file as "already advertised") would fail SILENTLY and send the operator to
// last session's port.
func TestPprofController_StartOverwritesStaleAddrFile(t *testing.T) {
	path := addrFileUnderNewDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("seeding stale addr dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("127.0.0.1:9999\n"), 0o600); err != nil {
		t.Fatalf("seeding stale addr file: %v", err)
	}
	c, _ := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := readAddrFile(t, path), bound+"\n"; got != want {
		t.Errorf("addr file = %q, want the stale contents replaced by %q", got, want)
	}
}

// TestPprofController_AddrFileUsesRestrictivePerms pins the perms called out as
// the security property; without this a widening to 0o644/0o777 is invisible.
func TestPprofController_AddrFileUsesRestrictivePerms(t *testing.T) {
	path := addrFileUnderNewDir(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat addr file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("addr file mode = %#o, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat addr dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o750 {
		t.Errorf("addr dir mode = %#o, want 0750", got)
	}
}

// TestPprofController_AlreadyRunningStartDoesNotRewriteAddrFile pins the
// invariant Start's own comment claims: the file's lifetime is exactly one
// pprofRun's lifetime, so a second Start must leave the advertisement of the
// listener that is actually running alone.
func TestPprofController_AlreadyRunningStartDoesNotRewriteAddrFile(t *testing.T) {
	path := addrFileUnderNewDir(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, already, err := c.Start("127.0.0.1:65000"); err != nil || !already {
		t.Fatalf("second Start = (already=%v, err=%v), want already with no error", already, err)
	}
	if got, want := readAddrFile(t, path), bound+"\n"; got != want {
		t.Errorf("addr file = %q, want the running listener's addr %q", got, want)
	}
}

// TestPprofController_AddrFileHoldsEphemeralFallbackPort is why the two fixes
// are coupled: once a bind can relocate to a kernel-assigned port, an
// undiscoverable address is useless. The file must hold the ACTUAL port.
func TestPprofController_AddrFileHoldsEphemeralFallbackPort(t *testing.T) {
	occupied := occupyAddr(t)
	path := addrFileUnderNewDir(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{DefaultAddrForTest: occupied, AddrFile: path})

	bound, _, err := c.Start("")
	if err != nil {
		t.Fatalf("Start(\"\") with an occupied default: %v", err)
	}
	got := strings.TrimSpace(readAddrFile(t, path))
	if got == occupied {
		t.Fatalf("addr file holds the occupied default %q, not the port actually bound", got)
	}
	if got != bound {
		t.Errorf("addr file = %q, want the real bound fallback addr %q", got, bound)
	}
	// The file is only useful if it can be used directly.
	if code := mustGet(t, got, "/debug/pprof/"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ at the advertised addr = %d, want 200", code)
	}
}

// TestPprofController_AddrFileDisabledWhenPathEmpty keeps the controller usable
// with no sprawl root, and silent when it has nowhere to write.
func TestPprofController_AddrFileDisabledWhenPathEmpty(t *testing.T) {
	// Chdir so a relative-path write would land somewhere observable rather
	// than polluting the repo during `make test`.
	dir := t.TempDir()
	t.Chdir(dir)
	c, buf := newTestPprofControllerWith(t, pprofOptions{})

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("log output = %q, want empty when no addr file is configured", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading cwd: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("cwd contains %d entries, want none written when no addr file is configured", len(entries))
	}
}

// TestPprofController_AddrFileWriteFailureIsNotFatal: the addr file is a
// convenience. A diagnostic endpoint must never fail to come up because it could
// not advertise itself.
func TestPprofController_AddrFileWriteFailureIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seeding blocker file: %v", err)
	}
	// Parent is a regular file, so MkdirAll fails deterministically.
	path := filepath.Join(blocker, "pprof-addr")
	c, buf := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start must succeed even when the addr file cannot be written: %v", err)
	}
	if code := mustGet(t, bound, "/debug/pprof/"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/ = %d, want 200", code)
	}
	// "pprof" alone would match every line this component can emit; the path
	// and the cause are what make the log actionable.
	logged := buf.String()
	if !strings.Contains(logged, path) {
		t.Errorf("log = %q, want it to name the addr file path %q", logged, path)
	}
	if !strings.Contains(logged, "not a directory") {
		t.Errorf("log = %q, want it to carry the underlying cause", logged)
	}
	if _, err := c.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestPprofController_StopRemovesAddrFileEvenWhenDrainFails: the file must
// mirror Status(). Once Stop has published "not running", an addr file still
// advertising the endpoint is a lie.
func TestPprofController_StopRemovesAddrFileEvenWhenDrainFails(t *testing.T) {
	stallPprofShutdown(t, context.DeadlineExceeded)
	path := addrFileUnderNewDir(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	readAddrFile(t, path) // present while running

	was, err := c.Stop(context.Background())
	if !was {
		t.Error("Stop reported nothing was running")
	}
	if err == nil {
		t.Error("Stop should report the degraded drain")
	}
	requireNoAddrFile(t, path, "after a failed drain")
}

// TestPprofController_StopRemovesAddrFileBeforeDraining: the file must mirror
// Status() throughout, including the up-to-2s draining window in which Status()
// already reads not-running and a concurrent Start gets errPprofStopping. A file
// removed only after the drain would advertise a called-down endpoint.
func TestPprofController_StopRemovesAddrFileBeforeDraining(t *testing.T) {
	waitAccepting := installSlowUnwindListen(t)
	path := addrFileUnderNewDir(t)
	c, _ := newTestPprofControllerWith(t, pprofOptions{AddrFile: path})

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitAccepting()
	readAddrFile(t, path) // present while running

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.Stop(ctx)
	}()

	// The slow listener holds Stop on the serve goroutine, so the window where
	// the run is cleared but the socket is still held is observable.
	waitFor(t, "Stop to clear the running state", func() bool {
		return c.serveDone() == nil
	})
	requireNoAddrFile(t, path, "during the draining window")
	<-stopped
}

// --- QUM-934 follow-up: honest drain-timeout reporting ----------------------

// TestPprofController_StopDrainTimeoutReportsDrainIncomplete: a drain that
// outlasts the timeout is a successful stop with a degraded drain, not a failed
// stop. The cause must survive alongside the sentinel so the log line can name
// it.
func TestPprofController_StopDrainTimeoutReportsDrainIncomplete(t *testing.T) {
	stallPprofShutdown(t, context.DeadlineExceeded)
	c, _ := newTestPprofController(t)

	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	was, err := c.Stop(context.Background())
	if !was {
		t.Error("Stop reported nothing was running")
	}
	if !errors.Is(err, errPprofDrainIncomplete) {
		t.Errorf("Stop error = %v, want it to wrap errPprofDrainIncomplete", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop error = %v, want the underlying cause preserved", err)
	}
	if got := c.Status(); got.Running {
		t.Errorf("Status() after a timed-out drain = %+v, want stopped (the listener IS down)", got)
	}
}

// TestPprofToggleLogLine covers the four things a SIGUSR2 toggle can report.
// The drain-timeout row is the fix: the listener did stop, so telling the user
// the toggle "failed" is false.
func TestPprofToggleLogLine(t *testing.T) {
	drainErr := fmt.Errorf("draining pprof listener on 127.0.0.1:6060: %w: %w",
		errPprofDrainIncomplete, context.DeadlineExceeded)

	// The rows are non-vacuous as a SET: rows 1-2 forbid "failed" while row 4
	// requires it, so no single constant string can satisfy all four. Row 3
	// forbids only the specific phrase "toggle failed" rather than "failed",
	// because it may legitimately say the drain failed to complete — what it
	// must not claim is that the TOGGLE failed. Row 2 forbids "drain" so a
	// degraded stop cannot render identically to a clean one.
	tests := []struct {
		name       string
		st         pprofStatus
		err        error
		wantSubstr []string
		wantAbsent []string
	}{
		{
			name:       "toggled on reports the bound address",
			st:         pprofStatus{Addr: "127.0.0.1:6061", Running: true},
			wantSubstr: []string{"ON", "127.0.0.1:6061"},
			wantAbsent: []string{"failed", "drain"},
		},
		{
			name:       "toggled off",
			wantSubstr: []string{"OFF"},
			wantAbsent: []string{"failed", "drain"},
		},
		{
			name:       "drain timeout is an off, not a failure",
			err:        drainErr,
			wantSubstr: []string{"OFF", "drain"},
			wantAbsent: []string{"toggle failed"},
		},
		{
			name: "a real failure still reports failure with its cause",
			err:  fmt.Errorf("binding pprof listener on 127.0.0.1:6060: %w", eaddrinuseErr()),
			// The cause is the whole diagnostic value; "pprof toggle failed"
			// on its own leaves the operator nothing to act on.
			wantSubstr: []string{"failed", "address already in use"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pprofToggleLogLine(tc.st, tc.err)
			if strings.Contains(got, "\n") {
				t.Errorf("pprofToggleLogLine = %q, want a single line with no newline", got)
			}
			for _, want := range tc.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("pprofToggleLogLine = %q, want it to contain %q", got, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("pprofToggleLogLine = %q, want it NOT to contain %q", got, absent)
				}
			}
		})
	}
}

// TestPprofSignalLoop_DrainTimeoutReportsOffNotFailure closes the gap between
// pprofToggleLogLine and its only caller: an implementation that adds the
// classifier but leaves the loop's own `err != nil -> "toggle failed"` branch in
// place would pass every unit test while the operator still reads "toggle
// failed" on a drain that did in fact stop the listener.
func TestPprofSignalLoop_DrainTimeoutReportsOffNotFailure(t *testing.T) {
	stallPprofShutdown(t, context.DeadlineExceeded)
	c, buf := newTestPprofController(t)
	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan os.Signal, 1)
	toggled := make(chan pprofStatus, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pprofSignalLoop(ctx, ch, c, func(st pprofStatus) { toggled <- st })
	}()

	ch <- pprofToggleSignal
	select {
	case st := <-toggled:
		if st.Running {
			t.Errorf("status after the toggle = %+v, want stopped", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the signal loop never processed the toggle")
	}
	cancel()
	<-done

	logged := buf.String()
	if strings.Contains(logged, "toggle failed") {
		t.Errorf("log = %q, want a degraded-drain OFF rather than a claimed toggle failure", logged)
	}
	if !strings.Contains(logged, "OFF") {
		t.Errorf("log = %q, want it to report the listener went OFF", logged)
	}
}

// canaryOnce registers a DefaultServeMux route exactly once per process:
// http.HandleFunc panics on a duplicate pattern, which would break -count=2.
var canaryOnce = sync.OnceFunc(func() {
	http.HandleFunc("/sprawl-default-mux-canary", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
})

// TestPprofController_DoesNotServeDefaultServeMux guards the exposure footgun:
// the listener must serve only the pprof handlers off a dedicated mux, not
// whatever any other package registered on http.DefaultServeMux.
func TestPprofController_DoesNotServeDefaultServeMux(t *testing.T) {
	canaryOnce()

	c, _ := newTestPprofController(t)
	bound, _, err := c.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if code := mustGet(t, bound, "/sprawl-default-mux-canary"); code != http.StatusNotFound {
		t.Errorf("DefaultServeMux route reachable on the pprof listener (got %d, want 404)", code)
	}
	if code := mustGet(t, bound, "/debug/pprof/goroutine?debug=1"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/goroutine = %d, want 200", code)
	}
	if code := mustGet(t, bound, "/debug/pprof/cmdline"); code != http.StatusOK {
		t.Errorf("GET /debug/pprof/cmdline = %d, want 200", code)
	}
}

// TestPprofController_PrefersConfiguredAddrOverDefault: a session launched
// with --pprof/SPRAWL_PPROF_ADDR must come back on THAT address after a
// toggle off/on, not silently relocate to the well-known default.
func TestPprofController_PrefersConfiguredAddrOverDefault(t *testing.T) {
	rec := fakePprofListen(t)
	c, _ := newTestPprofControllerFor(t, "127.0.0.1:7777")
	ctx := context.Background()

	if _, _, err := c.Start(""); err != nil {
		t.Fatalf("Start(\"\"): %v", err)
	}
	if _, err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := c.Toggle(ctx, ""); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	want := []string{"127.0.0.1:7777", "127.0.0.1:7777"}
	got := rec.requested()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("bind requests = %v, want %v (the configured addr, not %s)", got, want, defaultPprofAddr)
	}
}

// TestPprofController_ExplicitAddrOverridesPreferred keeps the explicit-addr
// parameter meaningful for a future TUI surface.
func TestPprofController_ExplicitAddrOverridesPreferred(t *testing.T) {
	rec := fakePprofListen(t)
	c, _ := newTestPprofControllerFor(t, "127.0.0.1:7777")

	if _, _, err := c.Start("127.0.0.1:7778"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := rec.requested(); len(got) != 1 || got[0] != "127.0.0.1:7778" {
		t.Errorf("bind requests = %v, want [127.0.0.1:7778]", got)
	}
}

// TestPprofController_StartDuringStopFailsClearly: Stop clears the running
// state before the socket is actually released, so a concurrent Start must be
// told the listener is shutting down rather than blindly re-binding the same
// address and failing with a bare EADDRINUSE.
func TestPprofController_StartDuringStopFailsClearly(t *testing.T) {
	waitAccepting := installSlowUnwindListen(t)

	c, _ := newTestPprofController(t)
	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitAccepting()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.Stop(ctx)
	}()

	// The slow listener keeps Stop parked on the serve goroutine long enough
	// to observe the window where the run is cleared but the port is held.
	waitFor(t, "Stop to clear the running state", func() bool {
		return c.serveDone() == nil
	})
	if _, _, err := c.Start("127.0.0.1:0"); !errors.Is(err, errPprofStopping) {
		t.Errorf("Start during Stop = %v, want errPprofStopping", err)
	}

	<-stopped
	// Once Stop has finished, starting works again.
	if _, _, err := c.Start("127.0.0.1:0"); err != nil {
		t.Errorf("Start after Stop completed: %v", err)
	}
}

func TestPprofController_ToggleFlipsOnThenOff(t *testing.T) {
	fakePprofListen(t)
	c, _ := newTestPprofController(t)
	ctx := context.Background()

	on, err := c.Toggle(ctx, "")
	if err != nil {
		t.Fatalf("Toggle on: %v", err)
	}
	if !on.Running || on.Addr == "" {
		t.Fatalf("first Toggle = %+v, want running with an addr", on)
	}

	off, err := c.Toggle(ctx, "")
	if err != nil {
		t.Fatalf("Toggle off: %v", err)
	}
	if off != (pprofStatus{}) {
		t.Errorf("second Toggle = %+v, want zero value", off)
	}

	again, err := c.Toggle(ctx, "")
	if err != nil {
		t.Fatalf("Toggle on again: %v", err)
	}
	if !again.Running || again.Addr == "" {
		t.Errorf("third Toggle = %+v, want running with an addr", again)
	}
}

// TestPprofController_ConcurrentStartStopIsSafe covers mutex safety only:
// fakePprofListen rewrites every bind to an ephemeral port, so it cannot
// exercise port reuse. TestPprofController_StartDuringStopFailsClearly covers
// the stop/start window.
func TestPprofController_ConcurrentStartStopIsSafe(t *testing.T) {
	fakePprofListen(t)
	c, _ := newTestPprofController(t)
	ctx := context.Background()

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		// errPprofStopping is a legitimate outcome of racing a Stop.
		if err != nil && !errors.Is(err, errPprofStopping) {
			errs = append(errs, err)
		}
	}
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, err := c.Start("127.0.0.1:0")
			record(err)
		}()
		go func() {
			defer wg.Done()
			_, err := c.Stop(ctx)
			record(err)
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Errorf("concurrent Start/Stop produced errors: %v", errs)
	}
	if _, err := c.Stop(ctx); err != nil {
		t.Fatalf("final Stop: %v", err)
	}
	if got := c.Status(); got.Running {
		t.Errorf("Status() after final Stop = %+v, want not running", got)
	}
}

// TestPprofSignalLoop_TogglesOnEachSignal drives the signal-handler contract
// through an injected channel — never a real process-wide signal.
func TestPprofSignalLoop_TogglesOnEachSignal(t *testing.T) {
	fakePprofListen(t)
	c, buf := newTestPprofController(t)
	ch := make(chan os.Signal, 2)
	toggled := make(chan pprofStatus, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		pprofSignalLoop(ctx, ch, c, func(st pprofStatus) { toggled <- st })
	}()

	ch <- pprofToggleSignal
	on := <-toggled
	if !on.Running || on.Addr == "" {
		t.Fatalf("after first signal: %+v, want running with an addr", on)
	}
	waitFor(t, "the toggle-on log to name the bound addr", func() bool {
		return strings.Contains(buf.String(), on.Addr)
	})

	ch <- pprofToggleSignal
	off := <-toggled
	if off.Running {
		t.Errorf("after second signal: %+v, want not running", off)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pprofSignalLoop did not exit after ctx cancel")
	}
}

func TestStartPprof_NoopWhenEmpty(t *testing.T) {
	rec := fakePprofListen(t)
	c, buf := newTestPprofController(t)

	startPprof("", c)

	if got := rec.count(); got != 0 {
		t.Errorf("bind attempts = %d, want 0 when addr is empty", got)
	}
	if out := buf.String(); out != "" {
		t.Errorf("expected no log output when addr empty, got %q", out)
	}
}

func TestStartPprof_StartsAndLogsWhenAddrSet(t *testing.T) {
	c, buf := newTestPprofController(t)

	startPprof("127.0.0.1:0", c)

	st := c.Status()
	if !st.Running || st.Addr == "" {
		t.Fatalf("Status() = %+v, want running with an addr", st)
	}
	out := buf.String()
	if !strings.Contains(out, "pprof listening on http://") {
		t.Errorf("startup log missing the QUM-678 line; got: %q", out)
	}
	if !strings.Contains(out, st.Addr) {
		t.Errorf("startup log missing bound address %q; got: %q", st.Addr, out)
	}
	if !strings.Contains(out, "/debug/pprof/") {
		t.Errorf("startup log missing the pprof path; got: %q", out)
	}
}

func TestStartPprof_BindFailureLogsAndDoesNotStart(t *testing.T) {
	failingPprofListen(t, errors.New("bind refused"))
	c, buf := newTestPprofController(t)

	startPprof("127.0.0.1:6060", c)

	if got := c.Status(); got.Running {
		t.Errorf("Status() = %+v, want not running after a bind failure", got)
	}
	if out := buf.String(); !strings.Contains(out, "bind refused") {
		t.Errorf("bind failure should be logged; got: %q", out)
	}
}

// signalNotifyRecorder captures the channel and signal set handed to
// signal.Notify, so a test can deliver a signal on the SAME channel the
// handler goroutine is reading — proving the wiring, not just the bookkeeping.
type signalNotifyRecorder struct {
	mu      sync.Mutex
	sigs    []os.Signal
	ch      chan<- os.Signal
	stopped bool
}

func (r *signalNotifyRecorder) notify(ch chan<- os.Signal, sigs ...os.Signal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ch = ch
	r.sigs = append(r.sigs, sigs...)
}

func (r *signalNotifyRecorder) stop(chan<- os.Signal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
}

func (r *signalNotifyRecorder) channel() chan<- os.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ch
}

func (r *signalNotifyRecorder) signals() []os.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]os.Signal(nil), r.sigs...)
}

func (r *signalNotifyRecorder) didStop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// fakePprofSignalNotify swaps the signal.Notify/Stop seams for a recorder so a
// unit test never installs a real process-wide SIGUSR2 handler.
func fakePprofSignalNotify(t *testing.T) *signalNotifyRecorder {
	t.Helper()
	rec := &signalNotifyRecorder{}
	origNotify, origStop := pprofSignalNotify, pprofSignalStop
	pprofSignalNotify = rec.notify
	pprofSignalStop = rec.stop
	t.Cleanup(func() { pprofSignalNotify, pprofSignalStop = origNotify, origStop })
	return rec
}

// stopPprofOnCleanup guarantees a listener runEnter created cannot leak into
// the rest of the package run: runEnter's post-runProgram teardown is skipped
// entirely if a t.Fatalf inside runProgram calls runtime.Goexit.
func stopPprofOnCleanup(t *testing.T, deps *enterDeps) {
	t.Helper()
	t.Cleanup(func() {
		if deps.pprofCtl == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = deps.pprofCtl.Stop(ctx)
	})
}

// TestEnter_NoPprofListenerWhenUnset verifies that runEnter does NOT start
// a pprof listener when neither --pprof nor SPRAWL_PPROF_ADDR is set. This
// is the default behavior the issue requires (QUM-678 acceptance criterion).
func TestEnter_NoPprofListenerWhenUnset(t *testing.T) {
	rec := fakePprofListen(t)
	fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	deps := &enterDeps{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return tmpDir, nil },
		runProgram: func(tea.Model, func(func(tea.Msg))) error { return nil },
		newSession: nil,
		// pprofAddr left empty intentionally
	}
	stopPprofOnCleanup(t, deps)
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("bind attempts = %d, want 0 when --pprof/SPRAWL_PPROF_ADDR are unset", got)
	}
}

// TestEnter_AutoStartsPprofWhenAddrSet is the QUM-678 launch-time path.
func TestEnter_AutoStartsPprofWhenAddrSet(t *testing.T) {
	rec := fakePprofListen(t)
	fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	deps := &enterDeps{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return tmpDir, nil },
		runProgram: func(tea.Model, func(func(tea.Msg))) error { return nil },
		newSession: nil,
		pprofAddr:  "127.0.0.1:0",
	}
	stopPprofOnCleanup(t, deps)
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	if got := rec.requested(); len(got) != 1 || got[0] != "127.0.0.1:0" {
		t.Errorf("bind requests = %v, want [127.0.0.1:0]", got)
	}
	if deps.pprofCtl == nil {
		t.Fatal("runEnter should expose the pprof controller on deps")
	}
	// runEnter must tear the listener down on return so the socket does not
	// outlive the enter session.
	if got := deps.pprofCtl.Status(); got.Running {
		t.Errorf("Status() after runEnter returned = %+v, want stopped", got)
	}
}

// TestEnter_WritesPprofAddrFileUnderSprawlRoot pins the one production
// construction site: the controller must be pointed at a path derived from the
// sprawl root, and the file's lifetime must match the listener's.
func TestEnter_WritesPprofAddrFileUnderSprawlRoot(t *testing.T) {
	fakePprofListen(t)
	fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	path := pprofAddrFilePath(tmpDir)
	if want := filepath.Join(tmpDir, ".sprawl", "runtime", "pprof-addr"); path != want {
		t.Errorf("pprofAddrFilePath = %q, want %q", path, want)
	}

	deps := &enterDeps{
		getenv:    func(string) string { return "" },
		getwd:     func() (string, error) { return tmpDir, nil },
		pprofAddr: "127.0.0.1:0",
	}
	stopPprofOnCleanup(t, deps)
	deps.runProgram = func(tea.Model, func(func(tea.Msg))) error {
		bound := deps.pprofCtl.Status().Addr
		if bound == "" {
			t.Fatal("pprof listener is not running inside the session")
		}
		if got, want := strings.TrimSpace(readAddrFile(t, path)), bound; got != want {
			t.Errorf("addr file = %q, want the bound addr %q", got, want)
		}
		return nil
	}
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	requireNoAddrFile(t, path, "after runEnter returned")
}

// TestEnter_LosingTheWeaveLockLeavesAnotherSessionsAddrFileAlone: the addr file
// is shared per sprawl root, so a second `sprawl enter` that loses the
// single-weave flock must not touch the live session's advertisement. Binding
// (and therefore advertising) before the flock is held would let the loser
// overwrite the file and then delete it on its way out, leaving the surviving
// session serving with no advertisement at all.
func TestEnter_LosingTheWeaveLockLeavesAnotherSessionsAddrFileAlone(t *testing.T) {
	rec := fakePprofListen(t)
	fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	// Stand in for the session that already holds the root.
	held, err := rootinit.AcquireWeaveLock(tmpDir)
	if err != nil {
		t.Fatalf("acquiring the weave lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	path := pprofAddrFilePath(tmpDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("seeding addr dir: %v", err)
	}
	const incumbent = "127.0.0.1:41234\n"
	if err := os.WriteFile(path, []byte(incumbent), 0o600); err != nil {
		t.Fatalf("seeding incumbent addr file: %v", err)
	}

	deps := &enterDeps{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return tmpDir, nil },
		runProgram: func(tea.Model, func(func(tea.Msg))) error { return nil },
		pprofAddr:  "127.0.0.1:0",
	}
	stopPprofOnCleanup(t, deps)
	if err := runEnter(deps); err == nil {
		t.Fatal("runEnter should fail when another session holds the weave lock")
	}
	if got := readAddrFile(t, path); got != incumbent {
		t.Errorf("addr file = %q, want the incumbent %q untouched", got, incumbent)
	}
	if got := rec.count(); got != 0 {
		t.Errorf("bind attempts = %d, want 0 — a session that cannot hold the root must not open a listener", got)
	}
}

// TestEnter_ClearsStaleAddrFileAtLaunch: a SIGKILLed session leaves the file
// behind. It is NOT self-diagnosing — a stale 127.0.0.1:6060 may well connect to
// whatever unrelated process now holds that port and return confusing garbage
// rather than refusing — so the launch that owns the root clears it.
func TestEnter_ClearsStaleAddrFileAtLaunch(t *testing.T) {
	fakePprofListen(t)
	fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	path := pprofAddrFilePath(tmpDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("seeding addr dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(defaultPprofAddr+"\n"), 0o600); err != nil {
		t.Fatalf("seeding stale addr file: %v", err)
	}

	deps := &enterDeps{
		getenv: func(string) string { return "" },
		getwd:  func() (string, error) { return tmpDir, nil },
		// No pprof addr: nothing should be advertised at all.
	}
	stopPprofOnCleanup(t, deps)
	deps.runProgram = func(tea.Model, func(func(tea.Msg))) error {
		requireNoAddrFile(t, path, "inside a session with no pprof listener")
		return nil
	}
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
}

// TestEnter_ToggleReusesLaunchConfiguredAddr: toggling off and back on during
// a session launched with --pprof/SPRAWL_PPROF_ADDR must return to the
// configured address. Relocating to the well-known default would break
// whatever the operator pointed at the configured port.
func TestEnter_ToggleReusesLaunchConfiguredAddr(t *testing.T) {
	const configured = "127.0.0.1:7779"
	rec := fakePprofListen(t)
	sigRec := fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	deps := &enterDeps{
		getenv:    func(string) string { return "" },
		getwd:     func() (string, error) { return tmpDir, nil },
		pprofAddr: configured,
	}
	stopPprofOnCleanup(t, deps)
	deps.runProgram = func(tea.Model, func(func(tea.Msg))) error {
		waitFor(t, "runEnter to register the toggle signal", func() bool {
			return sigRec.channel() != nil
		})
		for _, want := range []bool{false, true} {
			select {
			case sigRec.channel() <- pprofToggleSignal:
			case <-time.After(2 * time.Second):
				t.Fatal("nobody is reading the registered pprof toggle channel")
			}
			waitFor(t, fmt.Sprintf("the listener to become running=%v", want), func() bool {
				return deps.pprofCtl.Status().Running == want
			})
		}
		return nil
	}
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}

	want := []string{configured, configured}
	got := rec.requested()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("bind requests = %v, want %v (launch addr reused, not %s)", got, want, defaultPprofAddr)
	}
}

// TestEnter_TogglesPprofOnSignalDuringSession is the core deliverable: pprof
// must come up on a RUNNING session with no relaunch. The signal is delivered
// on the very channel runEnter registered, while runEnter is still inside
// runProgram — so a handler wired to a different channel fails here.
func TestEnter_TogglesPprofOnSignalDuringSession(t *testing.T) {
	rec := fakePprofListen(t)
	sigRec := fakePprofSignalNotify(t)

	tmpDir := t.TempDir()
	deps := &enterDeps{
		getenv: func(string) string { return "" },
		getwd:  func() (string, error) { return tmpDir, nil },
		// pprofAddr left empty: nothing is listening at launch.
	}
	stopPprofOnCleanup(t, deps)
	deps.runProgram = func(tea.Model, func(func(tea.Msg))) error {
		waitFor(t, "runEnter to register the toggle signal", func() bool {
			return sigRec.channel() != nil
		})
		if got := rec.count(); got != 0 {
			t.Errorf("bind attempts before the signal = %d, want 0", got)
		}
		select {
		case sigRec.channel() <- pprofToggleSignal:
		case <-time.After(2 * time.Second):
			t.Fatal("nobody is reading the registered pprof toggle channel")
		}
		waitFor(t, "the toggle signal to start the pprof listener", func() bool {
			return deps.pprofCtl != nil && deps.pprofCtl.Status().Running
		})
		bound := deps.pprofCtl.Status().Addr
		if code := mustGet(t, bound, "/debug/pprof/"); code != http.StatusOK {
			t.Errorf("GET /debug/pprof/ after the toggle signal = %d, want 200", code)
		}
		// The toggle path must advertise the address too, not just the launch
		// path: this is the only way an operator who pressed SIGUSR2 can learn
		// where the listener came up.
		if got, want := strings.TrimSpace(readAddrFile(t, pprofAddrFilePath(tmpDir))), bound; got != want {
			t.Errorf("addr file after the toggle = %q, want the bound addr %q", got, want)
		}
		return nil
	}
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}

	found := false
	for _, s := range sigRec.signals() {
		if s == pprofToggleSignal {
			found = true
		}
	}
	if !found {
		t.Errorf("runEnter registered signals %v, want it to include %v", sigRec.signals(), pprofToggleSignal)
	}
	if !sigRec.didStop() {
		t.Error("runEnter should unregister the pprof toggle signal handler on return")
	}
	if got := deps.pprofCtl.Status(); got.Running {
		t.Errorf("Status() after runEnter returned = %+v, want stopped", got)
	}
	requireNoAddrFile(t, pprofAddrFilePath(tmpDir), "after runEnter returned")
}
