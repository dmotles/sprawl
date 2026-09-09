package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Defaults for the listener. Addr is :8080 to match hubd's container
// convention; the deployment injects whatever it wants.
const (
	DefaultAddr  = ":8080"
	DefaultGrace = 10 * time.Second
)

// Config is everything the server needs. Readers are interfaces so the whole
// HTTP surface is assertable without a database.
//
// When the read-WRITE slice lands it adds a *store.Ledger field here, beside
// Events, and no field below changes meaning.
type Config struct {
	Addr   string
	Grace  time.Duration
	Logger *slog.Logger

	// Events backs /api/events. Required.
	Events EventReader
	// Goals backs /api/goals. Required.
	Goals GoalReader
	// Health backs /healthz's dependency check. Optional: nil means the probe
	// reports liveness only, which is what a request arriving at all proves.
	Health Pinger
}

// Pinger is the dependency check /healthz makes. Narrower than Pool on purpose:
// the health probe has no business reading rows.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewMux builds the router. Separated from Serve so tests drive the real
// handlers through httptest without binding a socket.
func NewMux(cfg Config) *http.ServeMux {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(nopWriter{}, nil))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz(cfg.Health))
	mux.HandleFunc("GET /api/events", handleEvents(cfg.Events, logger))
	// The reader is called through a closure rather than passed as the method
	// value `cfg.Goals.ListGoals`: a method value on a nil interface panics
	// where it is TAKEN, so the latter would crash router construction with a
	// bare nil-dereference. Wrapped, a missing reader behaves like every other
	// endpoint's — it fails on a request, after Serve's guard has had its say.
	mux.HandleFunc("GET /api/goals", handleList("goals", logger,
		func(ctx context.Context, o ListOptions) ([]Goal, error) { return cfg.Goals.ListGoals(ctx, o) }))
	return mux
}

// Serve runs the read API until ctx is cancelled, then drains for Grace.
//
// The drain is not decoration: without it an in-flight request is severed at
// SIGTERM during every rolling deploy, and the browser shows an error for a
// deployment that went fine.
func Serve(ctx context.Context, cfg Config) error {
	// Every reader NewMux wires up must be named here. A nil one is a
	// misconfigured deployment, and it should be a boot failure that says which
	// field is missing rather than a 500 on whichever view an operator opens
	// first.
	for _, req := range []struct {
		name string
		nil  bool
	}{
		{"Events", cfg.Events == nil},
		{"Goals", cfg.Goals == nil},
	} {
		if req.nil {
			return fmt.Errorf("uiapi: Config.%s is nil, so its endpoint would fail on the first request", req.name)
		}
	}
	addr := cfg.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	grace := cfg.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           NewMux(cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("read API listening", "component", "uiapi", "addr", addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("uiapi: serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// A fresh context: ctx is already cancelled, so passing it to Shutdown
	// would abort the drain immediately and make Grace a lie.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	logger.Info("draining", "component", "uiapi", "grace", grace.String())
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("uiapi: shutdown: %w", err)
	}
	return <-errCh
}

// handleHealthz is dependency-free liveness when Health is nil, and a real
// database check when it is not.
//
// It answers 503 rather than 500 on a failed Ping: the process is fine and the
// dependency is not, which is the difference between "restart me" and "wait".
func handleHealthz(health Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if health != nil {
			if err := health.Ping(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"status": "unavailable",
					"error":  "database unreachable",
				})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleEvents serves the raw ledger, newest first.
func handleEvents(reader EventReader, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, err := parseLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		events, err := reader.ListEvents(r.Context(), limit)
		if err != nil {
			// The error is LOGGED and not returned: it can name a table, a
			// column or a role, and this surface is browser-reachable with no
			// auth in v1.
			logger.Error("listing events failed", "component", "uiapi", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reading the event log failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	}
}

// handleList is the shape every list endpoint shares: validate the query,
// call one reader, wrap the result in a named key.
//
// Generic because the alternative is six near-identical handlers, and the
// failure mode of that duplication is not verbosity but drift — the fifth copy
// quietly returns the database's error text, or defaults a bad limit instead of
// rejecting it, and nothing in the type system notices.
//
// `key` is the response envelope's single field. Responses are objects rather
// than bare arrays so a field can be added later without breaking a consumer.
func handleList[T any](key string, logger *slog.Logger, load func(context.Context, ListOptions) ([]T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts, err := parseListOptions(r.URL.Query())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		items, err := load(r.Context(), opts)
		if err != nil {
			// LOGGED, not returned: it can name a table, a column or a role,
			// and this surface is browser-reachable with no auth in v1.
			logger.Error("read failed", "component", "uiapi", "collection", key, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reading " + key + " failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{key: items})
	}
}

// parseListOptions validates ?limit= and ?project_id=.
//
// An unparseable project_id is a 400 rather than an ignored filter: silently
// dropping it would answer a question about one project with every project's
// data, which is the most misleading thing this API could do.
func parseListOptions(q url.Values) (ListOptions, error) {
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		return ListOptions{}, err
	}
	opts := ListOptions{Limit: limit}
	if raw := q.Get("project_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return ListOptions{}, fmt.Errorf("project_id must be a uuid, got %q", raw)
		}
		opts.ProjectID = &id
	}
	return opts, nil
}

// parseLimit validates ?limit=.
//
// A bad limit is a 400 rather than a silent fallback to the default: a caller
// that asked for 5000 and got 100 has been given a truncated answer that looks
// complete, which is worse than an error.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return DefaultEventLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("limit must be an integer, got %q", raw)
	}
	if n < 1 {
		return 0, fmt.Errorf("limit must be at least 1, got %d", n)
	}
	if n > MaxEventLimit {
		return 0, fmt.Errorf("limit must be at most %d, got %d", MaxEventLimit, n)
	}
	return n, nil
}

// writeJSON writes status and body. The header is written BEFORE encoding, so
// an encoding failure mid-body cannot be turned into a second WriteHeader
// (which net/http logs and ignores); a truncated body is the honest outcome.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// nopWriter discards log output for the nil-Logger case in NewMux.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
