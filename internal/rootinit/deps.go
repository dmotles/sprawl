package rootinit

import (
	"context"
	"os"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
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
}

// DefaultDeps wires Deps against real implementations from agent, memory,
// and state packages.
func DefaultDeps() *Deps {
	return &Deps{
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
	}
}
