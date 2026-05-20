// THROWAWAY DIAGNOSTIC TESTS: QUM-608. Delete alongside cmd/input_debug.go.

package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInputDebugCmd_Registered(t *testing.T) {
	t.Parallel()
	var found *cobraCmdRef
	for _, c := range rootCmd.Commands() {
		if c.Use == "input-debug" {
			found = &cobraCmdRef{hidden: c.Hidden}
			break
		}
	}
	if found == nil {
		t.Fatal("input-debug subcommand not registered on rootCmd")
	}
	if !found.hidden {
		t.Error("input-debug must be Hidden: true")
	}
}

type cobraCmdRef struct{ hidden bool }

func TestInputDebugCmd_HiddenFromHelp(t *testing.T) {
	t.Parallel()
	usage := rootCmd.UsageString()
	if strings.Contains(usage, "input-debug") {
		t.Errorf("input-debug appeared in sprawl --help usage output:\n%s", usage)
	}
}

func TestDebugLogger_WritesJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	lg, err := newDebugLogger(path)
	if err != nil {
		t.Fatalf("newDebugLogger: %v", err)
	}
	lg.write(debugRecord{Kind: "msg", MsgType: "tea.KeyPressMsg", Content: "a"})
	lg.write(debugRecord{Kind: "view", ViewNs: 1234})
	if err := lg.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var recs []debugRecord
	for sc.Scan() {
		var r debugRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("parse line %q: %v", sc.Text(), err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].Kind != "msg" || recs[0].MsgType != "tea.KeyPressMsg" || recs[0].Content != "a" {
		t.Errorf("record 0 unexpected: %+v", recs[0])
	}
	if recs[1].Kind != "view" || recs[1].ViewNs != 1234 {
		t.Errorf("record 1 unexpected: %+v", recs[1])
	}
	if recs[1].TsNs <= 0 || recs[1].DeltaNs < 0 {
		t.Errorf("expected ts_ns > 0 and delta_ns >= 0, got ts=%d delta=%d", recs[1].TsNs, recs[1].DeltaNs)
	}
}

func TestInputDebugModel_LogsMsgWithTiming(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	lg, err := newDebugLogger(path)
	if err != nil {
		t.Fatalf("newDebugLogger: %v", err)
	}
	m := newInputDebugModel(lg)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a'})
	_ = updated.View()

	if err := lg.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("want at least 2 records (msg + view), got %d:\n%s", len(lines), string(data))
	}
	var msgRec debugRecord
	if err := json.Unmarshal([]byte(lines[0]), &msgRec); err != nil {
		t.Fatalf("parse msg record: %v", err)
	}
	if msgRec.Kind != "msg" {
		t.Errorf("first record kind=%q, want msg", msgRec.Kind)
	}
	if !strings.Contains(msgRec.MsgType, "KeyPressMsg") {
		t.Errorf("msg_type=%q, want contain KeyPressMsg", msgRec.MsgType)
	}
	if msgRec.UpdateNs <= 0 {
		t.Errorf("update_ns=%d, want > 0", msgRec.UpdateNs)
	}

	var viewRec debugRecord
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &viewRec); err != nil {
		t.Fatalf("parse view record: %v", err)
	}
	if viewRec.Kind != "view" {
		t.Errorf("last record kind=%q, want view", viewRec.Kind)
	}
	if viewRec.ViewNs <= 0 {
		t.Errorf("view_ns=%d, want > 0", viewRec.ViewNs)
	}
}

func TestInputDebugModel_CtrlCQuits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lg, err := newDebugLogger(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		t.Fatalf("newDebugLogger: %v", err)
	}
	defer lg.close()
	m := newInputDebugModel(lg)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+C must return a quit cmd")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("Ctrl+C cmd returned %T, want tea.QuitMsg", msg)
	}
}

func TestInputDebugModel_TruncatesLongContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	lg, _ := newDebugLogger(path)
	m := newInputDebugModel(lg)

	// Construct an artificial msg whose %+v rendering is huge.
	big := strings.Repeat("x", 500)
	m.Update(tea.WindowSizeMsg{Width: len(big), Height: 1})
	// Force a long content via a synthetic msg.
	m.Update(syntheticBigMsg{payload: big})
	lg.close()

	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r debugRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if strings.Contains(r.MsgType, "syntheticBigMsg") {
			// Truncated to ~120 chars; allow for a small suffix marker.
			if len(r.Content) > defaultContentMax+8 {
				t.Errorf("content not truncated: len=%d", len(r.Content))
			}
			return
		}
	}
	t.Fatal("syntheticBigMsg record not found")
}

type syntheticBigMsg struct{ payload string }
