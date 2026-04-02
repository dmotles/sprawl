package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeCWDForClaude_BasicPath(t *testing.T) {
	got := EncodeCWDForClaude("/home/user/project")
	want := "-home-user-project"
	if got != want {
		t.Errorf("EncodeCWDForClaude(%q) = %q, want %q", "/home/user/project", got, want)
	}
}

func TestEncodeCWDForClaude_DotsInPath(t *testing.T) {
	got := EncodeCWDForClaude("/home/user/.config")
	want := "-home-user--config"
	if got != want {
		t.Errorf("EncodeCWDForClaude(%q) = %q, want %q", "/home/user/.config", got, want)
	}
}

func TestEncodeCWDForClaude_DendraWorktree(t *testing.T) {
	got := EncodeCWDForClaude("/home/coder/dendra/.dendra/worktrees/oak")
	want := "-home-coder-dendra--dendra-worktrees-oak"
	if got != want {
		t.Errorf("EncodeCWDForClaude(%q) = %q, want %q", "/home/coder/dendra/.dendra/worktrees/oak", got, want)
	}
}

func TestSessionLogPath(t *testing.T) {
	got := SessionLogPath("/home/user", "/home/user/project", "abc-123")
	want := filepath.Join("/home/user", ".claude", "projects", "-home-user-project", "abc-123.jsonl")
	if got != want {
		t.Errorf("SessionLogPath() = %q, want %q", got, want)
	}
}

func TestReadSessionLog_ValidJSONL(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"Hello world"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 100, 1<<20)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if !strings.Contains(result, "Hello world") {
		t.Errorf("expected output to contain 'Hello world', got: %q", result)
	}
	if !strings.Contains(result, "Hi there") {
		t.Errorf("expected output to contain 'Hi there', got: %q", result)
	}
}

func TestReadSessionLog_MissingFile(t *testing.T) {
	result, err := ReadSessionLog("/nonexistent/path/file.jsonl", 100, 1<<20)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for missing file, got: %q", result)
	}
}

func TestReadSessionLog_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "empty.jsonl")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 100, 1<<20)
	if err != nil {
		t.Fatalf("expected nil error for empty file, got: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for empty file, got: %q", result)
	}
}

func TestReadSessionLog_MaxMessageTruncation(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "many.jsonl")

	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"msg-%04d"}}`, i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 5, 1<<20)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}

	// The last 5 messages should be present (messages 0095-0099)
	for i := 95; i < 100; i++ {
		needle := fmt.Sprintf("msg-%04d", i)
		if !strings.Contains(result, needle) {
			t.Errorf("expected output to contain %q", needle)
		}
	}
	// Earlier messages should NOT be present
	for i := 0; i < 90; i++ {
		needle := fmt.Sprintf("msg-%04d", i)
		if strings.Contains(result, needle) {
			t.Errorf("expected output NOT to contain %q (should be truncated)", needle)
		}
	}
}

func TestReadSessionLog_MaxByteTruncation(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.jsonl")

	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"message-number-%04d-with-padding"}}`, i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 1000, 200)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}

	if len(result) > 200 {
		t.Errorf("expected output to be at most 200 bytes, got %d", len(result))
	}
}

func TestReadSessionLog_MalformedLines(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "mixed.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"valid message"}}`,
		`not valid json at all`,
		`{"broken":`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"also valid"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 100, 1<<20)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if !strings.Contains(result, "valid message") {
		t.Errorf("expected output to contain 'valid message', got: %q", result)
	}
	if !strings.Contains(result, "also valid") {
		t.Errorf("expected output to contain 'also valid', got: %q", result)
	}
}

func TestReadSessionLog_ToolUseBlocks(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "tools.jsonl")

	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"path":"/foo"}}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 100, 1<<20)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if !strings.Contains(result, "[tool: Read]") {
		t.Errorf("expected output to contain '[tool: Read]' marker, got: %q", result)
	}
}

