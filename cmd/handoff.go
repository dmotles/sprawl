package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dmotles/dendra/internal/memory"
	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

type handoffDeps struct {
	getenv              func(string) string
	readStdin           func() ([]byte, error)
	listAgents          func(dendraRoot string) ([]*state.AgentState, error)
	writeSessionSummary func(dendraRoot string, session memory.Session, body string) error
	readLastSessionID   func(dendraRoot string) (string, error)
	writeSignalFile     func(dendraRoot string) error
	now                 func() time.Time
}

var defaultHandoffDeps *handoffDeps

func init() {
	rootCmd.AddCommand(handoffCmd)
}

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Write session summary and signal handoff to next session",
	Long:  "Persist a session summary (read from stdin) and create a handoff signal file for the sensei loop to detect.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveHandoffDeps()
		return runHandoff(deps)
	},
}

func resolveHandoffDeps() *handoffDeps {
	if defaultHandoffDeps != nil {
		return defaultHandoffDeps
	}
	return &handoffDeps{
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
	agentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if agentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	// Only the root agent may run handoff
	rootName := state.ReadRootName(dendraRoot)
	if agentName != rootName {
		return fmt.Errorf("handoff can only be run by the root agent")
	}

	// Read summary from stdin
	stdinBytes, err := deps.readStdin()
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	// Read current session ID
	sessionID, err := deps.readLastSessionID(dendraRoot)
	if err != nil {
		return fmt.Errorf("reading session ID: %w", err)
	}
	if sessionID == "" {
		return fmt.Errorf("no session ID found; .dendra/memory/last-session-id is missing or empty")
	}

	// Collect active agent names
	agents, err := deps.listAgents(dendraRoot)
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
	if err := deps.writeSessionSummary(dendraRoot, session, string(stdinBytes)); err != nil {
		return fmt.Errorf("writing session summary: %w", err)
	}

	// Write handoff signal
	if err := deps.writeSignalFile(dendraRoot); err != nil {
		return fmt.Errorf("writing handoff signal: %w", err)
	}

	fmt.Println("Handoff complete. Session summary written.")
	return nil
}
