// Package cmd — deprecation helpers for QUM-337 Phase 2.1.
//
// These emit one-shot stderr warnings when an agent or human invokes a
// legacy CLI form that has been superseded by an MCP tool (or, for tmux-
// only commands, by `sprawl enter`). The warning fires once per process
// and can be silenced with SPRAWL_QUIET_DEPRECATIONS=1, which the e2e
// scripts that intentionally exercise the deprecated path set.
//
// Behavior is intentionally limited to a single line on stderr — exit
// code, stdout, and command semantics are unchanged. See QUM-337 / QUM-314
// for the M13 cutover plan.
package cmd

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// deprecationStderr is the writer the helpers emit to. Tests override this
// to capture output without fighting the global os.Stderr.
var deprecationStderr io.Writer = os.Stderr

// deprecationGetenv is the env-var lookup the helpers use. Tests override
// this to drive the SPRAWL_QUIET_DEPRECATIONS gate without mutating the
// real process environment (which is racy under t.Parallel).
var deprecationGetenv = os.Getenv

// deprecationOnce ensures at most one warning is emitted per process even
// if multiple deprecated entry points are exercised in the same binary
// invocation (e.g. an in-process supervisor that re-enters runX helpers).
var deprecationOnce sync.Once

// deprecationWarning emits a one-shot stderr warning that `sprawl <cmdName>`
// is deprecated in favor of the named MCP tool. Suppressed when
// SPRAWL_QUIET_DEPRECATIONS is set to any non-empty value.
func deprecationWarning(cmdName, replacement string) {
	if deprecationGetenv("SPRAWL_QUIET_DEPRECATIONS") != "" {
		return
	}
	deprecationOnce.Do(func() {
		fmt.Fprintf(deprecationStderr,
			"warning: `sprawl %s` is deprecated. Use the %s MCP tool instead.\n"+
				"  This CLI form will be removed in a future release.\n"+
				"  Set SPRAWL_QUIET_DEPRECATIONS=1 to suppress.\n",
			cmdName, replacement)
	})
}

// deprecationWarningCustom emits a one-shot stderr warning with a free-form
// body, used by commands that have no MCP equivalent (init, poke, color)
// and are slated for outright deletion in a future phase.
func deprecationWarningCustom(cmdName, body string) {
	if deprecationGetenv("SPRAWL_QUIET_DEPRECATIONS") != "" {
		return
	}
	deprecationOnce.Do(func() {
		fmt.Fprintf(deprecationStderr,
			"warning: `sprawl %s` is deprecated. %s\n"+
				"  Set SPRAWL_QUIET_DEPRECATIONS=1 to suppress.\n",
			cmdName, body)
	})
}

// resetDeprecationOnce is a test-only helper used via t.Cleanup to reset
// the once-per-process gate between tests in the same binary.
func resetDeprecationOnce() { deprecationOnce = sync.Once{} }
