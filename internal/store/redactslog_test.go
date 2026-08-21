package store

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Redaction at the slog HANDLER rather than at the ~17 `"error", err` call
// sites in this package (QUM-1280). The call sites are unchanged on purpose: a
// per-site fix is one chance to miss per site, plus one for the site somebody
// adds next month.
//
// EVERY ASSERTION HERE IS ABSENCE OF THE SECRET, never presence of a
// `[redacted]` marker — a marker check passes with the credential sitting next
// to the marker. And every absence assertion is paired with a survival
// assertion, because "printed nothing at all" satisfies absence.
//
// The traps these tests exist to pin, each of which produces a wrapper that
// looks correct and redacts nothing:
//
//   - `"error", err` arrives as KindAny, NOT KindString. A wrapper switching on
//     KindString only is a no-op on the shape this package actually logs.
//   - `logger.With("error", err)` preformats into the HANDLER via WithAttrs; the
//     attr never appears in any Record.
//   - `slog.Group(...)` (KindGroup in a Record) and `WithGroup` are two separate
//     escape hatches, and handling one does not handle the other.
//   - a LogValuer can carry the DSN behind Resolve().
const leakyDSN = "postgres://leakuser:" + probePassword + "@leak-probe.invalid:5432/leakdb"

func leakyAttrError() error {
	return errors.New("cannot parse `" + leakyDSN + "`: invalid port")
}

// assertRedacted is the paired probe: the diagnosis must survive AND the secret
// must be gone. Callers pass the benign fragment they expect to keep.
func assertRedacted(t *testing.T, got, wantSurvives string) {
	t.Helper()
	if !strings.Contains(got, wantSurvives) {
		t.Fatalf("the diagnosis did not survive: %q is absent, so the absence check below would pass vacuously; got:\n%s", wantSurvives, got)
	}
	for _, secret := range []string{probePassword, "leak-probe.invalid", leakyDSN} {
		if strings.Contains(got, secret) {
			t.Errorf("output leaked %q; got:\n%s", secret, got)
		}
	}
}

func redactingTextLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(RedactingHandler(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func TestRedactingHandler_RedactsAnErrorValuedAttr(t *testing.T) {
	var buf bytes.Buffer
	redactingTextLogger(&buf).Warn("event log unreachable — running degraded", "error", leakyAttrError())
	assertRedacted(t, buf.String(), "invalid port")
}

func TestRedactingHandler_RedactsTheMessage(t *testing.T) {
	var buf bytes.Buffer
	redactingTextLogger(&buf).Warn("cannot reach " + leakyDSN + " right now")
	assertRedacted(t, buf.String(), "right now")
}

func TestRedactingHandler_RedactsAStringAttr(t *testing.T) {
	var buf bytes.Buffer
	redactingTextLogger(&buf).Info("configured", slog.String("dsn", leakyDSN), slog.String("keep", "diagnosis-intact"))
	assertRedacted(t, buf.String(), "diagnosis-intact")
}

// The With(...) escape hatch: WithAttrs preformats into the handler, so a
// wrapper that redacts Records only lets this through untouched.
func TestRedactingHandler_RedactsAttrsAddedByWith(t *testing.T) {
	var buf bytes.Buffer
	redactingTextLogger(&buf).With("error", leakyAttrError()).Info("dispatch pass failed")
	assertRedacted(t, buf.String(), "dispatch pass failed")
}

func TestRedactingHandler_RedactsInsideGroups(t *testing.T) {
	t.Run("WithGroup", func(t *testing.T) {
		var buf bytes.Buffer
		redactingTextLogger(&buf).WithGroup("db").Warn("degraded", "error", leakyAttrError())
		assertRedacted(t, buf.String(), "invalid port")
	})
	t.Run("KindGroup attr", func(t *testing.T) {
		var buf bytes.Buffer
		redactingTextLogger(&buf).Warn("degraded", slog.Group("db", "error", leakyAttrError()))
		assertRedacted(t, buf.String(), "invalid port")
	})
	t.Run("nested KindGroup attr", func(t *testing.T) {
		var buf bytes.Buffer
		redactingTextLogger(&buf).Warn("degraded", slog.Group("outer", slog.Group("db", "error", leakyAttrError())))
		assertRedacted(t, buf.String(), "invalid port")
	})
}

type leakyLogValuer struct{}

func (leakyLogValuer) LogValue() slog.Value {
	return slog.StringValue("cannot parse `" + leakyDSN + "`: invalid port")
}

func TestRedactingHandler_ResolvesLogValuer(t *testing.T) {
	var buf bytes.Buffer
	redactingTextLogger(&buf).Info("configured", "dsn", leakyLogValuer{})
	assertRedacted(t, buf.String(), "invalid port")
}

// captureHandler captures what reached the inner handler, for the contract
// assertions that output text cannot express.
type captureHandler struct {
	records *[]slog.Record
	attrs   *[][]slog.Attr
	minimum slog.Level
	err     error
}

func (h captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.minimum }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return h.err
}

func (h captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	*h.attrs = append(*h.attrs, attrs)
	return h
}

func (h captureHandler) WithGroup(string) slog.Handler { return h }

func newRecorder() (slog.Handler, *[]slog.Record, *[][]slog.Attr) {
	recs := &[]slog.Record{}
	attrs := &[][]slog.Attr{}
	return captureHandler{records: recs, attrs: attrs}, recs, attrs
}

// A rewriting wrapper has to rebuild the Record, and rebuilding is where the
// metadata gets dropped: a lost PC makes AddSource print the wrapper's own
// frame or nothing, and a manufactured Time changes output on records that
// deliberately carry none.
func TestRedactingHandler_PreservesRecordMetadata(t *testing.T) {
	inner, recs, _ := newRecorder()
	h := RedactingHandler(inner)

	when := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	r := slog.NewRecord(when, slog.LevelWarn, "cannot parse `"+leakyDSN+"`: invalid port", 12345)
	r.AddAttrs(slog.String("keep", "diagnosis-intact"), slog.Int64("attempts", 3), slog.Duration("took", time.Second))
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(*recs) != 1 {
		t.Fatalf("inner handler saw %d records, want 1", len(*recs))
	}
	got := (*recs)[0]
	if !got.Time.Equal(when) {
		t.Errorf("Time = %v, want %v", got.Time, when)
	}
	if got.Level != slog.LevelWarn {
		t.Errorf("Level = %v, want WARN", got.Level)
	}
	if got.PC != 12345 {
		t.Errorf("PC = %d, want 12345 — a dropped PC makes AddSource name the wrapper instead of the caller", got.PC)
	}
	if got.NumAttrs() != 3 {
		t.Fatalf("NumAttrs = %d, want 3 — a dropped attr would panic the kind checks below", got.NumAttrs())
	}
	var kinds []slog.Kind
	got.Attrs(func(a slog.Attr) bool {
		kinds = append(kinds, a.Value.Kind())
		return true
	})
	want := []slog.Kind{slog.KindString, slog.KindInt64, slog.KindDuration}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("attr %d kind = %v, want %v — non-string kinds cannot carry a DSN and must keep their type", i, kinds[i], want[i])
		}
	}
	rendered := got.Message
	got.Attrs(func(a slog.Attr) bool {
		rendered += " " + a.Key + "=" + a.Value.String()
		return true
	})
	assertRedacted(t, rendered, "invalid port")
}

