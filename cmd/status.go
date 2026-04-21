package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/observe"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

type statusDeps struct {
	observeDeps observe.Deps
	getenv      func(string) string
	stdout      io.Writer
	stderr      io.Writer
}

var defaultStatusDeps *statusDeps

var (
	statusJSON   bool
	statusFamily string
	statusType   string
	statusParent string
	statusStatus string
	statusWatch  bool
	statusTail   int
)

// defaultWatchPoll is the activity-ring poll interval for `sprawl status --watch`.
const defaultWatchPoll = 500 * time.Millisecond

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "output as JSON array")
	statusCmd.Flags().StringVar(&statusFamily, "family", "", "filter by family")
	statusCmd.Flags().StringVar(&statusType, "type", "", "filter by type")
	statusCmd.Flags().StringVar(&statusParent, "parent", "", "filter by parent")
	statusCmd.Flags().StringVar(&statusStatus, "status", "", "filter by status")
	statusCmd.Flags().BoolVarP(&statusWatch, "watch", "w", false, "tail the activity ring across all agents")
	statusCmd.Flags().IntVar(&statusTail, "tail", 50, "when <agent> is given, number of recent activity entries to dump")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status [agent]",
	Short: "Show status of all agents, or detail/activity for one agent",
	Long: `Display a flat table of every agent in the system with status and process liveness.

With a positional [agent] argument, dump the agent's last_report plus the last
N activity entries (default 50; adjustable via --tail).

With --watch, tail the activity ring across all agents and stream new entries
as they land. Pre-existing entries are considered "already seen" and are not
replayed; only new entries appearing after watch starts are printed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveStatusDeps()
		if statusWatch {
			return runStatusWatch(cmd.Context(), deps, defaultWatchPoll)
		}
		if len(args) == 1 {
			return runStatusAgent(deps, args[0], statusTail)
		}
		return runStatus(deps, statusJSON, statusFamily, statusType, statusParent, statusStatus)
	},
}

func resolveStatusDeps() *statusDeps {
	if defaultStatusDeps != nil {
		return defaultStatusDeps
	}
	deps := &statusDeps{
		getenv: os.Getenv,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
	var runner tmux.Runner
	if tmuxPath, err := tmux.FindTmux(); err == nil {
		runner = &tmux.RealRunner{TmuxPath: tmuxPath}
	}
	deps.observeDeps = observe.Deps{
		TmuxRunner:    runner,
		ListAgents:    tolerantListAgents(deps.stderr),
		ReadRootName:  state.ReadRootName,
		ReadNamespace: state.ReadNamespace,
	}
	return deps
}

type statusEntry struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Family     string `json:"family"`
	Parent     string `json:"parent"`
	Status     string `json:"status"`
	Process    string `json:"process"`
	LastReport string `json:"last_report"`
	IsRoot     bool   `json:"is_root"`
}

func runStatus(deps *statusDeps, jsonOutput bool, family, typ, parent, statusFilter string) error {
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	agents, err := observe.LoadAll(deps.observeDeps, sprawlRoot)
	if err != nil {
		return fmt.Errorf("loading agents: %w", err)
	}

	// Apply filters (AND logic).
	var filtered []*observe.AgentInfo
	for _, a := range agents {
		if family != "" && a.Family != family {
			continue
		}
		if typ != "" && a.Type != typ {
			continue
		}
		if parent != "" && a.Parent != parent {
			continue
		}
		if statusFilter != "" && a.Status != statusFilter {
			continue
		}
		filtered = append(filtered, a)
	}

	if jsonOutput {
		return renderStatusJSON(deps.stdout, filtered)
	}
	return renderStatusTable(deps.stdout, filtered)
}

func renderStatusTable(w io.Writer, agents []*observe.AgentInfo) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "AGENT\tTYPE\tFAMILY\tPARENT\tSTATUS\tPROCESS\tLAST REPORT\n")
	for _, info := range agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			info.Name,
			dash(info.Type),
			dash(info.Family),
			dash(info.Parent),
			dash(info.Status),
			processDisplay(info),
			lastReportDisplay(info),
		)
	}
	return tw.Flush()
}

func renderStatusJSON(w io.Writer, agents []*observe.AgentInfo) error {
	entries := make([]statusEntry, 0, len(agents))
	for _, info := range agents {
		entries = append(entries, statusEntry{
			Name:       info.Name,
			Type:       info.Type,
			Family:     info.Family,
			Parent:     info.Parent,
			Status:     info.Status,
			Process:    processDisplay(info),
			LastReport: lastReportDisplay(info),
			IsRoot:     info.IsRoot,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// tolerantListAgents returns a ListAgents function that skips corrupt state files
// and logs warnings to stderr instead of failing.
func tolerantListAgents(stderr io.Writer) func(string) ([]*state.AgentState, error) {
	return func(sprawlRoot string) ([]*state.AgentState, error) {
		dir := state.AgentsDir(sprawlRoot)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("listing agents directory: %w", err)
		}

		var agents []*state.AgentState
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".json")
			agent, err := state.LoadAgent(sprawlRoot, name)
			if err != nil {
				fmt.Fprintf(stderr, "warning: skipping corrupt agent state %q: %v\n", name, err)
				continue
			}
			agents = append(agents, agent)
		}
		return agents, nil
	}
}

func processDisplay(info *observe.AgentInfo) string {
	if info.ProcessAlive == nil {
		return "?"
	}
	if *info.ProcessAlive {
		return "alive"
	}
	return "DEAD"
}

func lastReportDisplay(info *observe.AgentInfo) string {
	if info.LastReportType == "" {
		return "-"
	}
	tag := "[" + strings.ToUpper(info.LastReportType) + "]"
	if info.LastReportMessage == "" {
		return tag
	}
	msg := info.LastReportMessage
	const maxLen = 50
	if len(msg) > maxLen {
		msg = msg[:maxLen-3] + "..."
	}
	return tag + " " + msg
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runStatusAgent dumps the given agent's last_report plus the last `tail`
// activity entries. See docs/designs/messaging-overhaul.md §4.6 item 2.
func runStatusAgent(deps *statusDeps, agentName string, tail int) error {
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}
	if err := agent.ValidateName(agentName); err != nil {
		return fmt.Errorf("invalid agent name %q: %w", agentName, err)
	}

	st, err := state.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	w := deps.stdout
	fmt.Fprintf(w, "agent:  %s\n", st.Name)
	fmt.Fprintf(w, "type:   %s\n", dash(st.Type))
	fmt.Fprintf(w, "family: %s\n", dash(st.Family))
	fmt.Fprintf(w, "parent: %s\n", dash(st.Parent))
	fmt.Fprintf(w, "status: %s\n", dash(st.Status))

	if st.LastReportType != "" {
		fmt.Fprintf(w, "\nlast report: [%s] %s\n", strings.ToUpper(st.LastReportType), st.LastReportMessage)
		if st.LastReportAt != "" {
			fmt.Fprintf(w, "reported at: %s\n", st.LastReportAt)
		}
	} else {
		fmt.Fprintf(w, "\nlast report: -\n")
	}

	if tail <= 0 {
		tail = 50
	}
	path := agentloop.ActivityPath(sprawlRoot, agentName)
	entries, err := agentloop.ReadActivityFile(path, tail)
	if err != nil {
		return fmt.Errorf("reading activity: %w", err)
	}

	fmt.Fprintf(w, "\nactivity (last %d):\n", tail)
	if len(entries) == 0 {
		fmt.Fprintln(w, "  (no activity recorded)")
		return nil
	}
	for _, e := range entries {
		renderActivityLine(w, agentName, e, false)
	}
	return nil
}

// runStatusWatch tails the per-agent activity.ndjson files and streams new
// entries as they appear. Pre-existing entries at startup are remembered and
// not re-emitted. Blocks until ctx is cancelled (SIGINT/SIGTERM from the user,
// or explicit cancellation in tests). See §4.6 item 2.
func runStatusWatch(ctx context.Context, deps *statusDeps, pollInterval time.Duration) error {
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	// Install a signal handler tied to this call so Ctrl-C exits cleanly when
	// run interactively. The parent ctx (if already cancelled) short-circuits.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Per-agent offset: number of entries we've already rendered.
	seen := map[string]int{}

	// Seed with current state so existing entries are not replayed.
	names, err := listActivityAgents(sprawlRoot)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}
	for _, name := range names {
		entries, err := agentloop.ReadActivityFile(agentloop.ActivityPath(sprawlRoot, name), 0)
		if err != nil {
			fmt.Fprintf(deps.stderr, "warning: %s: %v\n", name, err)
			continue
		}
		seen[name] = len(entries)
	}

	fmt.Fprintf(deps.stdout, "# watching activity across %d agents — Ctrl-C to stop\n", len(names))

	if pollInterval <= 0 {
		pollInterval = defaultWatchPoll
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			if err == context.Canceled {
				return nil
			}
			return err
		case <-ticker.C:
			// Refresh agent list each tick — new agents may have spawned.
			names, err := listActivityAgents(sprawlRoot)
			if err != nil {
				fmt.Fprintf(deps.stderr, "warning: listing agents: %v\n", err)
				continue
			}
			for _, name := range names {
				entries, err := agentloop.ReadActivityFile(agentloop.ActivityPath(sprawlRoot, name), 0)
				if err != nil {
					fmt.Fprintf(deps.stderr, "warning: %s: %v\n", name, err)
					continue
				}
				prev := seen[name]
				if len(entries) <= prev {
					// No new entries; if file shrank (e.g. truncation) reset offset
					// so we start fresh without replaying.
					if len(entries) < prev {
						seen[name] = len(entries)
					}
					continue
				}
				for _, e := range entries[prev:] {
					renderActivityLine(deps.stdout, name, e, true)
				}
				seen[name] = len(entries)
			}
		}
	}
}

// listActivityAgents returns the names of all agents that have a state file
// under sprawlRoot, sorted. Corrupt state files are skipped silently (the
// caller surfaces warnings for activity-read errors instead).
func listActivityAgents(sprawlRoot string) ([]string, error) {
	dir := state.AgentsDir(sprawlRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// renderActivityLine writes a single activity entry as a one-liner. If
// withAgent is true (watch mode), the agent name is prefixed.
func renderActivityLine(w io.Writer, agentName string, e agentloop.ActivityEntry, withAgent bool) {
	ts := e.TS.Format("15:04:05")
	kind := e.Kind
	if e.Tool != "" {
		kind = fmt.Sprintf("%s/%s", e.Kind, e.Tool)
	}
	if withAgent {
		fmt.Fprintf(w, "%s  %-14s  %-24s  %s\n", ts, agentName, kind, e.Summary)
		return
	}
	fmt.Fprintf(w, "  %s  %-24s  %s\n", ts, kind, e.Summary)
}
