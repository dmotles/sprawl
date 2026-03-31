package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// reportDeps holds the dependencies for the report command, enabling testability.
type reportDeps struct {
	getenv      func(string) string
	nowFunc     func() time.Time
	tmuxRunner  tmux.Runner
	sendMessage func(dendraRoot, from, to, subject, body string, opts ...messages.SendOption) error
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
		deps := resolveReportDeps()
		message := strings.Join(args, " ")
		return runReport(deps, "status", message)
	},
}

var reportDoneCmd = &cobra.Command{
	Use:   "done <message>",
	Short: "Report that your task is complete",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveReportDeps()
		message := strings.Join(args, " ")
		return runReport(deps, "done", message)
	},
}

var reportProblemCmd = &cobra.Command{
	Use:   "problem <message>",
	Short: "Report a problem or blocker",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveReportDeps()
		message := strings.Join(args, " ")
		return runReport(deps, "problem", message)
	},
}

func resolveReportDeps() *reportDeps {
	if defaultReportDeps != nil {
		return defaultReportDeps
	}
	deps := &reportDeps{
		getenv:      os.Getenv,
		nowFunc:     time.Now,
		sendMessage: messages.Send,
	}
	if tmuxPath, err := tmux.FindTmux(); err == nil {
		deps.tmuxRunner = &tmux.RealRunner{TmuxPath: tmuxPath}
	}
	return deps
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
	if err := notifyParent(deps, dendraRoot, agentState, reportType, message); err != nil {
		// Notification failure is non-fatal — state is already persisted
		fmt.Fprintf(os.Stderr, "Warning: failed to notify parent: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Reported %s: %s\n", reportType, message)
	return nil
}

// notifyParent sends a notification to the agent's parent via the messaging system.
func notifyParent(deps *reportDeps, dendraRoot string, agentState *state.AgentState, reportType, message string) error {
	parent := agentState.Parent
	if parent == "" {
		return nil
	}

	subject := fmt.Sprintf("[%s] Agent %s reports %s", strings.ToUpper(reportType), agentState.Name, reportType)

	var sendOpts []messages.SendOption
	if deps.tmuxRunner != nil {
		sendOpts = append(sendOpts, messages.WithNotify(func(from, subj string) {
			notification := fmt.Sprintf("[inbox] Message from %s: %s", from, subj)
			deps.tmuxRunner.SendKeys(tmux.RootSessionName, tmux.RootWindowName, notification)
		}))
	}

	return deps.sendMessage(dendraRoot, agentState.Name, parent, subject, message, sendOpts...)
}
