package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

// reportDeps holds the dependencies for the report command, enabling testability.
type reportDeps struct {
	getenv      func(string) string
	nowFunc     func() time.Time
	tmuxRunner  tmux.Runner
	sendMessage func(sprawlRoot, from, to, subject, body string, opts ...messages.SendOption) error
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
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveReportDeps()
		message := strings.Join(args, " ")
		return runReport(deps, "status", message)
	},
}

var reportDoneCmd = &cobra.Command{
	Use:   "done <message>",
	Short: "Report that your task is complete",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveReportDeps()
		message := strings.Join(args, " ")
		return runReport(deps, "done", message)
	},
}

var reportProblemCmd = &cobra.Command{
	Use:   "problem <message>",
	Short: "Report a problem or blocker",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
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
	agentName := deps.getenv("SPRAWL_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set; report must be called from within a sprawl agent")
	}

	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set; report must be called from within a sprawl agent")
	}

	// Load agent state
	agentState, err := state.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("loading agent state: %w", err)
	}

	// Update report fields
	agentState.LastReportType = reportType
	agentState.LastReportMessage = message
	agentState.LastReportAt = deps.nowFunc().UTC().Format(time.RFC3339)

	// Update status for done/problem
	switch reportType {
	case "done":
		agentState.Status = "done"
	case "problem":
		agentState.Status = "problem"
	}

	// Persist to state file
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		return fmt.Errorf("saving agent state: %w", err)
	}

	// Notify parent for all report types
	if err := notifyParent(deps, sprawlRoot, agentState, reportType, message); err != nil {
		// Notification failure is non-fatal — state is already persisted
		fmt.Fprintf(os.Stderr, "Warning: failed to notify parent: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Reported %s: %s\n", reportType, message)
	return nil
}

// notifyParent sends a notification to the agent's parent via the messaging system.
func notifyParent(deps *reportDeps, sprawlRoot string, agentState *state.AgentState, reportType, message string) error {
	parent := agentState.Parent
	if parent == "" {
		return nil
	}

	subject := fmt.Sprintf("[%s] Agent %s reports %s", strings.ToUpper(reportType), agentState.Name, reportType)

	var sendOpts []messages.SendOption
	if deps.tmuxRunner != nil {
		namespace := deps.getenv("SPRAWL_NAMESPACE")
		if namespace == "" {
			namespace = state.ReadNamespace(sprawlRoot)
		}
		if namespace == "" {
			namespace = tmux.DefaultNamespace
		}
		rootName := state.ReadRootName(sprawlRoot)
		if rootName == "" {
			rootName = tmux.DefaultRootName
		}
		if parent == rootName {
			rootSession := tmux.RootSessionName(namespace, rootName)
			sendOpts = append(sendOpts, messages.WithNotify(func(from, _, msgID string) {
				notification := fmt.Sprintf("[inbox] New message from %s. Run: `sprawl messages read %s`", from, msgID)
				_ = deps.tmuxRunner.SendKeys(rootSession, tmux.RootWindowName, notification)
			}))
		}
	}

	return deps.sendMessage(sprawlRoot, agentState.Name, parent, subject, message, sendOpts...)
}
