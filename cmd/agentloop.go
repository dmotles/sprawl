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
	"sync"
	"syscall"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

// workLock provides acquire/release functions for the agent work lock.
type workLock struct {
	Acquire func() error
	Release func() error
}

// processManager is the interface for managing a Claude Code subprocess.
type processManager interface {
	Launch(ctx context.Context) error
	SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error)
	InterruptTurn(ctx context.Context) error
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
	mkdirAll     func(string, os.FileMode) error
	createFile   func(string) (*os.File, error)
	stdout       io.Writer
	exit         func(int)
	newProcess   func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager
	newWorkLock  func(lockDir, agentName string) (*workLock, error)
	getpid       func() int
	signalCh     <-chan os.Signal
}

// defaultAgentLoopDeps wires real implementations.
func defaultAgentLoopDeps() *agentLoopDeps {
	return &agentLoopDeps{
		getenv:       os.Getenv,
		loadAgent:    state.LoadAgent,
		nextTask:     state.NextTask,
		updateTask:   state.UpdateTask,
		listMessages: messages.List,
		sendMessage: func(root, from, to, subject, body string) error {
			return messages.Send(root, from, to, subject, body)
		},
		findClaude: func() (string, error) {
			return exec.LookPath("claude")
		},
		readFile:   os.ReadFile,
		removeFile: os.Remove,
		buildPrompt: func(a *state.AgentState) string {
			testMode := os.Getenv("SPRAWL_TEST_MODE") == "1"
			switch a.Type {
			case "researcher":
				env := agent.DefaultEnvConfig()
				env.TestMode = testMode
				return agent.BuildResearcherPrompt(a.Name, a.Parent, a.Branch, env)
			case "manager":
				env := agent.DefaultEnvConfig()
				env.WorkDir = a.Worktree
				env.TestMode = testMode
				return agent.BuildManagerPrompt(a.Name, a.Parent, a.Branch, a.Family, env)
			default:
				env := agent.DefaultEnvConfig()
				env.WorkDir = a.Worktree
				env.TestMode = testMode
				return agent.BuildEngineerPrompt(a.Name, a.Parent, a.Branch, env)
			}
		},
		sleepFunc:  time.Sleep,
		mkdirAll:   os.MkdirAll,
		createFile: os.Create,
		stdout:     os.Stdout,
		exit:       os.Exit,
		newProcess: func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager {
			starter := &agentloop.RealCommandStarter{}
			return agentloop.NewProcess(config, starter, agentloop.WithObserver(observer))
		},
		newWorkLock: func(lockDir, agentName string) (*workLock, error) {
			if err := os.MkdirAll(lockDir, 0o755); err != nil { //nolint:gosec // G301: world-readable lock dir is intentional
				return nil, fmt.Errorf("creating locks directory: %w", err)
			}
			lockPath := filepath.Join(lockDir, agentName+".lock")
			fl := flock.New(lockPath)
			return &workLock{
				Acquire: func() error { return fl.Lock() },
				Release: func() error { return fl.Unlock() },
			}, nil
		},
		getpid: os.Getpid,
	}
}

// timestampWriter wraps an io.Writer and prepends [HH:MM:SS] timestamps to each line.
type timestampWriter struct {
	w       io.Writer
	nowFunc func() time.Time
	mu      sync.Mutex
}

