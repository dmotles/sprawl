package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/agentloop"
	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/protocol"
	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

// processManager is the interface for managing a Claude Code subprocess.
type processManager interface {
	Start(ctx context.Context, initialPrompt string) error
	SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error)
	Stop(ctx context.Context) error
	IsRunning() bool
}

// agentLoopDeps holds all injectable dependencies for the agent loop command.
type agentLoopDeps struct {
	getenv       func(string) string
	loadAgent    func(string, string) (*state.AgentState, error)
	nextTask     func(string, string) (*state.Task, error)
	updateTask   func(string, string, *state.Task) error
	listMessages func(string, string, string) ([]*messages.Message, error)
	sendMessage  func(string, string, string, string, string) error
	findClaude   func() (string, error)
	readFile     func(string) ([]byte, error)
	removeFile   func(string) error
	buildPrompt  func(*state.AgentState) string
	sleepFunc    func(time.Duration)
	stdout       io.Writer
	exit         func(int)
	newProcess   func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager
}

// defaultAgentLoopDeps wires real implementations.
func defaultAgentLoopDeps() *agentLoopDeps {
	return &agentLoopDeps{
		getenv:    os.Getenv,
		loadAgent: state.LoadAgent,
		nextTask:  state.NextTask,
		updateTask: state.UpdateTask,
		listMessages: func(root, ag, filter string) ([]*messages.Message, error) {
			return messages.List(root, ag, filter)
		},
		sendMessage: func(root, from, to, subject, body string) error {
			return messages.Send(root, from, to, subject, body)
		},
		findClaude: func() (string, error) {
			return exec.LookPath("claude")
		},
		readFile:  os.ReadFile,
		removeFile: os.Remove,
		buildPrompt: func(a *state.AgentState) string {
			switch a.Type {
			case "researcher":
				return agent.BuildResearcherPrompt(a.Name, a.Parent, a.Branch, a.Prompt)
			default:
				return agent.BuildEngineerPrompt(a.Name, a.Parent, a.Branch, a.Prompt)
			}
		},
		sleepFunc: time.Sleep,
		stdout:    os.Stdout,
		exit:      os.Exit,
		newProcess: func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
			starter := &agentloop.RealCommandStarter{}
			return agentloop.NewProcess(config, starter, agentloop.WithObserver(observer))
		},
	}
}

// tmuxObserver implements agentloop.Observer, writing formatted output to w.
type tmuxObserver struct {
	w io.Writer
}

// OnMessage handles protocol messages from the Claude process.
func (t *tmuxObserver) OnMessage(msg *protocol.Message) {
	switch msg.Type {
	case "assistant":
		var outer struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(msg.Raw, &outer); err != nil {
			return
		}
		for _, block := range outer.Message.Content {
			if block.Type == "text" && block.Text != "" {
				fmt.Fprintf(t.w, "[claude] %s\n", block.Text)
			}
		}

	case "rate_limit_event":
		var evt protocol.RateLimitEvent
		if err := json.Unmarshal(msg.Raw, &evt); err != nil {
			return
		}
		if evt.RateLimitInfo != nil && evt.RateLimitInfo.Status == "blocked" {
			fmt.Fprintf(t.w, "[agent-loop] rate limit blocked (type=%s)\n", evt.RateLimitInfo.RateLimitType)
		}
	}
}

// agentLoopCmd is the hidden cobra command for the agent loop.
var agentLoopCmd = &cobra.Command{
	Use:    "agent-loop <agent-name>",
	Short:  "Run the agent loop for a named agent (internal use)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := defaultAgentLoopDeps()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			<-sigCh
			cancel()
		}()

		return runAgentLoop(ctx, deps, args[0])
	},
}

func init() {
	rootCmd.AddCommand(agentLoopCmd)
}

// startProcess creates and starts a process. On failure it reports to parent and calls exit(1).
// Returns (proc, true) on success, or (nil, false) on failure (after reporting and exiting).
func startProcess(ctx context.Context, deps *agentLoopDeps, config agentloop.ProcessConfig, observer agentloop.Observer, dendraRoot, agentName, parent, reason, initialPrompt string) (processManager, bool) {
	proc := deps.newProcess(config, observer)
	if err := proc.Start(ctx, initialPrompt); err != nil {
		fmt.Fprintf(deps.stdout, "[agent-loop] %s: %v\n", reason, err)
		_ = deps.sendMessage(dendraRoot, agentName, parent, "[PROBLEM] agent-loop failure", fmt.Sprintf("%s: %v", reason, err))
		deps.exit(1)
		return nil, false
	}
	return proc, true
}

