package rootinit

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
)

// Deps bundles the injectable dependencies used by Prepare and
// FinalizeHandoff. The pattern mirrors `rootLoopDeps` / `spawnDeps` in
// cmd/ — real wiring lives in DefaultDeps(), tests provide stubs.
type Deps struct {
	// LogPrefix is prepended to status/warning lines emitted by Prepare,
	// FinalizeHandoff, and the spinner. Kept injectable so callers can make
	// messages mode-specific — e.g. the tmux root loop uses "[root-loop]"
	// while `sprawl enter` uses "[enter]" so users don't see a bogus
	// root-loop label after the TUI exits. DefaultDeps sets it to
	// "[root-loop]" for backwards compatibility.
	LogPrefix string

	Getenv                    func(string) string
	BuildPrompt               func(agent.PromptConfig) string
	BuildContextBlob          func(sprawlRoot, rootName string) (string, error)
	WriteSystemPrompt         func(sprawlRoot, rootName, content string) (string, error)
	WriteLastSessionID        func(sprawlRoot, id string) error
	ReadLastSessionID         func(sprawlRoot string) (string, error)
	ReadFile                  func(path string) ([]byte, error)
	RemoveFile                func(path string) error
	NewUUID                   func() (string, error)
	UserHomeDir               func() (string, error)
	NewCLIInvoker             func() memory.ClaudeInvoker
	HasSessionSummary         func(sprawlRoot, sessionID string) (bool, error)
	AutoSummarize             func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error)
	Consolidate               func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error
	UpdatePersistentKnowledge func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error
	ListRecentSessions        func(sprawlRoot string, n int) ([]memory.Session, []string, error)
	ReadTimeline              func(sprawlRoot string) ([]memory.TimelineEntry, error)

	// MemoryModel is the Claude model name to use for consolidation and
	// persistent-knowledge invocations. Empty falls back to the claude CLI
	// default. Initial value comes from DefaultMemoryModel; loadMemoryModel
	// overrides from .sprawl/config.yaml `memory_model` if set.
	MemoryModel string

	// LoadMemoryModel reads the user's override (if any) for the memory
	// distillation model from .sprawl/config.yaml. Injected for testability.
	// Returns an empty string if no override is present.
	LoadMemoryModel func(sprawlRoot string) string

	// SaveAgent persists an AgentState to disk. Used to create the root
	// agent's state file during initialization.
	SaveAgent func(sprawlRoot string, agent *state.AgentState) error

	// LoadAgent reads an AgentState from disk. Used on the resume path
	// to avoid overwriting meaningful fields.
	LoadAgent func(sprawlRoot, name string) (*state.AgentState, error)

	// CurrentBranch returns the current git branch for the given repo root.
	CurrentBranch func(repoRoot string) (string, error)

	// BackgroundConsolidate runs the consolidation pipeline off the
	// handoff critical path and returns a channel closed when the
	// pipeline completes. The default wiring uses StartBackgroundConsolidation
	// (flock-protected goroutine). Tests swap in a synchronous implementation
	// so they can assert ordering deterministically.
	BackgroundConsolidate func(sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) <-chan struct{}
}

// DefaultDeps wires Deps against real implementations from agent, memory,
// and state packages.
func DefaultDeps() *Deps {
	d := &Deps{
		LogPrefix:   "[root-loop]",
		Getenv:      os.Getenv,
		BuildPrompt: agent.BuildRootPrompt,
		BuildContextBlob: func(sprawlRoot, rootName string) (string, error) {
			return memory.BuildContextBlob(sprawlRoot, rootName)
		},
		WriteSystemPrompt:         state.WriteSystemPrompt,
		WriteLastSessionID:        memory.WriteLastSessionID,
		ReadLastSessionID:         memory.ReadLastSessionID,
		ReadFile:                  os.ReadFile,
		RemoveFile:                os.Remove,
		NewUUID:                   state.GenerateUUID,
		UserHomeDir:               os.UserHomeDir,
		NewCLIInvoker:             func() memory.ClaudeInvoker { return memory.NewCLIInvoker() },
		HasSessionSummary:         memory.HasSessionSummary,
		AutoSummarize:             memory.AutoSummarize,
		Consolidate:               memory.Consolidate,
		UpdatePersistentKnowledge: memory.UpdatePersistentKnowledge,
		ListRecentSessions:        memory.ListRecentSessions,
		ReadTimeline:              memory.ReadTimeline,
		SaveAgent:                 state.SaveAgent,
		LoadAgent:                 state.LoadAgent,
		CurrentBranch:             gitCurrentBranch,
		MemoryModel:               memory.DefaultMemoryModel,
		LoadMemoryModel:           defaultLoadMemoryModel,
	}
	d.BackgroundConsolidate = func(sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) <-chan struct{} {
		return StartBackgroundConsolidation(d, sprawlRoot, stdout, events)
	}
	return d
}

// gitCurrentBranch returns the current git branch for the given repo root.
func gitCurrentBranch(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// defaultLoadMemoryModel reads .sprawl/config.yaml and returns the
// `memory_model` override if present. Returns "" on any error so callers
// fall back to Deps.MemoryModel (which is DefaultMemoryModel unless set).
func defaultLoadMemoryModel(sprawlRoot string) string {
	cfg, err := config.Load(sprawlRoot)
	if err != nil {
		return ""
	}
	v, ok := cfg.Get("memory_model")
	if !ok {
		return ""
	}
	return v
}
