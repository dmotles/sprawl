// Package commands defines the registry of slash commands available in the
// sprawl enter TUI command palette. Commands are either UI-level (handled
// entirely inside the TUI, e.g. /exit, /help) or prompt-injection (a fixed
// template sent through the bridge to Claude, e.g. /handoff).
package commands

import "strings"

// Kind categorizes how the palette dispatches a command.
type Kind int

const (
	// KindUI commands are handled entirely inside the TUI (no bridge message).
	KindUI Kind = iota
	// KindPromptInjection commands send PromptTemplate to Claude as a user
	// message via the bridge.
	KindPromptInjection
	// KindAgentSwitch commands prompt for an agent name, fuzzy-matched, and
	// dispatch an agent-switch on selection. Handled by the palette in a
	// dedicated agent-selection mode.
	KindAgentSwitch
)

// Action enumerates the UI-level actions a KindUI command can trigger.
type Action int

const (
	// ActionNone is the zero value (unused for KindUI commands).
	ActionNone Action = iota
	// ActionQuit requests an immediate app quit (same semantics as the
	// Ctrl-C-confirmed path).
	ActionQuit
	// ActionToggleHelp toggles the help overlay (same semantics as F1).
	ActionToggleHelp
)

// Command describes a palette entry.
type Command struct {
	Name           string // e.g. "/exit"
	Description    string // one-line summary shown in the palette
	Kind           Kind
	Action         Action // for KindUI
	PromptTemplate string // for KindPromptInjection
}

// registry holds the stable, ordered list of known commands. Order matters
// for palette display.
var registry = []Command{
	{
		Name:        "/exit",
		Description: "Quit sprawl enter",
		Kind:        KindUI,
		Action:      ActionQuit,
	},
	{
		Name:        "/help",
		Description: "Show key bindings and help",
		Kind:        KindUI,
		Action:      ActionToggleHelp,
	},
	{
		Name:           "/handoff",
		Description:    "Consolidate session and start fresh with updated memory",
		Kind:           KindPromptInjection,
		PromptTemplate: HandoffPromptTemplate,
	},
	{
		Name:        "/switch",
		Description: "Switch observed agent (fuzzy match on name)",
		Kind:        KindAgentSwitch,
	},
}

// FuzzyMatchAgents returns names where the lowercase query appears as a
// subsequence of the lowercase name. Empty query returns all names in input
// order. Results preserve input order (stable).
func FuzzyMatchAgents(query string, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	if query == "" {
		out := make([]string, len(names))
		copy(out, names)
		return out
	}
	q := strings.ToLower(query)
	out := make([]string, 0, len(names))
	for _, n := range names {
		if subsequenceMatch(q, strings.ToLower(n)) {
			out = append(out, n)
		}
	}
	return out
}

func subsequenceMatch(needle, haystack string) bool {
	i := 0
	for j := 0; j < len(haystack) && i < len(needle); j++ {
		if haystack[j] == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

// All returns a copy of the registry in stable registration order.
func All() []Command {
	out := make([]Command, len(registry))
	copy(out, registry)
	return out
}

// Filter returns commands whose name (without leading slash) starts with the
// given prefix, case-insensitively. Empty prefix returns All(). Stable order.
func Filter(prefix string) []Command {
	prefix = strings.ToLower(prefix)
	if prefix == "" {
		return All()
	}
	var out []Command
	for _, c := range registry {
		name := strings.ToLower(strings.TrimPrefix(c.Name, "/"))
		if strings.HasPrefix(name, prefix) {
			out = append(out, c)
		}
	}
	return out
}
