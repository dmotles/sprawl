package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// reportDeps holds the dependencies for the report command, enabling testability.
type reportDeps struct {
	tmuxRunner tmux.Runner
	getenv     func(string) string
	nowFunc    func() time.Time
}

var defaultReportDeps *reportDeps

func init() {
	reportCmd.AddCommand(reportStatusCmd)
	reportCmd.AddCommand(reportDoneCmd)
	reportCmd.AddCommand(reportProblemCmd)
	rootCmd.AddCommand(reportCmd)
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Report status, completion, or problems to your parent",
	Long:  "Report your current status, mark yourself as done, or report a problem. Updates are persisted to your agent state file.",
}

var reportStatusCmd = &cobra.Command{
	Use:   "status <message>",
	Short: "Report a status update",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveReportDeps()
		if err != nil {
			return err
		}
		message := strings.Join(args, " ")
		return runReport(deps, "status", message)
	},
}

var reportDoneCmd = &cobra.Command{
	Use:   "done <message>",
	Short: "Report that your task is complete",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveReportDeps()
		if err != nil {
			return err
		}
		message := strings.Join(args, " ")
		return runReport(deps, "done", message)
	},
}

var reportProblemCmd = &cobra.Command{
	Use:   "problem <message>",
	Short: "Report a problem or blocker",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveReportDeps()
		if err != nil {
			return err
		}
		message := strings.Join(args, " ")
		return runReport(deps, "problem", message)
	},
}

func resolveReportDeps() (*reportDeps, error) {
	if defaultReportDeps != nil {
		return defaultReportDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &reportDeps{
		tmuxRunner: &tmux.RealRunner{TmuxPath: tmuxPath},
		getenv:     os.Getenv,
		nowFunc:    time.Now,
	}, nil
}

func runReport(deps *reportDeps, reportType, message string) error {
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set; report must be called from within a dendra agent")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set; report must be called from within a dendra agent")
	}

	// Load agent state
	agentState, err := state.LoadAgent(dendraRoot, agentName)
	if err != nil {
		return fmt.Errorf("loading agent state: %w", err)
	}

	// Update report fields
	agentState.LastReportType = reportType
	agentState.LastReportMessage = message
	agentState.LastReportAt = deps.nowFunc().UTC().Format(time.RFC3339)

	// Update status for done/problem
	if reportType == "done" {
		agentState.Status = "done"
	} else if reportType == "problem" {
		agentState.Status = "problem"
	}

	// Persist to state file
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("saving agent state: %w", err)
	}

	// Notify parent for done/problem
	if reportType == "done" || reportType == "problem" {
		if err := notifyParent(deps, agentState, reportType, message); err != nil {
			// Notification failure is non-fatal — state is already persisted
			fmt.Fprintf(os.Stderr, "Warning: failed to notify parent: %v\n", err)
		}
	}

	fmt.Fprintf(os.Stderr, "Reported %s: %s\n", reportType, message)
	return nil
}

// notifyParent sends a notification to the agent's parent.
func notifyParent(deps *reportDeps, agentState *state.AgentState, reportType, message string) error {
	parent := agentState.Parent
	if parent == "" {
		return nil
	}

	if parent == "root" {
		// Send to root's tmux session via send-keys
		notification := fmt.Sprintf("Agent %s reports %s: %s", agentState.Name, reportType, message)
		return deps.tmuxRunner.SendKeys(tmux.RootSessionName, tmux.RootWindowName, notification)
	}

	// TODO: For non-root parents, the non-interactive wrapper loop (future work)
	// will handle polling for state changes. The state file has already been
	// updated above, so the parent's polling loop will pick up this report.
	return nil
}
