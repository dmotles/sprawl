// Package inboxprompt holds the inbox/interrupt prompt-formatter that both
// the legacy agentloop child harness and the unified-runtime supervisor path
// use to render pending queue entries into a turn prompt. Extracted from
// internal/agentloop in QUM-437 so the unified path no longer ships a stub
// "You have new messages" placeholder. Output must remain byte-identical to
// the prior agentloop implementation.
package inboxprompt

import (
	"fmt"
	"strings"
)

// Queue flush frame size caps per docs/designs/messaging-overhaul.md §8.6.
const (
	// MaxQueueFlushBodyBytes is the per-message body cap before truncation.
	MaxQueueFlushBodyBytes = 2 * 1024
	// MaxQueueFlushTotalBytes is the aggregate body cap across a single frame.
	MaxQueueFlushTotalBytes = 10 * 1024
)

// resumeHintPrefix is the tag prefix used by supervisor.SendInterrupt to
// smuggle a free-form resume_hint through the queue entry's Tags without
// needing a dedicated field. See internal/supervisor/real.go:SendInterrupt.
const resumeHintPrefix = "resume_hint:"

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
// to the queue UUID. The truncation hints in the flush prompts cite this so
// `sprawl messages read <id>` accepts the value (ResolvePrefix matches
// ShortID first). Entries enqueued before ShortID was added round-trip with
// an empty ShortID and gracefully fall back to ID. See QUM-412.
func displayMessageID(e Entry) string {
	if e.ShortID != "" {
		return e.ShortID
	}
	return e.ID
}

// extractResumeHint returns the value after the first "resume_hint:" tag in
// e.Tags, or "" if none.
func extractResumeHint(e Entry) string {
	for _, tag := range e.Tags {
		if strings.HasPrefix(tag, resumeHintPrefix) {
			return tag[len(resumeHintPrefix):]
		}
	}
	return ""
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

// BuildQueueFlushPrompt renders the notification frame that bundles N pending
// async queue entries into a single user turn, per §4.5.1. The frame inlines
// the subject, sender, tags, and (size-bounded) body of each entry. Returns
// "" if entries is empty.
func BuildQueueFlushPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[inbox] You received %d message(s) since the last turn:\n\n", len(entries))
	totalBody := 0
	for i, e := range entries {
		tagStr := ""
		if len(e.Tags) > 0 {
			tagStr = " [" + strings.Join(e.Tags, ",") + "]"
		}
		fmt.Fprintf(&b, "%d. from %s%s  subject: %s\n", i+1, e.From, tagStr, e.Subject)
		body := e.Body
		truncated := false
		if len(body) > MaxQueueFlushBodyBytes {
			body = body[:MaxQueueFlushBodyBytes]
			truncated = true
		}
		remaining := MaxQueueFlushTotalBytes - totalBody
		if remaining < 0 {
			remaining = 0
		}
		if len(body) > remaining {
			body = body[:remaining]
			truncated = true
		}
		for _, line := range strings.Split(body, "\n") {
			b.WriteString("   ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		if truncated {
			fmt.Fprintf(&b, "   ...[truncated — run `sprawl messages read %s` for full body]\n", displayMessageID(e))
		}
		b.WriteString("\n")
		totalBody += len(body)
	}
	b.WriteString("Continue your current work unless a message tells you otherwise.\n")
	return b.String()
}

// BuildInterruptFlushPrompt renders the §4.5.2 interrupt frame for one or
// more interrupt-class queue entries. The frame names the in-flight work
// (via the first entry's resume_hint, falling back to a generic description)
// and the resume/stop contract. Returns "" if entries is empty.
func BuildInterruptFlushPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	hint := extractResumeHint(entries[0])
	if hint == "" {
		hint = "your previous task"
	}

	var b strings.Builder
	senders := entries[0].From
	if len(entries) > 1 {
		senders = fmt.Sprintf("%d senders", len(entries))
	}
	fmt.Fprintf(&b, "[interrupt] %s has injected an important message. You were in the middle of: %s.\n\n", senders, hint)

	totalBody := 0
	for i, e := range entries {
		if len(entries) > 1 {
			fmt.Fprintf(&b, "--- %d of %d (from %s) ---\n", i+1, len(entries), e.From)
		}
		fmt.Fprintf(&b, "Subject: %s\n\n", e.Subject)
		body := e.Body
		truncated := false
		if len(body) > MaxQueueFlushBodyBytes {
			body = body[:MaxQueueFlushBodyBytes]
			truncated = true
		}
		remaining := MaxQueueFlushTotalBytes - totalBody
		if remaining < 0 {
			remaining = 0
		}
		if len(body) > remaining {
			body = body[:remaining]
			truncated = true
		}
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
		if truncated {
			fmt.Fprintf(&b, "...[truncated — run `sprawl messages read %s` for full body]\n", displayMessageID(e))
		}
		b.WriteString("\n")
		totalBody += len(body)
	}
	b.WriteString("After reading, decide whether to:\n")
	b.WriteString("- resume the interrupted work (default), OR\n")
	b.WriteString("- stop / change direction if the message says so.\n")
	return b.String()
}
