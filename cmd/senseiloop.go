package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

const (
	maxConsecutiveFailures = 3
	retryDelay             = 5 * time.Second
)

// rootSenseiTools is the set of tools available to the root sensei agent.
var rootSenseiTools = []string{
	"Bash", "Read", "Glob", "Grep", "WebSearch", "WebFetch",
	"Agent", "Task", "TaskOutput", "TaskStop", "ToolSearch",
	"Skill", "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet",
	"AskUserQuestion", "EnterPlanMode", "ExitPlanMode",
}

// senseiLoopDeps holds all injectable dependencies for the sensei-loop command.
type senseiLoopDeps struct {
	getenv                    func(string) string
	findClaude                func() (string, error)
	buildPrompt               func(agent.PromptConfig) string
	buildContextBlob          func(dendraRoot, rootName string) (string, error)
	writeSystemPrompt         func(string, string, string) (string, error)
	writeLastSessionID        func(string, string) error
	readFile                  func(string) ([]byte, error)
	removeFile                func(string) error
	newUUID                   func() (string, error)
	sleepFunc                 func(time.Duration)
	runCommand                func(name string, args []string) error
	stdout                    io.Writer
	readLastSessionID         func(string) (string, error)
	autoSummarize             func(ctx context.Context, dendraRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error)
	userHomeDir               func() (string, error)
	newCLIInvoker             func() memory.ClaudeInvoker
	consolidate               func(ctx context.Context, dendraRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error
	updatePersistentKnowledge func(ctx context.Context, dendraRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error
	listRecentSessions        func(dendraRoot string, n int) ([]memory.Session, []string, error)
	readTimeline              func(dendraRoot string) ([]memory.TimelineEntry, error)
}

// defaultSenseiLoopDeps wires real implementations.
func defaultSenseiLoopDeps() *senseiLoopDeps {
	return &senseiLoopDeps{
		getenv:      os.Getenv,
		findClaude:  func() (string, error) { return exec.LookPath("claude") },
		buildPrompt: agent.BuildRootPrompt,
		buildContextBlob: func(dendraRoot, rootName string) (string, error) {
			return memory.BuildContextBlob(dendraRoot, rootName)
		},
		writeSystemPrompt:  state.WriteSystemPrompt,
		writeLastSessionID: memory.WriteLastSessionID,
		readFile:           os.ReadFile,
		removeFile:         os.Remove,
		newUUID:            state.GenerateUUID,
		sleepFunc:          time.Sleep,
		runCommand: func(name string, args []string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
		stdout:                    os.Stdout,
		readLastSessionID:         memory.ReadLastSessionID,
		autoSummarize:             memory.AutoSummarize,
		userHomeDir:               os.UserHomeDir,
		newCLIInvoker:             func() memory.ClaudeInvoker { return memory.NewCLIInvoker() },
		consolidate:               memory.Consolidate,
		updatePersistentKnowledge: memory.UpdatePersistentKnowledge,
		listRecentSessions:        memory.ListRecentSessions,
		readTimeline:              memory.ReadTimeline,
	}
}

var senseiLoopCmd = &cobra.Command{
	Use:    "sensei-loop",
	Short:  "Run the sensei lifecycle loop (internal use)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		deps := defaultSenseiLoopDeps()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-sigCh
			cancel()
		}()

		return runSenseiLoop(ctx, deps)
	},
}

func init() {
	rootCmd.AddCommand(senseiLoopCmd)
}

// runSenseiLoop is the main loop logic for the sensei-loop command.
func runSenseiLoop(ctx context.Context, deps *senseiLoopDeps) error {
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	namespace := deps.getenv("DENDRA_NAMESPACE")
	rootName := tmux.DefaultRootName

	claudePath, err := deps.findClaude()
	if err != nil {
		return fmt.Errorf("finding claude binary: %w", err)
	}

	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// 0. Detect missed handoff from previous session.
		prevSessionID, _ := deps.readLastSessionID(dendraRoot)
		if prevSessionID != "" {
			homeDir, homeErr := deps.userHomeDir()
			if homeErr != nil {
				fmt.Fprintf(deps.stdout, "[sensei-loop] warning: could not determine home directory, skipping auto-summarize: %v\n", homeErr)
			} else {
				summarized, sumErr := deps.autoSummarize(ctx, dendraRoot, dendraRoot, homeDir, prevSessionID, deps.newCLIInvoker())
				if sumErr != nil {
					fmt.Fprintf(deps.stdout, "[sensei-loop] warning: auto-summarize failed for %s: %v\n", prevSessionID, sumErr)
				} else if summarized {
					fmt.Fprintf(deps.stdout, "[sensei-loop] auto-summarized missed handoff for session %s\n", prevSessionID)
					runConsolidationPipeline(ctx, deps, dendraRoot)
				}
			}
		}

		// 1. Build context blob (best-effort).
		contextBlob, ctxErr := deps.buildContextBlob(dendraRoot, rootName)
		if ctxErr != nil {
			fmt.Fprintf(deps.stdout, "[sensei-loop] warning: context blob partial or failed: %v\n", ctxErr)
		}

		// 2. Build system prompt.
		systemPrompt := deps.buildPrompt(agent.PromptConfig{
			RootName:    rootName,
			AgentCLI:    "claude-code",
			ContextBlob: contextBlob,
			TestMode:    deps.getenv("DENDRA_TEST_MODE") == "1",
		})
		promptPath, err := deps.writeSystemPrompt(dendraRoot, rootName, systemPrompt)
		if err != nil {
			return fmt.Errorf("writing system prompt: %w", err)
		}

		// 3. Generate session ID.
		sessionID, err := deps.newUUID()
		if err != nil {
			return fmt.Errorf("generating session ID: %w", err)
		}
		if err := deps.writeLastSessionID(dendraRoot, sessionID); err != nil {
			return fmt.Errorf("writing session ID: %w", err)
		}

		// 4. Build claude args (interactive mode).
		sessionName := tmux.RootSessionName(namespace, rootName)
		opts := claude.LaunchOpts{
			SystemPromptFile: promptPath,
			Tools:            rootSenseiTools,
			AllowedTools:     rootSenseiTools,
			DisallowedTools:  []string{"Edit", "Write", "NotebookEdit"},
			Name:             sessionName,
			Model:            "opus[1m]",
			SessionID:        sessionID,
		}
		args := opts.BuildArgs()

		fmt.Fprintf(deps.stdout, "[sensei-loop] starting session %s\n", sessionID)

		// 5. Run claude interactively (blocks until exit).
		runErr := deps.runCommand(claudePath, args)

		// Check if context was cancelled (signal received).
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				// Claude ran but exited non-zero — treat as normal exit.
				fmt.Fprintf(deps.stdout, "[sensei-loop] claude exited with code %d\n", exitErr.ExitCode())
				consecutiveFailures = 0
			} else {
				// Failed to start — retry logic.
				consecutiveFailures++
				if consecutiveFailures >= maxConsecutiveFailures {
					return fmt.Errorf("claude failed to start %d consecutive times, giving up: %w", maxConsecutiveFailures, runErr)
				}
				fmt.Fprintf(deps.stdout, "[sensei-loop] claude failed to start, retrying in %v (%d/%d): %v\n",
					retryDelay, consecutiveFailures, maxConsecutiveFailures, runErr)
				deps.sleepFunc(retryDelay)
				continue
			}
		} else {
			consecutiveFailures = 0
		}

		// 6. Housekeeping: check handoff signal.
		handoffPath := filepath.Join(dendraRoot, ".dendra", "memory", "handoff-signal")
		if _, readErr := deps.readFile(handoffPath); readErr == nil {
			_ = deps.removeFile(handoffPath)
			fmt.Fprintf(deps.stdout, "[sensei-loop] handoff signal detected, restarting\n")

			runConsolidationPipeline(ctx, deps, dendraRoot)
		} else {
			fmt.Fprintf(deps.stdout, "[sensei-loop] session ended, restarting\n")
		}

		// 7. Loop back to step 1.
	}
}

