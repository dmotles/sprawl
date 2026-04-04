package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

var spawnSubagentCmd = &cobra.Command{
	Use:   "subagent",
	Short: "Spawn a lightweight subagent on the parent's worktree",
	Long:  "Spawn a subagent that shares the parent agent's worktree instead of creating its own.",
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveSpawnSubagentDeps()
		if err != nil {
			return err
		}
		return runSpawnSubagent(deps, spawnFamily, spawnType, spawnPrompt)
	},
}

// spawnSubagentDeps holds the dependencies for the spawn subagent command.
// Unlike spawnDeps, it has no worktreeCreator or currentBranch since subagents
// share the parent's worktree.
type spawnSubagentDeps struct {
	tmuxRunner tmux.Runner
	getenv     func(string) string
	findDendra func() (string, error)
	loadAgent  func(dendraRoot, name string) (*state.AgentState, error)
}

var defaultSpawnSubagentDeps *spawnSubagentDeps

func init() {
	spawnCmd.AddCommand(spawnSubagentCmd)
}

func resolveSpawnSubagentDeps() (*spawnSubagentDeps, error) {
	if defaultSpawnSubagentDeps != nil {
		return defaultSpawnSubagentDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &spawnSubagentDeps{
		tmuxRunner: &tmux.RealRunner{TmuxPath: tmuxPath},
		getenv:     os.Getenv,
		findDendra: FindDendraBin,
		loadAgent:  state.LoadAgent,
	}, nil
}

func runSpawnSubagent(deps *spawnSubagentDeps, family, agentType, prompt string) error {
	// Validate type
	if !isValidType(agentType) {
		return fmt.Errorf("invalid agent type %q; valid types: %v", agentType, validTypes)
	}
	if !supportedTypes[agentType] {
		return fmt.Errorf("agent type %q is not yet supported; currently supported: engineer, researcher", agentType)
	}

	// Validate family
	if !isValidFamily(family) {
		return fmt.Errorf("invalid agent family %q; valid families: %v", family, validFamilies)
	}

	// Read environment
	parentName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if parentName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set; spawn must be called from within a dendra agent")
	}

	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set; spawn must be called from within a dendra agent")
	}

	// Allocate name
	agentsDir := state.AgentsDir(dendraRoot)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil { //nolint:gosec // G301: world-readable agent dir is intentional
		return fmt.Errorf("creating agents directory: %w", err)
	}
	agentName, err := agent.AllocateName(agentsDir, agentType)
	if err != nil {
		return err
	}

	// Load parent state to get worktree and branch
	parentState, err := deps.loadAgent(dendraRoot, parentName)
	if err != nil {
		return fmt.Errorf("loading parent agent state: %w", err)
	}

	// Find dendra binary
	dendraPath, err := deps.findDendra()
	if err != nil {
		return fmt.Errorf("finding dendra binary: %w", err)
	}

	// Build shell command: cd to parent's worktree, then run dendra agent-loop
	shellCmd := fmt.Sprintf("cd %s && %s",
		tmux.ShellQuote(parentState.Worktree),
		tmux.BuildShellCmd(dendraPath, []string{"agent-loop", agentName}))

	// Resolve namespace: env var > persisted file > default
	namespace := deps.getenv("DENDRA_NAMESPACE")
	if namespace == "" {
		namespace = state.ReadNamespace(dendraRoot)
	}
	if namespace == "" {
		namespace = tmux.DefaultNamespace
	}

	// Build tree path: parent's tree path + separator + child name
	parentTreePath := deps.getenv("DENDRA_TREE_PATH")
	if parentTreePath == "" {
		// Fallback: use root name from file + parent identity
		rootName := state.ReadRootName(dendraRoot)
		if rootName == "" {
			rootName = tmux.DefaultRootName
		}
		if parentName == rootName {
			parentTreePath = rootName
		} else {
			parentTreePath = rootName + tmux.BranchSeparator + parentName
		}
	}
	childTreePath := parentTreePath + tmux.BranchSeparator + agentName

	// Set environment for the child agent
	env := map[string]string{
		"DENDRA_AGENT_IDENTITY": agentName,
		"DENDRA_ROOT":           dendraRoot,
		"DENDRA_NAMESPACE":      namespace,
		"DENDRA_TREE_PATH":      childTreePath,
	}
	if v := deps.getenv("DENDRA_BIN"); v != "" {
		env["DENDRA_BIN"] = v
	}

	// Create or add to tmux session
	childrenSession := tmux.ChildrenSessionName(namespace, parentTreePath)
	if deps.tmuxRunner.HasSession(childrenSession) {
		if err := deps.tmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
			return fmt.Errorf("creating tmux window for %s: %w", agentName, err)
		}
	} else {
		if err := deps.tmuxRunner.NewSessionWithWindow(childrenSession, agentName, env, shellCmd); err != nil {
			return fmt.Errorf("creating tmux session for %s: %w", agentName, err)
		}
	}

	// Generate a UUID for the Claude session ID
	sessionID, err := state.GenerateUUID()
	if err != nil {
		return fmt.Errorf("generating session ID: %w", err)
	}

	// Save subagent state — note Subagent: true and parent's worktree/branch
	agentState := &state.AgentState{
		Name:        agentName,
		Type:        agentType,
		Family:      family,
		Parent:      parentName,
		Prompt:      prompt,
		Branch:      parentState.Branch,
		Worktree:    parentState.Worktree,
		TmuxSession: childrenSession,
		TmuxWindow:  agentName,
		Status:      "active",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		SessionID:   sessionID,
		Subagent:    true,
		TreePath:    childTreePath,
	}
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("saving agent state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Spawned subagent %s %s (on parent worktree: %s)\n", agentType, agentName, parentState.Worktree)
	return nil
}
