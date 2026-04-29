package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

// WorkLock provides acquire/release functions for an agent work lock.
type WorkLock struct {
	Acquire func() error
	Release func() error
}

// RunnerDeps holds the dependencies for the shared child runtime runner.
type RunnerDeps struct {
	Getenv            func(string) string
	LoadAgent         func(string, string) (*state.AgentState, error)
	NextTask          func(string, string) (*state.Task, error)
	UpdateTask        func(string, string, *state.Task) error
	ListMessages      func(string, string, string) ([]*messages.Message, error)
	SendMessage       func(string, string, string, string, string) error
	FindClaude        func() (string, error)
	ReadFile          func(string) ([]byte, error)
	RemoveFile        func(string) error
	BuildPrompt       func(*state.AgentState) string
	SleepFunc         func(time.Duration)
	MkdirAll          func(string, os.FileMode) error
	CreateFile        func(string) (*os.File, error)
	Stdout            io.Writer
	NewProcess        func(config ProcessConfig, observer Observer) ProcessManager
	NewBackendProcess func(spec backend.SessionSpec, observer Observer) ProcessManager
	NewWorkLock       func(lockDir, agentName string) (*WorkLock, error)
	Getpid            func() int
	SignalCh          <-chan os.Signal
}

// DefaultPollInterval is the default interrupt poll interval during a turn.
const DefaultPollInterval = 500 * time.Millisecond

// FatalError is returned when child startup fails after notifying the parent.
type FatalError struct {
	Reason string
	Err    error
}

func (e *FatalError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return fmt.Sprintf("%s: %v", e.Reason, e.Err)
}

func (e *FatalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// TimestampWriter prepends a wall-clock timestamp to each log line.
type TimestampWriter struct {
	W       io.Writer
	NowFunc func() time.Time
	mu      sync.Mutex
}

func (tw *TimestampWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	nowFunc := tw.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}
	lines := strings.Split(string(p), "\n")
	var buf strings.Builder
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			break
		}
		ts := nowFunc().Format("15:04:05")
		buf.WriteString("[")
		buf.WriteString(ts)
		buf.WriteString("] ")
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	_, err := io.WriteString(tw.W, buf.String())
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// ObserverWriter renders protocol events to the child runtime transcript/log.
type ObserverWriter struct {
	W    io.Writer
	Ring *ActivityRing
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (t *ObserverWriter) OnMessage(msg *protocol.Message) {
	if t.Ring != nil {
		t.Ring.RecordMessage(msg, time.Now)
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
					fmt.Fprintf(t.W, "[claude] %s\n", block.Text)
				}
			case "tool_use":
				if block.Name != "" {
					inputStr := truncateStr(string(block.Input), 200)
					fmt.Fprintf(t.W, "[tool] %s: %s\n", block.Name, inputStr)
				}
			}
		}

	case "system":
		if msg.Subtype == "session_state_changed" {
			var ssc protocol.SessionStateChanged
			if err := json.Unmarshal(msg.Raw, &ssc); err == nil && ssc.State != "" {
				fmt.Fprintf(t.W, "[system] %s: %s\n", msg.Subtype, ssc.State)
				return
			}
		}
		if msg.Subtype != "" {
			fmt.Fprintf(t.W, "[system] %s\n", msg.Subtype)
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
		fmt.Fprintf(t.W, "[result] %s (stop=%s, turns=%d)\n", status, res.StopReason, res.NumTurns)

	case "rate_limit_event":
		var evt protocol.RateLimitEvent
		if err := json.Unmarshal(msg.Raw, &evt); err != nil {
			return
		}
		if evt.RateLimitInfo != nil && evt.RateLimitInfo.Status == "blocked" {
			fmt.Fprintf(t.W, "[agent-loop] rate limit blocked (type=%s)\n", evt.RateLimitInfo.RateLimitType)
		}

	case "user":
	default:
		fmt.Fprintf(t.W, "[agent-loop] message: type=%s subtype=%s\n", msg.Type, msg.Subtype)
	}
}

