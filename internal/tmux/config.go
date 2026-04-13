package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigParams holds the parameters needed to generate a tmux config.
type ConfigParams struct {
	AccentColor string // tmux colour name, e.g. "colour39"
	Namespace   string // namespace emoji, e.g. "⚡"
	Version     string // sprawl version, e.g. "v0.1.3"
	SprawlRoot  string // absolute path to project root
}

// GenerateConfig returns the contents of the sprawl tmux.conf as a string.
// The generated config provides a cyberpunk-themed status bar with branding,
// dynamic agent/mail counts, and per-namespace accent colors.
// It does NOT set any keybindings — those come from the user's own config.
func GenerateConfig(params ConfigParams) string {
	accent := params.AccentColor
	if accent == "" {
		accent = "colour39"
	}
	ns := params.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	root := params.SprawlRoot
	version := params.Version
	if version == "" {
		version = "dev"
	}

	// Compute repo basename for display in status-right, truncated if too long.
	repoName := filepath.Base(root)
	const maxRepoLen = 15
	if len(repoName) > maxRepoLen {
		repoName = "..." + repoName[len(repoName)-(maxRepoLen-3):]
	}

	// Single-quote the root path for use inside #() shell commands in the status bar.
	// The outer status-right value uses double quotes, so single-quoting the path
	// (via ShellQuote) avoids nested double-quote conflicts.
	quotedRoot := ShellQuote(root)

	// Shell commands for dynamic status bar content (run every status-interval).
	// These must be fast (sub-millisecond) since they run every 5 seconds.
	// Mail count uses $(tmux display-message -p '#{window_name}') to resolve the
	// current window name dynamically — this works correctly in child sessions
	// where multiple agent windows share one session environment.
	agentCount := fmt.Sprintf("#(ls %s/.sprawl/agents/*.json 2>/dev/null | wc -l | tr -d ' ')", quotedRoot)
	mailCount := fmt.Sprintf("#(find %s/.sprawl/messages/$(tmux display-message -p '#{window_name}')"+
		"/new/ -type f 2>/dev/null | wc -l | tr -d ' ')", quotedRoot)
	versionFile := fmt.Sprintf("#(cat %s/.sprawl/state/version 2>/dev/null || echo '%s')", quotedRoot, version)

	// Build the mail indicator: show count with icon when > 0, dim when 0
	mailIndicator := fmt.Sprintf(
		"#[fg=%s]#{?#{!=:%s,0},✉ %s ,}#[default]",
		accent, mailCount, mailCount,
	)

	var b strings.Builder

	// Header
	b.WriteString("# Sprawl tmux configuration — auto-generated, do not edit\n")
	b.WriteString("# Cyberpunk-themed status bar with per-namespace accent colors\n\n")

	// General settings (no keybindings)
	b.WriteString("set -g mouse on\n")
	b.WriteString("set -g history-limit 50000\n")
	b.WriteString("set -g status-interval 5\n\n")

	// Status bar styling
	b.WriteString("set -g status-style 'bg=colour233,fg=colour245'\n")
	b.WriteString("set -g status-left-length 50\n")
	b.WriteString("set -g status-right-length 75\n\n")

	// Left status: namespace + branding + agent identity
	// Use #W (tmux built-in window name format variable) for agent identity — this
	// resolves per-window, which is correct for child sessions where multiple
	// agent windows share one session. Agent windows are named after the agent.
	b.WriteString(fmt.Sprintf(
		"set -g status-left '#[fg=colour233,bg=%s,bold] %s S P R A W L #[fg=%s,bg=colour235] #W #[fg=colour235,bg=colour233] '\n",
		accent, ns, accent,
	))

	// Right status: mail count + repo name + agent count + version
	b.WriteString(fmt.Sprintf(
		"set -g status-right \"%s#[fg=colour245,bg=colour233] │ %s │ agents: %s │ #[fg=%s]%s #[default]\"\n\n",
		mailIndicator, repoName, agentCount, accent, versionFile,
	))

	// Window list styling
	b.WriteString("set -g window-status-format '#[fg=colour245,bg=colour233] #I '\n")
	b.WriteString(fmt.Sprintf("set -g window-status-current-format '#[fg=colour233,bg=%s,bold] #I '\n\n", accent))

	// Pane borders
	b.WriteString("set -g pane-border-style 'fg=colour238'\n")
	b.WriteString(fmt.Sprintf("set -g pane-active-border-style 'fg=%s'\n\n", accent))

	// Message bar
	b.WriteString(fmt.Sprintf("set -g message-style 'fg=%s,bg=colour233,bold'\n", accent))
	b.WriteString(fmt.Sprintf("set -g message-command-style 'fg=%s,bg=colour233'\n", accent))

	return b.String()
}

// WriteConfig writes the generated tmux config to .sprawl/tmux.conf and returns the absolute path.
func WriteConfig(params ConfigParams) (string, error) {
	dir := filepath.Join(params.SprawlRoot, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return "", fmt.Errorf("creating .sprawl directory: %w", err)
	}

	content := GenerateConfig(params)
	path := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: world-readable config file is intentional
		return "", fmt.Errorf("writing tmux.conf: %w", err)
	}
	return path, nil
}
