package agentloop

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/dmotles/sprawl/internal/agent"
	backend "github.com/dmotles/sprawl/internal/backend"
	backendclaude "github.com/dmotles/sprawl/internal/backend/claude"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

// ProcessManager manages the lifecycle of a child backend process.
type ProcessManager interface {
	Launch(ctx context.Context) error
	SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error)
	InterruptTurn(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
}

// BuildAgentSessionSpec builds the shared backend session spec for a child agent.
func BuildAgentSessionSpec(agentState *state.AgentState, promptPath, sprawlRoot string, stderr io.Writer) backend.SessionSpec {
	additionalEnv := map[string]string{}
	if agentState.TreePath != "" {
		additionalEnv["SPRAWL_TREE_PATH"] = agentState.TreePath
	}
	if namespace := state.ReadNamespace(sprawlRoot); namespace != "" {
		additionalEnv["SPRAWL_NAMESPACE"] = namespace
	}
	if sprawlBin := os.Getenv("SPRAWL_BIN"); sprawlBin != "" {
		additionalEnv["SPRAWL_BIN"] = sprawlBin
	}
	if testMode := os.Getenv("SPRAWL_TEST_MODE"); testMode != "" {
		additionalEnv["SPRAWL_TEST_MODE"] = testMode
	}
	// QUM-408: engineer agents launch claude with the curated TDD sub-agent
	// set so they get oracle/test-writer/test-critic/implementer/code-reviewer/
	// qa-validator. Researchers, managers, and weave do NOT receive the flag —
	// they have different roles. The forward-compat requirement is documented
	// in docs/designs/unified-runtime.md.
	var agentsJSON string
	if agentState.Type == "engineer" {
		agentsJSON = agent.TDDSubAgentsJSON()
	}
	return backend.SessionSpec{
		WorkDir:        agentState.Worktree,
		Identity:       agentState.Name,
		SprawlRoot:     sprawlRoot,
		SessionID:      agentState.SessionID,
		PromptFile:     promptPath,
		Model:          rootinit.ModelForAgentType(agentState.Type),
		Effort:         "medium",
		PermissionMode: "bypassPermissions",
		Agents:         agentsJSON,
		AdditionalEnv:  additionalEnv,
		Stderr:         stderr,
	}
}

// StartBackendProcess launches a backend-backed child process manager.
func StartBackendProcess(
	ctx context.Context,
	deps *RunnerDeps,
	spec backend.SessionSpec,
	observer Observer,
) (ProcessManager, error) {
	proc := deps.NewBackendProcess(spec, deps.InitSpec, observer)
	if err := proc.Launch(ctx); err != nil {
		return nil, err
	}
	return proc, nil
}

// NewClaudeBackendProcess constructs a ProcessManager backed by the shared Claude adapter.
func NewClaudeBackendProcess(spec backend.SessionSpec, initSpec backend.InitSpec, observer Observer) ProcessManager {
	spec.Observer = observer
	return &claudeBackendProcess{
		spec:     spec,
		initSpec: initSpec,
	}
}

type claudeBackendProcess struct {
	spec     backend.SessionSpec
	initSpec backend.InitSpec
	session  backend.Session

	mu          sync.Mutex
	running     bool
	turnRunning bool
}

func (p *claudeBackendProcess) Launch(ctx context.Context) error {
	adapter := backendclaude.NewAdapter(backendclaude.Config{})
	session, err := adapter.Start(ctx, p.spec)
	if err != nil {
		return err
	}

	if p.initSpec.ToolBridge != nil || len(p.initSpec.MCPServerNames) > 0 {
		if err := session.Initialize(ctx, p.initSpec); err != nil {
			return err
		}
	}

	p.mu.Lock()
	p.session = session
	p.running = true
	p.turnRunning = false
	p.mu.Unlock()
	return nil
}

func (p *claudeBackendProcess) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	p.mu.Lock()
	session := p.session
	if session == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("process not launched")
	}
	p.turnRunning = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.turnRunning = false
		p.mu.Unlock()
	}()

	events, err := session.StartTurn(ctx, prompt, backend.TurnSpec{Init: p.initSpec})
	if err != nil {
		return nil, err
	}

	for msg := range events {
		if msg.Type != "result" {
			continue
		}
		var result protocol.ResultMessage
		if err := protocol.ParseAs(msg, &result); err != nil {
			return nil, fmt.Errorf("parsing result message: %w", err)
		}
		return &result, nil
	}

	if err := session.LastTurnError(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (p *claudeBackendProcess) InterruptTurn(ctx context.Context) error {
	p.mu.Lock()
	session := p.session
	turnRunning := p.turnRunning
	p.mu.Unlock()

	if !turnRunning {
		return ErrNotRunning
	}
	return session.Interrupt(ctx)
}

func (p *claudeBackendProcess) Stop(ctx context.Context) error {
	p.mu.Lock()
	session := p.session
	if session == nil {
		p.running = false
		p.turnRunning = false
		p.mu.Unlock()
		return nil
	}
	p.session = nil
	p.running = false
	p.turnRunning = false
	p.mu.Unlock()

	if err := session.Close(); err != nil {
		return fmt.Errorf("closing writer: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	select {
	case waitErr := <-done:
		if waitErr != nil {
			return fmt.Errorf("waiting for process: %w", waitErr)
		}
		return nil
	case <-ctx.Done():
		if err := session.Kill(); err != nil {
			return fmt.Errorf("killing process after stop timeout: %w", err)
		}
		if waitErr := <-done; waitErr != nil {
			return fmt.Errorf("waiting for killed process: %w", waitErr)
		}
		return nil
	}
}

func (p *claudeBackendProcess) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}
