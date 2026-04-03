package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPersistentPrompt(t *testing.T) {
	existing := "- Existing item 1\n- Existing item 2"
	session := "Session did X and Y"
	timeline := "- 2026-01-01T00:00:00Z: Timeline event"

	prompt := buildPersistentPrompt(existing, session, timeline, 20)

	if !strings.Contains(prompt, "Existing item 1") {
		t.Error("prompt should contain existing knowledge item 1")
	}
	if !strings.Contains(prompt, "Existing item 2") {
		t.Error("prompt should contain existing knowledge item 2")
	}
	if !strings.Contains(prompt, "Session did X and Y") {
		t.Error("prompt should contain session summary")
	}
	if !strings.Contains(prompt, "Timeline event") {
		t.Error("prompt should contain timeline bullets")
	}
	if !strings.Contains(prompt, "- ") {
		t.Error("prompt should describe bullet format")
	}
}

func TestBuildPersistentPrompt_NoExistingKnowledge(t *testing.T) {
	prompt := buildPersistentPrompt("", "Session summary here", "- 2026-01-01T00:00:00Z: event", 20)

	if !strings.Contains(prompt, "Session summary here") {
		t.Error("prompt should contain session summary")
	}
	if !strings.Contains(prompt, "event") {
		t.Error("prompt should contain timeline context")
	}
	if strings.Contains(prompt, "<nil>") {
		t.Error("prompt should not contain <nil>")
	}
	// Should indicate no existing knowledge
	lower := strings.ToLower(prompt)
	if !strings.Contains(lower, "no existing") && !strings.Contains(lower, "no persistent") {
		t.Error("prompt should indicate no existing knowledge")
	}
}

func TestBuildPersistentPrompt_NoTimeline(t *testing.T) {
	prompt := buildPersistentPrompt("- Existing item", "Session summary", "", 20)

	if !strings.Contains(prompt, "Existing item") {
		t.Error("prompt should contain existing knowledge")
	}
	if !strings.Contains(prompt, "Session summary") {
		t.Error("prompt should contain session summary")
	}
}

func TestParsePersistentOutput(t *testing.T) {
	raw := "- Item one\n- Item two\ngarbage\n\n- Item three"
	items := parsePersistentOutput(raw)

	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0] != "Item one" {
		t.Errorf("items[0] = %q, want %q", items[0], "Item one")
	}
	if items[1] != "Item two" {
		t.Errorf("items[1] = %q, want %q", items[1], "Item two")
	}
	if items[2] != "Item three" {
		t.Errorf("items[2] = %q, want %q", items[2], "Item three")
	}
}

