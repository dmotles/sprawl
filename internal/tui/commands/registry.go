// Package commands defines the registry of slash commands available in the
// sprawl enter TUI command palette. Commands are either UI-level (handled
// entirely inside the TUI, e.g. /exit, /help) or prompt-injection (a fixed
// template sent through the bridge to Claude, e.g. /handoff).
package commands

import (
	"sort"
	"strings"
	"unicode"
)

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
	// KindAttach commands prompt for a free-form argument line (file paths +
	// optional quoted prompt) and dispatch an AttachMsg on Enter. Handled by
	// the palette in a dedicated attach-argument mode (QUM-860).
	KindAttach
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
	// ActionShowUsage opens the /usage modal (QUM-721).
	ActionShowUsage
	// ActionToggleTree toggles the agent-tree modal overlay (QUM-733 5b).
	ActionToggleTree
)

// Command describes a palette entry.
type Command struct {
	Name           string // e.g. "/exit"
	Description    string // one-line summary shown in the palette
	Kind           Kind
	Action         Action // for KindUI
	PromptTemplate string // for KindPromptInjection
	// TakesArgs reports whether the command accepts a trailing argument line
	// (e.g. /switch <name>, /attach <path...> "prompt"). Submit-time routing
	// and the slice-B popover consult this; v1 is a binary flag (QUM-863).
	TakesArgs bool
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
		Name:        "/tree",
		Description: "Show full agent tree",
		Kind:        KindUI,
		Action:      ActionToggleTree,
	},
	{
		Name:           "/handoff",
		Description:    "Consolidate session and start fresh with updated memory",
		Kind:           KindPromptInjection,
		PromptTemplate: HandoffPromptTemplate,
	},
	{
		Name:        "/usage",
		Description: "Show token & cost usage by agent (1-5 select time window: 24h/week/month/year/all)",
		Kind:        KindUI,
		Action:      ActionShowUsage,
	},
	{
		Name:        "/switch",
		Description: "Switch observed agent (fuzzy match on name)",
		Kind:        KindAgentSwitch,
		TakesArgs:   true,
	},
	{
		Name:        "/attach",
		Description: "Attach local image(s) to a turn: <path...> \"prompt\"",
		Kind:        KindAttach,
		TakesArgs:   true,
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

// AllSorted returns a copy of the registry sorted alphabetically by Name.
// Slice B (the popover) renders in alphabetical order; All() preserves
// registration order for the current palette (QUM-863).
func AllSorted() []Command {
	out := All()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Match parses the leading whitespace-delimited token of input and, if it
// exactly matches a registered command name (case-insensitively), returns that
// command and the trimmed remainder as its args. Matching is exact on the token
// — not prefix or fuzzy — so an unregistered leading-slash prompt (e.g.
// "/etc/hosts is broken") returns ok=false and is passed through to Claude
// unchanged. (QUM-863)
func Match(input string) (cmd Command, args string, ok bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return Command{}, "", false
	}
	token := trimmed
	rest := ""
	if i := strings.IndexFunc(trimmed, unicode.IsSpace); i >= 0 {
		token = trimmed[:i]
		rest = strings.TrimSpace(trimmed[i:])
	}
	for _, c := range registry {
		if strings.EqualFold(c.Name, token) {
			return c, rest, true
		}
	}
	return Command{}, "", false
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

// FilterSorted is Filter with the result sorted alphabetically by Name. The
// inline command popover (QUM-864) renders matches in alphabetical order.
func FilterSorted(prefix string) []Command {
	out := Filter(prefix)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
