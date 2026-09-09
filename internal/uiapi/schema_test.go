package uiapi

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/jackc/pgx/v5"
)

// stubRow answers the to_regclass probe with a fixed verdict.
type stubRow struct {
	present bool
	err     error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.present
	return nil
}

// stubPool reports which tables exist and records what was probed.
type stubPool struct {
	present map[string]bool
	err     error
	probed  []string
}

func (p *stubPool) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	table := args[0].(string)
	p.probed = append(p.probed, table)
	return stubRow{present: p.present[table], err: p.err}
}

func (p *stubPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("VerifySchema must not run a row query")
}
func (p *stubPool) Ping(context.Context) error { return nil }

func allPresent() *stubPool {
	present := map[string]bool{}
	for _, table := range requiredTables {
		present[table] = true
	}
	return &stubPool{present: present}
}

// TestVerifySchema_AcceptsAMigratedDatabase is the negative control for every
// refusal below: a probe that fired on a healthy database would prove nothing
// when it fires on a broken one.
func TestVerifySchema_AcceptsAMigratedDatabase(t *testing.T) {
	pool := allPresent()
	if err := VerifySchema(context.Background(), pool); err != nil {
		t.Fatalf("VerifySchema on a fully migrated database = %v, want nil", err)
	}
	if len(pool.probed) != len(requiredTables) {
		t.Errorf("probed %v, want every one of %v — a check that inspected fewer tables than it claims is silent about the rest", pool.probed, requiredTables)
	}
}

// TestVerifySchema_RefusesAnUnmigratedDatabase is the load-bearing one: the
// read API connects as a SELECT-only role and must never repair a schema, so an
// unmigrated database has to be a loud boot failure that names the fix.
//
// It runs per required table rather than once, so a check that happened to
// probe only `events` cannot pass by covering the first case.
func TestVerifySchema_RefusesAnUnmigratedDatabase(t *testing.T) {
	for _, missing := range requiredTables {
		t.Run("missing "+missing, func(t *testing.T) {
			pool := allPresent()
			pool.present[missing] = false

			err := VerifySchema(context.Background(), pool)
			if err == nil {
				t.Fatalf("VerifySchema returned nil with %q absent — the API would boot, report healthy, and 500 on every request", missing)
			}
			if !errors.Is(err, ErrSchemaNotMigrated) {
				t.Errorf("error %v does not wrap ErrSchemaNotMigrated, so a caller cannot distinguish it from an unreachable database", err)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q does not name the missing table", err)
			}

			// A refusal an operator cannot act on is only half a diagnosis.
			var hint *store.HintError
			if !errors.As(err, &hint) {
				t.Fatalf("error %v is not a store.HintError, so `sprawl` prints no next step", err)
			}
			if !strings.Contains(hint.Hint, "sprawl store migrate") {
				t.Errorf("hint %q does not name the command that fixes this", hint.Hint)
			}
		})
	}
}

// TestVerifySchema_SurfacesAProbeFailure separates "the table is absent" from
// "the question could not be asked". Reporting a broken connection as an
// unmigrated schema would send an operator to run a migration against a
// database that is already fine.
func TestVerifySchema_SurfacesAProbeFailure(t *testing.T) {
	pool := allPresent()
	pool.err = errors.New("connection reset by peer")

	err := VerifySchema(context.Background(), pool)
	if err == nil {
		t.Fatal("VerifySchema swallowed a failed probe")
	}
	if errors.Is(err, ErrSchemaNotMigrated) {
		t.Errorf("a failed probe was reported as an unmigrated schema (%v) — the operator would migrate a database that is not the problem", err)
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("error %q drops the underlying cause", err)
	}
}

// TestPackage_RunsNoMigrations is a source-level assertion, and it is here
// because there is no runtime observation that can make it.
//
// "This process never migrates" is a claim about code that does NOT exist, so
// no test that runs the code can demonstrate it — a behavioural test only shows
// that the paths it exercised happened not to migrate. The check that matches
// the claim is that the package never names a migration entry point at all.
// Should a write slice ever need one, this test fails and the reviewer is
// handed the decision explicitly.
func TestPackage_RunsNoMigrations(t *testing.T) {
	fset := token.NewFileSet()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package's sources: %v", err)
	}

	var files int
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, source, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", source, err)
		}
		files++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// The callee's own name, whether `Migrate(...)` or
			// `store.Migrate(...)`. Matching CALLS rather than every
			// identifier keeps ErrSchemaNotMigrated — a name that exists
			// precisely to report that nothing migrated — from tripping it.
			var id *ast.Ident
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				id = fn
			case *ast.SelectorExpr:
				id = fn.Sel
			default:
				return true
			}
			if !strings.Contains(id.Name, "Migrate") {
				return true
			}
			t.Errorf("%s calls %s — the read API connects as a SELECT-only role and must never migrate: an unmigrated database is a boot failure, and an over-privileged connection that CAN migrate would make a browser-reachable process the owner of the shared event log's schema", fset.Position(id.Pos()), id.Name)
			return true
		})
	}
	if files == 0 {
		t.Fatal("parsed no non-test files, so this assertion inspected nothing and its silence is not evidence")
	}
}
