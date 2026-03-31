package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/dmotles/dendrarchy/internal/agent"
	"github.com/dmotles/dendrarchy/internal/state"
	"github.com/dmotles/dendrarchy/internal/tmux"
	"github.com/dmotles/dendrarchy/internal/worktree"
	"github.com/spf13/cobra"
)

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
	claudeLauncher  agent.Launcher
	worktreeCreator worktree.Creator
	getenv          func(string) string
	currentBranch   func(repoRoot string) (string, error)
}

var defaultSpawnDeps *spawnDeps

var (
	spawnFamily string
	spawnType   string
	spawnPrompt string
)

func init() {
	spawnCmd.Flags().StringVar(&spawnFamily, "family", "", "agent family: engineering, product, qa")
	spawnCmd.Flags().StringVar(&spawnType, "type", "", "agent type: manager, researcher, engineer, tester, code-merger")
	spawnCmd.Flags().StringVar(&spawnPrompt, "prompt", "", "task description for the agent")
	spawnCmd.MarkFlagRequired("family")
	spawnCmd.MarkFlagRequired("type")
	spawnCmd.MarkFlagRequired("prompt")
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

	claudeLauncher := &agent.RealLauncher{}
	if _, err := claudeLauncher.FindBinary(); err != nil {
		return nil, fmt.Errorf("claude CLI is required but not found")
	}

	return &spawnDeps{
		tmuxRunner:      &tmux.RealRunner{TmuxPath: tmuxPath},
		claudeLauncher:  claudeLauncher,
		worktreeCreator: &worktree.RealCreator{},
		getenv:          os.Getenv,
		currentBranch:   gitCurrentBranch,
	}, nil
}

func runSpawn(deps *spawnDeps, family, agentType, prompt string) error {
	// Validate type
	if !isValidType(agentType) {
		return fmt.Errorf("invalid agent type %q; valid types: %v", agentType, validTypes)
	}
	if !supportedTypes[agentType] {
		return fmt.Errorf("agent type %q is not yet supported; currently supported: engineer", agentType)
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

	// Build system prompt
	var systemPrompt string
	switch agentType {
	case "researcher":
		systemPrompt = agent.BuildResearcherPrompt(agentName, parentName, branchName, prompt)
	default:
		systemPrompt = agent.BuildEngineerPrompt(agentName, parentName, branchName, prompt)
	}

	// Build claude args
	claudePath, err := deps.claudeLauncher.FindBinary()
	if err != nil {
		return fmt.Errorf("claude CLI not found: %w", err)
	}

	opts := agent.LaunchOpts{
		SystemPrompt:               systemPrompt,
		InitialPrompt:              "You have been assigned a task. Read your system prompt and begin working immediately. When finished, run: dendra report done",
		Name:                       "dendra-" + agentName,
		Agents:                     agent.TDDSubAgentsJSON(),
		DangerouslySkipPermissions: true,
	}
	claudeArgs := deps.claudeLauncher.BuildArgs(opts)

	// Build shell command: cd to worktree, then run claude
	shellCmd := fmt.Sprintf("cd %s && %s", tmux.ShellQuote(worktreePath), tmux.BuildShellCmd(claudePath, claudeArgs))

	// Set environment for the child agent
	env := map[string]string{
		"DENDRA_AGENT_IDENTITY": agentName,
		"DENDRA_ROOT":           dendraRoot,
	}

	// Create or add to tmux session
	childrenSession := "dendra-" + parentName + "-children"
	if deps.tmuxRunner.HasSession(childrenSession) {
		if err := deps.tmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
			return fmt.Errorf("creating tmux window for %s: %w", agentName, err)
		}
	} else {
		if err := deps.tmuxRunner.NewSessionWithWindow(childrenSession, agentName, env, shellCmd); err != nil {
			return fmt.Errorf("creating tmux session for %s: %w", agentName, err)
		}
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
