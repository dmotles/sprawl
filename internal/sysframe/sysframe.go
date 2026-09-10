// Package sysframe builds the marked `<system-notification>` envelopes that
// deliver engine-authored content to an agent's stdin (QUM-1348).
//
// WHY THE MARKUP IS BUILT HERE AND NOT STORED IN THE EVENT LOG. The obvious
// alternative is for the dispatcher to write the framed string straight into
// the `spawn_requested` payload. It was rejected: the log is append-only, so a
// tag name recorded in a payload is pinned forever, and the renderer's
// vocabulary would become part of the durable wire format. Framing at the
// delivery seam keeps the log prose and lets the tag evolve.
//
// A leaf package on purpose. It imports only the standard library, so both
// internal/store's dependency direction and internal/dispatchadapt's reason for
// existing (keeping diffs out of internal/supervisor) survive intact.
//
// The tag vocabulary is duplicated in internal/tui/messages.go rather than
// imported from here — folding the existing emitters (internal/inboxprompt,
// internal/runtime) into this package would pull a large e2e bill for a
// cosmetic win. A cross-package constant-equality test pins the two together.
package sysframe

import "strings"

// Tag is the element name every system frame uses. Matched PREFIX-ONLY by the
// renderer (internal/tui/messages.go), which is why attributes are safe to add.
const Tag = "system-notification"

// TypeGoal is the `type` attribute for an engine-driven goal brief. Unknown
// types fall back to the message class in the renderer, so emitting this is
// safe against a TUI that has not learned it yet.
const TypeGoal = "goal"

// Preamble is the fixed first line of every goal envelope.
//
// It is LOAD-BEARING, not decoration. The renderer treats a body starting with
// the literal `[interrupt]` as an interrupt for back-compat with pre-attribute
// transcripts, so a goal whose text happened to begin that way would render
// with the interrupt glyph. A constant first line makes operator text
// structurally unable to occupy byte 0.
const Preamble = "You have been spawned with a goal. Complete it, then close it with report_result."

// Goal wraps a goal brief in a marked envelope for delivery as an agent's
// first message.
//
// The result must NEVER be routed through UnifiedRuntime.WriteSystemMessage.
// That path applies boundSystemFrame, whose line dedup is lossless for
// notification citations but LOSSY for prose (a goal with two identical lines
// would silently lose one), and whose 8192-byte truncation cuts on a line
// boundary — dropping the closing tag, which makes the renderer fail to peel
// the envelope and show raw markup instead. The initial-prompt seam this frame
// travels on already bypasses it.
func Goal(brief string) string {
	return "<" + Tag + " type=\"" + TypeGoal + "\">\n" +
		Preamble + "\n\n" +
		neutralize(brief) + "\n" +
		"</" + Tag + ">"
}

// IsGoalFrame reports whether s is a well-formed goal envelope of the exact
// shape Goal emits. It is the switch that decides whether a spawn prompt is
// injected VERBATIM or written to a prompt file, which makes it a security
// boundary rather than a formatting hint: a false positive delivers arbitrary
// operator text to an agent's stdin unneutralized.
//
// WHY THIS IS NOT A PREFIX CHECK. The renderer matches the tag prefix-only, and
// mirroring that here was the original implementation and a real hole: a spawn
// prompt is operator-authored text, so the one input that satisfies a prefix
// check is the one crafted to exploit the envelope. Such a string was handed
// straight through — forging a notification class (`interrupt`, `message`) the
// engine never emitted, and peeling any trailing text as further envelopes.
//
// The four conditions below are jointly unforgeable for the property that
// matters. A forged extra envelope necessarily adds a second open tag, and
// neutralize guarantees a genuine frame's body cannot contain one — so
// "exactly one open tag, exactly one close tag, and the close tag is the last
// thing in the string" cannot be met by anything carrying a second envelope or
// trailing loose text. TestIsGoalFrame_AcceptsEveryFrameGoalProduces pins the
// other direction, so the predicate can never reject our own output.
func IsGoalFrame(s string) bool {
	t := strings.TrimSpace(s)
	openTag, closeTag := "<"+Tag, "</"+Tag+">"
	return strings.HasPrefix(t, openTag+" type=\""+TypeGoal+"\">") &&
		strings.HasSuffix(t, closeTag) &&
		strings.Count(t, openTag) == 1 &&
		strings.Count(t, closeTag) == 1
}

// neutralize defangs tag syntax inside operator-authored text.
//
// A brief is arbitrary text. An embedded close tag would end the envelope early
// at the renderer's first-close anchor, stranding the rest of the brief outside
// it and letting a following open tag forge a second, attacker-chosen
// notification. Entity-escaping the angle brackets keeps the text readable
// while making it inert.
//
// The two rules are ASYMMETRIC on purpose, and the asymmetry is load-bearing
// rather than an oversight: the close-tag rule escapes both brackets because
// the tag is fixed and complete, while the open-tag rule escapes only `<Tag`
// and leaves the eventual `>` alone because an open tag carries arbitrary
// attributes and its end is not at a known offset. What both guarantee is the
// property IsGoalFrame depends on — that no `<Tag` or `</Tag>` substring
// survives in a body — so do not "tidy" this into symmetric escaping without
// re-reading that predicate.
func neutralize(brief string) string {
	r := strings.NewReplacer(
		"</"+Tag+">", "&lt;/"+Tag+"&gt;",
		"<"+Tag, "&lt;"+Tag,
	)
	return r.Replace(brief)
}
