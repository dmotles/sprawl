// Package agentloop manages the lifecycle of a Claude Code subprocess,
// handling message routing, control requests, and state transitions.
package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dmotles/dendra/internal/protocol"
)

// MessageReader reads protocol messages from the Claude process stdout.
type MessageReader interface {
	Next() (*protocol.Message, error)
}

// MessageWriter sends messages to the Claude process stdin.
type MessageWriter interface {
	SendUserMessage(prompt string) error
	ApproveToolUse(requestID string) error
	SendInterrupt(requestID string) error
	Close() error
}

// WaitFunc blocks until the subprocess exits, returning any error.
type WaitFunc func() error

// CancelFunc forcefully terminates the subprocess.
type CancelFunc func() error

// CommandStarter launches a Claude Code subprocess and returns I/O handles.
type CommandStarter interface {
	Start(ctx context.Context, config ProcessConfig) (MessageReader, MessageWriter, WaitFunc, CancelFunc, error)
}

// Observer receives protocol messages during a prompt loop for monitoring/logging.
type Observer interface {
	OnMessage(msg *protocol.Message)
}

// ProcessConfig holds the configuration for launching a Claude Code subprocess.
type ProcessConfig struct {
	ClaudePath       string
	WorkDir          string
	SessionID        string
	SystemPrompt     string
	SystemPromptFile string
	AgentName      string
	DendraRoot     string
	Env            map[string]string
	Resume         bool
	SettingSources string
}

// ProcessState represents the current lifecycle state of the process.
type ProcessState string

const (
	StateStarting ProcessState = "starting"
	StateIdle     ProcessState = "idle"
	StateRunning  ProcessState = "running"
	StateStopped  ProcessState = "stopped"
)

// ErrNotRunning is returned by InterruptTurn when no turn is active.
var ErrNotRunning = errors.New("agentloop: no turn in progress")

// newRequestID generates a unique request ID for interrupt messages.
func newRequestID() string {
	return fmt.Sprintf("interrupt-%d", time.Now().UnixNano())
}

// Option configures a Process.
type Option func(*Process)

// WithObserver attaches an observer that receives protocol messages.
func WithObserver(o Observer) Option {
	return func(p *Process) {
		p.observer = o
	}
}

// Process manages the lifecycle of a single Claude Code subprocess.
// A background readLoop goroutine continuously drains Claude's stdout,
// dispatching messages to the observer and delivering results via channels.
type Process struct {
	config   ProcessConfig
	starter  CommandStarter
	writer   MessageWriter
	waitFn   WaitFunc
	cancelFn CancelFunc
	state    ProcessState
	observer Observer
	mu       sync.Mutex

	// writerMu protects concurrent writes to stdin.
	// readLoop calls ApproveToolUse; SendPrompt calls SendUserMessage.
	writerMu sync.Mutex

	// Background reader delivers results and errors via channels.
	resultCh    chan *protocol.ResultMessage // buffered(1)
	readerErrCh chan error                  // buffered(1)

	// stopCh is closed on Stop/Kill to unblock readLoop's channel sends.
	stopCh chan struct{}
}