// Runner is the live in-process child harness.
type Runner struct {
	deps          *RunnerDeps
	sprawlRoot    string
	agentName     string
	agentState    *state.AgentState
	workLock      *WorkLock
	output        io.Writer
	proc          ProcessManager
	initialPrompt string
	pokePath      string
	pendingPoke   string
	config        ProcessConfig
	sessionSpec   backend.SessionSpec
	usingBackend  bool
	observer      Observer
	logFile       *os.File
	activityFile  *os.File
}

func hasPendingInterrupt(sprawlRoot, agentName string) bool {
	if sprawlRoot == "" || agentName == "" {
		return false
	}
	pending, err := ListPending(sprawlRoot, agentName)
	if err != nil {
		return false
	}
	for _, e := range pending {
		if e.Class == ClassInterrupt {
			return true
		}
	}
	return false
}

// SendPromptWithInterrupt wraps a turn with poke/wake/interrupt polling.
func SendPromptWithInterrupt(
	ctx context.Context,
	proc ProcessManager,
	deps *RunnerDeps,
	pokePath string,
	prompt string,
	pollInterval time.Duration,
	sprawlRoot string,
	agentName string,
) (*protocol.ResultMessage, string, error) {
	pokeCh := make(chan string, 1)
	done := make(chan struct{})

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
				content, err := deps.ReadFile(pokePath)
				if err == nil {
					_ = deps.RemoveFile(pokePath)
					select {
					case pokeCh <- strings.TrimSpace(string(content)):
					default:
					}
					_ = proc.InterruptTurn(ctx)
					return
				}

				if hasPendingInterrupt(sprawlRoot, agentName) {
					fmt.Fprintf(deps.Stdout, "[agent-loop] interrupt-class queue entry detected mid-turn, interrupting for immediate delivery\n")
					_ = proc.InterruptTurn(ctx)
					return
				}

				if wakePath != "" {
					if _, wakeErr := deps.ReadFile(wakePath); wakeErr == nil {
						_ = deps.RemoveFile(wakePath)
						fmt.Fprintf(deps.Stdout, "[agent-loop] wake file detected mid-turn, interrupting for message delivery\n")
						_ = proc.InterruptTurn(ctx)
						return
					}
				}

				if sprawlRoot != "" && agentName != "" {
					msgs, listErr := deps.ListMessages(sprawlRoot, agentName, "unread")
					if listErr == nil {
						for _, msg := range msgs {
							if !loggedMsgIDs[msg.ID] {
								loggedMsgIDs[msg.ID] = true
								fmt.Fprintf(deps.Stdout, "[agent-loop] message received from %s (subject: %q) — queued, waiting for current turn to finish\n", msg.From, msg.Subject)
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

func startProcess(ctx context.Context, deps *RunnerDeps, config ProcessConfig, observer Observer) (ProcessManager, error) {
	proc := deps.NewProcess(config, observer)
	if err := proc.Launch(ctx); err != nil {
		return nil, err
	}
	return proc, nil
}

// StartRunner performs synchronous child bootstrap and returns a runner whose
// initial prompt will be sent when Run starts.
func StartRunner(ctx context.Context, deps *RunnerDeps, agentName string) (*Runner, error) {
	if err := agent.ValidateName(agentName); err != nil {
		return nil, err
	}

	sprawlRoot := deps.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return nil, fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	agentState, err := deps.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return nil, fmt.Errorf("loading agent state: %w", err)
	}

	logsDir := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName, "logs")
	if err := deps.MkdirAll(logsDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating logs directory: %w", err)
	}
	logFile, err := deps.CreateFile(filepath.Join(logsDir, agentState.SessionID+".log"))
	if err != nil {
		return nil, fmt.Errorf("creating log file: %w", err)
	}

	lockDir := filepath.Join(sprawlRoot, ".sprawl", "locks")
	wl, err := deps.NewWorkLock(lockDir, agentName)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("creating work lock: %w", err)
	}

	baseStdout := deps.Stdout
	if baseStdout == nil {
		baseStdout = io.Discard
	}
	output := &TimestampWriter{
		W:       io.MultiWriter(baseStdout, logFile),
		NowFunc: time.Now,
	}
	deps.Stdout = output

	fmt.Fprintf(output, "[agent-loop] starting for agent %q\n", agentName)

	systemPrompt := deps.BuildPrompt(agentState)
	promptPath, err := state.WriteSystemPrompt(sprawlRoot, agentName, systemPrompt)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("writing system prompt file: %w", err)
	}
	sessionSpec := BuildAgentSessionSpec(agentState, promptPath, sprawlRoot, output)

	var config ProcessConfig
	usingBackend := deps.NewBackendProcess != nil
	if !usingBackend {
		claudePath, findErr := deps.FindClaude()
		if findErr != nil {
			_ = logFile.Close()
			return nil, fmt.Errorf("finding claude binary: %w", findErr)
		}
		config = ProcessConfig{
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
				Model:            rootinit.DefaultModel,
				Effort:           "medium",
				PermissionMode:   "bypassPermissions",
			},
		}
	}

	fmt.Fprintf(output, "[agent-loop] === SYSTEM PROMPT ===\n")
	for _, line := range strings.Split(systemPrompt, "\n") {
		fmt.Fprintf(output, "[agent-loop]   %s\n", line)
	}
	fmt.Fprintf(output, "[agent-loop] === INITIAL PROMPT/TASK ===\n")
	fmt.Fprintf(output, "[agent-loop]   %s\n", agentState.Prompt)
	fmt.Fprintf(output, "[agent-loop] === PROCESS CONFIG ===\n")
	fmt.Fprintf(output, "[agent-loop]   agent-name:      %s\n", sessionSpec.Identity)
	fmt.Fprintf(output, "[agent-loop]   session-id:      %s\n", sessionSpec.SessionID)
	fmt.Fprintf(output, "[agent-loop]   work-dir:        %s\n", sessionSpec.WorkDir)
	fmt.Fprintf(output, "[agent-loop]   prompt-file:     %s\n", sessionSpec.PromptFile)
	fmt.Fprintf(output, "[agent-loop]   sprawl-root:     %s\n", sessionSpec.SprawlRoot)
	fmt.Fprintf(output, "[agent-loop] === KEY ENV VARS ===\n")
	fmt.Fprintf(output, "[agent-loop]   SPRAWL_AGENT_IDENTITY=%s\n", deps.Getenv("SPRAWL_AGENT_IDENTITY"))
	fmt.Fprintf(output, "[agent-loop]   SPRAWL_ROOT=%s\n", deps.Getenv("SPRAWL_ROOT"))

	activityDir := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName)
	if err := deps.MkdirAll(activityDir, 0o755); err != nil {
		fmt.Fprintf(output, "[agent-loop] warn: could not create activity dir: %v\n", err)
	}
	var activityFile *os.File
	activityFile, actErr := os.OpenFile(ActivityPath(sprawlRoot, agentName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G304: path is derived from trusted inputs
	if actErr != nil {
		fmt.Fprintf(output, "[agent-loop] warn: could not open activity file: %v\n", actErr)
		activityFile = nil
	}
	var activityWriter io.Writer
	if activityFile != nil {
		activityWriter = activityFile
	}
	ring := NewActivityRing(DefaultActivityCapacity, activityWriter)
	observer := &ObserverWriter{W: output, Ring: ring}

	var proc ProcessManager
	if usingBackend {
		proc, err = StartBackendProcess(ctx, deps, sessionSpec, observer)
	} else {
		proc, err = startProcess(ctx, deps, config, observer)
	}
	if err != nil {
		msg := fmt.Sprintf("failed to start process: %v", err)
		fmt.Fprintf(output, "[agent-loop] %s\n", msg)
		_ = deps.SendMessage(sprawlRoot, agentName, agentState.Parent, "[PROBLEM] agent-loop failure", msg)
		if activityFile != nil {
			_ = activityFile.Close()
		}
		_ = logFile.Close()
		return nil, &FatalError{Reason: "failed to start process", Err: err}
	}

	fmt.Fprintf(output, "[agent-loop] READY agent=%s pid=%d\n", agentName, deps.Getpid())

	pokePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".poke")
	wakePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".wake")
	_ = deps.RemoveFile(wakePath)

	return &Runner{
		deps:          deps,
		sprawlRoot:    sprawlRoot,
		agentName:     agentName,
		agentState:    agentState,
		workLock:      wl,
		output:        output,
		proc:          proc,
		initialPrompt: agentState.Prompt,
		pokePath:      pokePath,
		config:        config,
		sessionSpec:   sessionSpec,
		usingBackend:  usingBackend,
		observer:      observer,
		logFile:       logFile,
		activityFile:  activityFile,
	}, nil
}

