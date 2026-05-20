// THROWAWAY DIAGNOSTIC: QUM-608. Safe to delete this file and the companion
// cmd/input_debug_test.go to remove the command entirely. Registration lives
// in this file's init(), so deletion requires no edits elsewhere.
//
// Purpose: a hidden `sprawl input-debug` cobra subcommand that mounts ONLY
// the bare InputModel (no tree, no viewport, no statusbar, no notifier) in a
// minimal Bubble Tea program and writes a line-delimited JSON log of every
// Msg received, with per-Update / per-View wall-clock cost and elapsed-since-
// previous-msg deltas. Used to attribute per-char paste latency in the
// coder→tmux stack where claude code pastes instantly but sprawl animates.

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/tui"
)

const (
	keyPressContentMax = 120
	defaultContentMax  = 160
)

type debugRecord struct {
	TsNs     int64  `json:"ts_ns"`
	DeltaNs  int64  `json:"delta_ns"`
	Kind     string `json:"kind"` // "msg" | "view"
	MsgType  string `json:"msg_type,omitempty"`
	Content  string `json:"content,omitempty"`
	UpdateNs int64  `json:"update_ns,omitempty"`
	ViewNs   int64  `json:"view_ns,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

type debugLogger struct {
	mu    sync.Mutex
	f     *os.File
	enc   *json.Encoder
	start time.Time
	prev  time.Time
}

func newDebugLogger(path string) (*debugLogger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}
	now := time.Now()
	return &debugLogger{
		f:     f,
		enc:   json.NewEncoder(f),
		start: now,
		prev:  now,
	}, nil
}

func (l *debugLogger) write(rec debugRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if rec.TsNs == 0 {
		rec.TsNs = now.Sub(l.start).Nanoseconds()
	}
	if rec.DeltaNs == 0 {
		rec.DeltaNs = now.Sub(l.prev).Nanoseconds()
	}
	l.prev = now
	_ = l.enc.Encode(&rec)
}

func (l *debugLogger) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// inputDebugModel wraps tui.InputModel and logs every Msg with timing data.
type inputDebugModel struct {
	input tui.InputModel
	log   *debugLogger
	width int

	// prevPendingEnter tracks (externally) whether the previous Update left
	// the InputModel in its post-Enter lookahead window. Used to detect
	// QUM-455 reclassification (Enter -> embedded "\n") without poking at
	// the InputModel's unexported fields.
	prevPendingEnter bool
}

func newInputDebugModel(lg *debugLogger) *inputDebugModel {
	theme := tui.NewTheme("")
	im := tui.NewInputModel(&theme)
	return &inputDebugModel{input: im, log: lg}
}

func (m *inputDebugModel) Init() tea.Cmd {
	return m.input.Focus()
}

func (m *inputDebugModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	msgType := fmt.Sprintf("%T", msg)
	content := snapshotContent(msg, msgType)

	// Window resize: forward width to input.
	if w, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = w.Width
		m.input.SetWidth(w.Width)
	}

	// Ctrl+C quits.
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.Code == 'c' && k.Mod&tea.ModCtrl != 0 {
			m.log.write(debugRecord{
				Kind:    "msg",
				MsgType: msgType,
				Content: content,
				Notes:   "ctrl-c-quit",
			})
			return m, tea.Quit
		}
	}

	prevValue := m.input.Value()
	prevPending := m.prevPendingEnter

	t0 := time.Now()
	next, cmd := m.input.Update(msg)
	updateNs := time.Since(t0).Nanoseconds()
	m.input = next

	newValue := m.input.Value()
	notes := classifyTransition(msg, msgType, prevValue, newValue, prevPending, cmd)

	// Track pending-Enter state for the NEXT update. A plain Enter on an
	// otherwise unchanged buffer with a non-nil cmd is the lookahead tick;
	// the lookahead msg itself, or any other key while pending, clears it.
	m.prevPendingEnter = computePendingEnter(msg, msgType, prevPending, prevValue, newValue, cmd)

	m.log.write(debugRecord{
		Kind:     "msg",
		MsgType:  msgType,
		Content:  content,
		UpdateNs: updateNs,
		Notes:    notes,
	})

	return m, cmd
}

func (m *inputDebugModel) View() tea.View {
	t0 := time.Now()
	s := m.input.View()
	viewNs := time.Since(t0).Nanoseconds()
	m.log.write(debugRecord{Kind: "view", ViewNs: viewNs})
	footer := fmt.Sprintf("\n[input-debug] paste freely; Ctrl+C to exit. width=%d", m.width)
	v := tea.NewView(s + footer)
	v.AltScreen = true
	return v
}

// snapshotContent returns a human-readable, truncated representation of msg.
// PasteMsg content is recorded in full; KeyPressMsg / generic msgs are
// truncated to keep log lines manageable.
func snapshotContent(msg tea.Msg, _ string) string {
	switch v := msg.(type) {
	case tea.PasteMsg:
		// Full content for paste — that's the whole point.
		return v.Content
	case tea.KeyPressMsg:
		s := fmt.Sprintf("code=%v mod=%v text=%q", v.Code, v.Mod, v.Text)
		return truncate(s, keyPressContentMax)
	case tea.WindowSizeMsg:
		return fmt.Sprintf("%dx%d", v.Width, v.Height)
	default:
		// Best-effort generic dump. Some msgs format with %+v better than %v.
		return truncate(fmt.Sprintf("%+v", v), defaultContentMax)
	}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// classifyTransition annotates the QUM-455 reclassification heuristic so the
// post-mortem log shows where Enter resolved as embedded newline vs. submit.
func classifyTransition(msg tea.Msg, msgType, prevValue, newValue string, prevPending bool, cmd tea.Cmd) string {
	// Lookahead tick: the unexported tui.pasteLookaheadMsg shows up by %T
	// string. Annotate so weave can correlate with the prior Enter.
	if strings.HasSuffix(msgType, "pasteLookaheadMsg") {
		if cmd != nil {
			return "lookahead-tick-resolved-submit"
		}
		return "lookahead-tick-stale-or-no-op"
	}
	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		return ""
	}
	isPlainEnter := k.Code == tea.KeyEnter && k.Mod == 0
	if prevPending && !isPlainEnter && len(newValue) == len(prevValue)+1 && strings.HasSuffix(newValue, "\n") {
		return "enter-reclassified-as-embedded-newline"
	}
	if prevPending && isPlainEnter {
		return "double-enter-prior-reclassified-as-newline"
	}
	if !prevPending && isPlainEnter && cmd != nil {
		return "plain-enter-scheduled-lookahead"
	}
	return ""
}

// computePendingEnter returns the pendingEnter state to remember for the next
// Update. Mirrors InputModel's internal transitions externally; necessarily
// heuristic because pendingEnter is unexported.
func computePendingEnter(msg tea.Msg, msgType string, prevPending bool, prevValue, newValue string, cmd tea.Cmd) bool {
	if strings.HasSuffix(msgType, "pasteLookaheadMsg") {
		// Tick resolved (submit or no-op): no longer pending.
		return false
	}
	k, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		return prevPending
	}
	isPlainEnter := k.Code == tea.KeyEnter && k.Mod == 0
	if isPlainEnter && cmd != nil {
		// Either freshly scheduled lookahead or double-Enter rescheduled it.
		return true
	}
	if prevPending {
		// Some other key arrived while pending → InputModel cleared pending.
		_ = prevValue
		_ = newValue
		return false
	}
	return prevPending
}

var inputDebugOut string

var inputDebugCmd = &cobra.Command{
	Use:    "input-debug",
	Short:  "QUM-608 diagnostic: bare InputModel + per-Msg latency log (hidden)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runInputDebug,
}

func init() {
	inputDebugCmd.Flags().StringVar(&inputDebugOut, "out", "./input-debug.log", "path to write JSONL diagnostic log")
	rootCmd.AddCommand(inputDebugCmd)
}

func runInputDebug(cmd *cobra.Command, _ []string) error {
	lg, err := newDebugLogger(inputDebugOut)
	if err != nil {
		return err
	}
	defer lg.close()

	m := newInputDebugModel(lg)
	p := tea.NewProgram(m)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		if _, ok := <-sigCh; ok {
			p.Quit()
		}
	}()
	defer signal.Stop(sigCh)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("input-debug program: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "input-debug log written to %s\n", inputDebugOut)
	return nil
}
