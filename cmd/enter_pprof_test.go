package cmd

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
)

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

func TestStartPprof_NoopWhenEmpty(t *testing.T) {
	var called atomic.Bool
	orig := pprofListenAndServe
	pprofListenAndServe = func(string, http.Handler) error {
		called.Store(true)
		return nil
	}
	t.Cleanup(func() { pprofListenAndServe = orig })

	var buf bytes.Buffer
	startPprof("", &buf)

	if called.Load() {
		t.Error("pprofListenAndServe should NOT be called when addr is empty")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output when addr empty, got %q", buf.String())
	}
}

func TestStartPprof_StartsAndLogsWhenAddrSet(t *testing.T) {
	var (
		gotAddr string
		wg      sync.WaitGroup
	)
	wg.Add(1)
	orig := pprofListenAndServe
	pprofListenAndServe = func(addr string, _ http.Handler) error {
		gotAddr = addr
		wg.Done()
		return http.ErrServerClosed
	}
	t.Cleanup(func() { pprofListenAndServe = orig })

	var buf bytes.Buffer
	startPprof("127.0.0.1:6060", &buf)
	wg.Wait()

	if gotAddr != "127.0.0.1:6060" {
		t.Errorf("pprofListenAndServe called with addr %q, want 127.0.0.1:6060", gotAddr)
	}
	out := buf.String()
	if !strings.Contains(out, "127.0.0.1:6060") {
		t.Errorf("startup log missing bound address; got: %q", out)
	}
	if !strings.Contains(out, "pprof") {
		t.Errorf("startup log should mention pprof; got: %q", out)
	}
}

// TestEnter_NoPprofListenerWhenUnset verifies that runEnter does NOT start
// a pprof listener when neither --pprof nor SPRAWL_PPROF_ADDR is set. This
// is the default behavior the issue requires (QUM-678 acceptance criterion).
func TestEnter_NoPprofListenerWhenUnset(t *testing.T) {
	var called atomic.Bool
	orig := pprofListenAndServe
	pprofListenAndServe = func(string, http.Handler) error {
		called.Store(true)
		return nil
	}
	t.Cleanup(func() { pprofListenAndServe = orig })

	tmpDir := t.TempDir()
	deps := &enterDeps{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return tmpDir, nil },
		runProgram: func(tea.Model, func(func(tea.Msg))) error { return nil },
		newSession: nil,
		// pprofAddr left empty intentionally
	}
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	if called.Load() {
		t.Error("pprof listener should NOT start when --pprof/SPRAWL_PPROF_ADDR are unset")
	}
}

// Discard sink so the test doesn't depend on the real os.Stderr.
var _ = io.Discard
