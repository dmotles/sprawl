package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session holds metadata for a session summary.
type Session struct {
	SessionID    string    `json:"session_id"`
	Timestamp    time.Time `json:"timestamp"`
	Handoff      bool      `json:"handoff"`
	AgentsActive []string  `json:"agents_active"`
}

func sessionsDir(dendraRoot string) string {
	return filepath.Join(memoryDir(dendraRoot), "sessions")
}

func memoryDir(dendraRoot string) string {
	return filepath.Join(dendraRoot, ".dendra", "memory")
}

func lastSessionIDPath(dendraRoot string) string {
	return filepath.Join(memoryDir(dendraRoot), "last-session-id")
}

func sessionFilename(s Session) string {
	return fmt.Sprintf("%s.md", s.SessionID)
}

func marshalFrontmatter(s Session) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "session_id: %s\n", s.SessionID)
	fmt.Fprintf(&b, "timestamp: %s\n", s.Timestamp.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "handoff: %t\n", s.Handoff)
	if len(s.AgentsActive) == 0 {
		b.WriteString("agents_active: []\n")
	} else {
		b.WriteString("agents_active:\n")
		for _, a := range s.AgentsActive {
			fmt.Fprintf(&b, "  - %s\n", a)
		}
	}
	b.WriteString("---\n")
	return b.String()
}

func parseFrontmatter(raw string) (Session, string, error) {
	// Must start with "---\n"
	if !strings.HasPrefix(raw, "---\n") {
		return Session{}, "", fmt.Errorf("missing opening frontmatter delimiter")
	}

	// Find closing "---\n" after the opening
	rest := raw[4:] // skip opening "---\n"
	idx := strings.Index(rest, "---\n")
	if idx < 0 {
		// Also accept "---" at EOF with no trailing newline
		if strings.HasSuffix(rest, "---") {
			idx = len(rest) - 3
		} else {
			return Session{}, "", fmt.Errorf("missing closing frontmatter delimiter")
		}
	}

	frontmatter := rest[:idx]
	body := rest[idx+4:] // after "---\n"
	// Strip one leading newline from body if present
	body = strings.TrimPrefix(body, "\n")

	var s Session
	var inAgents bool

	for _, line := range strings.Split(strings.TrimRight(frontmatter, "\n"), "\n") {
		if inAgents {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				s.AgentsActive = append(s.AgentsActive, strings.TrimPrefix(trimmed, "- "))
				continue
			}
			inAgents = false
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "session_id":
			s.SessionID = value
		case "timestamp":
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return Session{}, "", fmt.Errorf("parsing timestamp: %w", err)
			}
			s.Timestamp = t
		case "handoff":
			s.Handoff = value == "true"
		case "agents_active":
			if value == "[]" {
				s.AgentsActive = []string{}
			} else {
				inAgents = true
				s.AgentsActive = nil
			}
		}
	}

	// Normalize nil agents to empty slice for round-trip consistency
	if s.AgentsActive == nil {
		s.AgentsActive = []string{}
	}

	return s, body, nil
}

// WriteSessionSummary writes a session summary file with YAML frontmatter and markdown body.
// It uses write-to-temp-then-rename for atomicity.
func WriteSessionSummary(dendraRoot string, session Session, body string) error {
	dir := sessionsDir(dendraRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}

	content := marshalFrontmatter(session) + "\n" + body
	finalPath := filepath.Join(dir, sessionFilename(session))

	// Clean up any old-format files (timestamp-prefixed) for this session ID.
	oldPattern := filepath.Join(dir, "*_"+session.SessionID+".md")
	matches, _ := filepath.Glob(oldPattern)
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing old-format session file: %w", err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".tmp-session-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}

	success = true
	return nil
}

// ReadSessionSummary parses a session summary file, returning metadata and body.
func ReadSessionSummary(path string) (Session, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}, "", fmt.Errorf("reading session file: %w", err)
	}
	return parseFrontmatter(string(data))
}

// ListRecentSessions returns the N most recent sessions sorted oldest first.
// Returns (nil, nil, nil) if the sessions directory does not exist or is empty.
func ListRecentSessions(dendraRoot string, n int) ([]Session, []string, error) {
	dir := sessionsDir(dendraRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("listing sessions directory: %w", err)
	}

	// Filter to .md files only
	var mdEntries []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdEntries = append(mdEntries, e)
		}
	}

	if len(mdEntries) == 0 {
		return nil, nil, nil
	}

	// Parse all session files
	type sessionEntry struct {
		session Session
		body    string
	}
	var all []sessionEntry
	for _, e := range mdEntries {
		path := filepath.Join(dir, e.Name())
		s, body, err := ReadSessionSummary(path)
		if err != nil {
			return nil, nil, fmt.Errorf("reading session %s: %w", e.Name(), err)
		}
		all = append(all, sessionEntry{session: s, body: body})
	}

	// Sort by timestamp ascending (oldest first).
	// SliceStable for consistency with consolidate.go.
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].session.Timestamp.Before(all[j].session.Timestamp)
	})

	// Take the last N entries (most recent)
	start := 0
	if len(all) > n {
		start = len(all) - n
	}
	all = all[start:]

	sessions := make([]Session, len(all))
	bodies := make([]string, len(all))
	for i, e := range all {
		sessions[i] = e.session
		bodies[i] = e.body
	}

	return sessions, bodies, nil
}

// ReadLastSessionID reads the last session ID from .dendra/memory/last-session-id.
// Returns ("", nil) if the file does not exist.
func ReadLastSessionID(dendraRoot string) (string, error) {
	data, err := os.ReadFile(lastSessionIDPath(dendraRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading last session ID: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteLastSessionID writes the session ID to .dendra/memory/last-session-id.
func WriteLastSessionID(dendraRoot string, sessionID string) error {
	dir := memoryDir(dendraRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating memory directory: %w", err)
	}
	if err := os.WriteFile(lastSessionIDPath(dendraRoot), []byte(sessionID), 0644); err != nil {
		return fmt.Errorf("writing last session ID: %w", err)
	}
	return nil
}

// WriteHandoffSignal creates an empty handoff signal file at .dendra/memory/handoff-signal.
// The sensei loop detects the presence of this file and restarts.
func WriteHandoffSignal(dendraRoot string) error {
	dir := memoryDir(dendraRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating memory directory: %w", err)
	}
	path := filepath.Join(dir, "handoff-signal")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		return fmt.Errorf("writing handoff signal: %w", err)
	}
	return nil
}
