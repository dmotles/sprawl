package tmux

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const (
	DefaultNamespace = "⚡"
	DefaultRootName  = "weave"
	BranchSeparator  = "├"
	RootWindowName   = "weave"
)

// AccentColor represents a tmux color with human-readable aliases.
type AccentColor struct {
	Name    string   // tmux colour name, e.g. "colour39"
	Aliases []string // human-readable names, e.g. ["cyan", "DeepSkyBlue1"]
}

// AccentColors is the structured palette with aliases for each color.
var AccentColors = []AccentColor{
	{Name: "colour39", Aliases: []string{"cyan", "DeepSkyBlue1"}},
	{Name: "colour198", Aliases: []string{"magenta", "DeepPink1"}},
	{Name: "colour82", Aliases: []string{"green", "Chartreuse2"}},
	{Name: "colour208", Aliases: []string{"orange", "DarkOrange"}},
	{Name: "colour141", Aliases: []string{"purple", "MediumPurple1"}},
	{Name: "colour196", Aliases: []string{"red", "Red1"}},
	{Name: "colour220", Aliases: []string{"yellow", "Gold1"}},
	{Name: "colour43", Aliases: []string{"teal", "DarkCyan"}},
	{Name: "colour205", Aliases: []string{"pink", "HotPink"}},
	{Name: "colour69", Aliases: []string{"blue", "CornflowerBlue"}},
}

// AccentColorPool is a curated palette of tmux colors that look good on dark backgrounds.
// Used to assign a per-namespace accent color during sprawl init.
var AccentColorPool = []string{
	"colour39",  // cyan / DeepSkyBlue1
	"colour198", // magenta / DeepPink1
	"colour82",  // green / Chartreuse2
	"colour208", // orange / DarkOrange
	"colour141", // purple / MediumPurple1
	"colour196", // red / Red1
	"colour220", // yellow / Gold1
	"colour43",  // teal / DarkCyan
	"colour205", // pink / HotPink
	"colour69",  // blue / CornflowerBlue
}

// FindColor looks up a color by its tmux name or any alias (case-insensitive).
func FindColor(nameOrAlias string) (AccentColor, bool) {
	lower := strings.ToLower(nameOrAlias)
	for _, c := range AccentColors {
		if strings.ToLower(c.Name) == lower {
			return c, true
		}
		for _, a := range c.Aliases {
			if strings.ToLower(a) == lower {
				return c, true
			}
		}
	}
	return AccentColor{}, false
}

// PickAccentColor randomly selects an accent color from the curated palette.
func PickAccentColor() string {
	return AccentColorPool[rand.IntN(len(AccentColorPool))] //nolint:gosec // G404: weak RNG is fine for cosmetic color selection
}

// PickAccentColorExcluding randomly selects an accent color, excluding the given one.
func PickAccentColorExcluding(current string) string {
	candidates := make([]string, 0, len(AccentColorPool)-1)
	for _, c := range AccentColorPool {
		if c != current {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return PickAccentColor()
	}
	return candidates[rand.IntN(len(candidates))] //nolint:gosec // G404: weak RNG is fine for cosmetic color selection
}

// NamespacePool is a curated list of electric/cyberpunk emojis used for auto-selecting
// a unique namespace when running sprawl init.
var NamespacePool = []string{
	"⚡", "🔮", "💠", "🌃", "💜", "🔷", "✴️", "💎", "🌆", "🛰️",
	"🌐", "🔌", "💡", "🧿", "☄️",
}

// RootSessionName returns the tmux session name for the root agent.
// The root session is just the namespace (e.g. "⚡"). The root agent name
// becomes the window name within that session, not part of the session name.
func RootSessionName(namespace string) string {
	return namespace
}

// ChildrenSessionName returns the tmux session name for a parent's children.
// Format: {namespace}{treePath}├ e.g. "⚡weave├" or "⚡weave├ash├"
func ChildrenSessionName(namespace, treePath string) string {
	return namespace + treePath + BranchSeparator
}

// PickNamespace scans existing tmux sessions and returns the first emoji from
// NamespacePool that isn't already in use as a session name prefix.
// If tmux has no server running, returns DefaultNamespace.
// If all are taken, returns a fallback using the pool size as index.
func PickNamespace(runner Runner) string {
	sessions, err := runner.ListSessionNames()
	if err != nil {
		// tmux not running or no server — all namespaces are free.
		return DefaultNamespace
	}

	used := make(map[string]bool)
	for _, s := range sessions {
		for _, emoji := range NamespacePool {
			if s == RootSessionName(emoji) {
				used[emoji] = true
			}
		}
	}

	for _, emoji := range NamespacePool {
		if !used[emoji] {
			return emoji
		}
	}

	// All taken — fallback.
	return fmt.Sprintf("sprawl-%d-", len(NamespacePool))
}

// Runner abstracts tmux operations for testability.
type Runner interface {
	HasSession(name string) bool
	HasWindow(sessionName, windowName string) bool
	NewSession(name string, env map[string]string, shellCmd string) error
	NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error
	NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error
	KillWindow(sessionName, windowName string) error
	ListWindowPIDs(sessionName, windowName string) ([]int, error)
	ListSessionNames() ([]string, error)
	SendKeys(sessionName, windowName string, keys string) error
	Attach(name string) error
	SourceFile(sessionName, filePath string) error
}

// RealRunner implements Runner using the real tmux binary.
type RealRunner struct {
	TmuxPath string
}

// FindTmux locates the tmux binary in PATH.
func FindTmux() (string, error) {
	return exec.LookPath("tmux")
}

// exactTarget returns a tmux target string that forces exact session name matching.
// Without this, tmux performs prefix matching on -t arguments.
func exactTarget(name string) string {
	return "=" + name
}

// HasSession returns true if a tmux session with the given name exists.
func (r *RealRunner) HasSession(name string) bool {
	cmd := exec.Command(r.TmuxPath, "has-session", "-t", exactTarget(name)) //nolint:gosec // arguments are not user-controlled
	return cmd.Run() == nil
}

// HasWindow returns true if a tmux window with the given name exists in the session.
// It checks both the exit code AND the output content, because tmux display-message
// can return exit 0 with empty output for non-existent targets (QUM-191).
func (r *RealRunner) HasWindow(sessionName, windowName string) bool {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "display-message", "-t", target, "-p", "#{window_name}") //nolint:gosec // arguments are not user-controlled
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == windowName
}

