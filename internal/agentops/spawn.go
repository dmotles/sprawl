package agentops

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/dmotles/sprawl/internal/worktree"
)

// ValidTypes lists the known agent types.
var ValidTypes = []string{"manager", "researcher", "engineer", "tester", "code-merger"}

// ValidFamilies lists the known agent families.
var ValidFamilies = []string{"engineering", "product", "qa"}

// SupportedTypes are the types that are fully implemented.
var SupportedTypes = map[string]bool{
	"engineer":   true,
	"researcher": true,
	"manager":    true,
}

// SpawnDeps holds the injectable dependencies for Spawn.
type SpawnDeps struct {
	TmuxRunner      tmux.Runner
	WorktreeCreator worktree.Creator
	Getenv          func(string) string
	CurrentBranch   func(repoRoot string) (string, error)
	FindSprawl      func() (string, error)
	NewSpawnLock    func(lockPath string) (acquire func() error, release func() error)
	LoadConfig      func(sprawlRoot string) (*config.Config, error)
	RunScript       func(script, workDir string, env map[string]string) ([]byte, error)
	WorktreeRemove  func(repoRoot, worktreePath string, force bool) error
}

// IsValidType reports whether t is in ValidTypes.
func IsValidType(t string) bool {
	for _, v := range ValidTypes {
		if v == t {
			return true
		}
	}
	return false
}

// IsValidFamily reports whether f is in ValidFamilies.
func IsValidFamily(f string) bool {
	for _, v := range ValidFamilies {
		if v == f {
			return true
		}
	}
	return false
}

