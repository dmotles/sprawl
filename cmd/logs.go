package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/spf13/cobra"
)

type logsDeps struct {
	getenv   func(string) string
	readDir  func(string) ([]os.DirEntry, error)
	readFile func(string) ([]byte, error)
	stdout   io.Writer
}

var defaultLogsDeps *logsDeps

var logsTail int

func init() {
	logsCmd.Flags().IntVar(&logsTail, "tail", 0, "show last N lines of the most recent log")
	rootCmd.AddCommand(logsCmd)
}

var logsCmd = &cobra.Command{
	Use:   "logs <agent-name>",
	Short: "Show log files for an agent",
	Long:  "Display agent loop log files. Each log corresponds to one agent session (context window).",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveLogsDeps()
		return runLogs(deps, args[0], logsTail)
	},
}

func resolveLogsDeps() *logsDeps {
	if defaultLogsDeps != nil {
		return defaultLogsDeps
	}
	return &logsDeps{
		getenv:   os.Getenv,
		readDir:  os.ReadDir,
		readFile: os.ReadFile,
		stdout:   os.Stdout,
	}
}

func runLogs(deps *logsDeps, agentName string, tail int) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	logsDir := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "logs")
	entries, err := deps.readDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no logs found for agent %q", agentName)
		}
		return fmt.Errorf("reading logs directory: %w", err)
	}

	// Collect .log files sorted by name
	var logFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			logFiles = append(logFiles, entry.Name())
		}
	}
	sort.Strings(logFiles)

	if len(logFiles) == 0 {
		return fmt.Errorf("no logs found for agent %q", agentName)
	}

	if tail > 0 {
		// Show last N lines of the most recent log
		latest := logFiles[len(logFiles)-1]
		data, err := deps.readFile(filepath.Join(logsDir, latest))
		if err != nil {
			return fmt.Errorf("reading log file: %w", err)
		}
		lines := strings.Split(string(data), "\n")
		// Remove trailing empty line from split
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		start := len(lines) - tail
		if start < 0 {
			start = 0
		}
		for _, line := range lines[start:] {
			fmt.Fprintln(deps.stdout, line)
		}
		return nil
	}

	// Show all log files
	for i, logFile := range logFiles {
		sessionID := strings.TrimSuffix(logFile, ".log")
		if i > 0 {
			fmt.Fprintln(deps.stdout)
		}
		fmt.Fprintf(deps.stdout, "=== Session %s ===\n", sessionID)
		data, err := deps.readFile(filepath.Join(logsDir, logFile))
		if err != nil {
			fmt.Fprintf(deps.stdout, "error reading log: %v\n", err)
			continue
		}
		fmt.Fprint(deps.stdout, string(data))
	}

	return nil
}