// runConsolidationPipeline runs timeline consolidation and persistent knowledge
// update. Both steps are best-effort: failures are logged as warnings.
func runConsolidationPipeline(ctx context.Context, deps *senseiLoopDeps, dendraRoot string) {
	if err := deps.consolidate(ctx, dendraRoot, deps.newCLIInvoker(), nil, nil); err != nil {
		fmt.Fprintf(deps.stdout, "[sensei-loop] warning: consolidation failed: %v\n", err)
	}

	var sessionSummary string
	if sessions, bodies, err := deps.listRecentSessions(dendraRoot, 1); err != nil {
		fmt.Fprintf(deps.stdout, "[sensei-loop] warning: reading latest session for persistent knowledge: %v\n", err)
	} else if len(sessions) > 0 && len(bodies) > 0 {
		sessionSummary = bodies[0]
	}

	var timelineBullets string
	if entries, err := deps.readTimeline(dendraRoot); err != nil {
		fmt.Fprintf(deps.stdout, "[sensei-loop] warning: reading timeline for persistent knowledge: %v\n", err)
	} else {
		var tlb strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&tlb, "- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
		}
		timelineBullets = tlb.String()
	}

	if err := deps.updatePersistentKnowledge(ctx, dendraRoot, deps.newCLIInvoker(), nil, sessionSummary, timelineBullets); err != nil {
		fmt.Fprintf(deps.stdout, "[sensei-loop] warning: persistent knowledge update failed: %v\n", err)
	}
}
