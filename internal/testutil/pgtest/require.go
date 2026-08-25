package pgtest

import (
	"os"
	"sync/atomic"
	"testing"
)

// RequiredEnv is the variable that promotes an unavailable-Postgres skip into a
// setup failure. scripts/e2e-tests/store-pg-integration.sh sets it.
const RequiredEnv = "SPRAWL_STORE_PG_REQUIRED"

// pgSchemaNo backs NextID. It lives in this untagged file so NextID and
// shouldFatal — the two behaviours that are testable without a container — are
// testable at all: a test beside pgtest.go would only compile under `store_pg`,
// which is the tag `make validate` never sets.
var pgSchemaNo atomic.Int64

// NextID returns a suffix unique within this test binary. Callers that create
// their own cluster-scoped objects (roles, for instance, which are NOT
// schema-scoped and so are shared by every schema on the container) use it to
// avoid colliding with each other.
func NextID() int64 { return pgSchemaNo.Add(1) }

// shouldFatal decides between a setup failure and a skip.
//
// Split out from SkipOrFatal, whose other half calls t.Fatalf/t.Skip and so
// cannot be exercised without ending the calling test. This is the whole
// decision, and it is the thing standing between the row and a vacuous green:
// get it backwards and a host with no Docker reports a passing suite that
// asserted nothing.
func shouldFatal(env string) bool { return env == "1" }

// SkipOrFatal implements the SPRAWL_STORE_PG_REQUIRED contract: when the caller
// has declared that Postgres MUST be available, an unavailable container is a
// setup failure reported in the failure class, not a skip. A skip that can
// happen for any reason is indistinguishable from a skip that means "no Docker".
func SkipOrFatal(t *testing.T, reason string) {
	t.Helper()
	if shouldFatal(os.Getenv(RequiredEnv)) {
		t.Fatalf("%s=1 but Postgres is unavailable — this is a SETUP FAILURE, not a skip: %s", RequiredEnv, reason)
	}
	t.Skip(reason)
}
