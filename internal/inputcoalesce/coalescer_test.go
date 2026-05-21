// Tests for the stdin coalescer. QUM-608. The integration test that
// drives bytes through tea.NewProgram(m, tea.WithInput(coal)) is the
// regression-catch test that surfaced the four prototype bugs (data-
// loss on (n>0, EOF), readLoop leak on Close, burst-window non-reset,
// and Close idempotency).

package inputcoalesce

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// captureLog returns a LogFunc that appends each note to a slice and a
// thread-safe accessor for the captured notes.
func captureLog() (LogFunc, func() []string) {
	var mu sync.Mutex
	var notes []string
	return func(s string) {
			mu.Lock()
			defer mu.Unlock()
			notes = append(notes, s)
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := make([]string, len(notes))
			copy(out, notes)
			return out
		}
}

// readAllAvailable repeatedly calls Read until EOF or a deadline.
func readAllAvailable(t *testing.T, c *Coalescer, bufSize int, deadline time.Duration) []byte {
	t.Helper()
	var got bytes.Buffer
	buf := make([]byte, bufSize)
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		n, err := c.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
	}
	return got.Bytes()
}

func hasNoteContaining(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func TestCoalescer_SingleSmallByte_PassesThroughUnwrapped(t *testing.T) {
	t.Parallel()

	log, getNotes := captureLog()
	pr, pw := io.Pipe()
	c := New(pr, 10*time.Millisecond, log)
	defer c.Close()

	go func() {
		_, _ = pw.Write([]byte("a"))
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	got := readAllAvailable(t, c, 1024, 200*time.Millisecond)
	if string(got) != "a" {
		t.Fatalf("expected single byte 'a' unwrapped, got %q", got)
	}
	if bytes.Contains(got, []byte(pasteStart)) || bytes.Contains(got, []byte(pasteEnd)) {
		t.Fatalf("did not expect paste markers in single-byte output: %q", got)
	}
	if !hasNoteContaining(getNotes(), "wrapped=false") {
		t.Fatalf("expected at least one log note with wrapped=false; got: %+v", getNotes())
	}
}

func TestCoalescer_LargeBurst_GetsWrappedWithBracketedPaste(t *testing.T) {
	t.Parallel()

	log, getNotes := captureLog()
	pr, pw := io.Pipe()
	c := New(pr, 10*time.Millisecond, log)
	defer c.Close()

	payload := bytes.Repeat([]byte("a"), 200)

	go func() {
		_, _ = pw.Write(payload)
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	got := readAllAvailable(t, c, 4096, 300*time.Millisecond)

	wantLen := 200 + len(pasteStart) + len(pasteEnd)
	if len(got) != wantLen {
		t.Fatalf("expected wrapped length %d, got %d (data=%q)", wantLen, len(got), got)
	}
	if !bytes.HasPrefix(got, []byte(pasteStart)) {
		t.Fatalf("expected output to start with ESC[200~, got prefix %q", got[:min(len(got), 8)])
	}
	if !bytes.HasSuffix(got, []byte(pasteEnd)) {
		t.Fatalf("expected output to end with ESC[201~, got suffix %q", got[max(0, len(got)-8):])
	}
	middle := got[len(pasteStart) : len(got)-len(pasteEnd)]
	if !bytes.Equal(middle, payload) {
		t.Fatalf("middle of wrapped output should equal payload; got %q", middle)
	}
	if !hasNoteContaining(getNotes(), "wrapped=true") {
		t.Fatalf("expected log note with wrapped=true; got: %+v", getNotes())
	}
	if !hasNoteContaining(getNotes(), "bytes_in=200") {
		t.Fatalf("expected log note with bytes_in=200; got: %+v", getNotes())
	}
}

func TestCoalescer_BurstContainingESC_PassesThroughUnwrapped(t *testing.T) {
	t.Parallel()

	log, getNotes := captureLog()
	pr, pw := io.Pipe()
	c := New(pr, 10*time.Millisecond, log)
	defer c.Close()

	payload := append(bytes.Repeat([]byte("a"), 50), []byte("\x1b[A")...)
	payload = append(payload, bytes.Repeat([]byte("b"), 50)...)

	go func() {
		_, _ = pw.Write(payload)
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	got := readAllAvailable(t, c, 4096, 300*time.Millisecond)

	if !bytes.Equal(got, payload) {
		t.Fatalf("expected ESC-containing payload to pass through unmodified; got %q want %q", got, payload)
	}
	if bytes.HasPrefix(got, []byte(pasteStart)) {
		t.Fatalf("payload with ESC byte must NOT be wrapped; got %q", got[:min(len(got), 8)])
	}
	if !hasNoteContaining(getNotes(), "wrapped=false") {
		t.Fatalf("expected log note with wrapped=false; got: %+v", getNotes())
	}
}

func TestCoalescer_EOF_AfterPipeClose(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	c := New(pr, 10*time.Millisecond, nil)
	defer c.Close()

	go func() {
		_, _ = pw.Write([]byte("hi"))
		_ = pw.Close()
	}()

	var collected bytes.Buffer
	buf := make([]byte, 64)
	deadline := time.Now().Add(500 * time.Millisecond)
	sawEOF := false
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		// Per io.Reader best practice and ultraviolet's sendBytes
		// contract, Read MUST NOT return (n>0, io.EOF) in the same
		// call.
		if err != nil && n > 0 {
			t.Fatalf("Coalescer.Read returned (%d, %v) — data-loss-prone; EOF must come on next call", n, err)
		}
		if errors.Is(err, io.EOF) {
			sawEOF = true
			break
		}
		if err != nil {
			t.Fatalf("unexpected Read error: %v", err)
		}
	}
	if !sawEOF {
		t.Fatalf("expected io.EOF after writer closed; collected=%q", collected.String())
	}
	if collected.String() != "hi" {
		t.Fatalf("expected to receive %q before EOF, got %q", "hi", collected.String())
	}
}

func TestCoalescer_BurstWindow_ResetsPerChunk(t *testing.T) {
	t.Parallel()

	log, getNotes := captureLog()
	pr, pw := io.Pipe()
	// 20ms window, but feed chunks every 10ms for 60ms total — without
	// the reset fix, the first 20ms expiration would close the burst.
	c := New(pr, 20*time.Millisecond, log)
	defer c.Close()

	const chunkCount = 6
	const chunkSize = 40
	go func() {
		for i := 0; i < chunkCount; i++ {
			_, _ = pw.Write(bytes.Repeat([]byte("a"), chunkSize))
			time.Sleep(10 * time.Millisecond)
		}
		_ = pw.Close()
	}()

	got := readAllAvailable(t, c, 8192, 2*time.Second)

	wantPayloadLen := chunkCount * chunkSize
	wantWrappedLen := wantPayloadLen + len(pasteStart) + len(pasteEnd)
	if len(got) != wantWrappedLen {
		t.Fatalf("expected single wrapped output of %d bytes (240 payload + markers), got %d bytes — burst window did not reset per chunk", wantWrappedLen, len(got))
	}
	if !bytes.HasPrefix(got, []byte(pasteStart)) || !bytes.HasSuffix(got, []byte(pasteEnd)) {
		t.Fatalf("expected exactly one paste wrap; got %q ... %q", got[:min(len(got), 8)], got[max(0, len(got)-8):])
	}
	wrapped := 0
	for _, n := range getNotes() {
		if strings.Contains(n, "wrapped=true") {
			wrapped++
		}
	}
	if wrapped != 1 {
		t.Fatalf("expected exactly 1 wrapped log record (single paste), got %d; notes=%+v", wrapped, getNotes())
	}
}

func TestCoalescer_Close_UnblocksReadLoop(t *testing.T) {
	t.Parallel()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close() })
	c := New(pr, 5*time.Millisecond, nil)

	time.Sleep(50 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close() }()
	select {
	case <-closeDone:
	case <-time.After(1 * time.Second):
		t.Fatalf("Coalescer.Close() did not return within 1s")
	}

	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 16)
		for i := 0; i < 5; i++ {
			n, err := c.Read(buf)
			if err != nil && n == 0 {
				close(readDone)
				return
			}
		}
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(1 * time.Second):
		t.Fatalf("post-Close Read did not terminate — readLoop still blocked on src.Read")
	}
}