// SessionID returns the current child session ID.
func (r *Runner) SessionID() string {
	return r.sessionSpec.SessionID
}

// Capabilities returns the child backend capabilities.
func (r *Runner) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		SupportsInterrupt:  true,
		SupportsResume:     true,
		SupportsToolBridge: false,
	}
}

// Interrupt requests an interrupt on the in-flight child turn.
func (r *Runner) Interrupt(ctx context.Context) error {
	if r.proc == nil {
		return fmt.Errorf("runtime process not started")
	}
	return r.proc.InterruptTurn(ctx)
}

// Stop closes the child backend process.
func (r *Runner) Stop(ctx context.Context) error {
	if r.proc == nil {
		return nil
	}
	return r.proc.Stop(ctx)
}

func (r *Runner) sendWithInterrupt(ctx context.Context, prompt string) (*protocol.ResultMessage, string, error) {
	return SendPromptWithInterrupt(ctx, r.proc, r.deps, r.pokePath, prompt, DefaultPollInterval, r.sprawlRoot, r.agentName)
}

func (r *Runner) restartWithResume(ctx context.Context, observer Observer) error {
	var (
		proc ProcessManager
		err  error
	)
	if r.usingBackend {
		resumeSpec := r.sessionSpec
		resumeSpec.Resume = true
		proc, err = StartBackendProcess(ctx, r.deps, resumeSpec, observer)
	} else {
		resumeConfig := r.config
		resumeConfig.Args.Resume = true
		proc, err = startProcess(ctx, r.deps, resumeConfig, observer)
	}
	if err != nil {
		msg := fmt.Sprintf("failed to restart process after crash: %v", err)
		fmt.Fprintf(r.output, "[agent-loop] %s\n", msg)
		_ = r.deps.SendMessage(r.sprawlRoot, r.agentName, r.agentState.Parent, "[PROBLEM] agent-loop failure", msg)
		return &FatalError{Reason: "failed to restart process after crash", Err: err}
	}
	r.proc = proc
	return nil
}

