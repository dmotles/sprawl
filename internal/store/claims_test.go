package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Hermetic tests for the event claim — the mechanism Appendix B item 1 makes
// exactly-once rest on.
//
// WHAT A GREEN INTEGRATION TEST CANNOT TELL YOU, which is why these exist
// alongside claims_integration_test.go:
//
//   - Whether the lease deadline is computed from the DATABASE clock or from the
//     claiming host's clock. Both produce a plausible lease_expires against one
//     database; only the first survives two hosts with skewed clocks, and skew
//     is not hypothetical in a design whose whole point is multiple hosts.
//   - Whether a TRANSPORT failure is reported or silently converted into "I lost
//     the race". Both make Claim return false; only one is correct, and the
//     wrong one loses events permanently (see the test for why).
//   - Whether Renew and Release are conditional on the caller actually OWNING
//     the claim. Against a single-host fixture an unconditional UPDATE looks
//     identical.

// ---------------------------------------------------------------------------
// Test double
// ---------------------------------------------------------------------------

// capturingPool records statements AND their arguments.
//
// Separate from appender_test.go's recordingPool on purpose: that one discards
// args, and the argument list is exactly what the clock-source assertion below
// has to inspect. Widening the shared double would have made every existing
// appender assertion depend on a field only these tests read.
type capturingPool struct {
	mu    sync.Mutex
	stmts []capturedStmt
	// execErr, when set, is returned by every Exec.
	execErr error
	// rowsAffected is what each Exec reports, consumed in order; the last value
	// repeats once the slice is exhausted.
	rowsAffected []int64
	execCount    int
}

type capturedStmt struct {
	sql  string
	args []any
}

func (p *capturingPool) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stmts = append(p.stmts, capturedStmt{sql: sql, args: args})
	if p.execErr != nil {
		return pgconn.NewCommandTag(""), p.execErr
	}
	n := int64(1)
	if len(p.rowsAffected) > 0 {
		idx := p.execCount
		if idx >= len(p.rowsAffected) {
			idx = len(p.rowsAffected) - 1
		}
		n = p.rowsAffected[idx]
	}
	p.execCount++
	verb := "UPDATE"
	low := strings.ToLower(strings.TrimSpace(sql))
	switch {
	case strings.HasPrefix(low, "insert"):
		verb = "INSERT 0"
	case strings.HasPrefix(low, "delete"):
		verb = "DELETE"
	}
	return pgconn.NewCommandTag(verb + " " + itoa(n)), nil
}

func (p *capturingPool) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("capturingPool: Begin is not expected; a claim is a single statement")
}

func (p *capturingPool) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stmts = append(p.stmts, capturedStmt{sql: sql, args: args})
	return errRow{err: errors.New("capturingPool: QueryRow is not stubbed")}
}

func (p *capturingPool) Ping(context.Context) error { return p.execErr }

func (p *capturingPool) captured() []capturedStmt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedStmt(nil), p.stmts...)
}

// only returns the single statement the pool was asked to run, failing if there
// was not exactly one. A claim that issues two statements is not atomic.
func (p *capturingPool) only(t *testing.T) capturedStmt {
	t.Helper()
	got := p.captured()
	if len(got) != 1 {
		t.Fatalf("want exactly 1 statement, got %d: %+v", len(got), got)
	}
	return got[0]
}

