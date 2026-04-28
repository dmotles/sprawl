package cmd

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/dmotles/sprawl/internal/agentloop"
	backend "github.com/dmotles/sprawl/internal/backend"
	backendclaude "github.com/dmotles/sprawl/internal/backend/claude"
	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

func buildAgentSessionSpec(agentState *state.AgentState, promptPath, sprawlRoot string) backend.SessionSpec {
	return backend.SessionSpec{
		WorkDir:        agentState.Worktree,
		Identity:       agentState.Name,
		SprawlRoot:     sprawlRoot,
		SessionID:      agentState.SessionID,
		PromptFile:     promptPath,
		Model:          rootinit.DefaultModel,
		Effort:         "medium",
		PermissionMode: "bypassPermissions",
	}
}

func startBackendProcess(
	ctx context.Context,
	deps *agentLoopDeps,
	spec backend.SessionSpec,
	observer agentloop.Observer,
	sprawlRoot string,
	agentName string,
	parent string,
	reason string,
) (processManager, bool) {
	proc := deps.newBackendProcess(spec, observer)
	if err := proc.Launch(ctx); err != nil {
		fmt.Fprintf(deps.stdout, "[agent-loop] %s: %v\n", reason, err)
		_ = deps.sendMessage(sprawlRoot, agentName, parent, "[PROBLEM] agent-loop failure", fmt.Sprintf("%s: %v", reason, err))
		deps.exit(1)
		return nil, false
	}
	return proc, true
}

func newClaudeBackendProcess(spec backend.SessionSpec, observer agentloop.Observer) processManager {
	spec.Observer = observer
	return &claudeBackendProcess{
		spec: spec,
	}
}

type claudeBackendProcess struct {
	spec    backend.SessionSpec
	session backend.Session

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

	events, err := session.StartTurn(ctx, prompt)
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
		return agentloop.ErrNotRunning
	}
	return session.Interrupt(ctx)
}

func (p *claudeBackendProcess) Stop(_ context.Context) error {
	p.mu.Lock()
	session := p.session
	if session == nil {
		p.running = false
		p.turnRunning = false
		p.mu.Unlock()
		return nil
	}
	p.running = false
	p.turnRunning = false
	p.mu.Unlock()

	closeErr := session.Close()
	waitErr := session.Wait()
	if closeErr != nil {
		return fmt.Errorf("closing writer: %w", closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("waiting for process: %w", waitErr)
	}
	return nil
}

func (p *claudeBackendProcess) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}
