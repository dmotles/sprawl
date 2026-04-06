package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// pokeDeps holds the dependencies for the poke command, enabling testability.
type pokeDeps struct {
	getenv    func(string) string
	writeFile func(string, []byte, os.FileMode) error
	stdout    io.Writer
}

var defaultPokeDeps *pokeDeps

func resolvePokeDeps() *pokeDeps {
	if defaultPokeDeps != nil {
		return defaultPokeDeps
	}
	return &pokeDeps{
		getenv:    os.Getenv,
		writeFile: os.WriteFile,
		stdout:    os.Stderr,
	}
}

func init() {
	rootCmd.AddCommand(pokeCmd)
}

var pokeCmd = &cobra.Command{
	Use:   "poke <agent-name> <message>",
	Short: "Send a mid-turn interrupt message to a running agent",
	Long: `Write a message to the agent's .poke file. If the agent is mid-turn,
the agent loop will interrupt the current turn and deliver the message
as the next prompt. If the agent is between turns, the message is
delivered immediately on the next poll cycle.`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolvePokeDeps()
		return runPoke(deps, args[0], args[1])
	},
}

func runPoke(deps *pokeDeps, agentName, message string) error {
	dendraRoot := deps.getenv("SPRAWL_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	pokePath := filepath.Join(dendraRoot, ".sprawl", "agents", agentName+".poke")
	if err := deps.writeFile(pokePath, []byte(message), 0o644); err != nil {
		return fmt.Errorf("writing poke file: %w", err)
	}

	fmt.Fprintf(deps.stdout, "[poke] message queued for %s\n", agentName)
	return nil
}