// Write prepends a timestamp to each line in p.
func (tw *timestampWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	lines := strings.Split(string(p), "\n")
	var buf strings.Builder
	for i, line := range lines {
		// Skip empty trailing element from trailing newline
		if i == len(lines)-1 && line == "" {
			break
		}
		ts := tw.nowFunc().Format("15:04:05")
		buf.WriteString("[")
		buf.WriteString(ts)
		buf.WriteString("] ")
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	_, err := io.WriteString(tw.w, buf.String())
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// tmuxObserver implements agentloop.Observer, writing formatted output to w.
// If ring is non-nil, every protocol message is also recorded into the
// activity ring (and, if the ring was constructed with a writer, appended to
// the per-agent activity.ndjson file for cross-process readers).
type tmuxObserver struct {
	w    io.Writer
	ring *agentloop.ActivityRing
}

// truncateStr truncates s to maxLen bytes, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// OnMessage handles protocol messages from the Claude process.
func (t *tmuxObserver) OnMessage(msg *protocol.Message) {
	if t.ring != nil {
		t.ring.RecordMessage(msg, time.Now)
	}
	switch msg.Type {
	case "assistant":
		var outer struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name,omitempty"`
					Input json.RawMessage `json:"input,omitempty"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(msg.Raw, &outer); err != nil {
			return
		}
		for _, block := range outer.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					fmt.Fprintf(t.w, "[claude] %s\n", block.Text)
				}
			case "tool_use":
				if block.Name != "" {
					inputStr := truncateStr(string(block.Input), 200)
					fmt.Fprintf(t.w, "[tool] %s: %s\n", block.Name, inputStr)
				}
			}
		}

	case "system":
		if msg.Subtype == "session_state_changed" {
			var ssc protocol.SessionStateChanged
			if err := json.Unmarshal(msg.Raw, &ssc); err == nil && ssc.State != "" {
				fmt.Fprintf(t.w, "[system] %s: %s\n", msg.Subtype, ssc.State)
				return
			}
		}
		if msg.Subtype != "" {
			fmt.Fprintf(t.w, "[system] %s\n", msg.Subtype)
		}

	case "result":
		var res protocol.ResultMessage
		if err := json.Unmarshal(msg.Raw, &res); err != nil {
			return
		}
		status := "success"
		if res.IsError {
			status = "error"
		}
		fmt.Fprintf(t.w, "[result] %s (stop=%s, turns=%d)\n", status, res.StopReason, res.NumTurns)

	case "rate_limit_event":
		var evt protocol.RateLimitEvent
		if err := json.Unmarshal(msg.Raw, &evt); err != nil {
			return
		}
		if evt.RateLimitInfo != nil && evt.RateLimitInfo.Status == "blocked" {
			fmt.Fprintf(t.w, "[agent-loop] rate limit blocked (type=%s)\n", evt.RateLimitInfo.RateLimitType)
		}

	case "user":
		// Protocol-level echo messages — silently discard (no observability value).

	default:
		fmt.Fprintf(t.w, "[agent-loop] message: type=%s subtype=%s\n", msg.Type, msg.Subtype)
	}
}

// agentLoopCmd is the hidden cobra command for the agent loop.
var agentLoopCmd = &cobra.Command{
	Use:    "agent-loop <agent-name>",
	Short:  "Run the agent loop for a named agent (internal use)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := defaultAgentLoopDeps()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		deps.signalCh = sigCh

		ctx := context.Background()
		return runAgentLoop(ctx, deps, args[0])
	},
}

func init() {
	rootCmd.AddCommand(agentLoopCmd)
}

// startProcess creates and launches a process (without sending a prompt).
// On failure it reports to parent and calls exit(1).
// Returns (proc, true) on success, or (nil, false) on failure (after reporting and exiting).
func startProcess(ctx context.Context, deps *agentLoopDeps, config agentloop.ProcessConfig, observer agentloop.Observer, sprawlRoot, agentName, parent, reason string) (processManager, bool) {
	proc := deps.newProcess(config, observer)
	if err := proc.Launch(ctx); err != nil {
		fmt.Fprintf(deps.stdout, "[agent-loop] %s: %v\n", reason, err)
		_ = deps.sendMessage(sprawlRoot, agentName, parent, "[PROBLEM] agent-loop failure", fmt.Sprintf("%s: %v", reason, err))
		deps.exit(1)
		return nil, false
	}
	return proc, true
}

// sendPromptWithInterrupt wraps SendPrompt with a concurrent poller that
// watches for a .poke file and interrupts the turn if one appears.
// It also watches for .wake files (written by messages.Send) and interrupts
// when one appears — but without storing poke content, since the inbox
// delivery (step 2 of the agent loop) will handle the actual notification.
// Returns the SendPrompt result, any poke content (non-empty if interrupted), and error.
func sendPromptWithInterrupt(
	ctx context.Context,
	proc processManager,
	deps *agentLoopDeps,
	pokePath string,
	prompt string,
	pollInterval time.Duration,
	sprawlRoot string,
	agentName string,
) (*protocol.ResultMessage, string, error) {
	pokeCh := make(chan string, 1)
	done := make(chan struct{})

	// Derive wake file path from agent identity.
	var wakePath string
	if sprawlRoot != "" && agentName != "" {
		wakePath = filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".wake")
	}

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		loggedMsgIDs := make(map[string]bool)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// Check for poke file (highest priority — explicit interrupt with content).
				content, err := deps.readFile(pokePath)
				if err == nil {
					_ = deps.removeFile(pokePath)
					select {
					case pokeCh <- strings.TrimSpace(string(content)):
					default:
					}
					// Trigger interrupt — ignore error (turn may have already ended).
					_ = proc.InterruptTurn(ctx)
					return
				}

				// Check for wake file (message notification — interrupt without content).
				// The wake file signals a new message; the inbox delivery in the agent
				// loop provides the detailed notification, so we just interrupt here.
				if wakePath != "" {
					if _, wakeErr := deps.readFile(wakePath); wakeErr == nil {
						_ = deps.removeFile(wakePath)
						fmt.Fprintf(deps.stdout, "[agent-loop] wake file detected mid-turn, interrupting for message delivery\n")
						_ = proc.InterruptTurn(ctx)
						return
					}
				}

				// Check inbox for unread messages and log them (but don't deliver).
				if sprawlRoot != "" && agentName != "" {
					msgs, listErr := deps.listMessages(sprawlRoot, agentName, "unread")
					if listErr == nil {
						for _, msg := range msgs {
							if !loggedMsgIDs[msg.ID] {
								loggedMsgIDs[msg.ID] = true
								fmt.Fprintf(deps.stdout, "[agent-loop] message received from %s (subject: %q) — queued, waiting for current turn to finish\n", msg.From, msg.Subject)
							}
						}
					}
				}
			}
		}
	}()

	result, err := proc.SendPrompt(ctx, prompt)
	close(done)

	var pokeContent string
	select {
	case pokeContent = <-pokeCh:
	default:
	}

	return result, pokeContent, err
}

// defaultPollInterval is the default interval for checking poke files during a turn.
const defaultPollInterval = 500 * time.Millisecond

// runAgentLoop is the main loop logic for the agent-loop command.
func runAgentLoop(ctx context.Context, deps *agentLoopDeps, agentName string) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	// Validate SPRAWL_ROOT
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := deps.loadAgent(sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("loading agent state: %w", err)
	}

	// Find claude binary
	claudePath, err := deps.findClaude()
	if err != nil {
		return fmt.Errorf("finding claude binary: %w", err)
	}

	// Create log file
	logsDir := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "logs")
	if err := deps.mkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("creating logs directory: %w", err)
	}
	logFile, err := deps.createFile(filepath.Join(logsDir, agentState.SessionID+".log"))
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	defer logFile.Close()

	// Create work lock for synchronization with merge operations.
	lockDir := filepath.Join(sprawlRoot, ".sprawl", "locks")
	wl, err := deps.newWorkLock(lockDir, agentName)
	if err != nil {
		return fmt.Errorf("creating work lock: %w", err)
	}

	// Tee output to both stdout and log file, then wrap with timestamps
	deps.stdout = &timestampWriter{
		w:       io.MultiWriter(deps.stdout, logFile),
		nowFunc: time.Now,
	}

	defer fmt.Fprintf(deps.stdout, "[agent-loop] STOPPED agent=%s\n", agentName)

	// Set up signal handling inside runAgentLoop so log lines go through the
	// timestamped writer and into the log file.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if deps.signalCh != nil {
		go func() {
			select {
			case sig := <-deps.signalCh:
				fmt.Fprintf(deps.stdout, "[agent-loop] received signal %s, shutting down\n", sig)
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	fmt.Fprintf(deps.stdout, "[agent-loop] starting for agent %q\n", agentName)

	// Build process config
	systemPrompt := deps.buildPrompt(agentState)
	promptPath, err := state.WriteSystemPrompt(sprawlRoot, agentName, systemPrompt)
	if err != nil {
		return fmt.Errorf("writing system prompt file: %w", err)
	}
	config := agentloop.ProcessConfig{
		AgentName:  agentName,
		WorkDir:    agentState.Worktree,
		ClaudePath: claudePath,
		SprawlRoot: sprawlRoot,
		Args: claude.LaunchOpts{
			SessionID:        agentState.SessionID,
			SystemPromptFile: promptPath,
			Print:            true,
			InputFormat:      "stream-json",
			OutputFormat:     "stream-json",
			Verbose:          true,
			Model:            "opus[1m]",
			Effort:           "medium",
			PermissionMode:   "bypassPermissions",
		},
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
	fmt.Fprintf(deps.stdout, "[agent-loop]   session-id:      %s\n", config.Args.SessionID)
	fmt.Fprintf(deps.stdout, "[agent-loop]   work-dir:        %s\n", config.WorkDir)
	fmt.Fprintf(deps.stdout, "[agent-loop]   claude-path:     %s\n", config.ClaudePath)
	fmt.Fprintf(deps.stdout, "[agent-loop]   setting-sources: %s\n", config.Args.SettingSources)
	fmt.Fprintf(deps.stdout, "[agent-loop]   sprawl-root:     %s\n", config.SprawlRoot)
	fmt.Fprintf(deps.stdout, "[agent-loop] === KEY ENV VARS ===\n")
	fmt.Fprintf(deps.stdout, "[agent-loop]   SPRAWL_AGENT_IDENTITY=%s\n", deps.getenv("SPRAWL_AGENT_IDENTITY"))
	fmt.Fprintf(deps.stdout, "[agent-loop]   SPRAWL_ROOT=%s\n", deps.getenv("SPRAWL_ROOT"))

	// Open the append-only activity log for this agent. Errors are
	// non-fatal — activity recording is observability-only. An ordinary
	// os.OpenFile suffices here; the file is only written from this
	// process and occasional tailing from other processes is safe given
	// line-buffered writes.
	activityDir := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName)
	if err := deps.mkdirAll(activityDir, 0o755); err != nil {
		fmt.Fprintf(deps.stdout, "[agent-loop] warn: could not create activity dir: %v\n", err)
	}
	var activityFile *os.File
	activityFile, actErr := os.OpenFile(agentloop.ActivityPath(sprawlRoot, agentName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G304: path is derived from trusted inputs
	if actErr != nil {
		fmt.Fprintf(deps.stdout, "[agent-loop] warn: could not open activity file: %v\n", actErr)
		activityFile = nil
	} else {
		defer activityFile.Close()
	}
	var activityWriter io.Writer
	if activityFile != nil {
		activityWriter = activityFile
	}
	ring := agentloop.NewActivityRing(agentloop.DefaultActivityCapacity, activityWriter)

	observer := &tmuxObserver{w: deps.stdout, ring: ring}

	// Create and launch the initial process (without sending a prompt yet).
	proc, ok := startProcess(ctx, deps, config, observer, sprawlRoot, agentName, agentState.Parent, "failed to start process")
	if !ok {
		return nil
	}

	fmt.Fprintf(deps.stdout, "[agent-loop] READY agent=%s pid=%d\n", agentName, deps.getpid())

	// Use a closure defer so it always stops the most-recently-assigned proc.
	// Guard against nil in case a restart failure left proc unset.
	defer func() {
		if proc != nil {
			_ = proc.Stop(ctx)
		}
	}()

	pokePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".poke")

	// sendWithInterrupt wraps sendPromptWithInterrupt with the poke path and default interval.
	sendWithInterrupt := func(prompt string) (*protocol.ResultMessage, string, error) {
		return sendPromptWithInterrupt(ctx, proc, deps, pokePath, prompt, defaultPollInterval, sprawlRoot, agentName)
	}

	// pendingPoke holds poke content from a mid-turn interrupt, delivered on the next iteration.
	var pendingPoke string

	// Clear any stale wake file before the initial turn — a leftover wake
	// file from a previous agent (or a message sent before launch) would
	// trigger a spurious interrupt on the very first turn.
	wakePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".wake")
	_ = deps.removeFile(wakePath)

	// Send the initial prompt through the interrupt-aware path so poke/wake
	// files are detected during the first turn, not just subsequent turns.
	fmt.Fprintf(deps.stdout, "[agent-loop] sending initial prompt with interrupt support\n")
	_, initialPoke, initialSendErr := sendWithInterrupt(agentState.Prompt)
	if initialSendErr != nil {
		fmt.Fprintf(deps.stdout, "[agent-loop] failed to send initial prompt: %v\n", initialSendErr)
		_ = deps.sendMessage(sprawlRoot, agentName, agentState.Parent, "[PROBLEM] agent-loop failure",
			fmt.Sprintf("failed to send initial prompt: %v", initialSendErr))
		deps.exit(1)
		return nil
	}
	if initialPoke != "" {
		pendingPoke = initialPoke
	}
	// If the initial turn was interrupted by a wake (no poke content),
	// do NOT re-queue the initial prompt. The agent already has session
	// context from the partially-completed first turn and knows its task.
	// Let the main loop fall through to inbox delivery so the message
	// that triggered the interrupt gets delivered immediately.

	// restartWithResume creates a new process with Resume=true after a crash.
	// Returns false (and exits) if the restart fails.
	restartWithResume := func() bool {
		resumeConfig := config
		resumeConfig.Args.Resume = true
		var ok bool
		proc, ok = startProcess(ctx, deps, resumeConfig, observer, sprawlRoot, agentName, agentState.Parent, "failed to restart process after crash")
		return ok
	}

	// Main loop
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(deps.stdout, "[agent-loop] shutting down: %v\n", ctx.Err())
			return nil
		default:
		}

		// 0. Check for kill sentinel file.
		killFilePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".kill")
		if _, readErr := deps.readFile(killFilePath); readErr == nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] kill sentinel detected, shutting down\n")
			_ = deps.removeFile(killFilePath)
			return nil // triggers deferred proc.Stop()
		}

		// 0.1. Check if agent state file still exists (defense against external retirement).
		if _, loadErr := deps.loadAgent(sprawlRoot, agentName); loadErr != nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] agent state file missing, shutting down\n")
			return nil
		}

		// Acquire the work lock before checking for work and invoking Claude.
		// This synchronizes with merge operations that need exclusive branch access.
		if err := wl.Acquire(); err != nil {
			return fmt.Errorf("acquiring work lock: %w", err)
		}

		// 0.5. Check for poke file between turns (or consume pending poke from interrupt).
		if pendingPoke == "" {
			if content, readErr := deps.readFile(pokePath); readErr == nil {
				pendingPoke = strings.TrimSpace(string(content))
				_ = deps.removeFile(pokePath)
			}
		}

		// If we have pending poke content, deliver it immediately.
		if pendingPoke != "" {
			prompt := pendingPoke
			pendingPoke = ""
			fmt.Fprintf(deps.stdout, "[agent-loop] delivering poke message\n")
			fmt.Fprintf(deps.stdout, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(prompt, "\n") {
				fmt.Fprintf(deps.stdout, "[agent-loop]   %s\n", line)
			}
			_, pokeContent, sendErr := sendWithInterrupt(prompt)
			if pokeContent != "" {
				pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on poke delivery, restarting: %v\n", sendErr)
				if !restartWithResume() {
					_ = wl.Release()
					return nil
				}
			}
			_ = wl.Release()
			continue
		}

		// 1. Check for a queued task.
		task, err := deps.nextTask(sprawlRoot, agentName)
		if err != nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] error fetching next task: %v\n", err)
		} else if task != nil {
			task.Status = "in-progress"
			_ = deps.updateTask(sprawlRoot, agentName, task)

			fmt.Fprintf(deps.stdout, "[agent-loop] starting task %s\n", task.ID)
			var taskPrompt string
			if task.PromptFile != "" {
				taskPrompt = fmt.Sprintf("You have a new task. Read it from @%s and begin working.", task.PromptFile)
			} else {
				taskPrompt = task.Prompt
			}
			fmt.Fprintf(deps.stdout, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(taskPrompt, "\n") {
				fmt.Fprintf(deps.stdout, "[agent-loop]   %s\n", line)
			}
			_, pokeContent, sendErr := sendWithInterrupt(taskPrompt)
			if pokeContent != "" {
				pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on task %s, restarting: %v\n", task.ID, sendErr)
				if !restartWithResume() {
					_ = wl.Release()
					return nil
				}
				// Retry on recovered process.
				_, retryPoke, _ := sendWithInterrupt(taskPrompt)
				if retryPoke != "" {
					pendingPoke = retryPoke
				}
			}

			// Only mark task done if it wasn't interrupted by a poke.
			// An interrupted task still completed its turn (Claude emits a result),
			// but the poke message takes priority for the next turn.
			task.Status = "done"
			_ = deps.updateTask(sprawlRoot, agentName, task)
			_ = wl.Release()
			continue
		}

		// 2. Check inbox for unread messages.
		msgs, err := deps.listMessages(sprawlRoot, agentName, "unread")
		if err == nil && len(msgs) > 0 {
			var cmdLines []string
			for _, msg := range msgs {
				msgID := msg.ShortID
				if msgID == "" {
					msgID = msg.ID
				}
				cmdLines = append(cmdLines, fmt.Sprintf(
					"Run `sprawl messages read %s` to read a message from %s (subject: %q)",
					msgID, msg.From, msg.Subject,
				))
			}

			prompt := fmt.Sprintf("You have %d new message(s). Read them with the commands below:\n\n%s",
				len(cmdLines), strings.Join(cmdLines, "\n"))
			fmt.Fprintf(deps.stdout, "[agent-loop] delivering %d inbox message(s) to agent\n", len(cmdLines))
			fmt.Fprintf(deps.stdout, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(prompt, "\n") {
				fmt.Fprintf(deps.stdout, "[agent-loop]   %s\n", line)
			}
			// Consume any pending wake file BEFORE starting the turn — inbox
			// delivery already handles the notification, so don't let
			// sendPromptWithInterrupt find a stale wake file and double-interrupt.
			wakePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".wake")
			_ = deps.removeFile(wakePath)
			_, pokeContent, sendErr := sendWithInterrupt(prompt)
			if pokeContent != "" {
				pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on inbox delivery, restarting: %v\n", sendErr)
				if !restartWithResume() {
					_ = wl.Release()
					return nil
				}
			}
			_ = wl.Release()
			deps.sleepFunc(3 * time.Second)
			continue
		}

		// 3. Check for a wake file.
		wakeFilePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".wake")
		wakeContent, readErr := deps.readFile(wakeFilePath)
		if readErr == nil {
			fmt.Fprintf(deps.stdout, "[agent-loop] wake file detected\n")
			wakePrompt := string(wakeContent)
			fmt.Fprintf(deps.stdout, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(wakePrompt, "\n") {
				fmt.Fprintf(deps.stdout, "[agent-loop]   %s\n", line)
			}
			_, pokeContent, sendErr := sendWithInterrupt(wakePrompt)
			if pokeContent != "" {
				pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(deps.stdout, "[agent-loop] process crash on wake, restarting: %v\n", sendErr)
				if !restartWithResume() {
					_ = wl.Release()
					return nil
				}
			}
			// Only remove the wake file after it has been successfully delivered.
			_ = deps.removeFile(wakeFilePath)
			// Continue immediately — let the inbox check pick up message details on the next iteration.
			_ = wl.Release()
			continue
		}

		// 4. Nothing to do — release lock and sleep.
		_ = wl.Release()
		deps.sleepFunc(3 * time.Second)
	}
}