// pasteWatcherModel captures the first tea.PasteMsg seen, then quits.
type pasteWatcherModel struct {
	mu    sync.Mutex
	got   *tea.PasteMsg
	gotCh chan struct{}
	once  sync.Once
}

func (m *pasteWatcherModel) Init() tea.Cmd { return nil }

func (m *pasteWatcherModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if pm, ok := msg.(tea.PasteMsg); ok {
		m.mu.Lock()
		if m.got == nil {
			m.got = &pm
			m.once.Do(func() { close(m.gotCh) })
		}
		m.mu.Unlock()
		return m, tea.Quit
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.Code == 'c' && k.Mod&tea.ModCtrl != 0 {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *pasteWatcherModel) View() tea.View { return tea.NewView("") }

// TestCoalescer_PasteMsgEmittedThroughBubbleTea: pipe a burst through
// tea.NewProgram(m, tea.WithInput(coal)) and assert a tea.PasteMsg
// arrives end-to-end. Catches BOTH the (n>0, io.EOF) data-loss bug AND
// any deadlock that prevents shutdown from completing.
func TestCoalescer_PasteMsgEmittedThroughBubbleTea(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	coal := New(pr, 10*time.Millisecond, nil)
	defer coal.Close()

	m := &pasteWatcherModel{gotCh: make(chan struct{})}

	p := tea.NewProgram(
		m,
		tea.WithInput(coal),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	)

	payload := bytes.Repeat([]byte("a"), 200)
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = pw.Write(payload)
		time.Sleep(80 * time.Millisecond)
		_ = pw.Close()
	}()

	runDone := make(chan error, 1)
	go func() {
		_, err := p.Run()
		runDone <- err
	}()

	select {
	case <-m.gotCh:
	case <-time.After(3 * time.Second):
		p.Quit()
		t.Fatalf("no tea.PasteMsg observed within 3s — coalescer payload was dropped")
	}

	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			t.Fatalf("tea.Program.Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("tea.Program did not shut down within 2s after PasteMsg + EOF")
	}

	m.mu.Lock()
	got := m.got
	m.mu.Unlock()
	if got == nil {
		t.Fatalf("paste watcher unexpectedly missing message")
	}
	if got.Content != string(payload) {
		t.Fatalf("paste content mismatch: got %d bytes, want %d", len(got.Content), len(payload))
	}
}

func TestCoalescer_LoggerReceivesStructuredFields(t *testing.T) {
	t.Parallel()

	log, getNotes := captureLog()
	pr, pw := io.Pipe()
	c := New(pr, 10*time.Millisecond, log)
	defer c.Close()

	go func() {
		_, _ = pw.Write(bytes.Repeat([]byte("x"), 100))
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	_ = readAllAvailable(t, c, 4096, 300*time.Millisecond)

	notes := getNotes()
	if len(notes) == 0 {
		t.Fatalf("expected at least one log note")
	}
	for _, field := range []string{"bytes_in=", "bytes_out=", "wrapped=", "window_ms=", "duration_ns="} {
		if !hasNoteContaining(notes, field) {
			t.Fatalf("log notes missing %q field; notes=%+v", field, notes)
		}
	}
}

func TestCoalescer_SmallReadBuffer_DeliversFullPayloadAcrossCalls(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	c := New(pr, 10*time.Millisecond, nil)
	defer c.Close()

	payload := bytes.Repeat([]byte("z"), 200)
	go func() {
		_, _ = pw.Write(payload)
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	var got bytes.Buffer
	buf := make([]byte, 50)
	deadline := time.Now().Add(500 * time.Millisecond)
	callCount := 0
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
			callCount++
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected Read error: %v", err)
		}
		if got.Len() >= 212 {
			break
		}
	}

	if got.Len() != 212 {
		t.Fatalf("expected 212 total bytes delivered across small Reads, got %d (after %d calls)", got.Len(), callCount)
	}
	if callCount < 2 {
		t.Fatalf("expected at least 2 Read calls to drain 212 bytes via 50-byte buffer; got %d", callCount)
	}
	if !bytes.HasPrefix(got.Bytes(), []byte(pasteStart)) {
		t.Fatalf("expected wrapped output to start with ESC[200~")
	}
	if !bytes.HasSuffix(got.Bytes(), []byte(pasteEnd)) {
		t.Fatalf("expected wrapped output to end with ESC[201~")
	}
}

func TestCoalescer_Close_Idempotent(t *testing.T) {
	t.Parallel()

	pr, _ := io.Pipe()
	c := New(pr, 10*time.Millisecond, nil)

	if err := c.Close(); err != nil {
		t.Fatalf("first Close returned error: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Close panicked: %v", r)
		}
	}()
	_ = c.Close()
}
