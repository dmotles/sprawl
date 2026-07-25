package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	buf := &syncBuffer{}
	c := newPprofController(buf, preferred)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = c.Stop(ctx)
	})
	return c, buf
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
	orig := pprofListen
	pprofListen = func(network, addr string) (net.Listener, error) {
		rec.record(addr)
		return orig(network, "127.0.0.1:0")
	}
	t.Cleanup(func() { pprofListen = orig })
	return rec
}

// failingPprofListen installs a pprofListen seam that always fails to bind.
func failingPprofListen(t *testing.T, err error) *pprofListenRecorder {
	t.Helper()
	rec := &pprofListenRecorder{}
	orig := pprofListen
	pprofListen = func(_, addr string) (net.Listener, error) {
		rec.record(addr)
		return nil, err
	}
	t.Cleanup(func() { pprofListen = orig })
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
	orig := pprofListen
	pprofListen = func(network, _ string) (net.Listener, error) {
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
	t.Cleanup(func() { pprofListen = orig })
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
		if code := mustGet(t, deps.pprofCtl.Status().Addr, "/debug/pprof/"); code != http.StatusOK {
			t.Errorf("GET /debug/pprof/ after the toggle signal = %d, want 200", code)
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
}
