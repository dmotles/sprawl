package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	backend "github.com/dmotles/sprawl/internal/backend"
	claudecli "github.com/dmotles/sprawl/internal/claude"
	"github.com/dmotles/sprawl/internal/protocol"
)

// defaultTermGrace is the default SIGTERM → SIGKILL grace window applied by
// transport.Close()'s polite-shutdown handshake (QUM-638). 200ms is enough
// for claude to flush its transcript/wirelog files cleanly on a normal exit
// while keeping teardown well under the 500ms happy-path budget.
const defaultTermGrace = 200 * time.Millisecond

// ExecSpec is the subprocess launch description produced by the Claude adapter.
type ExecSpec struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	Stderr io.Writer
	// WireLogPath, when non-empty, is the NDJSON file the realStarter tees
	// the subprocess stdin/stdout into (QUM-632).
	WireLogPath string
}

// Starter launches a Claude subprocess from an ExecSpec.
//
// Start takes no ctx by design (QUM-612). The ctx parameter was previously
// forwarded into `exec.CommandContext`, which made it possible for a
// short-lived request ctx to SIGKILL the freshly-spawned subprocess the
// instant the request returned — see QUM-606. Subprocess teardown is owned
// by the returned ManagedTransport's Close/Kill path, so the ctx-cancel
// safety net was unused and actively dangerous. Dropping ctx from the
// signature makes the bug class impossible to reintroduce.
type Starter interface {
	Start(spec ExecSpec) (backend.ManagedTransport, error)
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
// Start launches a Claude-backed backend session.
//
// The ctx parameter is unused (QUM-612): it used to be forwarded into the
// Starter, which forwarded it into exec.CommandContext — the exact ctx-cancel
// chain that produced the QUM-606 zombie. The Starter interface no longer
// accepts a ctx, so this seam can't forward one. The parameter is kept on
// the signature because Adapter.Start sits behind the seam consumed by
// internal/supervisor's `unifiedAdapterStartFn` (a `func(ctx, spec)` seam)
// and the supervisor's call site already passes `context.Background()`.
func (a *Adapter) Start(_ context.Context, spec backend.SessionSpec) (backend.Session, error) {
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

	wireLogPath := ""
	if spec.SprawlRoot != "" && spec.Identity != "" && spec.SessionID != "" {
		wireLogPath = filepath.Join(spec.SprawlRoot, ".sprawl", "logs", "sessions", spec.Identity, spec.SessionID+".ndjson")
	}

	execSpec := ExecSpec{
		Path:        path,
		Args:        args,
		Dir:         spec.WorkDir,
		Env:         buildEnv(spec),
		Stderr:      stderr,
		WireLogPath: wireLogPath,
	}

	var err error
	transport, err = a.starter.Start(execSpec)
	if err != nil {
		return nil, err
	}

	cfg := backend.SessionConfig{
		SessionID: spec.SessionID,
		Identity:  spec.Identity,
		Capabilities: backend.Capabilities{
			SupportsInterrupt:  true,
			SupportsResume:     true,
			SupportsToolBridge: true,
		},
		Observer: spec.Observer,
	}
	// QUM-635: optional override for the D1 frame-stall watchdog window. The
	// 10-minute default makes the "ask_user_question idle past the watchdog"
	// scenario impractical to exercise in an automated e2e; the idle-past-
	// watchdog row sets a short duration so it runs in seconds. Production
	// leaves SPRAWL_BACKEND_HANG_TIMEOUT unset and keeps the default.
	if d, ok := resolveHangTimeout(); ok {
		cfg.HangTimeout = d
	}
	return backend.NewSession(transport, cfg), nil
}

// resolveHangTimeout reads an optional override for the backend D1 hang
// watchdog window from SPRAWL_BACKEND_HANG_TIMEOUT (a Go duration, e.g.
// "20s"; a negative value disables the watchdog). Returns (0, false) when
// unset or unparseable so the caller falls back to backend defaults. This is
// a diagnostic / test seam — see QUM-635's idle-past-watchdog e2e.
func resolveHangTimeout() (time.Duration, bool) {
	raw := os.Getenv("SPRAWL_BACKEND_HANG_TIMEOUT")
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	return d, true
}

// resolveTermGrace reads the polite-shutdown grace from SPRAWL_TERM_GRACE
// (a Go duration). Falls back to defaultTermGrace on unset / empty /
// unparseable. Negative values are passed through to allow tests to force
// immediate SIGKILL escalation. Mirrors the resolveHangTimeout test-seam
// pattern (QUM-635). See QUM-638.
func resolveTermGrace() time.Duration {
	raw := os.Getenv("SPRAWL_TERM_GRACE")
	if raw == "" {
		return defaultTermGrace
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return defaultTermGrace
	}
	return d
}

// terminateProcess implements the QUM-638 polite-shutdown handshake on a
// running subprocess: SIGTERM → wait up to grace for exit → SIGKILL if the
// subprocess is still alive. The exited channel must be closed by the
// caller's cmd.Wait reaper when the process exits, so we can race the grace
// timer against actual exit. nil-process is a no-op. An already-exited
// subprocess (exited already closed) is a no-op. Errors from Signal are
// best-effort (the subprocess may have died between checks); only the
// SIGKILL escalation error is returned. See internal/merge/runtests.go's
// killGracePeriod pattern for the equivalent on subprocess trees.
func terminateProcess(p *os.Process, exited <-chan struct{}, grace time.Duration) error {
	if p == nil {
		return nil
	}
	// Fast path: already exited.
	select {
	case <-exited:
		return nil
	default:
	}
	// Polite signal. ErrProcessDone (subprocess already reaped) means we
	// raced with cmd.Wait — treat as success.
	if err := p.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		// Other errors (ESRCH on non-Linux variants, etc.) — fall through
		// to the grace wait + escalation. The race is rare enough that
		// best-effort is correct.
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exited:
		return nil
	case <-timer.C:
	}
	// Escalate.
	if err := p.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return nil
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

func (s *realStarter) Start(spec ExecSpec) (backend.ManagedTransport, error) {
	// QUM-612: Subprocess lifetime MUST outlive any request-scoped ctx. The
	// QUM-606 bug class — where a short-lived MCP request ctx (e.g.
	// `toolRecover`'s) SIGKILLed the freshly-spawned claude the moment the
	// MCP call returned — flowed entirely through exec.CommandContext. By
	// deriving context.Background() internally (and refusing a ctx parameter
	// at the type boundary) we make the bug impossible to reintroduce.
	// Teardown is owned by the returned ManagedTransport's Kill/Close path.
	cmd := exec.CommandContext(context.Background(), spec.Path, spec.Args...) //nolint:gosec // spec.Path/spec.Args are constructed from trusted session policy and LookPath/config
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
	var wl *wireLog
	if spec.WireLogPath != "" {
		w, werr := newWireLog(spec.WireLogPath)
		if werr != nil {
			fmt.Fprintf(os.Stderr, "sprawl: wire-log disabled (open %s: %v)\n", spec.WireLogPath, werr)
		} else {
			wl = w
		}
	}

	var rdr io.Reader = stdout
	var wtr io.Writer = stdin
	if wl != nil {
		rdr = io.TeeReader(stdout, wl.dirWriter("out"))
		wtr = io.MultiWriter(stdin, wl.dirWriter("in"))
	}

	if err := cmd.Start(); err != nil {
		if wl != nil {
			_ = wl.Close()
		}
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	// QUM-638: a single shared cmd.Wait reaper publishes the exit result on
	// `exited`. transport.Wait reads it; transport.Close's polite-shutdown
	// races the grace timer against it. cmd.Wait can only be called once, so
	// centralizing it here is the only safe way to expose subprocess-exit
	// observability to multiple call sites.
	exited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(exited)
	}()

	proc := cmd.Process
	return &transport{
		reader:  protocol.NewReader(rdr),
		writer:  protocol.NewWriter(wtr),
		wireLog: wl,
		wait: func() error {
			<-exited
			return waitErr
		},
		terminate: func(grace time.Duration) error {
			return terminateProcess(proc, exited, grace)
		},
		kill: func() error {
			if proc != nil {
				return proc.Kill()
			}
			return nil
		},
		pid: pid,
	}, nil
}

type transport struct {
	reader    *protocol.Reader
	writer    *protocol.Writer
	wireLog   *wireLog
	wait      func() error
	kill      func() error
	terminate func(grace time.Duration) error
	pid       int
}

// Pid returns the OS process ID of the underlying claude subprocess.
func (t *transport) Pid() int { return t.pid }

// Send honors ctx natively (QUM-603). WriteJSON is a blocking syscall write
// to claude's stdin pipe; when claude is wedged and not draining stdin, the
// kernel buffer fills and the write blocks forever. We run WriteJSON in a
// goroutine and race against ctx.Done() so callers can unwind cleanly on
// cancellation. On the wedged-pipe edge the goroutine leaks until the OS
// reaps the subprocess (typically via SIGKILL during teardown — see QUM-600).
func (t *transport) Send(ctx context.Context, msg any) error {
	errCh := make(chan error, 1)
	go func() { errCh <- t.writer.WriteJSON(msg) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *transport) Recv(_ context.Context) (*protocol.Message, error) {
	return t.reader.Next()
}

// Close performs the QUM-638 polite-shutdown handshake — SIGTERM → wait
// SPRAWL_TERM_GRACE for exit → SIGKILL escalation — then closes the stdin
// pipe. Issuing SIGTERM before writer.Close means the kernel closes the
// subprocess's stdout pipe as soon as it exits, so the host-side reader
// unwinds in milliseconds rather than the 1-2s claude takes to notice
// stdin EOF. The terminate hook is nil for transports constructed without
// a backing subprocess (test seams); in that case Close degrades to the
// pre-QUM-638 writer.Close-only behaviour.
func (t *transport) Close() error {
	if t.terminate != nil {
		_ = t.terminate(resolveTermGrace())
	}
	return t.writer.Close()
}

func (t *transport) Wait() error {
	if t.wait == nil {
		return nil
	}
	err := t.wait()
	if t.wireLog != nil {
		_ = t.wireLog.Close()
	}
	return err
}

func (t *transport) Kill() error {
	if t.wireLog != nil {
		_ = t.wireLog.Close()
	}
	if t.kill == nil {
		return nil
	}
	return t.kill()
}
