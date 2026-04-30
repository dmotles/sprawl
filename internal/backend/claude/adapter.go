package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	backend "github.com/dmotles/sprawl/internal/backend"
	claudecli "github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/protocol"
)

// ExecSpec is the subprocess launch description produced by the Claude adapter.
type ExecSpec struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	Stderr io.Writer
}

// Starter launches a Claude subprocess from an ExecSpec.
type Starter interface {
	Start(ctx context.Context, spec ExecSpec) (backend.ManagedTransport, error)
}

// Config configures the Claude adapter.
type Config struct {
	Path     string
	LookPath func(string) (string, error)
	Starter  Starter
}

// Adapter launches Claude-backed backend sessions.
type Adapter struct {
	path     string
	lookPath func(string) (string, error)
	starter  Starter
}

// NewAdapter constructs a Claude adapter with real defaults unless overridden.
func NewAdapter(cfg Config) *Adapter {
	lookPath := cfg.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	starter := cfg.Starter
	if starter == nil {
		starter = &realStarter{}
	}
	return &Adapter{
		path:     cfg.Path,
		lookPath: lookPath,
		starter:  starter,
	}
}

// Start launches a Claude-backed backend session.
func (a *Adapter) Start(ctx context.Context, spec backend.SessionSpec) (backend.Session, error) {
	path := a.path
	if path == "" {
		var err error
		path, err = a.lookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("finding claude binary: %w", err)
		}
	}

	args := claudecli.LaunchOpts{
		Print:            true,
		InputFormat:      "stream-json",
		OutputFormat:     "stream-json",
		Verbose:          true,
		Model:            spec.Model,
		Effort:           spec.Effort,
		PermissionMode:   spec.PermissionMode,
		SessionID:        spec.SessionID,
		SystemPromptFile: spec.PromptFile,
		AllowedTools:     spec.AllowedTools,
		DisallowedTools:  spec.DisallowedTools,
		Agents:           spec.Agents,
		Resume:           spec.Resume,
	}.BuildArgs()

	stderr := spec.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	var transport backend.ManagedTransport
	if spec.OnResumeFailure != nil {
		stderr = claudecli.NewMarkerWriter(stderr, claudecli.NoConversationMarker, claudecli.ResumeMarkerScanCap, func() {
			spec.OnResumeFailure()
			if transport != nil {
				_ = transport.Kill()
			}
		})
	}

	execSpec := ExecSpec{
		Path:   path,
		Args:   args,
		Dir:    spec.WorkDir,
		Env:    buildEnv(spec),
		Stderr: stderr,
	}

	var err error
	transport, err = a.starter.Start(ctx, execSpec)
	if err != nil {
		return nil, err
	}

	return backend.NewSession(transport, backend.SessionConfig{
		SessionID: spec.SessionID,
		Identity:  spec.Identity,
		Capabilities: backend.Capabilities{
			SupportsInterrupt:  true,
			SupportsResume:     true,
			SupportsToolBridge: true,
		},
		Observer: spec.Observer,
	}), nil
}

func buildEnv(spec backend.SessionSpec) []string {
	env := os.Environ()
	env = append(env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
	if spec.Identity != "" {
		env = append(env, fmt.Sprintf("SPRAWL_AGENT_IDENTITY=%s", spec.Identity))
	}
	if spec.SprawlRoot != "" {
		env = append(env, fmt.Sprintf("SPRAWL_ROOT=%s", spec.SprawlRoot))
	}
	for k, v := range spec.AdditionalEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

type realStarter struct{}

func (s *realStarter) Start(ctx context.Context, spec ExecSpec) (backend.ManagedTransport, error) {
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...) //nolint:gosec // spec.Path/spec.Args are constructed from trusted session policy and LookPath/config
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Stderr != nil {
		cmd.Stderr = spec.Stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	return &transport{
		reader: protocol.NewReader(stdout),
		writer: protocol.NewWriter(stdin),
		wait:   cmd.Wait,
		kill: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
	}, nil
}

type transport struct {
	reader *protocol.Reader
	writer *protocol.Writer
	wait   func() error
	kill   func() error
}

func (t *transport) Send(_ context.Context, msg any) error {
	return t.writer.WriteJSON(msg)
}

func (t *transport) Recv(_ context.Context) (*protocol.Message, error) {
	return t.reader.Next()
}

func (t *transport) Close() error {
	return t.writer.Close()
}

func (t *transport) Wait() error {
	if t.wait == nil {
		return nil
	}
	return t.wait()
}

func (t *transport) Kill() error {
	if t.kill == nil {
		return nil
	}
	return t.kill()
}