// Spawn creates a new agent with its own worktree and tmux window.
// On success, returns the persisted AgentState so callers can surface the allocated name.
func Spawn(deps *SpawnDeps, family, agentType, prompt, branch string) (*state.AgentState, error) {
	// Validate type
	if !IsValidType(agentType) {
		return nil, fmt.Errorf("invalid agent type %q; valid types: %v", agentType, ValidTypes)
	}
	if !SupportedTypes[agentType] {
		return nil, fmt.Errorf("agent type %q is not yet supported; currently supported: engineer, researcher", agentType)
	}

	// Validate family
	if !IsValidFamily(family) {
		return nil, fmt.Errorf("invalid agent family %q; valid families: %v", family, ValidFamilies)
	}

	// Validate branch
	if branch == "" {
		return nil, fmt.Errorf("--branch is required; provide a descriptive branch name for the agent's worktree")
	}

	// Read environment
	parentName := deps.Getenv("SPRAWL_AGENT_IDENTITY")
	if parentName == "" {
		return nil, fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set; spawn must be called from within a sprawl agent")
	}

	sprawlRoot := deps.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return nil, fmt.Errorf("SPRAWL_ROOT environment variable is not set; spawn must be called from within a sprawl agent")
	}

	// Allocate name (inside spawn lock to prevent concurrent name collisions)
	agentsDir := state.AgentsDir(sprawlRoot)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil { //nolint:gosec // G301: world-readable agent dir is intentional
		return nil, fmt.Errorf("creating agents directory: %w", err)
	}

	lockPath := filepath.Join(agentsDir, ".spawn.lock")
	acquire, release := deps.NewSpawnLock(lockPath)
	if err := acquire(); err != nil {
		return nil, fmt.Errorf("acquiring spawn lock: %w", err)
	}
	defer release() //nolint:errcheck // best-effort lock release

	agentName, err := agent.AllocateName(agentsDir, agentType)
	if err != nil {
		return nil, err
	}

	// Get current branch for worktree base
	baseBranch, err := deps.CurrentBranch(sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("determining current branch: %w", err)
	}

	// Create worktree
	worktreePath, branchName, err := deps.WorktreeCreator.Create(sprawlRoot, agentName, branch, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("creating worktree for %s: %w", agentName, err)
	}

	// Run worktree setup script if configured
	cfg, cfgErr := deps.LoadConfig(sprawlRoot)
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load config: %v\n", cfgErr)
	} else if setupScript, ok := cfg.Get("worktree.setup"); ok && setupScript != "" {
		setupEnv := map[string]string{
			"SPRAWL_AGENT_IDENTITY": agentName,
			"SPRAWL_ROOT":           sprawlRoot,
		}
		fmt.Fprintf(os.Stderr, "Running worktree setup script for %s...\n", agentName)
		output, scriptErr := deps.RunScript(setupScript, worktreePath, setupEnv)
		if scriptErr != nil {
			// Clean up the partially-created worktree
			_ = deps.WorktreeRemove(sprawlRoot, worktreePath, true)
			return nil, fmt.Errorf("worktree setup script failed for %s:\n%s\nEscalate to your parent agent or the user — agent spawning is broken and needs attention", agentName, string(output))
		}
	}

	// Find sprawl binary
	sprawlPath, err := deps.FindSprawl()
	if err != nil {
		return nil, fmt.Errorf("finding sprawl binary: %w", err)
	}

	// Build shell command: cd to worktree, then run sprawl agent-loop
	shellCmd := fmt.Sprintf("cd %s && %s", tmux.ShellQuote(worktreePath), tmux.BuildShellCmd(sprawlPath, []string{"agent-loop", agentName}))

	// Resolve namespace: env var > persisted file > default
	namespace := deps.Getenv("SPRAWL_NAMESPACE")
	if namespace == "" {
		namespace = state.ReadNamespace(sprawlRoot)
	}
	if namespace == "" {
		namespace = tmux.DefaultNamespace
	}

	// Build tree path: parent's tree path + separator + child name
	parentTreePath := deps.Getenv("SPRAWL_TREE_PATH")
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
	if v := deps.Getenv("SPRAWL_BIN"); v != "" {
		env["SPRAWL_BIN"] = v
	}
	if v := deps.Getenv("SPRAWL_TEST_MODE"); v != "" {
		env["SPRAWL_TEST_MODE"] = v
	}

	// Compute children session name (pure computation, no tmux dependency).
	childrenSession := tmux.ChildrenSessionName(namespace, parentTreePath)

	// Generate a UUID for the Claude session ID
	sessionID, err := state.GenerateUUID()
	if err != nil {
		return nil, fmt.Errorf("generating session ID: %w", err)
	}

	// Write initial prompt to file and use @file reference
	promptPath, err := state.WritePromptFile(sprawlRoot, agentName, "initial", prompt)
	if err != nil {
		return nil, fmt.Errorf("writing initial prompt file: %w", err)
	}

	// Save agent state BEFORE creating tmux session/window.
	// The agent-loop command started by tmux needs the state file at startup.
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
		return nil, fmt.Errorf("saving agent state: %w", err)
	}

	// Create or add to tmux session using try-then-fallback to avoid TOCTOU race.
	// Multiple concurrent spawns may all try to create the session simultaneously.
	if err := deps.TmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
		// Session may not exist yet — try creating it
		if err := deps.TmuxRunner.NewSessionWithWindow(childrenSession, agentName, env, shellCmd); err != nil {
			// Another concurrent spawn may have just created the session — retry NewWindow
			if err := deps.TmuxRunner.NewWindow(childrenSession, agentName, env, shellCmd); err != nil {
				// Clean up state and prompt files since tmux creation failed
				_ = state.DeleteAgent(sprawlRoot, agentName)
				_ = os.RemoveAll(filepath.Dir(promptPath))
				return nil, fmt.Errorf("creating tmux window/session for %s: %w", agentName, err)
			}
		}
	}

	// Apply the branded tmux config if it exists (generated at init time).
	confPath := filepath.Join(sprawlRoot, ".sprawl", "tmux.conf")
	if _, statErr := os.Stat(confPath); statErr == nil {
		if err := deps.TmuxRunner.SourceFile(childrenSession, confPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not apply tmux config: %v\n", err)
		}
	}

	fmt.Fprintf(os.Stderr, "Spawned %s %s (branch: %s)\n", agentType, agentName, branchName)
	fmt.Fprintf(os.Stderr, "Agent will message you when done — no need to poll.\n")
	return agentState, nil
}
