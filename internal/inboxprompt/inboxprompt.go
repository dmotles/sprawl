// Package inboxprompt holds the inbox/interrupt prompt-formatter that both
// the legacy agentloop child harness and the unified-runtime supervisor path
// use to render pending queue entries into a turn prompt.
//
// QUM-555: the per-entry frame is a single `<system-notification>` line
// naming the sender and the short message ID. The recipient pulls the body
// on demand rather than receiving the full body inlined into every turn.
//
// QUM-556: the line names the canonical MCP tool `mcp__sprawl__messages_read`
// in function-call shape so agents pattern-match it against their registered
// tool list — the bare verb "Read" was ambiguous with the legacy CLI form
// and triggered the wrong path in practice.
//
// QUM-562: each `<system-notification>` now carries a `type` attribute so the
// TUI parser (internal/tui/messages.go) can branch on signal kind without
// string-sniffing the body. Two wire shapes:
//
//	<system-notification type="message">From $AGENT — mcp__sprawl__messages_read(id=$ID)</system-notification>
//	<system-notification type="message" interrupt="true">[interrupt] From $AGENT — mcp__sprawl__messages_read(id=$ID)</system-notification>
//
// QUM-1186 deleted a third, type="status_change", shape. The TUI parser still
// recognises it so pre-QUM-1186 transcripts replay, but nothing writes it.
//
// The inner `[interrupt]` body marker is retained on interrupt-class entries
// for human-readability when the wrapper is stripped from rendered output;
// the `interrupt="true"` attribute is the machine-parseable channel.
// Untyped legacy tags (pre-QUM-562 transcripts) replay as type="message".
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

// displayMessageID returns the short maildir ID when available, falling back
// to the queue UUID. The flush prompts cite this as the `id=` argument of
// `mcp__sprawl__messages_read(...)` (ResolvePrefix on the MCP tool side
// matches ShortID first). Entries enqueued before ShortID was added
// round-trip with an empty ShortID and gracefully fall back to ID. See
// QUM-412.
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
// async queue entry. The line names the sender and cites the canonical MCP
// tool `mcp__sprawl__messages_read` in function-call form with the entry's
// id — the fully-qualified tool name maximizes pattern-match against the
// recipient's registered tool list (QUM-556). No body is inlined; no footer
// prose is emitted. Returns "" if entries is empty.
func BuildQueueFlushPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "<system-notification type=\"message\">From %s — mcp__sprawl__messages_read(id=%s)</system-notification>\n",
			e.From, displayMessageID(e))
	}
	return b.String()
}

// BuildInterruptFlushPrompt renders one `<system-notification>` line per
// pending interrupt-class entry. QUM-562: the line carries `type="message"
// interrupt="true"` attributes for the TUI parser, AND keeps the inner
// `[interrupt]` body marker so the body remains self-describing once the
// wrapper is stripped from rendered output. Same shape otherwise as
// BuildQueueFlushPrompt — `mcp__sprawl__messages_read(id=<id>)` citation,
// no inlined body, no footer prose. Returns "" if entries is empty.
func BuildInterruptFlushPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From %s — mcp__sprawl__messages_read(id=%s)</system-notification>\n",
			e.From, displayMessageID(e))
	}
	return b.String()
}