func normalizeSQL(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

const (
	testConsumer = "dispatcher"
	testHost     = "host-a"
	testLease    = 30 * time.Second
)

// ---------------------------------------------------------------------------
// The claim itself
// ---------------------------------------------------------------------------

// A claim is ONE conditional insert. Both halves of that are load-bearing.
//
// "One statement": a claim that took two round trips would have a window in
// which it had partially happened, and there is no transaction here to close it.
// "Conditional": ON CONFLICT DO NOTHING is what turns a duplicate claim into a
// no-op returning false instead of an error, which is the entire exactly-once
// mechanism (Appendix B item 1). A plain INSERT raises 23505 on the loser, and a
// loser that gets an ERROR rather than a polite false cannot be distinguished
// from a real failure by its caller.
func TestPgClaimStore_ClaimIsASingleConditionalInsert(t *testing.T) {
	pool := &capturingPool{}
	s := &PgClaimStore{Pool: pool}

	if _, err := s.Claim(context.Background(), uuid.New(), testConsumer, testHost, testLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	sql := normalizeSQL(s2(t, pool).sql)
	if !strings.HasPrefix(sql, "insert into event_claims") {
		t.Errorf("Claim does not insert into event_claims: %s", sql)
	}
	if !strings.Contains(sql, "on conflict") || !strings.Contains(sql, "do nothing") {
		t.Errorf("Claim is not a CONDITIONAL insert; a plain INSERT makes the loser see 23505 instead of a polite refusal: %s", sql)
	}
	if strings.Contains(sql, "do update") {
		t.Errorf("Claim uses ON CONFLICT DO UPDATE, which STEALS a live claim from whoever holds it: %s", sql)
	}
}

// s2 is `only` with the fatal message worded for claim tests.
func s2(t *testing.T, p *capturingPool) capturedStmt {
	t.Helper()
	return p.only(t)
}

// THE LEASE DEADLINE COMES FROM THE DATABASE CLOCK, NEVER THE CLAIMER'S.
//
// This is the assertion a single-database integration test structurally cannot
// make: `now() + interval` and `time.Now().Add(lease)` write indistinguishable
// rows when there is one host, and every integration fixture has one host.
//
// The consequence of getting it wrong is a fleet-wide correctness failure that
// looks like a clock problem rather than a dispatch problem: a host whose clock
// runs ten minutes fast writes lease_expires ten minutes early relative to every
// other host's reading of it, so its own claims look expired to it immediately
// and — worse — it considers every OTHER host's live claim expired and takes
// them over. Two hosts then act on the same event, which is the one thing
// event_claims exists to prevent.
//
// Asserted two ways, because either alone is weak: the statement must derive the
// deadline in SQL (now()), and NO argument may be a time. The second is what
// catches the shape that computes the deadline in Go and passes it as a
// parameter — which contains no "now()" to look for, so the first check alone
// would pass it.
func TestPgClaimStore_LeaseDeadlineIsDerivedFromTheDatabaseClock(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(s *PgClaimStore) error
	}{
		{"Claim", func(s *PgClaimStore) error {
			_, err := s.Claim(context.Background(), uuid.New(), testConsumer, testHost, testLease)
			return err
		}},
		{"Renew", func(s *PgClaimStore) error {
			_, err := s.Renew(context.Background(), uuid.New(), testConsumer, testHost, testLease)
			return err
		}},
		{"TakeoverExpired", func(s *PgClaimStore) error {
			_, err := s.TakeoverExpired(context.Background(), uuid.New(), testConsumer, testHost, testLease)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &capturingPool{}
			s := &PgClaimStore{Pool: pool}
			if err := tc.call(s); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			stmt := s2(t, pool)

			if !strings.Contains(normalizeSQL(stmt.sql), "now()") {
				t.Errorf("%s does not derive its lease deadline from the database clock: %s", tc.name, stmt.sql)
			}
			for i, a := range stmt.args {
				switch a.(type) {
				case time.Time, *time.Time:
					t.Errorf("%s passes arg %d as a %T; the lease deadline must be computed by the DATABASE, or a host with a skewed clock expires every other host's claims", tc.name, i, a)
				}
			}
		})
	}
}

// A TRANSPORT FAILURE IS NOT A LOST RACE, and conflating them loses events
// permanently rather than transiently.
//
// Both cases make Claim return false, which is why this needs its own
// assertion. Trace the wrong version: the database blips, Claim returns
// (false, nil), the dispatcher reads that as "another host has this one" and
// skips the event — and then, because it believes it has finished with that
// seq, ADVANCES ITS CURSOR PAST IT. Nothing ever comes back for it. No claim row
// exists, so no lease expires, so no takeover recovers it. That is a silently
// dropped goal, produced by a two-second network hiccup.
func TestPgClaimStore_TransportFailureIsReportedNotReadAsLosingTheRace(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	pool := &capturingPool{execErr: boom}
	s := &PgClaimStore{Pool: pool}

	won, err := s.Claim(context.Background(), uuid.New(), testConsumer, testHost, testLease)
	if err == nil {
		t.Fatalf("Claim returned (%v, nil) on a transport failure; the caller cannot tell that from losing the race, and will advance its cursor past an event nobody claimed", won)
	}
	if !errors.Is(err, boom) {
		t.Errorf("Claim did not wrap the underlying failure: %v", err)
	}
	if won {
		t.Error("Claim reported a win on a failed statement")
	}
}

// ---------------------------------------------------------------------------
// Renew / Release / Takeover are all conditional on OWNERSHIP
// ---------------------------------------------------------------------------

// A host may only renew or release ITS OWN claim.
//
// Unconditional forms look correct against any single-host fixture, and each has
// a distinct failure: an unconditional Renew lets a host that LOST the race keep
// the winner's lease alive forever (so a genuinely crashed winner is never taken
// over, and the event wedges); an unconditional Release lets any host drop
// another host's claim mid-handler, after which a second host claims the same
// event and both act.
func TestPgClaimStore_RenewAndReleaseAreScopedToTheOwningHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(s *PgClaimStore) error
	}{
		{"Renew", func(s *PgClaimStore) error {
			_, err := s.Renew(context.Background(), uuid.New(), testConsumer, testHost, testLease)
			return err
		}},
		{"Release", func(s *PgClaimStore) error {
			return s.Release(context.Background(), uuid.New(), testConsumer, testHost)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &capturingPool{}
			s := &PgClaimStore{Pool: pool}
			if err := tc.call(s); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			sql := normalizeSQL(s2(t, pool).sql)

			if !strings.Contains(sql, "where") {
				t.Fatalf("%s has no WHERE clause at all: %s", tc.name, sql)
			}
			if !strings.Contains(sql, "host =") {
				t.Errorf("%s is not scoped to the calling host, so one host can %s another host's claim: %s",
					tc.name, strings.ToLower(tc.name), sql)
			}
			if !strings.Contains(sql, "event_id =") || !strings.Contains(sql, "consumer =") {
				t.Errorf("%s is not scoped to a single (event_id, consumer): %s", tc.name, sql)
			}
		})
	}
}

