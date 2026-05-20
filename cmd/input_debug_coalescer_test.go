// THROWAWAY DIAGNOSTIC TESTS: QUM-608 Path 2 prototype. Pairs with
// cmd/input_debug_coalescer.go (the stdin coalescer that synthesizes
// bracketed-paste markers around detected bursts). Safe to delete with
// the rest of the input-debug command.

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// newTestLogger creates a debugLogger writing to a temp file inside t.TempDir().
// Returns the logger and a function that closes it and returns all decoded
// records.
func newTestLogger(t *testing.T) (*debugLogger, func() []debugRecord) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coalesce.log")
	lg, err := newDebugLogger(path)
	if err != nil {
		t.Fatalf("newDebugLogger: %v", err)
	}
	return lg, func() []debugRecord {
		_ = lg.close()
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open log: %v", err)
		}
		defer f.Close()
		dec := json.NewDecoder(f)
		var out []debugRecord
		for {
			var r debugRecord
			if err := dec.Decode(&r); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("decode log: %v", err)
			}
			out = append(out, r)
		}
		return out
	}
}

// readAllAvailable repeatedly calls Read until EOF or a deadline. Used because
// the coalescer can return data across multiple Read() calls.
func readAllAvailable(t *testing.T, c *coalescer, bufSize int, deadline time.Duration) []byte {
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

func TestCoalescer_SingleSmallByte_PassesThroughUnwrapped(t *testing.T) {
	t.Parallel()

	lg, records := newTestLogger(t)
	pr, pw := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)
	defer c.Close()

	go func() {
		_, _ = pw.Write([]byte("a"))
		// Close to allow the reader to terminate cleanly after delivering.
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

	recs := records()
	if !hasCoalesceRead(recs, "wrapped=false") {
		t.Fatalf("expected at least one coalesce-read record with wrapped=false; got: %+v", recs)
	}
}

func TestCoalescer_LargeBurst_GetsWrappedWithBracketedPaste(t *testing.T) {
	t.Parallel()

	lg, records := newTestLogger(t)
	pr, pw := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)
	defer c.Close()

	payload := bytes.Repeat([]byte("a"), 200)

	go func() {
		_, _ = pw.Write(payload)
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	got := readAllAvailable(t, c, 4096, 300*time.Millisecond)

	wantLen := 200 + len(pasteStart) + len(pasteEnd) // 212
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

	recs := records()
	if !hasCoalesceRead(recs, "wrapped=true") {
		t.Fatalf("expected at least one coalesce-read record with wrapped=true; got: %+v", recs)
	}
	if !hasCoalesceRead(recs, "bytes_in=200") {
		t.Fatalf("expected coalesce-read record with bytes_in=200; got: %+v", recs)
	}
}

func TestCoalescer_BurstContainingESC_PassesThroughUnwrapped(t *testing.T) {
	t.Parallel()

	lg, records := newTestLogger(t)
	pr, pw := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)
	defer c.Close()

	// Mix printable bytes with an ESC byte — simulates arrow-key sequences
	// interleaved or a real terminal escape inside the burst.
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

	recs := records()
	if !hasCoalesceRead(recs, "wrapped=false") {
		t.Fatalf("expected at least one coalesce-read record with wrapped=false; got: %+v", recs)
	}
}

func TestCoalescer_EOF_AfterPipeClose(t *testing.T) {
	t.Parallel()

	lg, _ := newTestLogger(t)
	pr, pw := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)
	defer c.Close()

	go func() {
		_, _ = pw.Write([]byte("hi"))
		_ = pw.Close()
	}()

	// Drain content first.
	var collected bytes.Buffer
	buf := make([]byte, 64)
	deadline := time.Now().Add(500 * time.Millisecond)
	sawEOF := false
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if n > 0 {
			collected.Write(buf[:n])
		}
		// Per io.Reader best practice and ultraviolet's sendBytes contract
		// (terminal_reader.go:121-127), Read MUST NOT return (n>0, io.EOF)
		// in the same call — n bytes would be discarded by the consumer.
		// EOF must arrive on a subsequent (0, io.EOF) Read.
		if err != nil && n > 0 {
			t.Fatalf("coalescer.Read returned (%d, %v) — data-loss-prone; EOF must come on next call", n, err)
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

// TestCoalescer_BurstWindow_ResetsPerChunk verifies that the burst window
// timer resets each time a chunk arrives within the window — so a paste
// trickling in over a duration longer than the window is still coalesced
// into a single wrapped output (not split into multiple PasteMsgs).
func TestCoalescer_BurstWindow_ResetsPerChunk(t *testing.T) {
	t.Parallel()

	lg, records := newTestLogger(t)
	pr, pw := io.Pipe()
	// 20ms window, but feed chunks every 10ms for 60ms total — without the
	// reset fix, the first 20ms expiration would close the burst early.
	c := newCoalescer(pr, 20*time.Millisecond, lg)
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

	wantPayloadLen := chunkCount * chunkSize // 240
	wantWrappedLen := wantPayloadLen + len(pasteStart) + len(pasteEnd)
	if len(got) != wantWrappedLen {
		t.Fatalf("expected single wrapped output of %d bytes (240 payload + markers), got %d bytes — burst window did not reset per chunk", wantWrappedLen, len(got))
	}
	if !bytes.HasPrefix(got, []byte(pasteStart)) || !bytes.HasSuffix(got, []byte(pasteEnd)) {
		t.Fatalf("expected exactly one paste wrap; got %q ... %q", got[:min(len(got), 8)], got[max(0, len(got)-8):])
	}

	// Count wrapped=true coalesce-reads — should be exactly one.
	recs := records()
	wrapped := 0
	for _, r := range recs {
		if r.Kind == "coalesce-read" && strings.Contains(r.Notes, "wrapped=true") {
			wrapped++
		}
	}
	if wrapped != 1 {
		t.Fatalf("expected exactly 1 wrapped coalesce-read record (single paste), got %d; records=%+v", wrapped, recs)
	}
}

// TestCoalescer_Close_UnblocksReadLoop verifies that Close() interrupts the
// background read goroutine even when src.Read is blocked on a real OS pipe.
// Pre-fix the readLoop would leak until the underlying reader returned on
// its own (process exit). Uses os.Pipe() — a *os.File backed pipe — so the
// cancellation path mirrors how os.Stdin would be cancelled in production.
func TestCoalescer_Close_UnblocksReadLoop(t *testing.T) {
	t.Parallel()

	lg, _ := newTestLogger(t)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pw.Close() })
	c := newCoalescer(pr, 5*time.Millisecond, lg)

	// Give the readLoop time to enter the blocking Read on the empty pipe.
	time.Sleep(50 * time.Millisecond)

	// trackedReader wrapped pr; expose exit signal via a goroutine count
	// approximation: after Close, repeatedly call c.Read and assert EOF
	// arrives promptly. With the bug, the readLoop holds pr open and
	// blocks forever; Close alone doesn't interrupt it.
	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close() }()
	select {
	case <-closeDone:
	case <-time.After(1 * time.Second):
		t.Fatalf("coalescer.Close() did not return within 1s")
	}

	// After Close, the readLoop should have exited — verify by issuing a
	// Read that must complete promptly (returning EOF / canceled error).
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

