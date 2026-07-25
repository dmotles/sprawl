package tui

// Shared helpers for the QUM-928 sidechain-suppression tests.

import (
	"fmt"
	"testing"
)

// withSidechainVisible flips the SPRAWL_SHOW_SIDECHAIN debug hatch for the
// duration of one test and restores it via t.Cleanup.
//
// Two things to know:
//   - The hatch is read ONCE at package init (the environment is fixed per
//     process), so t.Setenv does not affect it — tests must flip the package
//     var directly.
//   - Because it mutates package state, any test using this MUST NOT call
//     t.Parallel().
//
// Deliberately returns nothing: an earlier version returned a restore func,
// which made `defer withSidechainVisible(t, true)` (missing the trailing call)
// compile, silently do nothing, and leak the flag into every later test in the
// package — order-dependent green.
func withSidechainVisible(t *testing.T, v bool) {
	t.Helper()
	prev := sidechainVisible
	sidechainVisible = v
	t.Cleanup(func() { sidechainVisible = prev })
}

// fmtMsg renders a tea.Msg for leak assertions. Uses %+v so nested struct
// fields (tool inputs, text bodies) are included.
func fmtMsg(m any) string {
	if m == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T%+v", m, m)
}