// TAKEOVER NEVER STEALS A LIVE LEASE.
//
// This is the one operation in the file that deliberately trades exactly-once
// for liveness, so its guard is the whole safety story. Without the
// `lease_expires < now()` predicate, TakeoverExpired is an unconditional steal
// and event_claims stops meaning anything: any host can take any event from any
// other host at any moment, and two handlers run concurrently on the same event
// with no error anywhere.
func TestPgClaimStore_TakeoverRequiresAnExpiredLease(t *testing.T) {
	pool := &capturingPool{}
	s := &PgClaimStore{Pool: pool}

	if _, err := s.TakeoverExpired(context.Background(), uuid.New(), testConsumer, testHost, testLease); err != nil {
		t.Fatalf("TakeoverExpired: %v", err)
	}
	sql := normalizeSQL(s2(t, pool).sql)

	if !strings.Contains(sql, "lease_expires <") {
		t.Errorf("TakeoverExpired does not require the existing lease to have expired, so it is an unconditional steal: %s", sql)
	}
	if !strings.Contains(sql, "now()") {
		t.Errorf("TakeoverExpired compares the lease against something other than the database clock: %s", sql)
	}
	if !strings.Contains(sql, "host =") {
		t.Errorf("TakeoverExpired does not record the new owning host, so the claim's audit trail still names the crashed host: %s", sql)
	}
}

// A takeover that changes nothing reports false rather than true.
//
// The row-count check is the only thing that distinguishes "I took this over"
// from "the lease was still live"; an implementation that ignores RowsAffected
// returns true unconditionally, and then EVERY host believes it owns EVERY
// event.
func TestPgClaimStore_TakeoverReportsFalseWhenNoRowChanged(t *testing.T) {
	pool := &capturingPool{rowsAffected: []int64{0}}
	s := &PgClaimStore{Pool: pool}

	won, err := s.TakeoverExpired(context.Background(), uuid.New(), testConsumer, testHost, testLease)
	if err != nil {
		t.Fatalf("TakeoverExpired: %v", err)
	}
	if won {
		t.Error("TakeoverExpired reported a win while updating 0 rows; the lease was still live and its holder is still working")
	}
}

