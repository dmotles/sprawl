package hub

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// waitForReady polls the readiness endpoint until it returns 200 or the
// deadline elapses.
func waitForReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never became ready at %s", url)
}

func TestServe_ReadyThenDrainOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	base := "http://" + ln.Addr().String()

	cfg := HubConfig{Grace: 200 * time.Millisecond, Logger: discardLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveOn(ctx, ln, cfg) }()

	// Server should flip readiness to 200 once it is up.
	waitForReady(t, base+"/readyz")

	// Cancelling the context triggers graceful drain: Serve must return cleanly
	// within the grace window (+ margin).
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveOn returned error on graceful drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveOn did not return within grace window after cancel")
	}

	// After drain the listener is closed, so readiness is no longer reachable.
	if resp, err := http.Get(base + "/readyz"); err == nil {
		resp.Body.Close()
		t.Fatal("expected connection refused after drain, but /readyz still answered")
	}
}

// TestRunHTTP_DrainWaitsForInflight drives the drain primitive directly with a
// slow handler to prove in-flight requests finish within the grace window
// rather than being cut off.
func TestRunHTTP_DrainWaitsForInflight(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	base := "http://" + ln.Addr().String()

	h := &Health{}
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/readyz", h.ReadinessHandler())
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHTTP(ctx, ln, mux, h, 2*time.Second, discardLogger()) }()
	waitForReady(t, base+"/readyz")

	respCh := make(chan int, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			respCh <- -1
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		respCh <- resp.StatusCode
	}()

	<-started // ensure the request is in flight
	cancel()  // begin drain while /slow is still running

	select {
	case code := <-respCh:
		if code != http.StatusOK {
			t.Fatalf("in-flight request was cut off during drain: got status %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHTTP returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runHTTP did not return after drain")
	}
}

// TestRunHTTP_AlreadyCancelledNeverStaysReady guards the drain-ordering race:
// if ctx is already cancelled when runHTTP starts, readiness must not be left
// true after it returns. (With readiness set inside the serve goroutine, the
// drain's SetReady(false) could run before the goroutine's SetReady(true),
// leaving /readyz=200 during drain.)
func TestRunHTTP_AlreadyCancelledNeverStaysReady(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	h := &Health{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before runHTTP even starts

	done := make(chan error, 1)
	go func() {
		done <- runHTTP(ctx, ln, http.NewServeMux(), h, time.Second, discardLogger())
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runHTTP did not return for an already-cancelled ctx")
	}
	if h.Ready() {
		t.Fatal("readiness left true after drain of an already-cancelled ctx")
	}
}

// TestRunHTTP_FlipsReadinessBeforeDrain proves readiness fails BEFORE the
// in-flight drain completes, so the orchestrator stops routing new traffic at
// the start of shutdown. The slow handler blocks until released; by the time it
// is released the drain sequence has already run SetReady(false), so an
// in-process readiness probe observes 503.
func TestRunHTTP_FlipsReadinessBeforeDrain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	base := "http://" + ln.Addr().String()

	h := &Health{}
	started := make(chan struct{})
	release := make(chan struct{})
	readinessDuringDrain := make(chan int, 1)
	mux := http.NewServeMux()
	mux.Handle("/readyz", h.ReadinessHandler())
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release // stay in flight until the test releases us
		// Probe readiness in-process (Shutdown refuses new connections, so we
		// must not self-dial over HTTP here).
		rec := httptest.NewRecorder()
		h.ReadinessHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		readinessDuringDrain <- rec.Code
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHTTP(ctx, ln, mux, h, 2*time.Second, discardLogger()) }()
	waitForReady(t, base+"/readyz")

	go func() { //nolint:errcheck
		resp, err := http.Get(base + "/slow")
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started // request is in flight
	cancel()  // begin drain: runHTTP runs SetReady(false), then Shutdown blocks on /slow

	// Deterministically wait for readiness to flip false (the first drain step)
	// before releasing the handler — no time-bet. Shutdown cannot complete while
	// /slow is still blocked, so this proves the flip happened before drain end.
	deadline := time.Now().Add(2 * time.Second)
	for h.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("readiness never flipped to not-ready after cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	select {
	case code := <-readinessDuringDrain:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("readiness during drain: want 503, got %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("never observed readiness during drain")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runHTTP did not return after drain")
	}
}