func TestReadSessionLog_QueueOperationSkipped(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "queue.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"real message"}}`,
		`{"type":"queue-operation","data":{"op":"enqueue"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"response"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 100, 1<<20)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if strings.Contains(result, "queue-operation") || strings.Contains(result, "enqueue") {
		t.Errorf("expected queue-operation lines to be excluded, got: %q", result)
	}
	if !strings.Contains(result, "real message") {
		t.Errorf("expected output to contain 'real message', got: %q", result)
	}
	if !strings.Contains(result, "response") {
		t.Errorf("expected output to contain 'response', got: %q", result)
	}
}

func TestReadSessionLog_UserContentString(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "string-content.jsonl")

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"plain string content"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadSessionLog(path, 100, 1<<20)
	if err != nil {
		t.Fatalf("ReadSessionLog: %v", err)
	}
	if !strings.Contains(result, "plain string content") {
		t.Errorf("expected output to contain 'plain string content', got: %q", result)
	}
}

func TestHasSessionSummary_Exists(t *testing.T) {
	root := t.TempDir()
	sessionID := "test-session-abc"

	session := Session{
		SessionID:    sessionID,
		Timestamp:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		Handoff:      false,
		AgentsActive: []string{},
	}
	if err := WriteSessionSummary(root, session, "summary body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	exists, err := HasSessionSummary(root, sessionID)
	if err != nil {
		t.Fatalf("HasSessionSummary: %v", err)
	}
	if !exists {
		t.Error("expected HasSessionSummary to return true after writing summary")
	}
}

func TestHasSessionSummary_NotExists(t *testing.T) {
	root := t.TempDir()
	// Create sessions dir but leave it empty
	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	exists, err := HasSessionSummary(root, "nonexistent-session")
	if err != nil {
		t.Fatalf("HasSessionSummary: %v", err)
	}
	if exists {
		t.Error("expected HasSessionSummary to return false for nonexistent session")
	}
}

func TestHasSessionSummary_NoDirYet(t *testing.T) {
	root := t.TempDir()
	// Don't create sessions dir at all

	exists, err := HasSessionSummary(root, "no-dir-session")
	if err != nil {
		t.Fatalf("HasSessionSummary: %v", err)
	}
	if exists {
		t.Error("expected HasSessionSummary to return false when sessions dir does not exist")
	}
}

// mockInvoker is a test double for ClaudeInvoker.
type mockInvoker struct {
	response string
	err      error
	called   bool
	prompt   string
}

func (m *mockInvoker) Invoke(_ context.Context, prompt string, opts ...InvokeOption) (string, error) {
	m.called = true
	m.prompt = prompt
	return m.response, m.err
}

func TestAutoSummarize_AlreadyHasSummary(t *testing.T) {
	root := t.TempDir()
	sessionID := "already-summarized"
	homeDir := t.TempDir()

	// Pre-write a summary so HasSessionSummary returns true
	session := Session{
		SessionID:    sessionID,
		Timestamp:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		Handoff:      false,
		AgentsActive: []string{},
	}
	if err := WriteSessionSummary(root, session, "existing summary"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	invoker := &mockInvoker{response: "should not be called"}
	summarized, err := AutoSummarize(context.Background(), root, root, homeDir, sessionID, invoker)
	if err != nil {
		t.Fatalf("AutoSummarize: %v", err)
	}
	if summarized {
		t.Error("expected AutoSummarize to return false when summary already exists")
	}
	if invoker.called {
		t.Error("expected invoker NOT to be called when summary already exists")
	}
}

func TestAutoSummarize_NoJSONLFile(t *testing.T) {
	root := t.TempDir()
	homeDir := t.TempDir()
	sessionID := "no-jsonl-session"

	invoker := &mockInvoker{response: "should not be called"}
	summarized, err := AutoSummarize(context.Background(), root, root, homeDir, sessionID, invoker)
	if err != nil {
		t.Fatalf("AutoSummarize: %v", err)
	}
	if summarized {
		t.Error("expected AutoSummarize to return false when no JSONL file exists")
	}
	if invoker.called {
		t.Error("expected invoker NOT to be called when no JSONL file exists")
	}
}

func TestAutoSummarize_Success(t *testing.T) {
	root := t.TempDir()
	homeDir := t.TempDir()
	sessionID := "success-session"
	cwd := root

	// Write a JSONL file at the expected path
	encodedCWD := EncodeCWDForClaude(cwd)
	jsonlDir := filepath.Join(homeDir, ".claude", "projects", encodedCWD)
	if err := os.MkdirAll(jsonlDir, 0755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(jsonlDir, sessionID+".jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"What is dendra?"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Dendra is a multi-agent system."}]}}`,
	}
	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	invoker := &mockInvoker{response: "Summary text from Claude"}
	summarized, err := AutoSummarize(context.Background(), root, cwd, homeDir, sessionID, invoker)
	if err != nil {
		t.Fatalf("AutoSummarize: %v", err)
	}
	if !summarized {
		t.Error("expected AutoSummarize to return true on success")
	}
	if !invoker.called {
		t.Fatal("expected invoker to be called")
	}

	// Verify summary file was written with handoff:false
	exists, err := HasSessionSummary(root, sessionID)
	if err != nil {
		t.Fatalf("HasSessionSummary: %v", err)
	}
	if !exists {
		t.Error("expected summary file to be written")
	}

	// Read the summary and verify contents
	sessDir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), sessionID) {
			path := filepath.Join(sessDir, e.Name())
			s, body, readErr := ReadSessionSummary(path)
			if readErr != nil {
				t.Fatalf("ReadSessionSummary: %v", readErr)
			}
			if s.Handoff {
				t.Error("expected handoff to be false in summary")
			}
			if !strings.Contains(body, "Summary text from Claude") {
				t.Errorf("expected body to contain 'Summary text from Claude', got: %q", body)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("did not find summary file matching session ID")
	}
}

