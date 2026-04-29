package cmd

import (
	"context"
	"encoding/json"
	"errors"
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
	backend "github.com/dmotles/sprawl/internal/backend"
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
	getenv            func(string) string
	loadAgent         func(string, string) (*state.AgentState, error)
	nextTask          func(string, string) (*state.Task, error)
	updateTask        func(string, string, *state.Task) error
	listMessages      func(string, string, string) ([]*messages.Message, error)
	sendMessage       func(string, string, string, string, string) error
	findClaude        func() (string, error)
	readFile          func(string) ([]byte, error)
	removeFile        func(string) error
	buildPrompt       func(*state.AgentState) string
	sleepFunc         func(time.Duration)
	mkdirAll          func(string, os.FileMode) error
	createFile        func(string) (*os.File, error)
	stdout            io.Writer
	exit              func(int)
	newProcess        func(config agentloop.ProcessConfig, observer agentloop.Observer) processManager
	newBackendProcess func(spec backend.SessionSpec, observer agentloop.Observer) processManager
	newWorkLock       func(lockDir, agentName string) (*workLock, error)
	getpid            func() int
	signalCh          <-chan os.Signal
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
		newBackendProcess: newClaudeBackendProcess,
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

// hasPendingInterrupt reports whether the agent's pending queue contains any
// class=interrupt entry. Used by the mid-turn poller to trigger preemption
// per docs/designs/messaging-overhaul.md §4.5.2.
func hasPendingInterrupt(sprawlRoot, agentName string) bool {
	if sprawlRoot == "" || agentName == "" {
		return false
	}
	pending, err := agentloop.ListPending(sprawlRoot, agentName)
	if err != nil {
		return false
	}
	for _, e := range pending {
		if e.Class == agentloop.ClassInterrupt {
			return true
		}
	}
	return false
}

// sendPromptWithInterrupt wraps SendPrompt with a concurrent poller that
// watches for a .poke file and interrupts the turn if one appears.
// It also watches for .wake files (written by messages.Send) and interrupts
// when one appears — but without storing poke content, since the inbox
// delivery (step 2 of the agent loop) will handle the actual notification.
// Finally it watches for any class=interrupt entry in the harness pending
// queue; when one appears mid-turn, it calls InterruptTurn so the main loop
// can deliver the §4.5.2 interrupt frame on the next iteration.
// Returns the SendPrompt result, any poke content (non-empty if interrupted), and error.
//
//nolint:unparam // test helpers exercise different prompts and poll intervals across scenarios.
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

				// Check for pending interrupt-class queue entry. When an
				// interrupt is enqueued mid-turn (§4.5.2), we preempt the
				// current turn so the main loop can deliver the interrupt
				// frame. Like wake, no content is returned — the main-loop
				// flush step formats and delivers the interrupt frame.
				if hasPendingInterrupt(sprawlRoot, agentName) {
					fmt.Fprintf(deps.stdout, "[agent-loop] interrupt-class queue entry detected mid-turn, interrupting for immediate delivery\n")
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

// Flush-prompt building is shared with the weave root-loop (tmux and TUI)
// via internal/agentloop/flush.go. These thin wrappers keep the cmd-package
// call sites stable; the canonical implementations live in that file.
// QUM-323.
func buildQueueFlushPrompt(entries []agentloop.Entry) string {
	return agentloop.BuildQueueFlushPrompt(entries)
}

func buildInterruptFlushPrompt(entries []agentloop.Entry) string {
	return agentloop.BuildInterruptFlushPrompt(entries)
}

func splitByClass(entries []agentloop.Entry) (interrupts, asyncs []agentloop.Entry) {
	return agentloop.SplitByClass(entries)
}

// Re-exported constants for tests that still reference the old lowercase names.
// The canonical definitions live in internal/agentloop/flush.go.
const (
	maxQueueFlushBodyBytes  = agentloop.MaxQueueFlushBodyBytes
	maxQueueFlushTotalBytes = agentloop.MaxQueueFlushTotalBytes
)

// runAgentLoop is the main loop logic for the agent-loop command.
func runAgentLoop(ctx context.Context, deps *agentLoopDeps, agentName string) error {
	runner, err := agentloop.StartRunner(ctx, toSharedRunnerDeps(deps), agentName)
	if err != nil {
		var fatal *agentloop.FatalError
		if errors.As(err, &fatal) {
			deps.exit(1)
			return nil
		}
		return err
	}
	if err := runner.Run(ctx); err != nil {
		var fatal *agentloop.FatalError
		if errors.As(err, &fatal) {
			deps.exit(1)
			return nil
		}
		return err
	}
	return nil
}

func toSharedRunnerDeps(deps *agentLoopDeps) *agentloop.RunnerDeps {
	shared := &agentloop.RunnerDeps{
		Getenv: func(key string) string {
			return deps.getenv(key)
		},
		LoadAgent: func(root, name string) (*state.AgentState, error) {
			return deps.loadAgent(root, name)
		},
		NextTask: func(root, name string) (*state.Task, error) {
			return deps.nextTask(root, name)
		},
		UpdateTask: func(root, name string, task *state.Task) error {
			return deps.updateTask(root, name, task)
		},
		ListMessages: func(root, agentName, filter string) ([]*messages.Message, error) {
			return deps.listMessages(root, agentName, filter)
		},
		SendMessage: func(root, from, to, subject, body string) error {
			return deps.sendMessage(root, from, to, subject, body)
		},
		FindClaude: func() (string, error) {
			return deps.findClaude()
		},
		ReadFile: func(path string) ([]byte, error) {
			return deps.readFile(path)
		},
		RemoveFile: func(path string) error {
			return deps.removeFile(path)
		},
		BuildPrompt: func(agentState *state.AgentState) string {
			return deps.buildPrompt(agentState)
		},
		SleepFunc: func(d time.Duration) {
			deps.sleepFunc(d)
		},
		MkdirAll: func(path string, mode os.FileMode) error {
			return deps.mkdirAll(path, mode)
		},
		CreateFile: func(path string) (*os.File, error) {
			return deps.createFile(path)
		},
		Stdout: deps.stdout,
		NewWorkLock: func(lockDir, agentName string) (*agentloop.WorkLock, error) {
			lock, err := deps.newWorkLock(lockDir, agentName)
			if err != nil {
				return nil, err
			}
			return &agentloop.WorkLock{
				Acquire: lock.Acquire,
				Release: lock.Release,
			}, nil
		},
		Getpid: func() int {
			return deps.getpid()
		},
		SignalCh: deps.signalCh,
	}
	if deps.newProcess != nil {
		shared.NewProcess = func(config agentloop.ProcessConfig, observer agentloop.Observer) agentloop.ProcessManager {
			return deps.newProcess(config, observer)
		}
	}
	if deps.newBackendProcess != nil {
		shared.NewBackendProcess = func(spec backend.SessionSpec, observer agentloop.Observer) agentloop.ProcessManager {
			return deps.newBackendProcess(spec, observer)
		}
	}
	return shared
}
