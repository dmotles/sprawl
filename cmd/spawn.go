package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/spf13/cobra"
)

var spawnAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Spawn a new agent",
	Long:  "Spawn a new agent with the given family, type, and task prompt.",
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveSpawnDeps()
		if err != nil {
			return err
		}
		return runSpawn(deps, spawnFamily, spawnType, spawnPrompt, spawnBranch)
	},
}

var (
	validTypes    = []string{"manager", "researcher", "engineer", "tester", "code-merger"}
	validFamilies = []string{"engineering", "product", "qa"}

	// supportedTypes are the types that are fully implemented.
	supportedTypes = map[string]bool{
		"engineer":   true,
		"researcher": true,
		"manager":    true,
	}
)

// spawnDeps holds the dependencies for the spawn command, enabling testability.
type spawnDeps struct {
	tmuxRunner      tmux.Runner
	worktreeCreator worktree.Creator
	getenv          func(string) string
	currentBranch   func(repoRoot string) (string, error)
	findSprawl      func() (string, error)
}

var defaultSpawnDeps *spawnDeps

var (
	spawnFamily string
	spawnType   string
	spawnPrompt string
	spawnBranch string
)

func init() {
	spawnCmd.PersistentFlags().StringVar(&spawnFamily, "family", "", "agent family: engineering, product, qa")
	spawnCmd.PersistentFlags().StringVar(&spawnType, "type", "", "agent type: manager, researcher, engineer, tester, code-merger")
	spawnCmd.PersistentFlags().StringVar(&spawnPrompt, "prompt", "", "task description for the agent")
	spawnCmd.PersistentFlags().StringVar(&spawnBranch, "branch", "", "git branch name for the agent's worktree")
	_ = spawnCmd.MarkPersistentFlagRequired("family")
	_ = spawnCmd.MarkPersistentFlagRequired("type")
	_ = spawnCmd.MarkPersistentFlagRequired("prompt")
	_ = spawnCmd.MarkPersistentFlagRequired("branch")
	spawnCmd.AddCommand(spawnAgentCmd)
	rootCmd.AddCommand(spawnCmd)
}

var spawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn a new agent",
	Long:  "Spawn a new agent with the given family, type, and task prompt.",
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveSpawnDeps()
		if err != nil {
			return err
		}
		return runSpawn(deps, spawnFamily, spawnType, spawnPrompt, spawnBranch)
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
		findSprawl:      FindSprawlBin,
	}, nil
}

func runSpawn(deps *spawnDeps, family, agentType, prompt, branch string) error {
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

	// Validate branch
	if branch == "" {
		return fmt.Errorf("--branch is required; provide a descriptive branch name for the agent's worktree")
	}

	// Read environment
	parentName := deps.getenv("SPRAWL_AGENT_IDENTITY")
	if parentName == "" {
		return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set; spawn must be called from within a dendra agent")
	}

	dendraRoot := deps.getenv("SPRAWL_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set; spawn must be called from within a dendra agent")
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

	// Get current branch for worktree base
	baseBranch, err := deps.currentBranch(dendraRoot)
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	// Create worktree
	worktreePath, branchName, err := deps.worktreeCreator.Create(dendraRoot, agentName, branch, baseBranch)
	if err != nil {
		return fmt.Errorf("creating worktree for %s: %w", agentName, err)
	}

	// Find dendra binary
	dendraPath, err := deps.findSprawl()
	if err != nil {
		return fmt.Errorf("finding dendra binary: %w", err)
	}

	// Build shell command: cd to worktree, then run dendra agent-loop
	shellCmd := fmt.Sprintf("cd %s && %s", tmux.ShellQuote(worktreePath), tmux.BuildShellCmd(dendraPath, []string{"agent-loop", agentName}))

	// Resolve namespace: env var > persisted file > default
	namespace := deps.getenv("SPRAWL_NAMESPACE")
	if namespace == "" {
		namespace = state.ReadNamespace(dendraRoot)
	}
	if namespace == "" {
		namespace = tmux.DefaultNamespace
	}

	// Build tree path: parent's tree path + separator + child name
	parentTreePath := deps.getenv("SPRAWL_TREE_PATH")
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
		"SPRAWL_AGENT_IDENTITY": agentName,
		"SPRAWL_ROOT":           dendraRoot,
		"SPRAWL_NAMESPACE":      namespace,
		"SPRAWL_TREE_PATH":      childTreePath,
	}
	if v := deps.getenv("SPRAWL_BIN"); v != "" {
		env["SPRAWL_BIN"] = v
	}
	if v := deps.getenv("SPRAWL_TEST_MODE"); v != "" {
		env["SPRAWL_TEST_MODE"] = v
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

	// Write initial prompt to file and use @file reference
	promptPath, err := state.WritePromptFile(dendraRoot, agentName, "initial", prompt)
	if err != nil {
		return fmt.Errorf("writing initial prompt file: %w", err)
	}

	// Save agent state
	agentState := &state.AgentState{
		Name:        agentName,
		Type:        agentType,
		Family:      family,
		Parent:      parentName,
		Prompt:      fmt.Sprintf("Your task is in @%s — read it and begin working.", promptPath),
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
	fmt.Fprintf(os.Stderr, "Agent will message you when done — no need to poll.\n")
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
