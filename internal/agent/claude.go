package agent

import "os/exec"

// LaunchOpts holds the options for launching a Claude Code instance.
type LaunchOpts struct {
	SystemPrompt              string
	Tools                     []string
	AllowedTools              []string
	DisallowedTools           []string
	Name                      string
	Bare                      bool
	DangerouslySkipPermissions bool
}

// Launcher builds claude CLI arguments and finds the binary.
type Launcher interface {
	BuildArgs(opts LaunchOpts) []string
	FindBinary() (string, error)
}

// RealLauncher implements Launcher using the real claude CLI.
type RealLauncher struct{}

// FindBinary locates the claude binary in PATH.
func (r *RealLauncher) FindBinary() (string, error) {
	return exec.LookPath("claude")
}

// BuildArgs constructs the claude CLI arguments from LaunchOpts.
func (r *RealLauncher) BuildArgs(opts LaunchOpts) []string {
	var args []string

	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}

	if len(opts.Tools) > 0 {
		for _, t := range opts.Tools {
			args = append(args, "--tools", t)
		}
	}

	if len(opts.AllowedTools) > 0 {
		for _, t := range opts.AllowedTools {
			args = append(args, "--allowed-tools", t)
		}
	}

	if len(opts.DisallowedTools) > 0 {
		for _, t := range opts.DisallowedTools {
			args = append(args, "--disallowed-tools", t)
		}
	}

	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}

	if opts.Bare {
		args = append(args, "--bare")
	}

	if opts.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	return args
}