// pasteWatcherModel is a minimal tea.Model used to assert that a tea.PasteMsg
// reaches the program when bytes flow through the coalescer. Captures the
// first PasteMsg seen, then quits.
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

// TestCoalescer_PasteMsgEmittedThroughBubbleTea is the integration test
// ghost called out: pipe a burst through tea.NewProgram(m, tea.WithInput(coal))
// and assert a tea.PasteMsg arrives end-to-end. This single test catches
// BOTH the (n>0, io.EOF) data-loss bug AND any deadlock that prevents
// shutdown from completing.
func TestCoalescer_PasteMsgEmittedThroughBubbleTea(t *testing.T) {
	t.Parallel()

	lg, _ := newTestLogger(t)
	pr, pw := io.Pipe()
	coal := newCoalescer(pr, 10*time.Millisecond, lg)
	defer coal.Close()

	m := &pasteWatcherModel{gotCh: make(chan struct{})}

	p := tea.NewProgram(
		m,
		tea.WithInput(coal),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	)

	// Drive bytes in parallel with p.Run.
	payload := bytes.Repeat([]byte("a"), 200)
	go func() {
		// Small delay to let program init.
		time.Sleep(30 * time.Millisecond)
		_, _ = pw.Write(payload)
		// Hold the pipe open just long enough for the burst window to
		// close and the PasteMsg to be dispatched before EOF arrives.
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
		// success — PasteMsg observed.
	case <-time.After(3 * time.Second):
		p.Quit()
		t.Fatalf("no tea.PasteMsg observed within 3s — coalescer payload was dropped")
	}

	// Ensure the program shuts down cleanly (catches deadlocks).
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			t.Fatalf("tea.Program.Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("tea.Program did not shut down within 2s after PasteMsg + EOF")
	}

	// Verify the paste payload reached the model intact.
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

	lg, records := newTestLogger(t)
	pr, pw := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)
	defer c.Close()

	go func() {
		_, _ = pw.Write(bytes.Repeat([]byte("x"), 100))
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	_ = readAllAvailable(t, c, 4096, 300*time.Millisecond)

	recs := records()
	var found *debugRecord
	for i := range recs {
		if recs[i].Kind == "coalesce-read" {
			found = &recs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected at least one coalesce-read record; got: %+v", recs)
	}
	for _, field := range []string{"bytes_in=", "bytes_out=", "wrapped=", "window_ms=", "duration_ns="} {
		if !strings.Contains(found.Notes, field) {
			t.Fatalf("coalesce-read Notes missing %q field; Notes=%q", field, found.Notes)
		}
	}
}

func TestCoalescer_SmallReadBuffer_DeliversFullPayloadAcrossCalls(t *testing.T) {
	t.Parallel()

	lg, _ := newTestLogger(t)
	pr, pw := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)
	defer c.Close()

	payload := bytes.Repeat([]byte("z"), 200)
	go func() {
		_, _ = pw.Write(payload)
		time.Sleep(30 * time.Millisecond)
		_ = pw.Close()
	}()

	// Use a tiny 50-byte buffer; we should still receive all 212 wrapped bytes
	// across multiple Read calls.
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

	lg, _ := newTestLogger(t)
	pr, _ := io.Pipe()
	c := newCoalescer(pr, 10*time.Millisecond, lg)

	if err := c.Close(); err != nil {
		t.Fatalf("first Close returned error: %v", err)
	}
	// Second close must not panic and must not return a "fatal" error.
	// io.ErrClosedPipe / nil are both acceptable.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Close panicked: %v", r)
		}
	}()
	_ = c.Close()
}

// hasCoalesceRead returns true if any record has Kind=="coalesce-read" and
// Notes containing the given substring.
func hasCoalesceRead(recs []debugRecord, noteSubstr string) bool {
	for _, r := range recs {
		if r.Kind == "coalesce-read" && strings.Contains(r.Notes, noteSubstr) {
			return true
		}
	}
	return false
}