func (r *Runner) runInitialPrompt(ctx context.Context) error {
	if r.initialPrompt == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	fmt.Fprintf(r.output, "[agent-loop] sending initial prompt with interrupt support\n")
	_, initialPoke, initialSendErr := r.sendWithInterrupt(ctx, r.initialPrompt)
	if initialPoke != "" {
		r.pendingPoke = initialPoke
	}
	r.initialPrompt = ""
	if initialSendErr == nil {
		return nil
	}

	msg := fmt.Sprintf("failed to send initial prompt: %v", initialSendErr)
	fmt.Fprintf(r.output, "[agent-loop] %s\n", msg)
	_ = r.deps.SendMessage(r.sprawlRoot, r.agentName, r.agentState.Parent, "[PROBLEM] agent-loop failure", msg)
	_ = r.proc.Stop(ctx)
	return &FatalError{Reason: "failed to send initial prompt", Err: initialSendErr}
}

// Run executes the steady-state child loop until ctx is canceled.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer fmt.Fprintf(r.output, "[agent-loop] STOPPED agent=%s\n", r.agentName)
	defer func() {
		if r.activityFile != nil {
			_ = r.activityFile.Close()
		}
		if r.logFile != nil {
			_ = r.logFile.Close()
		}
	}()
	defer func() {
		if r.proc != nil {
			_ = r.proc.Stop(ctx)
		}
	}()

	if r.deps.SignalCh != nil {
		go func() {
			select {
			case sig := <-r.deps.SignalCh:
				fmt.Fprintf(r.output, "[agent-loop] received signal %s, shutting down\n", sig)
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	if err := r.runInitialPrompt(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(r.output, "[agent-loop] shutting down: %v\n", ctx.Err())
			return nil
		default:
		}

		killFilePath := filepath.Join(r.sprawlRoot, ".sprawl", "agents", r.agentName+".kill")
		if _, readErr := r.deps.ReadFile(killFilePath); readErr == nil {
			fmt.Fprintf(r.output, "[agent-loop] kill sentinel detected, shutting down\n")
			_ = r.deps.RemoveFile(killFilePath)
			return nil
		}

		if _, loadErr := r.deps.LoadAgent(r.sprawlRoot, r.agentName); loadErr != nil {
			fmt.Fprintf(r.output, "[agent-loop] agent state file missing, shutting down\n")
			return nil
		}

		if err := r.workLock.Acquire(); err != nil {
			return fmt.Errorf("acquiring work lock: %w", err)
		}

		if r.pendingPoke == "" {
			if content, readErr := r.deps.ReadFile(r.pokePath); readErr == nil {
				r.pendingPoke = strings.TrimSpace(string(content))
				_ = r.deps.RemoveFile(r.pokePath)
			}
		}

		if r.pendingPoke != "" {
			prompt := r.pendingPoke
			r.pendingPoke = ""
			fmt.Fprintf(r.output, "[agent-loop] delivering poke message\n")
			fmt.Fprintf(r.output, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(prompt, "\n") {
				fmt.Fprintf(r.output, "[agent-loop]   %s\n", line)
			}
			_, pokeContent, sendErr := r.sendWithInterrupt(ctx, prompt)
			if pokeContent != "" {
				r.pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(r.output, "[agent-loop] process crash on poke delivery, restarting: %v\n", sendErr)
				if err := r.restartWithResume(ctx, r.observer); err != nil {
					_ = r.workLock.Release()
					return err
				}
			}
			_ = r.workLock.Release()
			continue
		}

		task, err := r.deps.NextTask(r.sprawlRoot, r.agentName)
		if err != nil {
			fmt.Fprintf(r.output, "[agent-loop] error fetching next task: %v\n", err)
		} else if task != nil {
			task.Status = "in-progress"
			_ = r.deps.UpdateTask(r.sprawlRoot, r.agentName, task)

			fmt.Fprintf(r.output, "[agent-loop] starting task %s\n", task.ID)
			var taskPrompt string
			if task.PromptFile != "" {
				taskPrompt = fmt.Sprintf("You have a new task. Read it from @%s and begin working.", task.PromptFile)
			} else {
				taskPrompt = task.Prompt
			}
			fmt.Fprintf(r.output, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(taskPrompt, "\n") {
				fmt.Fprintf(r.output, "[agent-loop]   %s\n", line)
			}
			_, pokeContent, sendErr := r.sendWithInterrupt(ctx, taskPrompt)
			if pokeContent != "" {
				r.pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(r.output, "[agent-loop] process crash on task %s, restarting: %v\n", task.ID, sendErr)
				if err := r.restartWithResume(ctx, r.observer); err != nil {
					_ = r.workLock.Release()
					return err
				}
				_, retryPoke, _ := r.sendWithInterrupt(ctx, taskPrompt)
				if retryPoke != "" {
					r.pendingPoke = retryPoke
				}
			}

			task.Status = "done"
			_ = r.deps.UpdateTask(r.sprawlRoot, r.agentName, task)
			_ = r.workLock.Release()
			continue
		}

		pending, pendErr := ListPending(r.sprawlRoot, r.agentName)
		if pendErr != nil {
			fmt.Fprintf(r.output, "[agent-loop] error listing pending queue: %v\n", pendErr)
		}
		interrupts, asyncs := SplitByClass(pending)
		if len(interrupts) > 0 {
			prompt := BuildInterruptFlushPrompt(interrupts)
			fmt.Fprintf(r.output, "[agent-loop] flushing %d interrupt message(s) to agent\n", len(interrupts))
			fmt.Fprintf(r.output, "[agent-loop] === INJECTED PROMPT (interrupt) ===\n")
			for _, line := range strings.Split(prompt, "\n") {
				fmt.Fprintf(r.output, "[agent-loop]   %s\n", line)
			}
			wakePath := filepath.Join(r.sprawlRoot, ".sprawl", "agents", r.agentName+".wake")
			_ = r.deps.RemoveFile(wakePath)
			_, pokeContent, sendErr := r.sendWithInterrupt(ctx, prompt)
			if pokeContent != "" {
				r.pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(r.output, "[agent-loop] process crash on interrupt flush, restarting: %v\n", sendErr)
				if err := r.restartWithResume(ctx, r.observer); err != nil {
					_ = r.workLock.Release()
					return err
				}
			} else {
				for _, e := range interrupts {
					if err := MarkDelivered(r.sprawlRoot, r.agentName, e.ID); err != nil {
						fmt.Fprintf(r.output, "[agent-loop] warn: failed to mark interrupt %s delivered: %v\n", e.ID, err)
					}
				}
			}
			_ = r.workLock.Release()
			continue
		}
		if len(asyncs) > 0 {
			prompt := BuildQueueFlushPrompt(asyncs)
			fmt.Fprintf(r.output, "[agent-loop] flushing %d queued message(s) to agent\n", len(asyncs))
			fmt.Fprintf(r.output, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(prompt, "\n") {
				fmt.Fprintf(r.output, "[agent-loop]   %s\n", line)
			}
			wakePath := filepath.Join(r.sprawlRoot, ".sprawl", "agents", r.agentName+".wake")
			_ = r.deps.RemoveFile(wakePath)
			_, pokeContent, sendErr := r.sendWithInterrupt(ctx, prompt)
			if pokeContent != "" {
				r.pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(r.output, "[agent-loop] process crash on queue flush, restarting: %v\n", sendErr)
				if err := r.restartWithResume(ctx, r.observer); err != nil {
					_ = r.workLock.Release()
					return err
				}
			} else {
				for _, e := range asyncs {
					if err := MarkDelivered(r.sprawlRoot, r.agentName, e.ID); err != nil {
						fmt.Fprintf(r.output, "[agent-loop] warn: failed to mark %s delivered: %v\n", e.ID, err)
					}
				}
			}
			_ = r.workLock.Release()
			r.deps.SleepFunc(3 * time.Second)
			continue
		}

		msgs, err := r.deps.ListMessages(r.sprawlRoot, r.agentName, "unread")
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
			fmt.Fprintf(r.output, "[agent-loop] delivering %d inbox message(s) to agent\n", len(cmdLines))
			fmt.Fprintf(r.output, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(prompt, "\n") {
				fmt.Fprintf(r.output, "[agent-loop]   %s\n", line)
			}
			wakePath := filepath.Join(r.sprawlRoot, ".sprawl", "agents", r.agentName+".wake")
			_ = r.deps.RemoveFile(wakePath)
			_, pokeContent, sendErr := r.sendWithInterrupt(ctx, prompt)
			if pokeContent != "" {
				r.pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(r.output, "[agent-loop] process crash on inbox delivery, restarting: %v\n", sendErr)
				if err := r.restartWithResume(ctx, r.observer); err != nil {
					_ = r.workLock.Release()
					return err
				}
			}
			_ = r.workLock.Release()
			r.deps.SleepFunc(3 * time.Second)
			continue
		}

		wakeFilePath := filepath.Join(r.sprawlRoot, ".sprawl", "agents", r.agentName+".wake")
		wakeContent, readErr := r.deps.ReadFile(wakeFilePath)
		if readErr == nil {
			fmt.Fprintf(r.output, "[agent-loop] wake file detected\n")
			wakePrompt := string(wakeContent)
			fmt.Fprintf(r.output, "[agent-loop] === INJECTED PROMPT ===\n")
			for _, line := range strings.Split(wakePrompt, "\n") {
				fmt.Fprintf(r.output, "[agent-loop]   %s\n", line)
			}
			_, pokeContent, sendErr := r.sendWithInterrupt(ctx, wakePrompt)
			if pokeContent != "" {
				r.pendingPoke = pokeContent
			}
			if sendErr != nil {
				fmt.Fprintf(r.output, "[agent-loop] process crash on wake, restarting: %v\n", sendErr)
				if err := r.restartWithResume(ctx, r.observer); err != nil {
					_ = r.workLock.Release()
					return err
				}
			}
			_ = r.deps.RemoveFile(wakeFilePath)
			_ = r.workLock.Release()
			continue
		}

		_ = r.workLock.Release()
		r.deps.SleepFunc(3 * time.Second)
	}
}
