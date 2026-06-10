// Package inboxprompt: dead-target route-up wrapper. QUM-725.
//
// When a message is destined for a Died agent, the supervisor reroutes it up
// the ancestor chain to the first live ancestor. WrapForDeadTarget builds the
// inbox text the live ancestor sees so the recipient can tell the wrapper
// apart from a normal direct message.
package inboxprompt

import (
	"fmt"
	"strings"
)

// unknownSender is the sentinel substituted for an empty originating-sender
// argument so we never produce "Originating sender: . Original body:" with a
// stray period.
const unknownSender = "unknown"

// WrapForDeadTarget renders the route-up wrapper text.
//
//	sender    — originating caller identity. Empty → "unknown" sentinel.
//	target    — the ORIGINAL name the sender intended to message (not the
//	            live ancestor we routed up to).
//	deadChain — ordered list of dead names walked from target upward. The
//	            first entry is always the original target. Empty deadChain
//	            is treated as a single-hop chain containing the target.
//	body      — original message body, preserved verbatim after the
//	            "Original body:\n\n" marker.
//
// Templates:
//   - 1 dead:  "This message was sent to <T> but <T> is dead. ..."
//   - ≥2 dead: "This message was sent to <T> but <T>, <P1>[, ...] are dead. ..."
func WrapForDeadTarget(sender, target string, deadChain []string, body string) string {
	if sender == "" {
		sender = unknownSender
	}
	names := deadChain
	if len(names) == 0 {
		names = []string{target}
	}
	var deadClause string
	if len(names) == 1 {
		deadClause = fmt.Sprintf("%s is dead", names[0])
	} else {
		deadClause = fmt.Sprintf("%s are dead", strings.Join(names, ", "))
	}
	return fmt.Sprintf(
		"This message was sent to %s but %s. Originating sender: %s. Original body:\n\n%s",
		target, deadClause, sender, body,
	)
}
