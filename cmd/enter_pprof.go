// Package cmd / enter_pprof.go — opt-in net/http/pprof endpoint for `sprawl enter`.
//
// Owns the blank import of net/http/pprof so the pprof handlers only land in
// the binary's default mux when this file compiles (which is always, but
// keeping the import here keeps the import surface localized). The endpoint
// is only started when `--pprof <addr>` or `SPRAWL_PPROF_ADDR=<addr>` is set
// on `sprawl enter`. See QUM-678.
package cmd

import (
	"fmt"
	"io"
	"net/http"

	// Registers the /debug/pprof/* handlers on http.DefaultServeMux as a
	// side effect of import. Opt-in via the --pprof flag / env var below —
	// the handlers are inert unless startPprof binds a listener.
	_ "net/http/pprof" //nolint:gosec // dev-only diagnostic; bound to loopback per docs
)

// pprofListenAndServe is the indirection used by startPprof so tests can
// substitute a fake without binding a real socket.
var pprofListenAndServe = http.ListenAndServe

// resolvePprofAddr returns the address pprof should bind to, given the
// CLI flag value and the SPRAWL_PPROF_ADDR env var value. The flag wins
// when both are set. Empty return ⇒ no listener.
func resolvePprofAddr(flagVal, envVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return envVal
}

// startPprof starts an HTTP server exposing /debug/pprof/* on addr in a
// background goroutine. No-op when addr is empty. Logs the bound address
// to logW on startup, and any listener error on exit.
func startPprof(addr string, logW io.Writer) {
	if addr == "" {
		return
	}
	fmt.Fprintf(logW, "[enter] pprof listening on http://%s/debug/pprof/\n", addr)
	go func() {
		if err := pprofListenAndServe(addr, nil); err != nil {
			fmt.Fprintf(logW, "[enter] pprof listener exited: %v\n", err)
		}
	}()
}
