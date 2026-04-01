package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/dmotles/dendra/internal/worktree"
	"github.com/spf13/cobra"
)

var spawnAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Spawn a new agent",
	Long:  "Spawn a new agent with the given family, type, and task prompt.",
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveSpawnDeps()
		if err != nil {
			return err
		}
		return runSpawn(deps, spawnFamily, spawnType, spawnPrompt)
	},
}

var (
	validTypes    = []string{"manager", "researcher", "engineer", "tester", "code-merger"}
	validFamilies = []string{"engineering", "product", "qa"}

	// supportedTypes are the types that are fully implemented.
	supportedTypes = map[string]bool{
		"engineer":   true,
		"researcher": true,
	}
)

// spawnDeps holds the dependencies for the spawn command, enabling testability.
type spawnDeps struct {
	tmuxRunner      tmux.Runner
	worktreeCreator worktree.Creator
	getenv          func(string) string
	currentBranch   func(repoRoot string) (string, error)
	findDendra      func() (string, error)
}

var defaultSpawnDeps *spawnDeps

var (
	spawnFamily string
	spawnType   string
	spawnPrompt string
)

func init() {
	spawnCmd.PersistentFlags().StringVar(&spawnFamily, "family", "", "agent family: engineering, product, qa")
	spawnCmd.PersistentFlags().StringVar(&spawnType, "type", "", "agent type: manager, researcher, engineer, tester, code-merger")
	spawnCmd.PersistentFlags().StringVar(&spawnPrompt, "prompt", "", "task description for the agent")
	spawnCmd.MarkPersistentFlagRequired("family")
	spawnCmd.MarkPersistentFlagRequired("type")
	spawnCmd.MarkPersistentFlagRequired("prompt")
	spawnCmd.AddCommand(spawnAgentCmd)
	rootCmd.AddCommand(spawnCmd)
}

var spawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn a new agent",
	Long:  "Spawn a new agent with the given family, type, and task prompt.",
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveSpawnDeps()
		if err != nil {
			return err
		}
		return runSpawn(deps, spawnFamily, spawnType, spawnPrompt)
	},
}

func resolveSpawnDeps() (*spawnDeps, error) {
	if defaultSpawnDeps != nil {
		return defaultSpawnDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &spawnDeps{
		tmuxRunner:      &tmux.RealRunner{TmuxPath: tmuxPath},
		worktreeCreator: &worktree.RealCreator{},
		getenv:          os.Getenv,
		currentBranch:   gitCurrentBranch,
		findDendra:      os.Executable,
	}, nil
}

func runSpawn(deps *spawnDeps, family, agentType, prompt string) error {
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
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("creating agents directory: %w", err)
	}
	agentName, err := agent.AllocateName(agentsDir)
	if err != nil {
		return err
	}

	// Get current branch for worktree base
	baseBranch, err := deps.currentBranch(dendraRoot)
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	// Create worktree
	worktreePath, branchName, err := deps.worktreeCreator.Create(dendraRoot, agentName, baseBranch)
	if err != nil {
		return fmt.Errorf("creating worktree for %s: %w", agentName, err)
	}

	// Find dendra binary
	dendraPath, err := deps.findDendra()
	if err != nil {
		return fmt.Errorf("finding dendra binary: %w", err)
	}

	// Build shell command: cd to worktree, then run dendra agent-loop
	shellCmd := fmt.Sprintf("cd %s && %s", tmux.ShellQuote(worktreePath), tmux.BuildShellCmd(dendraPath, []string{"agent-loop", agentName}))

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
		if parentName == "root" {
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

	// Save agent state
	agentState := &state.AgentState{
		Name:        agentName,
		Type:        agentType,
		Family:      family,
		Parent:      parentName,
		Prompt:      prompt,
		Branch:      branchName,
		Worktree:    worktreePath,
		TmuxSession: childrenSession,
		TmuxWindow:  agentName,
		Status:      "active",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		SessionID:   sessionID,
		TreePath:    childTreePath,
	}
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("saving agent state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Spawned %s %s (branch: %s)\n", agentType, agentName, branchName)
	return nil
}

func isValidType(t string) bool {
	for _, v := range validTypes {
		if v == t {
			return true
		}
	}
	return false
}

func isValidFamily(f string) bool {
	for _, v := range validFamilies {
		if v == f {
			return true
		}
	}
	return false
}

// gitCurrentBranch returns the current branch name of the repo at the given root.
func gitCurrentBranch(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	branch := string(out)
	// Trim trailing newline
	if len(branch) > 0 && branch[len(branch)-1] == '\n' {
		branch = branch[:len(branch)-1]
	}
	return branch, nil
}
