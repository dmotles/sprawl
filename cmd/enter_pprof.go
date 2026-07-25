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
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// defaultPprofAddr is where the runtime toggle binds when no address was
// configured at launch. Loopback only: the endpoint exposes process internals,
// and a runtime toggle makes it easy to open one without thinking about
// exposure.
const defaultPprofAddr = "127.0.0.1:6060"

// pprofEphemeralAddr is the fallback bind target when our OWN default is
// occupied. Loopback with port 0: the kernel picks a free port, and the host
// half must never widen (see pprofTarget for why this fallback exists at all).
const pprofEphemeralAddr = "127.0.0.1:0"

// pprofAddrFileName is where the live listener advertises its bound address,
// relative to the sprawl root. The toggle's log line only reaches
// .sprawl/logs/tui-stderr-*.log in TUI mode (stderr is redirected before the
// toggle can fire), so an operator who sends SIGUSR2 has no other way to learn
// the address — and once the bind can land on an ephemeral port, an
// undiscoverable address is useless. Sits beside the SIGUSR1 dumps in
// .sprawl/runtime/ for the same reason: it is a live-process artifact.
//
// Advisory only: a SIGKILLed session leaves the file behind. That is
// self-diagnosing (a consumer connects and fails), so there is deliberately no
// pid/mtime staleness protocol — Start overwrites it unconditionally.
//
// A const, not a var: it is only ever read (pprofAddrFilePath joins it onto the
// sprawl root), and a mutable package-level path would be exactly the kind of
// shared global that makes tests observe each other. Slash-separated is fine —
// this file is Unix-only by construction (it references syscall.SIGUSR2).
const pprofAddrFileName = ".sprawl/runtime/pprof-addr"

// errPprofStopping is returned by Start while a previous listener is still
// draining. Stop clears the running state before the socket is actually
// released, so without this a concurrent Start would re-bind the same address
// and fail with a bare "address already in use".
var errPprofStopping = errors.New("pprof listener is shutting down")

// errPprofDrainIncomplete marks a stop whose graceful drain outlasted its
// deadline. The listener IS down — Stop closed it abruptly — so this is a
// successful stop with a degraded drain, not a failed stop. Reporting it as a
// failure would tell the user something false, which matters most exactly when
// it fires: mid-`/debug/pprof/profile?seconds=30`.
var errPprofDrainIncomplete = errors.New("graceful drain did not complete; listener closed abruptly")

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
	pprofShutdown     = func(ctx context.Context, srv *http.Server) error { return srv.Shutdown(ctx) }
)

// pprofReadHeaderTimeout bounds header reads so a stuck client cannot pin the
// listener open (and satisfies gosec G112).
const pprofReadHeaderTimeout = 10 * time.Second

// pprofStopTimeout bounds the graceful drain when the session exits.
const pprofStopTimeout = 2 * time.Second

// pprofTarget is a bind target plus the provenance of the address, which is
// what decides the bind-failure policy. The two policies MUST NOT be merged:
//
//   - An address the operator named (--pprof, SPRAWL_PPROF_ADDR, or an explicit
//     Start/Toggle argument) is a promise. They will curl that port, so a
//     silent relocation leaves them worse off than a loud failure.
//     allowEphemeralFallback is false — bind it or fail.
//   - defaultPprofAddr is OUR choice, not theirs. Nobody asked for 6060, so
//     dead-ending because some unrelated process holds it defeats the whole
//     point of a runtime toggle ("this live session wasn't launched with
//     --pprof and I need to profile it now"). allowEphemeralFallback is true.
//
// Provenance, not the address value, decides this: naming defaultPprofAddr
// explicitly is still explicit.
type pprofTarget struct {
	addr                   string
	allowEphemeralFallback bool
}

