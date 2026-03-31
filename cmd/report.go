package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

// reportDeps holds the dependencies for the report command, enabling testability.
type reportDeps struct {
	getenv  func(string) string
	nowFunc func() time.Time
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

	return &reportDeps{
		getenv:  os.Getenv,
		nowFunc: time.Now,
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

	// Notify parent for all report types
	if err := notifyParent(dendraRoot, agentState, reportType, message); err != nil {
		// Notification failure is non-fatal — state is already persisted
		fmt.Fprintf(os.Stderr, "Warning: failed to notify parent: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Reported %s: %s\n", reportType, message)
	return nil
}

// notifyParent sends a notification to the agent's parent via the messaging system.
func notifyParent(dendraRoot string, agentState *state.AgentState, reportType, message string) error {
	parent := agentState.Parent
	if parent == "" {
		return nil
	}

	subject := fmt.Sprintf("[%s] Agent %s reports %s", strings.ToUpper(reportType), agentState.Name, reportType)
	return messages.Send(dendraRoot, agentState.Name, parent, subject, message)
}
