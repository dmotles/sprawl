package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeEvents is an EventReader that records what it was asked for.
type fakeEvents struct {
	gotLimit int
	events   []Event
	err      error
}

func (f *fakeEvents) ListEvents(_ context.Context, limit int) ([]Event, error) {
	f.gotLimit = limit
	return f.events, f.err
}

// fakePinger is a Health whose verdict the test chooses.
type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func get(t *testing.T, cfg Config, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewMux(cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestParseLimit covers the whole domain of ?limit=, including the boundaries
// on both sides of each rejection, because an off-by-one here is the difference
// between a rejected request and an unbounded scan of the ledger.
func TestParseLimit(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr string
	}{
		{name: "absent means the default", raw: "", want: DefaultEventLimit},
		{name: "the smallest accepted value", raw: "1", want: 1},
		{name: "the ceiling itself is accepted", raw: "1000", want: MaxEventLimit},
		{name: "one past the ceiling is refused", raw: "1001", wantErr: "at most"},
		{name: "zero is refused", raw: "0", wantErr: "at least 1"},
		{name: "negative is refused", raw: "-1", wantErr: "at least 1"},
		{name: "non-numeric is refused", raw: "all", wantErr: "must be an integer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLimit(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseLimit(%q) = %d, nil — a limit this handler cannot serve must be an error, never a silent fallback to the default: a caller that asked for 5000 and got 100 has a truncated answer that looks complete", tc.raw, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("parseLimit(%q) error = %q, want it to contain %q so the caller can tell which rule it broke", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLimit(%q) = error %v, want %d", tc.raw, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("parseLimit(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestHandleEvents_PassesTheValidatedLimitThrough is the control that the
// parsed limit actually reaches the reader. Without it every parseLimit case
// above could be correct while the handler queried a constant.
func TestHandleEvents_PassesTheValidatedLimitThrough(t *testing.T) {
	reader := &fakeEvents{}
	if got := get(t, Config{Events: reader}, "/api/events?limit=7").Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if reader.gotLimit != 7 {
		t.Errorf("reader was asked for limit %d, want 7 — ?limit= is parsed but not honoured", reader.gotLimit)
	}
}

// TestHandleEvents_EmptyLedgerEncodesAsAnArray pins the non-nil slice. A nil
// slice marshals to `null`, which makes every consumer handle a second empty
// case for no reason.
func TestHandleEvents_EmptyLedgerEncodesAsAnArray(t *testing.T) {
	rec := get(t, Config{Events: &fakeEvents{events: []Event{}}}, "/api/events")
	if body := rec.Body.String(); !strings.Contains(body, `"events":[]`) {
		t.Errorf("empty ledger encoded as %s, want an empty JSON array", strings.TrimSpace(body))
	}
}

// TestHandleEvents_BadLimitIs400AndNeverReachesTheDatabase asserts both halves:
// the status, and that the rejection happened BEFORE the query. A handler that
// queried first and rejected after would pass a status-only assertion.
func TestHandleEvents_BadLimitIs400AndNeverReachesTheDatabase(t *testing.T) {
	reader := &fakeEvents{gotLimit: -1}
	rec := get(t, Config{Events: reader}, "/api/events?limit=nine")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if reader.gotLimit != -1 {
		t.Errorf("the reader was called with limit %d despite an invalid ?limit= — validation must gate the query", reader.gotLimit)
	}
}

// TestHandleEvents_DatabaseErrorIsLoggedNotReturned is a leak assertion, not an
// error-handling one. This surface is browser-reachable with no auth in v1, and
// a pgx error names tables, columns and roles.
func TestHandleEvents_DatabaseErrorIsLoggedNotReturned(t *testing.T) {
	var logged strings.Builder
	reader := &fakeEvents{err: errors.New(`relation "secret_internal_table" does not exist`)}

	rec := get(t, Config{Events: reader, Logger: textLogger(&logged)}, "/api/events")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "secret_internal_table") {
		t.Errorf("the response body carries the database error verbatim: %s — this surface is unauthenticated, so the detail belongs in the log", strings.TrimSpace(body))
	}
	// The other direction: suppressing the detail is only correct if the
	// operator can still get at it. Without this the handler could discard the
	// error entirely and pass the assertion above.
	if !strings.Contains(logged.String(), "secret_internal_table") {
		t.Errorf("the database error reached neither the body nor the log (%q) — it has been silently dropped", logged.String())
	}
}

// TestHandleHealthz covers both verdicts. 503 rather than 500 on a failed Ping
// is the load-bearing detail: the process is fine and the dependency is not,
// which is the difference between "restart me" and "wait".
func TestHandleHealthz(t *testing.T) {
	tests := []struct {
		name       string
		health     Pinger
		wantStatus int
		wantBody   string
	}{
		{name: "no dependency configured is liveness only", health: nil, wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "a reachable database is ok", health: fakePinger{}, wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "an unreachable database is 503, not 500", health: fakePinger{err: errors.New("connection refused")}, wantStatus: http.StatusServiceUnavailable, wantBody: "unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(t, Config{Events: &fakeEvents{}, Health: tc.health}, "/healthz")
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
			}
			if got["status"] != tc.wantBody {
				t.Errorf("status field = %q, want %q", got["status"], tc.wantBody)
			}
		})
	}
}

// TestHandleHealthz_DoesNotLeakTheConnectionError: a Ping failure from pgx
// carries the host and user it tried, and /healthz is the most probed endpoint
// there is.
func TestHandleHealthz_DoesNotLeakTheConnectionError(t *testing.T) {
	health := fakePinger{err: errors.New("dial tcp db.internal.example:5432: connect: refused")}
	rec := get(t, Config{Events: &fakeEvents{}, Health: health}, "/healthz")
	if body := rec.Body.String(); strings.Contains(body, "db.internal.example") {
		t.Errorf("/healthz body names the database host: %s", strings.TrimSpace(body))
	}
}

// TestNewMux_RejectsNonGET pins the method patterns. Registered as "GET /path",
// so a POST is a 405 from the mux itself and no handler runs.
func TestNewMux_RejectsNonGET(t *testing.T) {
	for _, path := range []string{"/healthz", "/api/events"} {
		rec := httptest.NewRecorder()
		NewMux(Config{Events: &fakeEvents{}}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405 — the read API answers reads only", path, rec.Code)
		}
	}
}

// TestServe_RefusesANilReader: Config.Events is the one required field, and a
// nil one would panic on the first request instead of failing at boot.
func TestServe_RefusesANilReader(t *testing.T) {
	err := Serve(context.Background(), Config{Addr: "127.0.0.1:0"})
	if err == nil {
		t.Fatal("Serve accepted a nil Config.Events — it would have bound a socket and panicked on the first request")
	}
	if !strings.Contains(err.Error(), "Events") {
		t.Errorf("error = %q, want it to name the field that was nil", err)
	}
}

// TestServe_DrainsOnContextCancellation proves the shutdown path returns rather
// than hanging, and that a cancelled ctx does not abort its own drain — Serve
// builds a fresh context for Shutdown precisely because passing the cancelled
// one would make Grace a lie.
func TestServe_DrainsOnContextCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{Addr: addr, Events: &fakeEvents{}, Logger: textLogger(io.Discard)})
	}()

	// Wait for the listener to be up, so cancellation exercises the drain path
	// rather than racing ListenAndServe.
	waitFor(t, func() bool {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v after a clean drain, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of context cancellation — SIGTERM would hang the container until the orchestrator SIGKILLs it")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the server never became reachable")
}

func textLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}