// pprofOptions are the controller's write-once settings. They are set at
// construction and never mutated, which is what makes them safe to read without
// the mutex from the signal-toggle goroutine; adding a setter would turn those
// reads into data races.
type pprofOptions struct {
	// Preferred is the launch-configured address (--pprof / SPRAWL_PPROF_ADDR).
	// A toggle with no explicit address returns here, so re-enabling never
	// relocates the endpoint away from what the operator asked for.
	Preferred string
	// AddrFile is where the bound address is advertised. Empty ⇒ disabled.
	AddrFile string
	// DefaultAddrForTest overrides defaultPprofAddr. It keeps the default a
	// const (a mutable global would race the toggle goroutine) while letting a
	// test occupy a real port and observe the fallback.
	//
	// INVARIANT: any operator-sourced address MUST go in Preferred. Whatever
	// lands here inherits relocate-on-EADDRINUSE semantics, so wiring a flag,
	// env var, or config key into this field would silently give away the
	// never-relocate guarantee that Preferred exists to keep.
	DefaultAddrForTest string
}

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
	// Write-once; see pprofOptions for why these are read without the mutex.
	preferred   string
	addrFile    string
	defaultAddr string

	mu       sync.Mutex
	run      *pprofRun
	stopping bool
}

func newPprofController(logW io.Writer, opts pprofOptions) *pprofController {
	return &pprofController{
		logW:        logW,
		preferred:   opts.Preferred,
		addrFile:    opts.AddrFile,
		defaultAddr: opts.DefaultAddrForTest,
	}
}

// resolveTarget picks the address to bind and the provenance that governs a
// bind failure: an explicit request wins, then the launch-configured address,
// then our own default. Only the last is relocatable — see pprofTarget.
func (c *pprofController) resolveTarget(addr string) pprofTarget {
	switch {
	case addr != "":
		return pprofTarget{addr: addr}
	case c.preferred != "":
		return pprofTarget{addr: c.preferred}
	default:
		def := c.defaultAddr
		if def == "" {
			def = defaultPprofAddr
		}
		return pprofTarget{addr: def, allowEphemeralFallback: true}
	}
}

// writeAddrFile advertises the bound address. Best-effort by design: a
// diagnostic endpoint must never fail to come up because it could not announce
// itself, so failures are logged and swallowed. No error return — an
// always-ignored one would just invite a caller to pretend it matters.
func (c *pprofController) writeAddrFile(addr string) {
	if c.addrFile == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.addrFile), 0o750); err != nil {
		fmt.Fprintf(c.logW, "[enter] cannot advertise pprof addr in %s: %v\n", c.addrFile, err)
		return
	}
	// Unconditional overwrite: a stale file from a killed session must not
	// outlive it. Bare "addr\n" so `curl http://$(cat <file>)/debug/pprof/`
	// works — never a URL or JSON.
	if err := os.WriteFile(c.addrFile, []byte(addr+"\n"), 0o600); err != nil {
		fmt.Fprintf(c.logW, "[enter] cannot advertise pprof addr in %s: %v\n", c.addrFile, err)
	}
}

// removeAddrFile withdraws the advertisement. Absence is the expected case when
// the file was never written, so it is not worth a log line.
func (c *pprofController) removeAddrFile() {
	if c.addrFile == "" {
		return
	}
	if err := os.Remove(c.addrFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(c.logW, "[enter] removing pprof addr file %s: %v\n", c.addrFile, err)
	}
}

