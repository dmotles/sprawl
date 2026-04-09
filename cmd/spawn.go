package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/gofrs/flock"
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
	newSpawnLock    func(lockPath string) (acquire func() error, release func() error)
	loadConfig      func(sprawlRoot string) (*config.Config, error)
	runScript       func(script, workDir string, env map[string]string) ([]byte, error)
	worktreeRemove  func(repoRoot, worktreePath string, force bool) error
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
		newSpawnLock: func(lockPath string) (func() error, func() error) {
			fl := flock.New(lockPath)
			return fl.Lock, fl.Unlock
		},
		loadConfig:     config.Load,
		runScript:      runBashScript,
		worktreeRemove: realWorktreeRemove,
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
		return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set; spawn must be called from within a sprawl agent")
	}

	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set; spawn must be called from within a sprawl agent")
	}

	// Allocate name (inside spawn lock to prevent concurrent name collisions)
	agentsDir := state.AgentsDir(sprawlRoot)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil { //nolint:gosec // G301: world-readable agent dir is intentional
		return fmt.Errorf("creating agents directory: %w", err)
	}

	lockPath := filepath.Join(agentsDir, ".spawn.lock")
	acquire, release := deps.newSpawnLock(lockPath)
	if err := acquire(); err != nil {
		return fmt.Errorf("acquiring spawn lock: %w", err)
	}
	defer release() //nolint:errcheck // best-effort lock release

	agentName, err := agent.AllocateName(agentsDir, agentType)
	if err != nil {
		return err
	}

	// Get current branch for worktree base
	baseBranch, err := deps.currentBranch(sprawlRoot)
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	// Create worktree
	worktreePath, branchName, err := deps.worktreeCreator.Create(sprawlRoot, agentName, branch, baseBranch)
	if err != nil {
		return fmt.Errorf("creating worktree for %s: %w", agentName, err)
	}

	// Run worktree setup script if configured
	cfg, cfgErr := deps.loadConfig(sprawlRoot)
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load config: %v\n", cfgErr)
	} else if setupScript, ok := cfg.Get("worktree.setup"); ok && setupScript != "" {
		setupEnv := map[string]string{
			"SPRAWL_AGENT_IDENTITY": agentName,
			"SPRAWL_ROOT":           sprawlRoot,
		}
		fmt.Fprintf(os.Stderr, "Running worktree setup script for %s...\n", agentName)
		output, scriptErr := deps.runScript(setupScript, worktreePath, setupEnv)
		if scriptErr != nil {
			// Clean up the partially-created worktree
			_ = deps.worktreeRemove(sprawlRoot, worktreePath, true)
			return fmt.Errorf("worktree setup script failed for %s:\n%s\nEscalate to your parent agent or the user — agent spawning is broken and needs attention", agentName, string(output))
		}
	}

	// Find sprawl binary
	sprawlPath, err := deps.findSprawl()
	if err != nil {
		return fmt.Errorf("finding sprawl binary: %w", err)
	}

	// Build shell command: cd to worktree, then run sprawl agent-loop
	shellCmd := fmt.Sprintf("cd %s && %s", tmux.ShellQuote(worktreePath), tmux.BuildShellCmd(sprawlPath, []string{"agent-loop", agentName}))

	// Resolve namespace: env var > persisted file > default
	namespace := deps.getenv("SPRAWL_NAMESPACE")
	if namespace == "" {
		namespace = state.ReadNamespace(sprawlRoot)
	}
	if namespace == "" {
		namespace = tmux.DefaultNamespace
	}

	// Build tree path: parent's tree path + separator + child name
	parentTreePath := deps.getenv("SPRAWL_TREE_PATH")
	if parentTreePath == "" {
		// Fallback: use root name from file + parent identity
		rootName := state.ReadRootName(sprawlRoot)
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
		"SPRAWL_ROOT":           sprawlRoot,
		"SPRAWL_NAMESPACE":      namespace,
		"SPRAWL_TREE_PATH":      childTreePath,
	}
	if v := deps.getenv("SPRAWL_BIN"); v != "" {
		env["SPRAWL_BIN"] = v
	}
	if v := deps.getenv("SPRAWL_TEST_MODE"); v != "" {
		env["SPRAWL_TEST_MODE"] = v
	}

	// Create or add to tmux session using try-then-fallback to avoid TOCTOU race.
	// Multiple concurrent spawns may all try to create the session simultaneously.
	childrenSession := tmux.ChildrenSessionName(namespace, parentTreePath)
	if err := deps.tmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
		// Session may not exist yet — try creating it
		if err := deps.tmuxRunner.NewSessionWithWindow(childrenSession, agentName, env, shellCmd); err != nil {
			// Another concurrent spawn may have just created the session — retry NewWindow
			if err := deps.tmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
				return fmt.Errorf("creating tmux window/session for %s: %w", agentName, err)
			}
		}
	}

	// Apply the branded tmux config if it exists (generated at init time).
	confPath := filepath.Join(sprawlRoot, ".sprawl", "tmux.conf")
	if _, statErr := os.Stat(confPath); statErr == nil {
		if err := deps.tmuxRunner.SourceFile(childrenSession, confPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not apply tmux config: %v\n", err)
		}
	}

	// Generate a UUID for the Claude session ID
	sessionID, err := state.GenerateUUID()
	if err != nil {
		return fmt.Errorf("generating session ID: %w", err)
	}

	// Write initial prompt to file and use @file reference
	promptPath, err := state.WritePromptFile(sprawlRoot, agentName, "initial", prompt)
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
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
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
