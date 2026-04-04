package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndReadSessionSummary(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID:    "abc-123",
		Timestamp:    time.Date(2026, 4, 1, 10, 30, 0, 0, time.UTC),
		Handoff:      true,
		AgentsActive: []string{"oak", "elm", "ash"},
	}
	body := "## Summary\n\nThis is a test session summary.\n"

	if err := WriteSessionSummary(root, session, body); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	// Find the written file
	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	path := filepath.Join(dir, entries[0].Name())
	gotSession, gotBody, err := ReadSessionSummary(path)
	if err != nil {
		t.Fatalf("ReadSessionSummary: %v", err)
	}

	if gotSession.SessionID != session.SessionID {
		t.Errorf("SessionID = %q, want %q", gotSession.SessionID, session.SessionID)
	}
	if !gotSession.Timestamp.Equal(session.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", gotSession.Timestamp, session.Timestamp)
	}
	if gotSession.Handoff != session.Handoff {
		t.Errorf("Handoff = %v, want %v", gotSession.Handoff, session.Handoff)
	}
	if len(gotSession.AgentsActive) != len(session.AgentsActive) {
		t.Fatalf("AgentsActive len = %d, want %d", len(gotSession.AgentsActive), len(session.AgentsActive))
	}
	for i, a := range session.AgentsActive {
		if gotSession.AgentsActive[i] != a {
			t.Errorf("AgentsActive[%d] = %q, want %q", i, gotSession.AgentsActive[i], a)
		}
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
}

func TestWriteSessionSummary_CreatesDirectories(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID: "dir-test",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	if err := WriteSessionSummary(root, session, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("sessions dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("sessions path is not a directory")
	}
}

func TestWriteSessionSummary_FilenameFormat(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID: "fmt-test-id",
		Timestamp: time.Date(2026, 4, 2, 15, 4, 5, 0, time.UTC),
	}

	if err := WriteSessionSummary(root, session, "test"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}

	expected := "fmt-test-id.md"
	if len(entries) != 1 || entries[0].Name() != expected {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("filename = %v, want [%s]", names, expected)
	}
}

func TestWriteSessionSummary_EmptyAgents(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID:    "empty-agents",
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AgentsActive: []string{},
	}

	if err := WriteSessionSummary(root, session, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	got, _, err := ReadSessionSummary(path)
	if err != nil {
		t.Fatalf("ReadSessionSummary: %v", err)
	}
	if len(got.AgentsActive) != 0 {
		t.Errorf("AgentsActive = %v, want empty", got.AgentsActive)
	}
}

func TestWriteSessionSummary_NilAgents(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID:    "nil-agents",
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AgentsActive: nil,
	}

	if err := WriteSessionSummary(root, session, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	got, _, err := ReadSessionSummary(path)
	if err != nil {
		t.Fatalf("ReadSessionSummary: %v", err)
	}
	if len(got.AgentsActive) != 0 {
		t.Errorf("AgentsActive = %v, want empty", got.AgentsActive)
	}
}

func TestReadSessionSummary_MalformedFrontmatter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bad.md")

	// No closing ---
	if err := os.WriteFile(path, []byte("---\nsession_id: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadSessionSummary(path)
	if err == nil {
		t.Error("expected error for missing closing ---")
	}
}

func TestReadSessionSummary_BadTimestamp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bad-ts.md")

	content := "---\nsession_id: x\ntimestamp: not-a-date\nhandoff: false\nagents_active: []\n---\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadSessionSummary(path)
	if err == nil {
		t.Error("expected error for bad timestamp")
	}
}

func TestReadSessionSummary_FileNotExist(t *testing.T) {
	_, _, err := ReadSessionSummary("/nonexistent/path/file.md")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadSessionSummary_NoOpeningDelimiter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "no-open.md")

	if err := os.WriteFile(path, []byte("just some text"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadSessionSummary(path)
	if err == nil {
		t.Error("expected error for missing opening ---")
	}
}

func TestListRecentSessions_DirNotExist(t *testing.T) {
	root := t.TempDir()
	sessions, bodies, err := ListRecentSessions(root, 5)
	if err != nil {
		t.Fatalf("ListRecentSessions: %v", err)
	}
	if sessions != nil {
		t.Errorf("sessions = %v, want nil", sessions)
	}
	if bodies != nil {
		t.Errorf("bodies = %v, want nil", bodies)
	}
}

func TestListRecentSessions_EmptyExistingDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions, bodies, err := ListRecentSessions(root, 5)
	if err != nil {
		t.Fatalf("ListRecentSessions: %v", err)
	}
	if sessions != nil {
		t.Errorf("sessions = %v, want nil", sessions)
	}
	if bodies != nil {
		t.Errorf("bodies = %v, want nil", bodies)
	}
}

