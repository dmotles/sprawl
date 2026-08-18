package agentloop

// The prompt-shape tests live in internal/inboxprompt. This case cannot: it
// exercises internal/messages ID resolution, and inboxprompt cannot import
// messages. It stays in this package for that reason alone, and calls
// inboxprompt directly — agentloop's re-export shim was deleted as dead.

import (
	"regexp"
	"testing"

	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/messages"
)

// TestBuildQueueFlushPrompt_HintIDResolvesViaMessages is an integration test:
// deliver a real maildir message via messages.Send, surface the resulting
// ShortID through an agentloop.Entry, build the flush prompt, parse the ID
// out of the `mcp__sprawl__messages_read(id=$ID)` clause, and confirm
// messages.ResolvePrefix can resolve it. This guards the contract that the
// notification cites an ID format the MCP `messages_read` tool actually
// accepts.
func TestBuildQueueFlushPrompt_HintIDResolvesViaMessages(t *testing.T) {
	root := t.TempDir()
	const recipient = "child-alpha"

	shortID, err := messages.Send(root, "weave", recipient, "subj", "body")
	if err != nil {
		t.Fatalf("messages.Send: %v", err)
	}
	if shortID == "" {
		t.Fatalf("messages.Send returned empty shortID")
	}

	entries := []Entry{{ID: "uuid-irrelevant", ShortID: shortID, From: "weave", Subject: "subj", Body: "body"}}
	p := inboxprompt.BuildQueueFlushPrompt(entries)

	re := regexp.MustCompile(`mcp__sprawl__messages_read\(id=([^)]+)\)`)
	m := re.FindStringSubmatch(p)
	if m == nil {
		t.Fatalf("could not find mcp__sprawl__messages_read(id=...) clause in prompt:\n%s", p)
	}
	cited := m[1]

	full, err := messages.ResolvePrefix(root, recipient, cited)
	if err != nil {
		t.Fatalf("ResolvePrefix(%q): %v", cited, err)
	}
	if full == "" {
		t.Fatalf("ResolvePrefix(%q) returned empty full ID", cited)
	}
}