// Same shape for Claim: 0 rows inserted means somebody else holds it.
func TestPgClaimStore_ClaimReportsFalseWhenTheInsertWasSuppressed(t *testing.T) {
	pool := &capturingPool{rowsAffected: []int64{0}}
	s := &PgClaimStore{Pool: pool}

	won, err := s.Claim(context.Background(), uuid.New(), testConsumer, testHost, testLease)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if won {
		t.Error("Claim reported a win while inserting 0 rows, so two hosts would both act on this event")
	}
}

// And the positive control for the two assertions above: 1 row means a win.
// Without this, an implementation that always returns false satisfies both.
func TestPgClaimStore_ClaimReportsTrueWhenTheRowWasInserted(t *testing.T) {
	pool := &capturingPool{rowsAffected: []int64{1}}
	s := &PgClaimStore{Pool: pool}

	won, err := s.Claim(context.Background(), uuid.New(), testConsumer, testHost, testLease)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !won {
		t.Error("Claim reported a loss after inserting its row, so no host would ever act on any event")
	}
}

// ---------------------------------------------------------------------------
// Argument validation
// ---------------------------------------------------------------------------

// The nil event id is refused rather than written.
//
// uuid.Nil is what an uninitialised struct field yields, so this is the shape a
// wiring bug takes. Written, it would produce ONE claim row shared by every
// event whose id nobody set — so the first such event is handled and every
// subsequent one is silently skipped as "already claimed", which is
// indistinguishable from correct operation.
func TestPgClaimStore_RefusesTheNilEventID(t *testing.T) {
	pool := &capturingPool{}
	s := &PgClaimStore{Pool: pool}

	if _, err := s.Claim(context.Background(), uuid.Nil, testConsumer, testHost, testLease); err == nil {
		t.Error("Claim accepted the nil event id; one shared claim row would make every unset-id event look already-claimed")
	}
	if got := len(pool.captured()); got != 0 {
		t.Errorf("a refused Claim still issued %d statement(s)", got)
	}
}

// An empty consumer or host is refused.
//
// Not cosmetic in either case. The consumer is half the primary key, so an empty
// one merges two different consumers' claims — the dispatcher and the notifier
// would then each skip events the other had taken. An empty host makes every
// ownership predicate in this file match the wrong rows: Renew and Release are
// scoped by host, so a claim owned by "" is renewable and releasable by any host
// that also passes "".
func TestPgClaimStore_RefusesEmptyConsumerOrHost(t *testing.T) {
	cases := []struct{ name, consumer, host string }{
		{"empty consumer", "", testHost},
		{"empty host", testConsumer, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := &capturingPool{}
			s := &PgClaimStore{Pool: pool}
			id := uuid.New()

			if _, err := s.Claim(context.Background(), id, tc.consumer, tc.host, testLease); err == nil {
				t.Error("Claim was accepted")
			}
			if _, err := s.Renew(context.Background(), id, tc.consumer, tc.host, testLease); err == nil {
				t.Error("Renew was accepted")
			}
			if _, err := s.TakeoverExpired(context.Background(), id, tc.consumer, tc.host, testLease); err == nil {
				t.Error("TakeoverExpired was accepted")
			}
			if err := s.Release(context.Background(), id, tc.consumer, tc.host); err == nil {
				t.Error("Release was accepted")
			}
			if got := len(pool.captured()); got != 0 {
				t.Errorf("refused calls still issued %d statement(s)", got)
			}
		})
	}
}

// A non-positive lease is refused.
//
// A zero or negative lease writes a claim that is expired the moment it is
// committed, so the very next host to look at the event takes it over while the
// first host is still working on it. That is the unconditional-steal failure
// arriving through the front door, and it would look like a tuning mistake
// rather than a correctness one.
func TestPgClaimStore_RefusesANonPositiveLease(t *testing.T) {
	for _, lease := range []time.Duration{0, -time.Second} {
		pool := &capturingPool{}
		s := &PgClaimStore{Pool: pool}
		if _, err := s.Claim(context.Background(), uuid.New(), testConsumer, testHost, lease); err == nil {
			t.Errorf("Claim accepted a lease of %v; the claim would be expired on arrival and immediately stealable", lease)
		}
		if got := len(pool.captured()); got != 0 {
			t.Errorf("a refused Claim still issued %d statement(s)", got)
		}
	}
}

// PgClaimStore satisfies ClaimStore.
var _ ClaimStore = (*PgClaimStore)(nil)
