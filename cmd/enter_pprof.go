// Package cmd / enter_pprof.go — net/http/pprof endpoint for `sprawl enter`.
//
// The endpoint auto-starts when `--pprof <addr>` or `SPRAWL_PPROF_ADDR=<addr>`
// is set at launch (QUM-678). QUM-934 additionally makes it togglable on a
// RUNNING session — sending SIGUSR2 to the `sprawl enter` process starts or
// stops the listener with no relaunch, which is what makes a session-scoped
// performance bug profilable at all (restarting resolves some of them, so the
// launch-only flag destroyed the evidence it existed to collect).
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// defaultPprofAddr is where the runtime toggle binds when no address was
// configured at launch. Loopback only: the endpoint exposes process internals,
// and a runtime toggle makes it easy to open one without thinking about
// exposure.
const defaultPprofAddr = "127.0.0.1:6060"

// errPprofStopping is returned by Start while a previous listener is still
// draining. Stop clears the running state before the socket is actually
// released, so without this a concurrent Start would re-bind the same address
// and fail with a bare "address already in use".
var errPprofStopping = errors.New("pprof listener is shutting down")

// pprofToggleSignal toggles the listener on a running session. SIGUSR2 is the
// only free signal: SIGUSR1 is the sigdump goroutine/fd dump (QUM-495),
// SIGTERM/SIGHUP are the enter quit path, SIGINT belongs to the terminal.
const pprofToggleSignal = syscall.SIGUSR2

// Test seams. Swapped by cmd/enter_pprof_test.go; never reassigned in
// production code.
var (
	pprofListen       = net.Listen
	pprofSignalNotify = signal.Notify
	pprofSignalStop   = signal.Stop
)

// pprofReadHeaderTimeout bounds header reads so a stuck client cannot pin the
// listener open (and satisfies gosec G112).
const pprofReadHeaderTimeout = 10 * time.Second

// pprofStopTimeout bounds the graceful drain when the session exits.
const pprofStopTimeout = 2 * time.Second

// pprofStatus is a snapshot of the listener state. Addr is the ACTUAL bound
// address, so a `:0` request reports the port the kernel assigned.
type pprofStatus struct {
	Addr    string
	Running bool
}

// pprofRun is one listener lifetime. A *http.Server is single-use — after
// Shutdown, Serve returns http.ErrServerClosed immediately — so a restart
// allocates a fresh run rather than reusing the old server.
type pprofRun struct {
	srv  *http.Server
	addr string
	done chan struct{} // closed when the serve goroutine has returned
}

// pprofController owns the pprof listener and can start, stop, and report it
// at any point in the session. Safe for concurrent use.
type pprofController struct {
	logW io.Writer
	// preferred is the launch-configured address (--pprof / SPRAWL_PPROF_ADDR).
	// A toggle with no explicit address returns here, so re-enabling never
	// relocates the endpoint away from what the operator asked for. Empty ⇒
	// defaultPprofAddr. Set once at construction and never mutated, which is
	// why resolveAddr reads it without the mutex — adding a setter would make
	// that read a data race.
	preferred string

	mu       sync.Mutex
	run      *pprofRun
	stopping bool
}

func newPprofController(logW io.Writer, preferred string) *pprofController {
	return &pprofController{logW: logW, preferred: preferred}
}

// resolveAddr picks the address to bind: an explicit request wins, then the
// launch-configured address, then the loopback default. A configured address
// that is occupied is reported as a bind failure and the listener stays down —
// silently falling back to a random port would leave an operator who asked for
// a specific port worse off than one who was told it failed.
func (c *pprofController) resolveAddr(addr string) string {
	switch {
	case addr != "":
		return addr
	case c.preferred != "":
		return c.preferred
	default:
		return defaultPprofAddr
	}
}

// pprofMux builds a dedicated mux carrying only the pprof handlers. Serving
// http.DefaultServeMux (the nil-Handler default) would also expose anything
// any other package happened to register there — an easy footgun now that
// users can open this socket at runtime.
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	// Index also serves the named profiles (heap, goroutine, ...) beneath
	// /debug/pprof/.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Start binds addr and serves /debug/pprof/* in a background goroutine. An
// empty addr resolves per resolveAddr. The bind happens synchronously so a
// failure is reported to the caller and the returned address is the one the
// listener actually holds. When a listener is already running it is left
// alone: already is true and boundAddr is the running address, so a second
// Start never relocates the endpoint out from under whoever is scraping it.
// Returns errPprofStopping while a previous listener is still draining.
func (c *pprofController) Start(addr string) (boundAddr string, already bool, err error) {
	addr = c.resolveAddr(addr)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil {
		return c.run.addr, true, nil
	}
	if c.stopping {
		return "", false, errPprofStopping
	}

	ln, err := pprofListen("tcp", addr)
	if err != nil {
		return "", false, fmt.Errorf("binding pprof listener on %s: %w", addr, err)
	}
	run := &pprofRun{
		srv:  &http.Server{Handler: pprofMux(), ReadHeaderTimeout: pprofReadHeaderTimeout},
		addr: ln.Addr().String(),
		done: make(chan struct{}),
	}
	c.run = run

	go func() {
		defer close(run.done)
		// ErrServerClosed is the normal Stop path, not a failure.
		if err := run.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(c.logW, "[enter] pprof listener exited: %v\n", err)
		}
	}()
	return run.addr, false, nil
}

