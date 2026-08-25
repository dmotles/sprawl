package pgtest

import (
	"sync"
	"testing"
)

// TestShouldFatal_BothDirections pins the SPRAWL_STORE_PG_REQUIRED contract.
//
// Both directions, deliberately. Knowing a control's name never tells you it is
// aimed right: "required=1 means fatal" alone is satisfied by a function that
// returns true for everything, which would turn every Docker-less developer run
// into a hard failure; "unset means skip" alone is satisfied by one that returns
// false for everything, which is the vacuous green this variable exists to
// prevent. Only the pair constrains it.
func TestShouldFatal_BothDirections(t *testing.T) {
	fatal := []string{"1"}
	skip := []string{"", "0", "true", "yes", "TRUE", " 1", "1 ", "2"}

	for _, v := range fatal {
		if !shouldFatal(v) {
			t.Errorf("shouldFatal(%q) = false, want true — Postgres was declared REQUIRED and an unavailable container must be a setup failure, not a skip", v)
		}
	}
	for _, v := range skip {
		if shouldFatal(v) {
			t.Errorf("shouldFatal(%q) = true, want false — only the exact string \"1\" opts in, so an ordinary Docker-less run must skip rather than fail", v)
		}
	}
}

// TestNextID_IsUniqueUnderConcurrency pins what NextID is for.
//
// Not merely "it increments": the values name cluster-scoped objects (roles are
// not schema-scoped, so every schema on the container shares them — see
// internal/store/ledger_integration_test.go), and a duplicate there is a
// CREATE ROLE collision that surfaces as one test being flaky rather than as a
// name clash. Uniqueness under concurrency is the property; t.Parallel subtests
// are exactly how it will be reached.
func TestNextID_IsUniqueUnderConcurrency(t *testing.T) {
	const n = 200
	var wg sync.WaitGroup
	got := make([]int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = NextID()
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for _, v := range got {
		if seen[v] {
			t.Fatalf("NextID returned %d twice across %d concurrent calls — two schemas would collide on a cluster-scoped object name", v, n)
		}
		seen[v] = true
	}
	if len(seen) != n {
		t.Errorf("got %d distinct ids from %d calls, want %d", len(seen), n, n)
	}
}
