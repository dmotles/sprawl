// Package claude provides types and utilities for launching Claude Code CLI subprocesses.
package claude

// LaunchOpts holds all CLI argument fields for launching a Claude Code instance
// in stream-json subprocess mode (the only launch mode left after the tmux
// teardown).
type LaunchOpts struct {
	SystemPrompt     string
	SystemPromptFile string
	SessionID        string
	Model            string
	Effort           string
	PermissionMode   string

	Print          bool   // -p (non-interactive print-and-exit mode)
	InputFormat    string // --input-format
	OutputFormat   string // --output-format
	Verbose        bool   // --verbose
	Resume         bool   // --resume (uses SessionID value)
	SettingSources string // --setting-sources

	AllowedTools    []string
	DisallowedTools []string
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

	for _, t := range o.AllowedTools {
		args = append(args, "--allowed-tools", t)
	}
	for _, t := range o.DisallowedTools {
		args = append(args, "--disallowed-tools", t)
	}

	if o.Resume {
		args = append(args, "--resume", o.SessionID)
	}
	if o.SettingSources != "" {
		args = append(args, "--setting-sources", o.SettingSources)
	}

	return args
}
