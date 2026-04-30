// Package claude provides types and utilities for launching Claude Code CLI subprocesses.
package claude

// LaunchOpts holds all CLI argument fields for launching a Claude Code instance.
// Both interactive (tmux) and subprocess (stream-json) launch paths use this
// single type, setting different subsets of fields.
type LaunchOpts struct {
	// Shared fields
	SystemPrompt     string
	SystemPromptFile string
	SessionID        string
	Model            string
	Effort           string
	PermissionMode   string

	// Stream-json / subprocess mode
	Print          bool   // -p (non-interactive print-and-exit mode)
	InputFormat    string // --input-format
	OutputFormat   string // --output-format
	Verbose        bool   // --verbose
	Resume         bool   // --resume (uses SessionID value)
	SettingSources string // --setting-sources

	// Interactive / tmux mode
	InitialPrompt              string
	Tools                      []string
	AllowedTools               []string
	DisallowedTools            []string
	Name                       string
	Agents                     string
	Bare                       bool
	DangerouslySkipPermissions bool
}

// BuildArgs constructs the claude CLI argument slice from the opts fields.
//
// When Resume is true, BuildArgs emits `--resume <SessionID>` and omits
// `--session-id` (since --resume supplies its own session context).
// System prompt flags (--system-prompt-file, --system-prompt) are always
// emitted when set, regardless of Resume, so the resumed session picks up
// the current system prompt.
func (o LaunchOpts) BuildArgs() []string {
	var args []string

	if o.Print {
		args = append(args, "-p")
	}
	if o.InputFormat != "" {
		args = append(args, "--input-format", o.InputFormat)
	}
	if o.OutputFormat != "" {
		args = append(args, "--output-format", o.OutputFormat)
	}
	if o.Verbose {
		args = append(args, "--verbose")
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.Effort != "" {
		args = append(args, "--effort", o.Effort)
	}
	if o.PermissionMode != "" {
		args = append(args, "--permission-mode", o.PermissionMode)
	}
	if o.SessionID != "" && !o.Resume {
		args = append(args, "--session-id", o.SessionID)
	}

	if o.SystemPromptFile != "" {
		args = append(args, "--system-prompt-file", o.SystemPromptFile)
	} else if o.SystemPrompt != "" {
		args = append(args, "--system-prompt", o.SystemPrompt)
	}

	for _, t := range o.Tools {
		args = append(args, "--tools", t)
	}
	for _, t := range o.AllowedTools {
		args = append(args, "--allowed-tools", t)
	}
	for _, t := range o.DisallowedTools {
		args = append(args, "--disallowed-tools", t)
	}

	if o.Name != "" {
		args = append(args, "--name", o.Name)
	}
	if o.Agents != "" {
		args = append(args, "--agents", o.Agents)
	}
	if o.Bare {
		args = append(args, "--bare")
	}
	if o.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	if o.Resume {
		args = append(args, "--resume", o.SessionID)
	}
	if o.SettingSources != "" {
		args = append(args, "--setting-sources", o.SettingSources)
	}

	// InitialPrompt is appended as a positional argument (must come last,
	// after all flags). This is the prompt Claude begins working on when it
	// launches. NOTE: do NOT use -p/--print here — that flag enables
	// non-interactive mode (print-and-exit), which would cause the agent to
	// terminate after one response instead of staying alive in the tmux session.
	if o.InitialPrompt != "" {
		args = append(args, o.InitialPrompt)
	}

	return args
}
