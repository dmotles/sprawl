package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/dmotles/dendra/internal/observe"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
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
)

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "output as JSON array")
	statusCmd.Flags().StringVar(&statusFamily, "family", "", "filter by family")
	statusCmd.Flags().StringVar(&statusType, "type", "", "filter by type")
	statusCmd.Flags().StringVar(&statusParent, "parent", "", "filter by parent")
	statusCmd.Flags().StringVar(&statusStatus, "status", "", "filter by status")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all agents",
	Long:  "Display a flat table of every agent in the system with status and process liveness.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveStatusDeps()
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
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	agents, err := observe.LoadAll(deps.observeDeps, dendraRoot)
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
	return func(dendraRoot string) ([]*state.AgentState, error) {
		dir := state.AgentsDir(dendraRoot)
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
			agent, err := state.LoadAgent(dendraRoot, name)
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
		if isTerminalStatus(info.Status) {
			return "-"
		}
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

func isTerminalStatus(status string) bool {
	switch status {
	case "done", "problem", "retiring":
		return true
	}
	return false
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