// A record with a zero Time must stay zero: handlers are required to omit the
// time field entirely in that case.
func TestRedactingHandler_PreservesAZeroTime(t *testing.T) {
	inner, recs, _ := newRecorder()
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "leaky "+leakyDSN, 0)
	if err := RedactingHandler(inner).Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !(*recs)[0].Time.IsZero() {
		t.Errorf("Time = %v, want the zero value preserved", (*recs)[0].Time)
	}
}

// A rewriting wrapper must not ADD to the record it was handed: Record copies
// share backing state, so AddAttrs on an un-Clone d copy appends slog's own
// !BUG marker and can corrupt the caller's attrs — and the mildest form of the
// same bug simply emits every attr twice.
//
// The observable assertion is the attr COUNT the inner handler sees, because the
// caller-side corruption is not reliably reproducible: whether the copy writes
// into shared spare capacity depends on the record's backing-slice capacity. The
// count fires on the mutation either way (measured: mutating Handle to
// `r.AddAttrs(attrs...)` doubles it to 14).
func TestRedactingHandler_DoesNotDuplicateOrCorruptAttrs(t *testing.T) {
	inner, recs, _ := newRecorder()
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "leaky "+leakyDSN, 0)
	for i := 0; i < 6; i++ {
		r.AddAttrs(slog.String("k", "v"))
	}
	r.AddAttrs(slog.String("dsn", leakyDSN))
	if err := RedactingHandler(inner).Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := (*recs)[0].NumAttrs(); got != 7 {
		t.Errorf("inner handler saw %d attrs, want 7 — the wrapper added to the record instead of rebuilding it", got)
	}
	n := 0
	r.Attrs(func(a slog.Attr) bool {
		n++
		if strings.Contains(a.Key, "!BUG") || strings.Contains(a.Value.String(), "!BUG") {
			t.Errorf("the wrapper mutated the caller's Record: %v", a)
		}
		return true
	})
	if n != 7 {
		t.Errorf("the caller's Record now has %d attrs, want 7", n)
	}
}