// pprofAddrFilePath is where a session rooted at sprawlRoot advertises its
// live pprof address.
func pprofAddrFilePath(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, pprofAddrFileName)
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
//
// A bind failure on a relocatable target (our own default — see pprofTarget)
// retries once on an ephemeral loopback port; anything the operator named fails
// loudly instead. Only EADDRINUSE is relocatable: EACCES/EADDRNOTAVAIL mean the
// configuration or environment is wrong, and relocating would hide that.
func (c *pprofController) Start(addr string) (boundAddr string, already bool, err error) {
	target := c.resolveTarget(addr)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil {
		return c.run.addr, true, nil
	}
	if c.stopping {
		return "", false, errPprofStopping
	}

	// Everything below runs under c.mu: up to two binds plus the addr-file
	// write. Holding the lock across the file I/O is load-bearing, not
	// incidental — it is what makes the advertisement's lifetime exactly one
	// pprofRun's lifetime, so it can never interleave with a concurrent Stop's
	// removal. The cost is that Status(), documented as the cheap TUI read,
	// contends with a small write on a stalled filesystem; that is the accepted
	// trade. Don't "fix" it by moving the write out of the lock.
	// A local, not the named `err` return: after a successful fallback the
	// named return would still hold the first bind's error, so a future naked
	// return in this block would report a spurious failure.
	ln, bindErr := pprofListen("tcp", target.addr)
	if bindErr != nil {
		if !target.allowEphemeralFallback || !errors.Is(bindErr, syscall.EADDRINUSE) {
			return "", false, fmt.Errorf("binding pprof listener on %s: %w", target.addr, bindErr)
		}
		var fallbackErr error
		ln, fallbackErr = pprofListen("tcp", pprofEphemeralAddr)
		if fallbackErr != nil {
			// Name the original target too: an error mentioning only the
			// ephemeral address hides what was actually requested.
			return "", false, fmt.Errorf("binding pprof listener on %s (occupied) then on fallback %s: %w",
				target.addr, pprofEphemeralAddr, fallbackErr)
		}
	}
	run := &pprofRun{
		srv:  &http.Server{Handler: pprofMux(), ReadHeaderTimeout: pprofReadHeaderTimeout},
		addr: ln.Addr().String(),
		done: make(chan struct{}),
	}
	c.run = run
	// Only a fresh bind advertises: the already-running path returned above, so
	// a second Start never rewrites a live listener's address.
	c.writeAddrFile(run.addr)

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
	// Withdraw the advertisement here, not after the drain: from this point
	// Status() reads not-running and a concurrent Start gets errPprofStopping,
	// so a file still naming the address would be a lie for up to the whole
	// drain window.
	c.removeAddrFile()
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.stopping = false
		c.mu.Unlock()
	}()

	// Shutdown closes the listener itself; never close it separately.
	shutdownErr := pprofShutdown(ctx, run.srv)
	if shutdownErr != nil {
		// Intentional: a cancelled/expired ctx (session teardown) downgrades
		// the graceful drain to an abrupt close rather than hanging. Close is
		// unconditional here, so "the listener is down" always holds — hence
		// the errPprofDrainIncomplete tag, which lets callers report this as a
		// degraded stop instead of a failed one. The cause is wrapped
		// alongside so it stays available for the log line.
		_ = run.srv.Close()
		shutdownErr = fmt.Errorf("draining pprof listener on %s: %w: %w",
			run.addr, errPprofDrainIncomplete, shutdownErr)
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
			// rather than on the terminal — which is why Start also writes
			// the bound address to pprofAddrFilePath. Surfacing it in the
			// TUI itself is a follow-on wave.
			//
			// WithoutCancel: a toggle that lands as the session tears down
			// should still get its bounded graceful drain rather than
			// reporting "context canceled" on a normal exit.
			toggleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pprofStopTimeout)
			st, err := c.Toggle(toggleCtx, "")
			cancel()
			fmt.Fprintln(c.logW, pprofToggleLogLine(st, err))
			if onToggle != nil {
				onToggle(st)
			}
		}
	}
}

// pprofToggleLogLine renders what a toggle did. Split out from the signal loop
// so the classification is testable without driving a real drain to its
// deadline.
//
// The drain-incomplete arm comes first and is checked against st.Running: the
// listener is genuinely down in that case, so calling it a failed toggle would
// be false. Toggle surfaces errors from both its stop and start legs, and only
// the stop leg can be drain-incomplete.
func pprofToggleLogLine(st pprofStatus, err error) string {
	switch {
	case !st.Running && errors.Is(err, errPprofDrainIncomplete):
		return fmt.Sprintf("[enter] pprof toggled OFF — an in-flight request outlasted the %s drain, so it was cut short: %v",
			pprofStopTimeout, err)
	case err != nil:
		return fmt.Sprintf("[enter] pprof toggle failed: %v", err)
	case st.Running:
		return fmt.Sprintf("[enter] pprof toggled ON — http://%s/debug/pprof/", st.Addr)
	default:
		return "[enter] pprof toggled OFF"
	}
}