// Stop shuts the listener down and waits for the serve goroutine to exit, so a
// stop→start cycle on a fixed port cannot race into "address already in use".
// was is false when nothing was running. A non-nil error means the graceful
// drain failed and the server was closed abruptly instead.
func (c *pprofController) Stop(ctx context.Context) (was bool, err error) {
	c.mu.Lock()
	run := c.run
	if run == nil {
		c.mu.Unlock()
		return false, nil
	}
	// Publish "not running" immediately but keep the stopping flag set until
	// the socket is released, so a concurrent Start gets errPprofStopping
	// rather than an EADDRINUSE bind attempt on the same address. The lock is
	// released across the drain: an in-flight /debug/pprof/profile can hold it
	// open for many seconds and Status must stay cheap for a TUI reader.
	c.run = nil
	c.stopping = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.stopping = false
		c.mu.Unlock()
	}()

	// Shutdown closes the listener itself; never close it separately.
	shutdownErr := run.srv.Shutdown(ctx)
	if shutdownErr != nil {
		// Intentional: a cancelled/expired ctx (session teardown) downgrades
		// the graceful drain to an abrupt close rather than hanging.
		_ = run.srv.Close()
		shutdownErr = fmt.Errorf("draining pprof listener on %s: %w", run.addr, shutdownErr)
	}
	<-run.done
	return true, shutdownErr
}

// Status snapshots the listener state. Cheap and safe from any goroutine —
// this is what a TUI surface reads.
func (c *pprofController) Status() pprofStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return pprofStatus{}
	}
	return pprofStatus{Addr: c.run.addr, Running: true}
}

// Toggle stops the listener if it is running, otherwise starts it on addr
// (empty ⇒ the launch-configured address, else defaultPprofAddr), and reports
// the resulting state. Returns errPprofStopping if another caller's Stop is
// still draining — the status then reads as not-running, so a caller that
// wants to distinguish "off" from "draining" must surface the error.
func (c *pprofController) Toggle(ctx context.Context, addr string) (pprofStatus, error) {
	if was, err := c.Stop(ctx); err != nil || was {
		return c.Status(), err
	}
	if _, _, err := c.Start(addr); err != nil {
		return c.Status(), err
	}
	return c.Status(), nil
}

// serveDone exposes the current run's completion channel. Test-only: it lets a
// test prove Stop waited for the serve goroutine rather than just flipping a
// flag. Nil when nothing is running.
func (c *pprofController) serveDone() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return nil
	}
	return c.run.done
}

// resolvePprofAddr returns the address pprof should bind to, given the
// CLI flag value and the SPRAWL_PPROF_ADDR env var value. The flag wins
// when both are set. Empty return ⇒ no listener at launch.
func resolvePprofAddr(flagVal, envVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return envVal
}

// startPprof is the QUM-678 launch-time path: no-op when addr is empty,
// otherwise start the listener and log the bound address. A bind failure is
// logged and swallowed — a diagnostic endpoint must never fail the session.
func startPprof(addr string, c *pprofController) {
	if addr == "" {
		return
	}
	bound, _, err := c.Start(addr)
	if err != nil {
		fmt.Fprintf(c.logW, "[enter] pprof listener failed to start: %v\n", err)
		return
	}
	fmt.Fprintf(c.logW, "[enter] pprof listening on http://%s/debug/pprof/\n", bound)
}

// installPprofSignalToggle arms pprofToggleSignal (SIGUSR2) to toggle the
// pprof listener for the lifetime of the session. The returned stop func
// unregisters the handler and waits for the listener goroutine to exit; it is
// safe to call more than once.
func installPprofSignalToggle(ctx context.Context, c *pprofController) (stop func()) {
	ch := make(chan os.Signal, 1)
	pprofSignalNotify(ch, pprofToggleSignal)

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pprofSignalLoop(ctx, ch, c, nil)
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			pprofSignalStop(ch)
			cancel()
			wg.Wait()
		})
	}
}

// pprofSignalLoop toggles the listener on every signal until ctx is done. The
// channel is injected so tests never raise a process-wide signal; onToggle,
// when non-nil, is invoked with the post-toggle status.
func pprofSignalLoop(ctx context.Context, ch <-chan os.Signal, c *pprofController, onToggle func(pprofStatus)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			// Note: in TUI mode stderr is redirected to
			// .sprawl/logs/tui-stderr-*.log, so this line lands there
			// rather than on the terminal. Surfacing the address in the
			// TUI itself is a follow-on wave.
			//
			// WithoutCancel: a toggle that lands as the session tears down
			// should still get its bounded graceful drain rather than
			// reporting "context canceled" on a normal exit.
			toggleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pprofStopTimeout)
			st, err := c.Toggle(toggleCtx, "")
			cancel()
			switch {
			case err != nil:
				fmt.Fprintf(c.logW, "[enter] pprof toggle failed: %v\n", err)
			case st.Running:
				fmt.Fprintf(c.logW, "[enter] pprof toggled ON — http://%s/debug/pprof/\n", st.Addr)
			default:
				fmt.Fprintf(c.logW, "[enter] pprof toggled OFF\n")
			}
			if onToggle != nil {
				onToggle(st)
			}
		}
	}
}