// The contract-level partner to the text-output With test: the attrs reaching
// the inner handler's WithAttrs must already be redacted, and an error attr
// must arrive as a STRING (KindAny cannot be rewritten in place).
func TestRedactingHandler_WithAttrsHandsTheInnerHandlerRedactedStrings(t *testing.T) {
	inner, _, attrs := newRecorder()
	RedactingHandler(inner).WithAttrs([]slog.Attr{slog.Any("error", leakyAttrError())})
	if len(*attrs) != 1 || len((*attrs)[0]) != 1 {
		t.Fatalf("inner handler saw %v, want one attr", *attrs)
	}
	got := (*attrs)[0][0]
	if got.Value.Kind() != slog.KindString {
		t.Errorf("error attr kind = %v, want KindString — a rewritten value cannot stay KindAny", got.Value.Kind())
	}
	assertRedacted(t, got.Value.String(), "invalid port")
}

func TestRedactingHandler_EnabledDelegates(t *testing.T) {
	recs, attrs := &[]slog.Record{}, &[][]slog.Attr{}
	h := RedactingHandler(captureHandler{records: recs, attrs: attrs, minimum: slog.LevelError})
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(Info) is true against an Error-level inner handler, so the wrapper is not delegating")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled(Error) is false against an Error-level inner handler")
	}
}

func TestRedactingHandler_PropagatesHandleError(t *testing.T) {
	sentinel := errors.New("inner handler failed")
	h := RedactingHandler(captureHandler{records: &[]slog.Record{}, attrs: &[][]slog.Attr{}, err: sentinel})
	err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "x", 0))
	if !errors.Is(err, sentinel) {
		t.Errorf("Handle swallowed the inner handler's error: %v", err)
	}
}

func TestRedactingHandler_DoesNotDoubleWrap(t *testing.T) {
	inner, _, _ := newRecorder()
	once := RedactingHandler(inner)
	if twice := RedactingHandler(once); twice != once {
		t.Error("wrapping an already-wrapped handler produced a second layer; cmd and store.Open both wrap and will meet")
	}
}

// NEGATIVE CONTROL. Clean records must come out byte-identical to what the bare
// handler would emit — including the shapes redact.go measured as collisions for
// its tail patterns (a filename after "lookup", a parenthesised token after an
// address). Direction: this probe must stay QUIET. Its own positive control is
// the final subject, which carries a DSN and MUST make it fire.
func TestRedactingHandler_LeavesCleanRecordsUntouched(t *testing.T) {
	clean := []string{
		"failed to lookup migration.sql: no such file",
		"dial 127.0.0.1:5432 (timeout)",
		"events.sql:12 (syntax)",
		"12:30 (UTC)",
		"dispatch pass failed, retrying",
	}
	for _, msg := range clean {
		var wrapped, bare bytes.Buffer
		opts := &slog.HandlerOptions{Level: slog.LevelDebug}
		slog.New(RedactingHandler(slog.NewTextHandler(&wrapped, opts))).Info(msg, "k", "some value", "n", 1)
		slog.New(slog.NewTextHandler(&bare, opts)).Info(msg, "k", "some value", "n", 1)
		if stripTime(wrapped.String()) != stripTime(bare.String()) {
			t.Errorf("clean record was altered:\n wrapped: %s bare:    %s", wrapped.String(), bare.String())
		}
	}

	// Positive control for the probe above: a dirty subject must differ.
	var wrapped, bare bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	slog.New(RedactingHandler(slog.NewTextHandler(&wrapped, opts))).Info("leaky", "dsn", leakyDSN)
	slog.New(slog.NewTextHandler(&bare, opts)).Info("leaky", "dsn", leakyDSN)
	if stripTime(wrapped.String()) == stripTime(bare.String()) {
		t.Error("a DSN-bearing record came through identical to the bare handler, so the comparison above proves nothing")
	}
}

// stripTime removes the time= field so two handlers writing at different
// instants can be compared.
func stripTime(s string) string {
	i := strings.Index(s, " ")
	if strings.HasPrefix(s, "time=") && i > 0 {
		return s[i+1:]
	}
	return s
}

func TestRedactingLogger_NilIsSafeAndDiscards(t *testing.T) {
	l := RedactingLogger(nil)
	if l == nil {
		t.Fatal("RedactingLogger(nil) returned nil; store.Open logs through this unconditionally and would panic")
	}
	// DISCARDS, asserted rather than assumed: defaulting nil to a stderr
	// handler would leave this green while printing the DSN below to the
	// terminal during `go test`.
	if l.Enabled(context.Background(), slog.LevelError) {
		t.Error("RedactingLogger(nil) is enabled at ERROR, so it is writing somewhere rather than discarding")
	}
	l.Warn("must not panic", "error", leakyAttrError())
}

func TestRedactingLogger_RedactsThroughAConfiguredLogger(t *testing.T) {
	var buf bytes.Buffer
	l := RedactingLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	l.Warn("degraded", "error", leakyAttrError())
	assertRedacted(t, buf.String(), "invalid port")
}
