package agentops

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/runtimecfg"
	"github.com/dmotles/sprawl/internal/state"
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
	WorktreeCreator worktree.Creator
	Getenv          func(string) string
	CurrentBranch   func(repoRoot string) (string, error)
	NewSpawnLock    func(lockPath string) (acquire func() error, release func() error)
	LoadConfig      func(sprawlRoot string) (*config.Config, error)
	RunScript       func(script, workDir string, env map[string]string) ([]byte, error)
	WorktreeRemove  func(repoRoot, worktreePath string, force bool) error
	GitBranchDelete func(repoRoot, branchName string) error
}

type preparedSpawn struct {
	agentState *state.AgentState
	promptPath string
	env        map[string]string
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

func prepareSpawn(deps *SpawnDeps, family, agentType, prompt, branch string) (*preparedSpawn, error) {
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

	// Resolve namespace: env var > persisted file > default
	namespace := deps.Getenv("SPRAWL_NAMESPACE")
	if namespace == "" {
		namespace = state.ReadNamespace(sprawlRoot)
	}
	if namespace == "" {
		namespace = runtimecfg.DefaultNamespace
	}

	// Build tree path: parent's tree path + separator + child name
	parentTreePath := deps.Getenv("SPRAWL_TREE_PATH")
	if parentTreePath == "" {
		// Fallback: use root name from file + parent identity
		rootName := state.ReadRootName(sprawlRoot)
		if rootName == "" {
			rootName = runtimecfg.DefaultRootName
		}
		if parentName == rootName {
			parentTreePath = rootName
		} else {
			parentTreePath = rootName + runtimecfg.TreePathSeparator + parentName
		}
	}
	childTreePath := parentTreePath + runtimecfg.TreePathSeparator + agentName

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

	// Persist state before the supervisor starts the in-process runtime so the
	// child runner can load its metadata immediately.
	agentState := &state.AgentState{
		Name:      agentName,
		Type:      agentType,
		Family:    family,
		Parent:    parentName,
		Prompt:    fmt.Sprintf("Your task is in @%s — read it and begin working.", promptPath),
		Branch:    branchName,
		Worktree:  worktreePath,
		Status:    "active",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SessionID: sessionID,
		TreePath:  childTreePath,
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		return nil, fmt.Errorf("saving agent state: %w", err)
	}

	return &preparedSpawn{
		agentState: agentState,
		promptPath: promptPath,
		env:        env,
	}, nil
}

// PrepareSpawn performs the runtime-neutral child bootstrap: validation,
// worktree creation, prompt/session metadata generation, and persisted state.
func PrepareSpawn(deps *SpawnDeps, family, agentType, prompt, branch string) (*state.AgentState, error) {
	prepared, err := prepareSpawn(deps, family, agentType, prompt, branch)
	if err != nil {
		return nil, err
	}
	return prepared.agentState, nil
}

// Spawn creates the persisted state for a new same-process child runtime.
// Production code should generally go through supervisor.Real so the runtime
// is started immediately after preparation.
func Spawn(deps *SpawnDeps, family, agentType, prompt, branch string) (*state.AgentState, error) {
	return PrepareSpawn(deps, family, agentType, prompt, branch)
}
