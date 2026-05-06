package host

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs all tests in this package under goleak so any leaked
// goroutine causes a failure. No ignores are registered intentionally:
// QUM-499 wants the harness to surface real leaks rather than paper
// over them.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