func TestListRecentSessions_Multiple(t *testing.T) {
	root := t.TempDir()

	// Write 5 sessions with distinct timestamps
	for i := range 5 {
		s := Session{
			SessionID:    strings.Replace("sess-X", "X", string(rune('a'+i)), 1),
			Timestamp:    time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC),
			AgentsActive: []string{"oak"},
		}
		body := strings.Replace("body-X", "X", string(rune('a'+i)), 1)
		if err := WriteSessionSummary(root, s, body); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	sessions, bodies, err := ListRecentSessions(root, 3)
	if err != nil {
		t.Fatalf("ListRecentSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}
	if len(bodies) != 3 {
		t.Fatalf("got %d bodies, want 3", len(bodies))
	}

	// Should be the 3 most recent, oldest first (hours 2, 3, 4)
	for i, s := range sessions {
		expectedHour := i + 2
		if s.Timestamp.Hour() != expectedHour {
			t.Errorf("sessions[%d].Timestamp.Hour() = %d, want %d", i, s.Timestamp.Hour(), expectedHour)
		}
	}
}

func TestListRecentSessions_FewerThanN(t *testing.T) {
	root := t.TempDir()

	for i := range 2 {
		s := Session{
			SessionID: strings.Replace("sess-X", "X", string(rune('a'+i)), 1),
			Timestamp: time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC),
		}
		if err := WriteSessionSummary(root, s, "body"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	sessions, bodies, err := ListRecentSessions(root, 10)
	if err != nil {
		t.Fatalf("ListRecentSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("got %d sessions, want 2", len(sessions))
	}
	if len(bodies) != 2 {
		t.Errorf("got %d bodies, want 2", len(bodies))
	}
}

func TestReadLastSessionID_NotExists(t *testing.T) {
	root := t.TempDir()
	id, err := ReadLastSessionID(root)
	if err != nil {
		t.Fatalf("ReadLastSessionID: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

func TestWriteAndReadLastSessionID(t *testing.T) {
	root := t.TempDir()

	if err := WriteLastSessionID(root, "session-42"); err != nil {
		t.Fatalf("WriteLastSessionID: %v", err)
	}

	id, err := ReadLastSessionID(root)
	if err != nil {
		t.Fatalf("ReadLastSessionID: %v", err)
	}
	if id != "session-42" {
		t.Errorf("id = %q, want %q", id, "session-42")
	}
}

func TestWriteLastSessionID_Overwrite(t *testing.T) {
	root := t.TempDir()

	if err := WriteLastSessionID(root, "first"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteLastSessionID(root, "second"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	id, err := ReadLastSessionID(root)
	if err != nil {
		t.Fatalf("ReadLastSessionID: %v", err)
	}
	if id != "second" {
		t.Errorf("id = %q, want %q", id, "second")
	}
}

func TestWriteSessionSummary_NoTempFilesRemain(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID: "atomic-test",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	if err := WriteSessionSummary(root, session, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file remaining: %s", e.Name())
		}
	}
	// Should have exactly one .md file
	mdCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mdCount++
		}
	}
	if mdCount != 1 {
		t.Errorf("expected 1 .md file, got %d", mdCount)
	}
}

func TestWriteSessionSummary_FileContent(t *testing.T) {
	root := t.TempDir()
	session := Session{
		SessionID:    "content-check",
		Timestamp:    time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
		Handoff:      false,
		AgentsActive: []string{"oak", "elm"},
	}
	body := "Some summary text.\n"

	if err := WriteSessionSummary(root, session, body); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, _ := os.ReadDir(dir)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Verify it starts with ---
	if !strings.HasPrefix(content, "---\n") {
		t.Error("file does not start with ---")
	}
	// Verify it contains the session_id
	if !strings.Contains(content, "session_id: content-check") {
		t.Error("missing session_id in frontmatter")
	}
	// Verify it contains the body after the second ---
	parts := strings.SplitN(content, "---\n", 3)
	if len(parts) < 3 {
		t.Fatalf("expected 3 parts split by ---, got %d", len(parts))
	}
	gotBody := parts[2]
	// Body follows frontmatter with a single separating newline
	expectedBody := "\n" + body
	if gotBody != expectedBody {
		t.Errorf("body = %q, want %q", gotBody, expectedBody)
	}
}

func TestWriteHandoffSignal_CreatesFile(t *testing.T) {
	root := t.TempDir()

	if err := WriteHandoffSignal(root); err != nil {
		t.Fatalf("WriteHandoffSignal: %v", err)
	}

	path := filepath.Join(root, ".dendra", "memory", "handoff-signal")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		t.Fatal("handoff-signal file should exist")
	}
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("handoff-signal file should be empty, got size %d", info.Size())
	}
}

func TestWriteHandoffSignal_CreatesDirectory(t *testing.T) {
	root := t.TempDir()
	// Don't pre-create the memory directory
	if err := WriteHandoffSignal(root); err != nil {
		t.Fatalf("WriteHandoffSignal: %v", err)
	}

	path := filepath.Join(root, ".dendra", "memory", "handoff-signal")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("handoff-signal file should exist")
	}
}

func TestWriteSessionSummary_Idempotent(t *testing.T) {
	root := t.TempDir()

	// First write
	s1 := Session{
		SessionID:    "idem-test",
		Timestamp:    time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		AgentsActive: []string{"oak"},
	}
	if err := WriteSessionSummary(root, s1, "first body\n"); err != nil {
		t.Fatalf("first WriteSessionSummary: %v", err)
	}

	// Second write with same session ID but different timestamp and body
	s2 := Session{
		SessionID:    "idem-test",
		Timestamp:    time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC),
		AgentsActive: []string{"oak", "elm"},
	}
	if err := WriteSessionSummary(root, s2, "second body\n"); err != nil {
		t.Fatalf("second WriteSessionSummary: %v", err)
	}

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}

	// Count .md files — should be exactly 1
	var mdFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, e.Name())
		}
	}
	if len(mdFiles) != 1 {
		t.Fatalf("expected 1 .md file, got %d: %v", len(mdFiles), mdFiles)
	}
	if mdFiles[0] != "idem-test.md" {
		t.Errorf("filename = %q, want %q", mdFiles[0], "idem-test.md")
	}

	// Verify content reflects the second write
	path := filepath.Join(dir, mdFiles[0])
	gotSession, gotBody, err := ReadSessionSummary(path)
	if err != nil {
		t.Fatalf("ReadSessionSummary: %v", err)
	}
	if gotBody != "second body\n" {
		t.Errorf("body = %q, want %q", gotBody, "second body\n")
	}
	if !gotSession.Timestamp.Equal(s2.Timestamp) {
		t.Errorf("timestamp = %v, want %v", gotSession.Timestamp, s2.Timestamp)
	}
}