// NewSession creates a new detached tmux session running the given shell command.
func (r *RealRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	args := []string{"new-session", "-d", "-s", name}

	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, shellCmd)

	cmd := exec.Command(r.TmuxPath, args...) //nolint:gosec // arguments are not user-controlled
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// NewSessionWithWindow creates a new detached tmux session with a named first window.
func (r *RealRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	args := []string{"new-session", "-d", "-s", sessionName, "-n", windowName}

	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, shellCmd)

	cmd := exec.Command(r.TmuxPath, args...) //nolint:gosec // arguments are not user-controlled
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// NewWindow adds a new named window to an existing tmux session.
func (r *RealRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	args := []string{"new-window", "-t", exactTarget(sessionName), "-n", windowName}

	for k, v := range env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, shellCmd)

	cmd := exec.Command(r.TmuxPath, args...) //nolint:gosec // arguments are not user-controlled
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	// Stderr is intentionally not set (defaults to nil / discarded) because callers
	// use NewWindow in a try-then-fallback pattern where failure is expected and
	// handled programmatically. Leaking tmux error messages to stderr confuses users.
	return cmd.Run()
}

// KillWindow closes a tmux window by name.
func (r *RealRunner) KillWindow(sessionName, windowName string) error {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "kill-window", "-t", target) //nolint:gosec // arguments are not user-controlled
	return cmd.Run()
}

// ListWindowPIDs returns the PIDs of processes running in the given tmux window.
func (r *RealRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "list-panes", "-t", target, "-F", "#{pane_pid}") //nolint:gosec // arguments are not user-controlled
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// ListSessionNames returns the names of all running tmux sessions.
func (r *RealRunner) ListSessionNames() ([]string, error) {
	cmd := exec.Command(r.TmuxPath, "list-sessions", "-F", "#{session_name}") //nolint:gosec // arguments are not user-controlled
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// SendKeys sends text to a specific tmux window, followed by Enter.
func (r *RealRunner) SendKeys(sessionName, windowName string, keys string) error {
	target := exactTarget(sessionName) + ":" + windowName
	cmd := exec.Command(r.TmuxPath, "send-keys", "-t", target, keys, "Enter") //nolint:gosec // arguments are not user-controlled
	return cmd.Run()
}

// Attach connects to the named tmux session. If called from inside an
// existing tmux session (TMUX env var is set), it uses switch-client to
// avoid nesting. Otherwise it replaces the current process with
// tmux attach-session via syscall.Exec.
func (r *RealRunner) Attach(name string) error {
	if IsInsideTmux() {
		args := []string{"tmux", "switch-client", "-t", exactTarget(name)}
		return syscall.Exec(r.TmuxPath, args, os.Environ()) //nolint:gosec // arguments are not user-controlled
	}
	args := []string{"tmux", "attach-session", "-t", exactTarget(name)}
	return syscall.Exec(r.TmuxPath, args, os.Environ()) //nolint:gosec // arguments are not user-controlled
}

// SourceFile sources a tmux config file, applying it to the tmux server.
// The config should use session-targeted set-option where possible.
func (r *RealRunner) SourceFile(_ string, filePath string) error {
	cmd := exec.Command(r.TmuxPath, "source-file", filePath) //nolint:gosec // arguments are not user-controlled
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// IsInsideTmux returns true if the current process is running inside a tmux session.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// BuildShellCmd joins a command and its arguments into a single shell command string
// suitable for passing to tmux new-session.
func BuildShellCmd(binary string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, ShellQuote(binary))
	for _, a := range args {
		parts = append(parts, ShellQuote(a))
	}
	return strings.Join(parts, " ")
}

// ShellQuote wraps a string in single quotes, escaping any embedded single quotes.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}