func TestAutoSummarize_InvokerError(t *testing.T) {
	root := t.TempDir()
	homeDir := t.TempDir()
	sessionID := "invoker-error-session"
	cwd := root

	// Write a JSONL file at the expected path
	encodedCWD := EncodeCWDForClaude(cwd)
	jsonlDir := filepath.Join(homeDir, ".claude", "projects", encodedCWD)
	if err := os.MkdirAll(jsonlDir, 0755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(jsonlDir, sessionID+".jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"Hello"}}`,
	}
	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	invoker := &mockInvoker{err: fmt.Errorf("claude invocation failed")}
	summarized, err := AutoSummarize(context.Background(), root, cwd, homeDir, sessionID, invoker)
	if err == nil {
		t.Fatal("expected error from AutoSummarize when invoker fails")
	}
	if summarized {
		t.Error("expected AutoSummarize to return false on invoker error")
	}

	// Verify no summary file was written
	exists, checkErr := HasSessionSummary(root, sessionID)
	if checkErr != nil {
		t.Fatalf("HasSessionSummary: %v", checkErr)
	}
	if exists {
		t.Error("expected no summary file to be written on invoker error")
	}
}

func TestAutoSummarize_EmptyTranscript(t *testing.T) {
	root := t.TempDir()
	homeDir := t.TempDir()
	sessionID := "empty-transcript-session"
	cwd := root

	// Write a JSONL file with only queue-operation lines
	encodedCWD := EncodeCWDForClaude(cwd)
	jsonlDir := filepath.Join(homeDir, ".claude", "projects", encodedCWD)
	if err := os.MkdirAll(jsonlDir, 0755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(jsonlDir, sessionID+".jsonl")
	lines := []string{
		`{"type":"queue-operation","data":{"op":"enqueue"}}`,
		`{"type":"queue-operation","data":{"op":"dequeue"}}`,
	}
	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	invoker := &mockInvoker{response: "should not be called"}
	summarized, err := AutoSummarize(context.Background(), root, cwd, homeDir, sessionID, invoker)
	if err != nil {
		t.Fatalf("AutoSummarize: %v", err)
	}
	if summarized {
		t.Error("expected AutoSummarize to return false for empty transcript")
	}
	if invoker.called {
		t.Error("expected invoker NOT to be called for empty transcript")
	}
}