func TestWriteSessionSummary_CleansOldFormat(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-create a file in the old timestamp-prefixed format
	oldContent := "---\nsession_id: migrate-test\ntimestamp: 2026-01-01T00:00:00Z\nhandoff: false\nagents_active: []\n---\n\nold body\n"
	oldPath := filepath.Join(dir, "20260101T000000_migrate-test.md")
	if err := os.WriteFile(oldPath, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write with the same session ID
	session := Session{
		SessionID: "migrate-test",
		Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := WriteSessionSummary(root, session, "new body\n"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	// Old file should be gone
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old-format file should be removed, but still exists")
	}

	// New file should exist
	newPath := filepath.Join(dir, "migrate-test.md")
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("new-format file should exist: %v", err)
	}

	// Should be exactly 1 .md file
	entries, _ := os.ReadDir(dir)
	mdCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mdCount++
		}
	}
	if mdCount != 1 {
		t.Errorf("expected 1 .md file, got %d", mdCount)
	}

	// Verify content of the new file
	gotSession, gotBody, err := ReadSessionSummary(newPath)
	if err != nil {
		t.Fatalf("ReadSessionSummary: %v", err)
	}
	if gotBody != "new body\n" {
		t.Errorf("body = %q, want %q", gotBody, "new body\n")
	}
	if gotSession.SessionID != "migrate-test" {
		t.Errorf("SessionID = %q, want %q", gotSession.SessionID, "migrate-test")
	}
}

func TestListRecentSessions_SortsByTimestamp(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, ".dendra", "memory", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create files whose alphabetical name order differs from chronological order.
	// Alphabetically: aaa < mmm < zzz
	// Chronologically: zzz (Jan) < mmm (Feb) < aaa (Mar)
	type entry struct {
		filename  string
		sessionID string
		timestamp time.Time
	}
	entries := []entry{
		{"zzz-session.md", "zzz-session", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"aaa-session.md", "aaa-session", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"mmm-session.md", "mmm-session", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, e := range entries {
		s := Session{
			SessionID:    e.sessionID,
			Timestamp:    e.timestamp,
			AgentsActive: []string{},
		}
		content := marshalFrontmatter(s) + "\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, e.filename), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sessions, _, err := ListRecentSessions(root, 3)
	if err != nil {
		t.Fatalf("ListRecentSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}

	// Should be sorted oldest-first by timestamp: zzz (Jan), mmm (Feb), aaa (Mar)
	wantOrder := []string{"zzz-session", "mmm-session", "aaa-session"}
	for i, s := range sessions {
		if s.SessionID != wantOrder[i] {
			t.Errorf("sessions[%d].SessionID = %q, want %q", i, s.SessionID, wantOrder[i])
		}
	}
}
