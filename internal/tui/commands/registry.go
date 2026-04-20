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
