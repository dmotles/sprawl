// Package inboxprompt holds the inbox/interrupt prompt-formatter that both
// the legacy agentloop child harness and the unified-runtime supervisor path
// use to render pending queue entries into a turn prompt.
//
// QUM-555: the per-entry frame is now a single `<system-notification>` line
// naming the sender and the short message ID. The recipient pulls the body
// on demand via `sprawl messages read <short_id>` rather than receiving the
// full body inlined into every turn. Interrupt-class entries carry an
// `[interrupt]` marker inside the tag so the recipient can decide whether to
// preempt current work.
package inboxprompt

import (
	"fmt"
	"strings"
)

// Class is the delivery class of a queued message.
type Class string

// Recognized message classes.
const (
	ClassAsync     Class = "async"
	ClassInterrupt Class = "interrupt"
)

// Entry is one message in the per-agent harness queue.
type Entry struct {
	Seq        int      `json:"seq"`
	ID         string   `json:"id"`
	ShortID    string   `json:"short_id,omitempty"`
	Class      Class    `json:"class"`
	From       string   `json:"from"`
	Subject    string   `json:"subject"`
	Body       string   `json:"body"`
	ReplyTo    string   `json:"reply_to,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	EnqueuedAt string   `json:"enqueued_at"`
}

// DisplaySubject returns the human-facing subject for an inbox entry. When
// Subject is non-empty, returns it as-is. Otherwise falls back to the first
// non-empty line of Body, hard-truncated at 80 bytes (QUM-550). The fallback
// supports send_message entries which carry no explicit subject. Retained
// post-QUM-555 for non-prompt surfaces (TUI labels, future tooling) even
// though the rendered flush prompts no longer embed it.
func DisplaySubject(e Entry) string {
	if e.Subject != "" {
		return e.Subject
	}
	for _, line := range strings.Split(e.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 80 {
			return trimmed[:80]
		}
		return trimmed
	}
	return ""
}

// displayMessageID returns the short maildir ID when available, falling back
// to the queue UUID. The flush prompts cite this so `sprawl messages read
// <id>` accepts the value (ResolvePrefix matches ShortID first). Entries
// enqueued before ShortID was added round-trip with an empty ShortID and
// gracefully fall back to ID. See QUM-412.
func displayMessageID(e Entry) string {
	if e.ShortID != "" {
		return e.ShortID
	}
	return e.ID
}

// SplitByClass separates pending entries into (interrupts, asyncs) preserving
// original order within each slice.
func SplitByClass(entries []Entry) (interrupts, asyncs []Entry) {
	for _, e := range entries {
		if e.Class == ClassInterrupt {
			interrupts = append(interrupts, e)
		} else {
			asyncs = append(asyncs, e)
		}
	}
	return interrupts, asyncs
}

// BuildQueueFlushPrompt renders one `<system-notification>` line per pending
// async queue entry. The line names the sender and the short message ID the
// recipient should pass to `sprawl messages read` if it wants to engage. No
// body is inlined; no footer prose is emitted. Returns "" if entries is empty.
func BuildQueueFlushPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "<system-notification>New message from %s. Read %s.</system-notification>\n",
			e.From, displayMessageID(e))
	}
	return b.String()
}

// BuildInterruptFlushPrompt renders one `<system-notification>` line per
// pending interrupt-class entry, tagged with `[interrupt]` so the recipient
// knows to consider preempting current work. Same shape as
// BuildQueueFlushPrompt — no inlined body, no footer prose. Returns "" if
// entries is empty.
func BuildInterruptFlushPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "<system-notification>[interrupt] New message from %s. Read %s.</system-notification>\n",
			e.From, displayMessageID(e))
	}
	return b.String()
}