func TestParsePersistentOutput_EmptyString(t *testing.T) {
	items := parsePersistentOutput("")
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestParsePersistentOutput_NoBullets(t *testing.T) {
	items := parsePersistentOutput("just text\nno bullets here\nnothing")
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestParsePersistentOutput_WhitespaceHandling(t *testing.T) {
	raw := "  - Indented item\n- Normal item\n   \n- Another item"
	items := parsePersistentOutput(raw)

	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if items[0] != "Indented item" {
		t.Errorf("items[0] = %q, want %q", items[0], "Indented item")
	}
}

func TestReadPersistentKnowledge_NoFile(t *testing.T) {
	root := t.TempDir()
	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestReadPersistentKnowledge_ExistingFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".dendra", "memory")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	knowledgeContent := "- Item A\n- Item B\n"
	if err := os.WriteFile(filepath.Join(dir, "persistent.md"), []byte(knowledgeContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != knowledgeContent {
		t.Errorf("got %q, want %q", content, knowledgeContent)
	}
}

func TestWriteReadRoundtrip(t *testing.T) {
	root := t.TempDir()
	items := []string{"Alpha", "Beta", "Gamma"}

	if err := writePersistentKnowledge(root, items); err != nil {
		t.Fatalf("writePersistentKnowledge: %v", err)
	}

	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}

	if !strings.Contains(content, "- Alpha") {
		t.Error("content should contain '- Alpha'")
	}
	if !strings.Contains(content, "- Beta") {
		t.Error("content should contain '- Beta'")
	}
	if !strings.Contains(content, "- Gamma") {
		t.Error("content should contain '- Gamma'")
	}
	// writePersistentKnowledge should NOT include a header — renderPersistentKnowledge adds one.
	if strings.Contains(content, "# Persistent Knowledge") {
		t.Error("file content should NOT contain a '# Persistent Knowledge' header")
	}
	// File should start directly with the first bullet
	if !strings.HasPrefix(content, "- Alpha") {
		t.Errorf("file should start with first bullet, got prefix: %q", content[:min(len(content), 40)])
	}
}

func TestUpdatePersistentKnowledge_FirstRun(t *testing.T) {
	root := t.TempDir()
	mock := &mockClaudeInvoker{response: "- Knowledge item A\n- Knowledge item B"}

	err := UpdatePersistentKnowledge(context.Background(), root, mock, nil, "session summary text", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify invoker was called with prompt containing session summary
	if !strings.Contains(mock.lastPrompt, "session summary text") {
		t.Error("prompt should contain session summary")
	}

	// Verify persistent.md was created
	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}
	if !strings.Contains(content, "Knowledge item A") {
		t.Error("persistent.md should contain 'Knowledge item A'")
	}
	if !strings.Contains(content, "Knowledge item B") {
		t.Error("persistent.md should contain 'Knowledge item B'")
	}
}

func TestUpdatePersistentKnowledge_WithExistingKnowledge(t *testing.T) {
	root := t.TempDir()

	// Pre-create persistent.md
	if err := writePersistentKnowledge(root, []string{"Old fact one", "Old fact two"}); err != nil {
		t.Fatalf("writePersistentKnowledge: %v", err)
	}

	mock := &mockClaudeInvoker{response: "- Updated fact one\n- Updated fact two\n- New fact three"}

	err := UpdatePersistentKnowledge(context.Background(), root, mock, nil, "new session", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify prompt included existing knowledge
	if !strings.Contains(mock.lastPrompt, "Old fact one") {
		t.Error("prompt should contain existing knowledge items")
	}

	// Verify persistent.md was updated with new items
	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}
	if !strings.Contains(content, "Updated fact one") {
		t.Error("persistent.md should contain 'Updated fact one'")
	}
	if !strings.Contains(content, "New fact three") {
		t.Error("persistent.md should contain 'New fact three'")
	}
}

func TestUpdatePersistentKnowledge_WithTimelineContext(t *testing.T) {
	root := t.TempDir()
	timeline := "- 2026-01-01T00:00:00Z: Did something\n- 2026-01-02T00:00:00Z: Did another thing"
	mock := &mockClaudeInvoker{response: "- Knowledge item"}

	err := UpdatePersistentKnowledge(context.Background(), root, mock, nil, "session", timeline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(mock.lastPrompt, "Did something") {
		t.Error("prompt should contain first timeline bullet")
	}
	if !strings.Contains(mock.lastPrompt, "Did another thing") {
		t.Error("prompt should contain second timeline bullet")
	}
}

func TestUpdatePersistentKnowledge_ItemCapEnforcement(t *testing.T) {
	root := t.TempDir()

	// Generate 25 items in the mock response
	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, fmt.Sprintf("- Item number %d", i))
	}
	mock := &mockClaudeInvoker{response: strings.Join(lines, "\n")}

	cfg := &PersistentKnowledgeConfig{MaxItems: 20, MaxSizeChars: 50000}
	err := UpdatePersistentKnowledge(context.Background(), root, mock, cfg, "session", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and count items
	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}

	itemCount := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			itemCount++
		}
	}
	if itemCount != 20 {
		t.Errorf("got %d items, want 20 (capped)", itemCount)
	}

	// First 20 items should be kept (deterministic truncation)
	if !strings.Contains(content, "Item number 1") {
		t.Error("should keep first item")
	}
	if !strings.Contains(content, "Item number 20") {
		t.Error("should keep item 20")
	}
	if strings.Contains(content, "Item number 21") {
		t.Error("should NOT contain item 21 (over cap)")
	}
}

func TestUpdatePersistentKnowledge_ClaudeError(t *testing.T) {
	root := t.TempDir()

	// Pre-create persistent.md
	if err := writePersistentKnowledge(root, []string{"Must survive"}); err != nil {
		t.Fatalf("writePersistentKnowledge: %v", err)
	}

	mock := &mockClaudeInvoker{err: fmt.Errorf("api unavailable")}

	err := UpdatePersistentKnowledge(context.Background(), root, mock, nil, "session", "")
	if err == nil {
		t.Fatal("expected error when invoker fails")
	}
	if !strings.Contains(err.Error(), "api unavailable") {
		t.Errorf("error should contain invoker error, got: %v", err)
	}

	// Verify persistent.md is unchanged
	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}
	if !strings.Contains(content, "Must survive") {
		t.Error("persistent.md should be unchanged after error")
	}
}

func TestUpdatePersistentKnowledge_EmptyResponse(t *testing.T) {
	root := t.TempDir()

	// Pre-create persistent.md
	if err := writePersistentKnowledge(root, []string{"Should stay"}); err != nil {
		t.Fatalf("writePersistentKnowledge: %v", err)
	}

	// Mock returns text but no bullet lines
	mock := &mockClaudeInvoker{response: "Some text but no bullet lines at all"}

	err := UpdatePersistentKnowledge(context.Background(), root, mock, nil, "session", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify persistent.md is unchanged (safety: don't destroy with empty)
	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}
	if !strings.Contains(content, "Should stay") {
		t.Error("persistent.md should be unchanged when AI returns no items")
	}
}

func TestUpdatePersistentKnowledge_NilConfig(t *testing.T) {
	root := t.TempDir()
	mock := &mockClaudeInvoker{response: "- Item one\n- Item two"}

	// cfg=nil should use defaults and not panic
	err := UpdatePersistentKnowledge(context.Background(), root, mock, nil, "session", "")
	if err != nil {
		t.Fatalf("unexpected error with nil config: %v", err)
	}

	content, err := ReadPersistentKnowledge(root)
	if err != nil {
		t.Fatalf("ReadPersistentKnowledge: %v", err)
	}
	if !strings.Contains(content, "Item one") {
		t.Error("persistent.md should contain items")
	}
}
