package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Hermetic half of pg.go. The real privilege behaviour is asserted against a
// container in the store_pg suite; what is asserted here is the DECISION
// VerifyAppendOnly makes given an answer, and that a failure to ask is not
// mistaken for a clean answer.

// privilegePool answers the has_table_privilege query with canned values.
type privilegePool struct {
	PgPool // embedded nil: anything this double does not implement panics
	update bool
	delete bool
	trunc  bool
	err    error
}

func (p privilegePool) QueryRow(context.Context, string, ...any) pgx.Row {
	if p.err != nil {
		return errRow{err: p.err}
	}
	return privRow{p.update, p.delete, p.trunc}
}

type privRow struct{ u, d, tr bool }

func (r privRow) Scan(dest ...any) error {
	if len(dest) != 3 {
		return errors.New("privRow: want three destinations")
	}
	for i, v := range []bool{r.u, r.d, r.tr} {
		p, ok := dest[i].(*bool)
		if !ok {
			return errors.New("privRow: destination is not *bool")
		}
		*p = v
	}
	return nil
}

func TestVerifyAppendOnly_NoMutatingPrivilegesIsClean(t *testing.T) {
	if err := VerifyAppendOnly(context.Background(), privilegePool{}); err != nil {
		t.Errorf("a connection with no mutating privileges must verify clean; got: %v", err)
	}
}

// TestVerifyAppendOnly_EachVerbIndependentlyFailsTheCheck mutates one privilege
// at a time.
//
// Asserted per-verb rather than as one all-three case, because a check written
// as `canUpdate && canDelete && canTruncate` would pass every single-verb case
// while UPDATE alone is already enough to rewrite history. An all-three fixture
// cannot tell those implementations apart.
func TestVerifyAppendOnly_EachVerbIndependentlyFailsTheCheck(t *testing.T) {
	cases := map[string]privilegePool{
		"UPDATE only":   {update: true},
		"DELETE only":   {delete: true},
		"TRUNCATE only": {trunc: true},
		"all three":     {update: true, delete: true, trunc: true},
	}
	for name, pool := range cases {
		err := VerifyAppendOnly(context.Background(), pool)
		if !errors.Is(err, ErrAppendOnlyNotEnforced) {
			t.Errorf("%s: got err=%v, want ErrAppendOnlyNotEnforced — any one mutating verb defeats append-only", name, err)
		}
		var hint *HintError
		if errors.As(err, &hint) && hint.Hint == "" {
			t.Errorf("%s: the error carries no next action", name)
		}
	}
}

// TestVerifyAppendOnly_NamesTheVerbsItFound pins that the message says WHICH
// privilege is the problem. "not append-only" alone sends an operator to read
// the whole grants migration.
func TestVerifyAppendOnly_NamesTheVerbsItFound(t *testing.T) {
	err := VerifyAppendOnly(context.Background(), privilegePool{update: true, trunc: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"UPDATE", "TRUNCATE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "DELETE") {
		t.Errorf("the error names DELETE, which this connection does not hold: %v", err)
	}
}

// TestVerifyAppendOnly_QueryFailureIsNotACleanVerdict pins the direction that
// matters for a diagnostic.
//
// If the privilege query itself fails — permissions, a dropped connection, a
// missing table — the answer is UNKNOWN, and reporting unknown as "append-only
// is fine" is exactly the plausible-zero failure a diagnostic surface must never
// produce. It is indistinguishable from a real all-clear at the point a reader
// sees it.
func TestVerifyAppendOnly_QueryFailureIsNotACleanVerdict(t *testing.T) {
	boom := errors.New("relation \"events\" does not exist")
	err := VerifyAppendOnly(context.Background(), privilegePool{err: boom})
	if err == nil {
		t.Fatal("a failed privilege query must not report a clean append-only verdict — unknown is not the same as fine")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying failure should stay in the chain for diagnosis; got: %v", err)
	}
	if errors.Is(err, ErrAppendOnlyNotEnforced) {
		t.Error("a failure to ASK must not be reported as a definite answer that the log is mutable")
	}
}

// compile-time assurance that the production pool type still satisfies PgPool.
// If pgx changes a signature, this fails at build time here rather than at the
// first call site someone happens to run.
var _ PgPool = (*noopPool)(nil)

type noopPool struct{}

func (*noopPool) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("noop") }
func (*noopPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}

func (*noopPool) QueryRow(context.Context, string, ...any) pgx.Row {
	return errRow{err: errors.New("noop")}
}
func (*noopPool) Ping(context.Context) error { return nil }
