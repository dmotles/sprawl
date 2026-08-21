package store

import (
	"context"
	"log/slog"
)

// Redaction of DSN-shaped text out of slog records, applied at the HANDLER.
//
// WHY NOT AT THE CALL SITES. This package logs an error-valued attribute in ~17
// places (`grep -rnE '"(error|err|cause|reason)", *[a-zA-Z_]' internal/store/*.go
// cmd/*.go`), and the count moves. Patching each is one chance to miss per site
// plus one for the site added next month; wrapping the handler is one place that
// cannot be bypassed by adding a log line. The call sites are deliberately
// unchanged.
//
// WHERE IT IS WIRED. Two places, because there are two loggers and only one of
// them belongs to a caller:
//
//   - `Open` wraps `LedgerConfig.Logger`. The degraded "event log unreachable"
//     WARN is built INSIDE Open, before any wrapping cmd could do at its own
//     slog.New, and it fires precisely during the outage this redaction exists
//     for. That one is the crux.
//   - `cmd/store_dispatch.go` wraps the dispatch logger, which receives the
//     Dispatcher/Sweeper/Notify records.
//
// Those two meet — the dispatch logger and the ledger logger are both wrapped
// on the same process — so RedactingHandler is idempotent by identity: wrapping
// an already-wrapped handler returns it unchanged.
//
// THE TRAPS THIS HANDLES, each of which produces a wrapper that looks right and
// redacts nothing (all four are pinned in redactslog_test.go):
//
//   - `"error", err` arrives as KindAny, NOT KindString. A KindString-only
//     wrapper is a no-op on the exact shape this package logs.
//   - `logger.With(...)` preformats attrs into the HANDLER via WithAttrs; those
//     attrs never appear in any Record, so redacting Records alone misses them.
//   - `slog.Group(...)` (a KindGroup value) and `WithGroup` are separate escape
//     hatches; handling one does not handle the other.
//   - a LogValuer can hide the DSN behind Resolve().
//
// EXPLICIT NON-GOALS, in the style of redact.go's, because an unstated one is
// read as protection that is not there:
//
//   - A value that carries a DSN only in its MarshalJSON / MarshalText output.
//     TextHandler renders KindAny through TextMarshaler and JSONHandler through
//     MarshalJSON, while this wrapper reads Value.String() (i.e. %v). Covering
//     the marshalled forms would mean marshalling every attr here and handing
//     the inner handler a string, which changes structured output for every
//     clean record. Nothing in this repo logs such a type.
//   - Non-string kinds (Int64, Bool, Duration, Time, Float64, Uint64). They
//     cannot carry DSN text, and stringifying them to check would destroy their
//     type in structured output.
//   - process.go's "no origin remote" WARN, which logs through slog.Default()
//     and is therefore outside both wrap points. Checked rather than assumed:
//     its attrs are a git remote URL and a git error, with no DSN on any path.
//   - Whatever RedactSecrets itself does not cover — see its own non-goals
//     list. This handler decides WHERE redaction runs, not WHAT counts as a
//     secret.
//
// A record that redaction does not change is passed through UNTOUCHED, byte for
// byte: the clean path allocates nothing and cannot perturb structured output.
type redactHandler struct {
	inner slog.Handler
}

// RedactingHandler wraps h so every record it emits has RedactSecrets applied to
// the message and to string-shaped attribute values.
//
// A nil handler yields slog.DiscardHandler rather than a panic, and an
// already-wrapped handler is returned as-is.
func RedactingHandler(h slog.Handler) slog.Handler {
	if h == nil {
		return slog.DiscardHandler
	}
	if rh, ok := h.(*redactHandler); ok {
		return rh
	}
	return &redactHandler{inner: h}
}

// RedactingLogger returns l with its handler wrapped by RedactingHandler.
//
// Nil-safe, and nil means DISCARD, not stderr: every deps struct in this package
// treats an absent logger as "log nowhere", and defaulting to a real stream
// would turn a library caller's silence into terminal output.
func RedactingLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(RedactingHandler(l.Handler()))
}

func (h *redactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	msg := RedactSecrets(r.Message)
	changed := msg != r.Message

	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		clean, dirty := redactAttr(a)
		changed = changed || dirty
		attrs = append(attrs, clean)
		return true
	})
	if !changed {
		return h.inner.Handle(ctx, r)
	}
	// A FRESH Record rather than r.Clone(): Clone keeps the existing attrs, so
	// adding the redacted ones would emit every attr twice, and AddAttrs on an
	// un-Cloned copy appends slog's own "!BUG" marker and can corrupt the
	// caller's record. Time (including the zero value, which handlers must omit
	// entirely), Level and PC are carried over — a dropped PC makes
	// AddSource:true name this file instead of the caller.
	out := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	out.AddAttrs(attrs...)
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i], _ = redactAttr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(clean)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	// An empty name is a no-op per the Handler contract; wrapping anyway would
	// add a phantom group level.
	if name == "" {
		return h
	}
	return &redactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr reports the redacted attr and whether anything changed. The
// unchanged case returns the ORIGINAL attr, so a clean value keeps its Kind and
// its own resolution semantics.
func redactAttr(a slog.Attr) (slog.Attr, bool) {
	v, changed := redactValue(a.Value)
	if !changed {
		return a, false
	}
	return slog.Attr{Key: a.Key, Value: v}, true
}

func redactValue(v slog.Value) (slog.Value, bool) {
	// Resolve FIRST: a LogValuer whose LogValue() yields the DSN is invisible
	// behind an unresolved KindAny. Resolve caps at 5 hops and recovers panics,
	// and the inner handler resolving again is idempotent.
	resolved := v.Resolve()
	switch resolved.Kind() {
	case slog.KindGroup:
		group := resolved.Group()
		clean := make([]slog.Attr, len(group))
		changed := false
		for i, a := range group {
			var dirty bool
			clean[i], dirty = redactAttr(a)
			changed = changed || dirty
		}
		if !changed {
			return v, false
		}
		return slog.GroupValue(clean...), true
	case slog.KindString, slog.KindAny:
		// KindAny covers the `"error", err` shape: Value.String() renders it
		// through %v, i.e. via Error(). Only substitute when redaction actually
		// changed something — rewriting a clean KindAny into a string would turn
		// structs into Go-syntax text under a JSON handler.
		s := resolved.String()
		if out := RedactSecrets(s); out != s {
			return slog.StringValue(out), true
		}
		return v, false
	default:
		return v, false
	}
}