// NewProcess creates a new Process with the given config, starter, and options.
func NewProcess(config ProcessConfig, starter CommandStarter, opts ...Option) *Process {
	p := &Process{
		config:  config,
		starter: starter,
		state:   StateStopped,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// readLoop continuously reads from the MessageReader in a background goroutine.
// It dispatches every message to the observer, sends results to resultCh,
// auto-approves control requests, and reports errors via readerErrCh.
// The reader is owned exclusively by this goroutine.
func (p *Process) readLoop(reader MessageReader) {
	for {
		msg, err := reader.Next()
		if err != nil {
			select {
			case p.readerErrCh <- err:
			default:
			}
			return
		}

		// Observer sees every message type.
		if p.observer != nil {
			p.observer.OnMessage(msg)
		}

		switch msg.Type {
		case "result":
			var result protocol.ResultMessage
			if err := protocol.ParseAs(msg, &result); err != nil {
				select {
				case p.readerErrCh <- fmt.Errorf("parsing result message: %w", err):
				default:
				}
				return
			}
			// Block until the consumer reads the result, or stop is signaled.
			select {
			case p.resultCh <- &result:
			case <-p.stopCh:
				return
			}

		case "control_request":
			var cr protocol.ControlRequest
			if err := protocol.ParseAs(msg, &cr); err != nil {
				select {
				case p.readerErrCh <- fmt.Errorf("parsing control request: %w", err):
				default:
				}
				return
			}
			p.writerMu.Lock()
			err := p.writer.ApproveToolUse(cr.RequestID)
			p.writerMu.Unlock()
			if err != nil {
				select {
				case p.readerErrCh <- fmt.Errorf("approving tool use: %w", err):
				default:
				}
				return
			}
		}
	}
}

// Start launches the Claude Code subprocess, sends the initial prompt,
// and blocks until the initial prompt's result arrives.
func (p *Process) Start(ctx context.Context, initialPrompt string) error {
	p.mu.Lock()
	p.state = StateStarting
	p.mu.Unlock()

	reader, writer, waitFn, cancelFn, err := p.starter.Start(ctx, p.config)
	if err != nil {
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return fmt.Errorf("starting claude process: %w", err)
	}

	p.mu.Lock()
	p.writer = writer
	p.waitFn = waitFn
	p.cancelFn = cancelFn
	p.resultCh = make(chan *protocol.ResultMessage, 1)
	p.readerErrCh = make(chan error, 1)
	p.stopCh = make(chan struct{})
	p.mu.Unlock()

	// Start the background reader goroutine.
	go p.readLoop(reader)

	// Send the initial user message.
	p.writerMu.Lock()
	sendErr := writer.SendUserMessage(initialPrompt)
	p.writerMu.Unlock()
	if sendErr != nil {
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return fmt.Errorf("sending initial prompt: %w", sendErr)
	}

	// Wait for the initial prompt's result.
	select {
	case <-p.resultCh:
		p.mu.Lock()
		p.state = StateIdle
		p.mu.Unlock()
		return nil
	case err := <-p.readerErrCh:
		// Check for a pending result that arrived before the error.
		select {
		case <-p.resultCh:
			p.mu.Lock()
			p.state = StateIdle
			p.mu.Unlock()
			return nil
		default:
		}
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return fmt.Errorf("reading during start: %w", err)
	case <-ctx.Done():
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return ctx.Err()
	}
}

// SendPrompt sends a user prompt and blocks until a result is received.
func (p *Process) SendPrompt(ctx context.Context, prompt string) (*protocol.ResultMessage, error) {
	p.mu.Lock()
	p.state = StateRunning
	p.mu.Unlock()

	p.writerMu.Lock()
	sendErr := p.writer.SendUserMessage(prompt)
	p.writerMu.Unlock()
	if sendErr != nil {
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return nil, fmt.Errorf("sending prompt: %w", sendErr)
	}

	select {
	case result := <-p.resultCh:
		p.mu.Lock()
		p.state = StateIdle
		p.mu.Unlock()
		return result, nil
	case err := <-p.readerErrCh:
		// Check for a pending result that arrived before the error.
		select {
		case result := <-p.resultCh:
			// Result was available — prefer it. Re-queue the error for the next caller.
			select {
			case p.readerErrCh <- err:
			default:
			}
			p.mu.Lock()
			p.state = StateIdle
			p.mu.Unlock()
			return result, nil
		default:
		}
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return nil, fmt.Errorf("reading message: %w", err)
	case <-ctx.Done():
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

// State returns the current process state.
func (p *Process) State() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// SessionID returns the session ID from the process config.
func (p *Process) SessionID() string {
	return p.config.SessionID
}

// Stop gracefully shuts down the subprocess by closing the writer and waiting.
// Closing the writer causes EOF on Claude's stdin; readLoop sees EOF from
// the reader and exits.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.writer == nil {
		p.state = StateStopped
		p.mu.Unlock()
		return nil
	}
	stopCh := p.stopCh
	p.mu.Unlock()

	// Signal readLoop to stop blocking on channel sends.
	if stopCh != nil {
		select {
		case <-stopCh:
			// Already closed.
		default:
			close(stopCh)
		}
	}

	closeErr := p.writer.Close()

	// Always transition to stopped, even on close error.
	defer func() {
		p.mu.Lock()
		p.state = StateStopped
		p.mu.Unlock()
	}()

	if closeErr != nil {
		return fmt.Errorf("closing writer: %w", closeErr)
	}
	if err := p.waitFn(); err != nil {
		return fmt.Errorf("waiting for process: %w", err)
	}
	return nil
}

// Kill forcefully terminates the subprocess.
func (p *Process) Kill() error {
	p.mu.Lock()
	if p.cancelFn == nil {
		p.state = StateStopped
		p.mu.Unlock()
		return nil
	}
	stopCh := p.stopCh
	p.mu.Unlock()

	// Signal readLoop to stop blocking on channel sends.
	if stopCh != nil {
		select {
		case <-stopCh:
			// Already closed.
		default:
			close(stopCh)
		}
	}

	err := p.cancelFn()
	p.mu.Lock()
	p.state = StateStopped
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("killing process: %w", err)
	}
	return nil
}

// IsRunning reports whether the process is in an active state (idle or running).
func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == StateIdle || p.state == StateRunning
}

// InterruptTurn sends an interrupt control_request to cancel the current turn.
// It is safe to call concurrently with SendPrompt.
// Returns ErrNotRunning if the process is not in StateRunning.
func (p *Process) InterruptTurn(ctx context.Context) error {
	p.mu.Lock()
	if p.state != StateRunning {
		p.mu.Unlock()
		return ErrNotRunning
	}
	writer := p.writer
	p.mu.Unlock()

	requestID := newRequestID()
	p.writerMu.Lock()
	err := writer.SendInterrupt(requestID)
	p.writerMu.Unlock()
	return err
}
