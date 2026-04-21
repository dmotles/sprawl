package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/spf13/cobra"
)

// reportDeps holds the dependencies for the report command, enabling testability.
// Both `sprawl report` CLI and the `sprawl_report_status` MCP tool delegate to
// the same agentops.Report helper — there is one persistence path.
type reportDeps struct {
	getenv      func(string) string
	nowFunc     func() time.Time
	loadAgent   func(sprawlRoot, name string) (*state.AgentState, error)
	saveAgent   func(sprawlRoot string, agent *state.AgentState) error
	sendMessage func(sprawlRoot, from, to, subject, body string, opts ...messages.SendOption) error
	enqueue     func(sprawlRoot, to string, e agentloop.Entry) (agentloop.Entry, error)
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
	Long:  "Report your current status, mark yourself as done, or report a problem. Updates are persisted to your agent state file and delivered to the parent via the harness queue.",
}

var reportStatusCmd = &cobra.Command{
	Use:   "status <message>",
	Short: "Report a status update (state=working)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runReport(resolveReportDeps(), "status", strings.Join(args, " "))
	},
}

var reportDoneCmd = &cobra.Command{
	Use:   "done <message>",
	Short: "Report that your task is complete (state=complete)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runReport(resolveReportDeps(), "done", strings.Join(args, " "))
	},
}

var reportProblemCmd = &cobra.Command{
	Use:   "problem <message>",
	Short: "Report a problem or blocker (state=failure)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runReport(resolveReportDeps(), "problem", strings.Join(args, " "))
	},
}

func resolveReportDeps() *reportDeps {
	if defaultReportDeps != nil {
		return defaultReportDeps
	}
	return &reportDeps{
		getenv:      os.Getenv,
		nowFunc:     time.Now,
		loadAgent:   state.LoadAgent,
		saveAgent:   state.SaveAgent,
		sendMessage: messages.Send,
		enqueue:     agentloop.Enqueue,
	}
}

// cliTypeToState maps the CLI subcommand token (status/done/problem) to the
// canonical report state (working/complete/failure).
func cliTypeToState(reportType string) string {
	switch reportType {
	case "done":
		return agentops.ReportStateComplete
	case "problem":
		return agentops.ReportStateFailure
	default:
		return agentops.ReportStateWorking
	}
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

	opDeps := &agentops.ReportDeps{
		LoadAgent:   deps.loadAgent,
		SaveAgent:   deps.saveAgent,
		SendMessage: deps.sendMessage,
		Enqueue:     deps.enqueue,
		Now:         deps.nowFunc,
	}
	_, err := agentops.Report(opDeps, sprawlRoot, agentName, cliTypeToState(reportType), message, "")
	if err != nil {
		// Messaging failure is surfaced by agentops.Report but state is
		// already persisted in that case — match the old non-fatal behavior
		// for parent-notification errors.
		if strings.Contains(err.Error(), "sending message to parent") || strings.Contains(err.Error(), "enqueuing async report") {
			fmt.Fprintf(os.Stderr, "Warning: failed to notify parent: %v\n", err)
			fmt.Fprintf(os.Stderr, "Reported %s: %s\n", reportType, message)
			return nil
		}
		return err
	}

	fmt.Fprintf(os.Stderr, "Reported %s: %s\n", reportType, message)
	return nil
}
