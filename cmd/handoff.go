package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/spf13/cobra"
)

type handoffDeps struct {
	stdout              io.Writer
	getenv              func(string) string
	readStdin           func() ([]byte, error)
	listAgents          func(sprawlRoot string) ([]*state.AgentState, error)
	writeSessionSummary func(sprawlRoot string, session memory.Session, body string) error
	readLastSessionID   func(sprawlRoot string) (string, error)
	writeSignalFile     func(sprawlRoot string) error
	now                 func() time.Time
}

var defaultHandoffDeps *handoffDeps

func init() {
	rootCmd.AddCommand(handoffCmd)
}

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Write session summary and signal handoff to next session",
	Long:  "Persist a session summary (read from stdin) and create a handoff signal file for the root loop to detect.",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := resolveHandoffDeps()
		return runHandoff(deps)
	},
}

func resolveHandoffDeps() *handoffDeps {
	if defaultHandoffDeps != nil {
		return defaultHandoffDeps
	}
	return &handoffDeps{
		stdout: os.Stdout,
		getenv: os.Getenv,
		readStdin: func() ([]byte, error) {
			return io.ReadAll(os.Stdin)
		},
		listAgents:          state.ListAgents,
		writeSessionSummary: memory.WriteSessionSummary,
		readLastSessionID:   memory.ReadLastSessionID,
		writeSignalFile:     memory.WriteHandoffSignal,
		now:                 time.Now,
	}
}

func runHandoff(deps *handoffDeps) error {
	deprecationWarning("handoff", "handoff")
	agentName := deps.getenv("SPRAWL_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set")
	}

	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	// Only the root agent may run handoff
	rootName := state.ReadRootName(sprawlRoot)
	if agentName != rootName {
		return fmt.Errorf("handoff can only be run by the root agent")
	}

	// Read summary from stdin
	stdinBytes, err := deps.readStdin()
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	// Reject empty or whitespace-only summaries
	if strings.TrimSpace(string(stdinBytes)) == "" {
		//nolint:revive,staticcheck // multi-line error message is intentional for CLI UX
		return fmt.Errorf(`no summary provided on stdin. Pipe your session summary into this command:

  cat <<'EOF' | sprawl handoff
  ## What was accomplished
  ...
  ## Key decisions
  ...
  ## Outstanding work
  ...
  EOF

The summary is the primary context for the next session — make it count.`)
	}

	// Read current session ID
	sessionID, err := deps.readLastSessionID(sprawlRoot)
	if err != nil {
		return fmt.Errorf("reading session ID: %w", err)
	}
	if sessionID == "" {
		return fmt.Errorf("no session ID found; .sprawl/memory/last-session-id is missing or empty")
	}

	// Collect active agent names
	agents, err := deps.listAgents(sprawlRoot)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}
	var agentNames []string
	for _, a := range agents {
		agentNames = append(agentNames, a.Name)
	}

	// Build and write session summary
	session := memory.Session{
		SessionID:    sessionID,
		Timestamp:    deps.now().UTC(),
		Handoff:      true,
		AgentsActive: agentNames,
	}
	if err := deps.writeSessionSummary(sprawlRoot, session, string(stdinBytes)); err != nil {
		return fmt.Errorf("writing session summary: %w", err)
	}

	// Write handoff signal
	if err := deps.writeSignalFile(sprawlRoot); err != nil {
		return fmt.Errorf("writing handoff signal: %w", err)
	}

	fmt.Fprintln(deps.stdout, "Handoff complete. Session summary written.")
	fmt.Fprintln(deps.stdout)
	fmt.Fprintln(deps.stdout, "To restart with fresh context, exit this session:")
	fmt.Fprintln(deps.stdout, "  - Type /exit")
	fmt.Fprintln(deps.stdout, "  - Or press Ctrl+D")
	fmt.Fprintln(deps.stdout, "  - Or press Ctrl+C")
	fmt.Fprintln(deps.stdout)
	fmt.Fprintln(deps.stdout, "The root loop will automatically restart with your new context.")
	return nil
}