// runAgentLoop is the main loop logic for the agent-loop command.
func runAgentLoop(ctx context.Context, deps *agentLoopDeps, agentName string) error {
	// Validate DENDRA_ROOT
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := deps.loadAgent(dendraRoot, agentName)
	if err != nil {
		return fmt.Errorf("loading agent state: %w", err)
	}

	// Find claude binary
	claudePath, err := deps.findClaude()
	if err != nil {
		return fmt.Errorf("finding claude binary: %w", err)
	}

	fmt.Fprintf(deps.stdout, "[agent-loop] starting for agent %q\n", agentName)

	// Build process config
	systemPrompt := deps.buildPrompt(agentState)
	config := agentloop.ProcessConfig{
		AgentName:    agentName,
		WorkDir:      agentState.Worktree,
		SessionID:    agentState.SessionID,
		ClaudePath:   claudePath,
		SystemPrompt: systemPrompt,
		DendraRoot:   dendraRoot,
	}

	// Debug: print full configuration being passed to Claude
	fmt.Fprintf(deps.stdout, "[agent-loop] === SYSTEM PROMPT ===\n")
	for _, line := range strings.Split(systemPrompt, "\n") {
		fmt.Fprintf(deps.stdout, "[agent-loop]   %s\n", line)
	}
	fmt.Fprintf(deps.stdout, "[agent-loop] === INITIAL PROMPT/TASK ===\n")
	fmt.Fprintf(deps.stdout, "[agent-loop]   %s\n", agentState.Prompt)
	fmt.Fprintf(deps.stdout, "[agent-loop] === PROCESS CONFIG ===\n")
	fmt.Fprintf(deps.stdout, "[agent-loop]   agent-name:      %s\n", config.AgentName)
	fmt.Fprintf(deps.stdout, "[agent-loop]   session-id:      %s\n", config.SessionID)
	fmt.Fprintf(deps.stdout, "[agent-loop]   work-dir:        %s\n", config.WorkDir)
	fmt.Fprintf(deps.stdout, "[agent-loop]   claude-path:     %s\n", config.ClaudePath)
	fmt.Fprintf(deps.stdout, "[agent-loop]   setting-sources: %s\n", config.SettingSources)
	fmt.Fprintf(deps.stdout, "[agent-loop]   dendra-root:     %s\n", config.DendraRoot)
	fmt.Fprintf(deps.stdout, "[agent-loop] === KEY ENV VARS ===\n")
	fmt.Fprintf(deps.stdout, "[agent-loop]   DENDRA_AGENT_IDENTITY=%s\n", deps.getenv("DENDRA_AGENT_IDENTITY"))
	fmt.Fprintf(deps.stdout, "[agent-loop]   DENDRA_ROOT=%s\n", deps.getenv("DENDRA_ROOT"))

	observer := &tmuxObserver{w: deps.stdout}

	// Create and start the initial process.
	proc, ok := startProcess(ctx, deps, config, observer, dendraRoot, agentName, agentState.Parent, "failed to start process", agentState.Prompt)
	if !ok {
		return nil
	}
	// Use a closure defer so it always stops the most-recently-assigned proc.
	// Guard against nil in case a restart failure left proc unset.
	defer func() {
		if proc != nil {
			_ = proc.Stop(ctx)
		}
	}()

	// restartWithResume creates a new process with Resume=true after a crash.
	// Returns false (and exits) if the restart fails.
	restartWithResume := func() bool {
		resumeConfig := config
		resumeConfig.Resume = true
		var ok bool
		proc, ok = startProcess(ctx, deps, resumeConfig, observer, dendraRoot, agentName, agentState.Parent, "failed to restart process after crash", agentState.Prompt)
		return ok
	}

	// Main loop
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// 0. Check for kill sentinel file.
		killFilePath := filepath.Join(dendraRoot, ".dendra", "agents", agentName+".kill")
		if _, readErr := deps.readFile(killFilePath); readErr == nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] kill sentinel detected, shutting down\n")
			_ = deps.removeFile(killFilePath)
			return nil // triggers deferred proc.Stop()
		}

		// 1. Check for a queued task.
		task, err := deps.nextTask(dendraRoot, agentName)
		if err != nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] error fetching next task: %v\n", err)
		} else if task != nil {
			task.Status = "in-progress"
			_ = deps.updateTask(dendraRoot, agentName, task)

			fmt.Fprintf(deps.stdout, "[agent-loop] starting task %s\n", task.ID)
			_, sendErr := proc.SendPrompt(ctx, task.Prompt)
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on task %s, restarting: %v\n", task.ID, sendErr)
				if !restartWithResume() {
					return nil
				}
				// Retry on recovered process.
				_, _ = proc.SendPrompt(ctx, task.Prompt)
			}

			task.Status = "done"
			_ = deps.updateTask(dendraRoot, agentName, task)
			continue
		}

		// 2. Check inbox for unread messages.
		msgs, err := deps.listMessages(dendraRoot, agentName, "unread")
		if err == nil && len(msgs) > 0 {
			fmt.Fprintf(deps.stdout, "[agent-loop] new messages in inbox, notifying agent\n")
			_, sendErr := proc.SendPrompt(ctx, "You have new messages in your inbox. Please check your inbox and respond appropriately.")
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on inbox check, restarting: %v\n", sendErr)
				if !restartWithResume() {
					return nil
				}
			}
			deps.sleepFunc(3 * time.Second)
			continue
		}

		// 3. Check for a wake file.
		wakeFilePath := filepath.Join(dendraRoot, ".dendra", "agents", agentName+".wake")
		wakeContent, readErr := deps.readFile(wakeFilePath)
		if readErr == nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] wake file detected\n")
			_, sendErr := proc.SendPrompt(ctx, string(wakeContent))
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on wake, restarting: %v\n", sendErr)
				if !restartWithResume() {
					return nil
				}
			}
			// Only remove the wake file after it has been successfully delivered.
			_ = deps.removeFile(wakeFilePath)
			deps.sleepFunc(3 * time.Second)
			continue
		}

		// 4. Nothing to do — sleep and poll again.
		deps.sleepFunc(3 * time.Second)
	}
}
