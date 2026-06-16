package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

func newTestSupervisor(t *testing.T) (*Real, string) {
	t.Helper()
	tmpDir := t.TempDir()

	sup, err := NewReal(Config{
		SprawlRoot: tmpDir,
		CallerName: "weave",
	})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}

	return sup, tmpDir
}

func TestNewReal_DoesNotRequireTmuxOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	sup, err := NewReal(Config{
		SprawlRoot: t.TempDir(),
		CallerName: "weave",
	})
	if err != nil {
		t.Fatalf("NewReal() error with tmux absent from PATH: %v", err)
	}
	if sup == nil {
		t.Fatal("NewReal() returned nil supervisor")
	}
}

func saveTestAgent(t *testing.T, sprawlRoot string, a *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, a); err != nil {
		t.Fatalf("SaveAgent(%s): %v", a.Name, err)
	}
}

func TestStatus_ReturnsAllAgents(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
		Branch: "dmotles/feature-a",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Type:   "researcher",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
		Branch: "dmotles/research-b",
	})

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}

	nameMap := make(map[string]AgentInfo)
	for _, a := range agents {
		nameMap[a.Name] = a
	}

	ratz, ok := nameMap["ratz"]
	if !ok {
		t.Fatal("missing agent ratz")
	}
	if ratz.Type != "engineer" {
		t.Errorf("ratz.Type = %q, want engineer", ratz.Type)
	}
	if ratz.Status != "active" {
		t.Errorf("ratz.Status = %q, want active", ratz.Status)
	}

	ghost, ok := nameMap["ghost"]
	if !ok {
		t.Fatal("missing agent ghost")
	}
	if ghost.Type != "researcher" {
		t.Errorf("ghost.Type = %q, want researcher", ghost.Type)
	}
}

func TestStatus_EmptyReturnsEmpty(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	if len(agents) != 0 {
		t.Errorf("got %d agents, want 0", len(agents))
	}
}

func TestStatus_ProcessAliveTriStateComesFromRuntimeKnowledge(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	live := testAgentState("alive-agent")
	knownStopped := testAgentState("stopped-agent")
	knownStopped.Status = "killed"
	stoppedActive := testAgentState("stopped-active-agent")
	unknown := testAgentState("unknown-agent")
	saveTestAgent(t, tmpDir, live)
	saveTestAgent(t, tmpDir, knownStopped)
	saveTestAgent(t, tmpDir, stoppedActive)
	saveTestAgent(t, tmpDir, unknown)

	liveRT := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      live,
		Starter: &runtimeTestStarter{
			session: &runtimeTestSession{
				sessionID: "sess-alive",
				caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
			},
		},
	})
	if err := liveRT.Start(); err != nil {
		t.Fatalf("live runtime start: %v", err)
	}

	stoppedSession := &runtimeTestSession{
		sessionID: "sess-stopped",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		doneCh:    make(chan struct{}),
	}
	stoppedActiveRT := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      stoppedActive,
		Starter:    &runtimeTestStarter{session: stoppedSession},
	})
	if err := stoppedActiveRT.Start(); err != nil {
		t.Fatalf("stopped runtime start: %v", err)
	}
	close(stoppedSession.doneCh)
	deadline := time.After(2 * time.Second)
	// QUM-722: unexpected exit now classifies as Died (not Stopped).
	for stoppedActiveRT.Snapshot().Liveness != liveness.Died {
		select {
		case <-deadline:
			t.Fatalf("stopped-active runtime lifecycle = %q, want %q", stoppedActiveRT.Snapshot().Liveness, liveness.Died)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	stoppedRT := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      knownStopped,
	})
	stoppedRT.SyncAgentState(knownStopped)

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	byName := make(map[string]AgentInfo, len(agents))
	for _, info := range agents {
		byName[info.Name] = info
	}

	if byName["alive-agent"].ProcessAlive == nil || !*byName["alive-agent"].ProcessAlive {
		t.Fatalf("alive-agent ProcessAlive = %+v, want true", byName["alive-agent"].ProcessAlive)
	}
	if byName["stopped-agent"].ProcessAlive == nil || *byName["stopped-agent"].ProcessAlive {
		t.Fatalf("stopped-agent ProcessAlive = %+v, want false", byName["stopped-agent"].ProcessAlive)
	}
	if byName["stopped-active-agent"].ProcessAlive == nil || *byName["stopped-active-agent"].ProcessAlive {
		t.Fatalf("stopped-active-agent ProcessAlive = %+v, want false", byName["stopped-active-agent"].ProcessAlive)
	}
	if byName["unknown-agent"].ProcessAlive != nil {
		t.Fatalf("unknown-agent ProcessAlive = %+v, want nil", byName["unknown-agent"].ProcessAlive)
	}
}

func TestDelegate_EnqueuesTask(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
	})

	err := sup.Delegate(context.Background(), "ratz", "implement feature X", false)
	if err != nil {
		t.Fatalf("Delegate() error: %v", err)
	}

	// Verify task was enqueued
	tasks, err := state.ListTasks(tmpDir, "ratz")
	if err != nil {
		t.Fatalf("ListTasks() error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].Prompt != "implement feature X" {
		t.Errorf("task prompt = %q, want 'implement feature X'", tasks[0].Prompt)
	}
	if tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want queued", tasks[0].Status)
	}
}

func TestDelegate_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	err := sup.Delegate(context.Background(), "nonexistent", "do something", false)
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestDelegate_KilledAgent(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Status: "killed",
	})

	err := sup.Delegate(context.Background(), "ratz", "do something", false)
	if err == nil {
		t.Fatal("expected error for killed agent")
	}
}

func TestPeek_ReturnsStateAndActivity(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:              "ghost",
		Status:            "active",
		LastReportType:    "status",
		LastReportMessage: "working on X",
		LastReportAt:      "2026-04-21T00:00:00Z",
	})

	// Write one activity entry.
	path := agentloop.ActivityPath(tmpDir, "ghost")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"ts":"2026-04-21T00:00:01Z","kind":"assistant_text","summary":"hi"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	got, err := sup.Peek(context.Background(), "ghost", 10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("status = %q", got.Status)
	}
	if got.LastReport.Type != "status" || got.LastReport.Message != "working on X" {
		t.Errorf("last_report = %+v", got.LastReport)
	}
	if len(got.Activity) != 1 || got.Activity[0].Kind != "assistant_text" {
		t.Errorf("activity = %+v", got.Activity)
	}
}

func TestPeek_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.Peek(context.Background(), "nobody", 10)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReportStatus_PersistsAndNotifiesParent(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	// Caller is "weave"; report as the weave agent so ReportStatus resolves
	// agentName from r.callerName (MCP flow).
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Parent: "root", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "root", Status: "active"})

	res, err := sup.ReportStatus(context.Background(), "", "working", "halfway")
	if err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	if res.ReportedAt == "" {
		t.Error("ReportedAt empty")
	}

	got, _ := state.LoadAgent(tmpDir, "weave")
	if got.LastReportState != "working" {
		t.Errorf("LastReportState = %q", got.LastReportState)
	}
	if got.LastReportMessage != "halfway" {
		t.Errorf("LastReportMessage = %q", got.LastReportMessage)
	}
	// QUM-550 slice 2: detail dropped from the report_status surface; the
	// supervisor passes "" to agentops.Report, so LastReportDetail stays empty.
	if got.LastReportDetail != "" {
		t.Errorf("LastReportDetail = %q, want empty (detail dropped from report_status)", got.LastReportDetail)
	}

	// QUM-614: report_status writes a type=status_change envelope into the
	// parent's maildir, but it MUST stay hidden from the default messages
	// listing (Inbox == List("all")), the unread badge, and the harness
	// queue. The DrainStatusChangeLines helper surfaces it via the
	// out-of-band "status" view.
	msgs, _ := messages.Inbox(tmpDir, "root")
	if len(msgs) != 0 {
		t.Errorf("inbox len = %d, want 0 (status_change must be hidden from default filters, QUM-614)", len(msgs))
	}
	entries, _ := agentloop.ListPending(tmpDir, "root")
	if len(entries) != 0 {
		t.Errorf("queue len = %d, want 0 (QUM-614)", len(entries))
	}
	drained := inboxprompt.DrainStatusChangeLines(tmpDir, "root")
	if len(drained) != 1 {
		t.Fatalf("DrainStatusChangeLines(root) len = %d, want 1; got %#v", len(drained), drained)
	}
	if !strings.Contains(drained[0], "weave changed status to working: halfway") {
		t.Errorf("drained line = %q; want it to mention weave's status update", drained[0])
	}
}

func TestReportStatus_ExplicitAgentName(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "ratz", Parent: "weave", Status: "active"})

	_, err := sup.ReportStatus(context.Background(), "ratz", "complete", "done")
	if err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	got, _ := state.LoadAgent(tmpDir, "ratz")
	// QUM-668: report.go atomically flips Status → stopped on a "complete"
	// terminal outcome (partially reversing the QUM-625 M4 stance).
	if got.LastReportState != "complete" {
		t.Errorf("LastReportState = %q, want complete", got.LastReportState)
	}
	if got.Status != state.StatusComplete {
		t.Errorf("Status = %q, want %q (QUM-787: complete report lands in StatusComplete)", got.Status, state.StatusComplete)
	}
}

func TestReportStatus_InvalidState(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})
	_, err := sup.ReportStatus(context.Background(), "", "bogus", "x")
	if err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestPeek_DefaultsTailTo20(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "ghost", Status: "active"})

	got, err := sup.Peek(context.Background(), "ghost", 0)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.Activity == nil {
		t.Error("activity should be non-nil empty slice, got nil")
	}
}

// --- QUM-316: MessagesList / MessagesRead / MessagesArchive / MessagesPeek ---

// seedClock advances NowFunc one second per call so Maildir timestamps are
// strictly ordered for list/peek tests (RFC3339 is second-resolution).
var seedClock int64

func nextSeedTime() time.Time {
	seedClock++
	return time.Date(2026, 4, 21, 10, 0, int(seedClock), 0, time.UTC)
}

// seedInbox delivers a message to the caller ("weave") from `from`. Returns the short ID.
func seedInbox(t *testing.T, sprawlRoot, from, subject, body string) string {
	t.Helper()
	prev := messages.NowFunc
	messages.NowFunc = nextSeedTime
	defer func() { messages.NowFunc = prev }()
	if _, err := messages.Send(sprawlRoot, from, "weave", subject, body); err != nil {
		t.Fatalf("seed Send: %v", err)
	}
	msgs, err := messages.List(sprawlRoot, "weave", "unread")
	if err != nil {
		t.Fatalf("seed List: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("seed: no unread messages after Send")
	}
	// Return the most recently delivered one's ShortID.
	return msgs[len(msgs)-1].ShortID
}

func TestMessagesList_ReturnsInboxScopedToCaller(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t) // callerName=weave
	seedInbox(t, tmpDir, "ratz", "hello", "world")
	seedInbox(t, tmpDir, "ghost", "ping", "pong")

	// A message to someone else must NOT appear in weave's list.
	if _, err := messages.Send(tmpDir, "weave", "ratz", "fyi", "private"); err != nil {
		t.Fatalf("cross-agent send: %v", err)
	}

	res, err := sup.MessagesList(context.Background(), "unread", 0)
	if err != nil {
		t.Fatalf("MessagesList: %v", err)
	}
	if res.Agent != "weave" {
		t.Errorf("agent = %q, want weave", res.Agent)
	}
	if res.Count != 2 || len(res.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(res.Messages))
	}
	for _, m := range res.Messages {
		if m.Read {
			t.Errorf("unread filter returned a read msg: %+v", m)
		}
		if m.Dir != "new" {
			t.Errorf("dir = %q, want new", m.Dir)
		}
		if m.From != "ratz" && m.From != "ghost" {
			t.Errorf("unexpected from %q (cross-mailbox leak?)", m.From)
		}
	}
}

func TestMessagesList_FilterArchived(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	id := seedInbox(t, tmpDir, "ratz", "old", "body")
	// Archive it.
	full, err := messages.ResolvePrefix(tmpDir, "weave", id)
	if err != nil {
		t.Fatalf("ResolvePrefix: %v", err)
	}
	if err := messages.Archive(tmpDir, "weave", full); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	res, err := sup.MessagesList(context.Background(), "archived", 0)
	if err != nil {
		t.Fatalf("MessagesList: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("got %d archived, want 1", len(res.Messages))
	}
	if res.Messages[0].Dir != "archive" {
		t.Errorf("dir = %q, want archive", res.Messages[0].Dir)
	}
	if !res.Messages[0].Read {
		t.Error("archived messages should report Read=true")
	}
}

func TestMessagesList_FilterAllIncludesNewAndCur(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	unreadID := seedInbox(t, tmpDir, "ratz", "u", "b")
	readID := seedInbox(t, tmpDir, "ghost", "r", "b")

	// Mark the second one read.
	full, err := messages.ResolvePrefix(tmpDir, "weave", readID)
	if err != nil {
		t.Fatalf("ResolvePrefix: %v", err)
	}
	if err := messages.MarkRead(tmpDir, "weave", full); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	res, err := sup.MessagesList(context.Background(), "all", 0)
	if err != nil {
		t.Fatalf("MessagesList: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("got %d all, want 2", len(res.Messages))
	}
	readFlags := map[string]bool{}
	for _, m := range res.Messages {
		readFlags[m.ID] = m.Read
	}
	if readFlags[unreadID] {
		t.Errorf("unreadID %q reported Read=true", unreadID)
	}
	if !readFlags[readID] {
		t.Errorf("readID %q reported Read=false", readID)
	}
}

func TestMessagesList_LimitNewestFirst(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	seedInbox(t, tmpDir, "a", "1", "")
	seedInbox(t, tmpDir, "b", "2", "")
	seedInbox(t, tmpDir, "c", "3", "")

	res, err := sup.MessagesList(context.Background(), "all", 2)
	if err != nil {
		t.Fatalf("MessagesList: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("limit 2 got %d", len(res.Messages))
	}
	// Newest-first: subjects should be "3" then "2".
	if res.Messages[0].Subject != "3" || res.Messages[1].Subject != "2" {
		t.Errorf("expected newest-first [3,2], got [%s,%s]", res.Messages[0].Subject, res.Messages[1].Subject)
	}
}

func TestMessagesList_RejectsInvalidFilter(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.MessagesList(context.Background(), "bogus", 0)
	if err == nil {
		t.Fatal("expected error for invalid filter")
	}
}

func TestMessagesList_RejectsSentFilter(t *testing.T) {
	// AC §2.5 says filter ∈ {unread, read, archived, all}. `sent` is not in scope.
	sup, _ := newTestSupervisor(t)
	_, err := sup.MessagesList(context.Background(), "sent", 0)
	if err == nil {
		t.Fatal("expected error for 'sent' filter (out of AC scope)")
	}
}

func TestMessagesRead_AutoMarksUnread(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	id := seedInbox(t, tmpDir, "ratz", "hi", "hello body")

	res, err := sup.MessagesRead(context.Background(), id)
	if err != nil {
		t.Fatalf("MessagesRead: %v", err)
	}
	if res.Body != "hello body" {
		t.Errorf("body = %q", res.Body)
	}
	if res.Subject != "hi" {
		t.Errorf("subject = %q", res.Subject)
	}
	if res.From != "ratz" {
		t.Errorf("from = %q", res.From)
	}
	if res.To != "weave" {
		t.Errorf("to = %q", res.To)
	}
	if !res.WasUnread {
		t.Error("expected WasUnread=true for first read")
	}
	if res.Dir != "cur" {
		t.Errorf("dir = %q, want cur after auto-mark", res.Dir)
	}

	// Verify a second read reports WasUnread=false.
	res2, err := sup.MessagesRead(context.Background(), id)
	if err != nil {
		t.Fatalf("MessagesRead 2: %v", err)
	}
	if res2.WasUnread {
		t.Error("second read should have WasUnread=false")
	}
}

func TestMessagesRead_NotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.MessagesRead(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for missing message")
	}
}

func TestMessagesRead_EmptyIDRejected(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.MessagesRead(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestMessagesArchive_MovesToArchive(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	id := seedInbox(t, tmpDir, "ratz", "s", "b")

	res, err := sup.MessagesArchive(context.Background(), id)
	if err != nil {
		t.Fatalf("MessagesArchive: %v", err)
	}
	if !res.Archived {
		t.Error("Archived=false")
	}

	// File must now live in archive/, not in new/.
	archived, err := messages.List(tmpDir, "weave", "archived")
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("got %d archived, want 1", len(archived))
	}
	unread, err := messages.List(tmpDir, "weave", "unread")
	if err != nil {
		t.Fatalf("List unread: %v", err)
	}
	if len(unread) != 0 {
		t.Errorf("got %d unread after archive, want 0", len(unread))
	}
	// Verify file did not leak into another agent's maildir.
	ratzDir := filepath.Join(tmpDir, ".sprawl", "messages", "ratz", "archive")
	if entries, _ := os.ReadDir(ratzDir); len(entries) != 0 {
		t.Errorf("leaked %d files into ratz archive", len(entries))
	}
}

func TestMessagesArchive_NotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.MessagesArchive(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMessagesArchiveAll_ArchivesAll(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	seedInbox(t, tmpDir, "a", "one", "")
	seedInbox(t, tmpDir, "b", "two", "")
	seedInbox(t, tmpDir, "c", "three", "")

	res, err := sup.MessagesArchiveAll(context.Background(), "all")
	if err != nil {
		t.Fatalf("MessagesArchiveAll: %v", err)
	}
	if res.ArchivedCount != 3 {
		t.Errorf("archived_count = %d, want 3", res.ArchivedCount)
	}
	if !res.Archived {
		t.Error("Archived=false")
	}

	archived, err := messages.List(tmpDir, "weave", "archived")
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(archived) != 3 {
		t.Errorf("got %d archived, want 3", len(archived))
	}
}

func TestMessagesArchiveAll_ArchivesReadOnly(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	seedInbox(t, tmpDir, "a", "one", "")
	readID := seedInbox(t, tmpDir, "b", "two", "")
	// Mark one read.
	full, _ := messages.ResolvePrefix(tmpDir, "weave", readID)
	if err := messages.MarkRead(tmpDir, "weave", full); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	res, err := sup.MessagesArchiveAll(context.Background(), "read")
	if err != nil {
		t.Fatalf("MessagesArchiveAll read: %v", err)
	}
	if res.ArchivedCount != 1 {
		t.Errorf("archived_count = %d, want 1", res.ArchivedCount)
	}

	// Unread message should still exist.
	unread, err := messages.List(tmpDir, "weave", "unread")
	if err != nil {
		t.Fatalf("List unread: %v", err)
	}
	if len(unread) != 1 {
		t.Errorf("got %d unread, want 1", len(unread))
	}
}

func TestMessagesArchiveAll_InvalidMode(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.MessagesArchiveAll(context.Background(), "bogus")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestMessagesPeek_CountsUnreadAndPreviews(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	seedInbox(t, tmpDir, "a", "one", "")
	seedInbox(t, tmpDir, "b", "two", "")
	readID := seedInbox(t, tmpDir, "c", "three", "")
	// Mark one read.
	full, _ := messages.ResolvePrefix(tmpDir, "weave", readID)
	if err := messages.MarkRead(tmpDir, "weave", full); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	res, err := sup.MessagesPeek(context.Background())
	if err != nil {
		t.Fatalf("MessagesPeek: %v", err)
	}
	if res.UnreadCount != 2 {
		t.Errorf("unread count = %d, want 2", res.UnreadCount)
	}
	if res.Agent != "weave" {
		t.Errorf("agent = %q", res.Agent)
	}
	if len(res.Preview) != 2 {
		t.Fatalf("preview len = %d, want 2", len(res.Preview))
	}
	// Newest-first.
	if res.Preview[0].Subject != "two" || res.Preview[1].Subject != "one" {
		t.Errorf("preview order = [%s,%s], want [two,one]", res.Preview[0].Subject, res.Preview[1].Subject)
	}
}

func TestMessagesPeek_Empty(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	res, err := sup.MessagesPeek(context.Background())
	if err != nil {
		t.Fatalf("MessagesPeek: %v", err)
	}
	if res.UnreadCount != 0 {
		t.Errorf("count = %d, want 0", res.UnreadCount)
	}
	if len(res.Preview) != 0 {
		t.Errorf("preview = %v, want empty", res.Preview)
	}
}

func TestMessagesList_RequiresCallerIdentity(t *testing.T) {
	// An empty callerName (unidentified context) must be rejected — protects
	// against agents being able to read mailboxes they don't own.
	sup, err := NewReal(Config{SprawlRoot: t.TempDir(), CallerName: ""})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	if _, err := sup.MessagesList(context.Background(), "all", 0); err == nil {
		t.Error("MessagesList with empty callerName should fail")
	}
	if _, err := sup.MessagesRead(context.Background(), "x"); err == nil {
		t.Error("MessagesRead with empty callerName should fail")
	}
	if _, err := sup.MessagesArchive(context.Background(), "x"); err == nil {
		t.Error("MessagesArchive with empty callerName should fail")
	}
	if _, err := sup.MessagesPeek(context.Background()); err == nil {
		t.Error("MessagesPeek with empty callerName should fail")
	}
}

// --- QUM-399 RegisterRootRuntime tests ---

// fakeRootHandle is a minimal RuntimeHandle that records delivery wake/preempt
// calls. Used by RegisterRootRuntime tests to confirm child reports route to
// the registered weave runtime.
type fakeRootHandle struct {
	caps                 backendpkg.Capabilities
	sessionID            string
	wakeForDeliveryCalls int32
	stopCalls            int32
	stopAbandonCalls     int32
	doneCh               chan struct{}
}

func (h *fakeRootHandle) Interrupt(context.Context) error { return nil }
func (h *fakeRootHandle) Wake() error                     { return nil }

func (h *fakeRootHandle) WakeForDelivery() error {
	h.wakeForDeliveryCalls++
	return nil
}

func (h *fakeRootHandle) Stop(context.Context) error { h.stopCalls++; return nil }
func (h *fakeRootHandle) StopAbandon(context.Context) error {
	h.stopAbandonCalls++
	return nil
}
func (h *fakeRootHandle) SessionID() string                     { return h.sessionID }
func (h *fakeRootHandle) Capabilities() backendpkg.Capabilities { return h.caps }
func (h *fakeRootHandle) Done() <-chan struct{}                 { return h.doneCh }

func TestRegisterRootRuntime_RegistersInRegistryAsStarted(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	h := &fakeRootHandle{
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		sessionID: "sess-weave",
	}
	st := &state.AgentState{Name: "weave", Status: "running"}
	rt, err := sup.RegisterRootRuntime("weave", h, st)
	if err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	if rt == nil {
		t.Fatal("RegisterRootRuntime returned nil runtime")
	}
	got, ok := sup.RuntimeRegistry().Get("weave")
	if !ok {
		t.Fatal("registry Get(weave) miss")
	}
	if got != rt {
		t.Error("registry Get returned a different runtime pointer")
	}
	if got.Snapshot().Liveness != liveness.Running {
		t.Errorf("Lifecycle = %q, want %q", got.Snapshot().Liveness, liveness.Running)
	}
}

func TestRegisterRootRuntime_LoadsAgentStateFromDisk_WhenNilProvided(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Type:   "weave",
		Status: "active",
		Branch: "main",
	})
	h := &fakeRootHandle{}
	rt, err := sup.RegisterRootRuntime("weave", h, nil)
	if err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	snap := rt.Snapshot()
	if snap.Branch != "main" {
		t.Errorf("Branch = %q, want main (loaded from disk)", snap.Branch)
	}
}

func TestRegisterRootRuntime_SynthesizesAgentState_WhenStateMissing(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	h := &fakeRootHandle{}
	rt, err := sup.RegisterRootRuntime("weave", h, nil)
	if err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	snap := rt.Snapshot()
	if snap.Name != "weave" {
		t.Errorf("Name = %q, want weave (synthesized)", snap.Name)
	}
	// QUM-527: synthesized fallback must tag the runtime Type as "root" so
	// the MCP eligibility gate accepts weave's caller identity.
	if snap.Type != "root" {
		t.Errorf("Type = %q, want %q (synthesized root runtime)", snap.Type, "root")
	}
}

// QUM-527: when RegisterRootRuntime loads an existing AgentState from disk
// that has an empty Type (e.g. a legacy weave record), the in-memory runtime
// must report Type=="root" so MCP eligibility passes.
func TestRegisterRootRuntime_TagsLoadedAgentStateAsRoot_WhenTypeEmpty(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Type:   "",
		Status: "active",
		Branch: "main",
	})
	h := &fakeRootHandle{}
	rt, err := sup.RegisterRootRuntime("weave", h, nil)
	if err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	snap := rt.Snapshot()
	if snap.Type != "root" {
		t.Errorf("Type = %q, want %q (empty Type on disk should be promoted to root)", snap.Type, "root")
	}
}

// QUM-527: RegisterRootRuntime must NOT overwrite a non-empty Type that was
// loaded from disk — only fill in an empty one.
func TestRegisterRootRuntime_PreservesNonEmptyTypeOnDisk(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Type:   "weave",
		Status: "active",
		Branch: "main",
	})
	h := &fakeRootHandle{}
	rt, err := sup.RegisterRootRuntime("weave", h, nil)
	if err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	snap := rt.Snapshot()
	if snap.Type != "weave" {
		t.Errorf("Type = %q, want %q (non-empty Type on disk must be preserved)", snap.Type, "weave")
	}
}

// QUM-535: RegisterRootRuntime must persist Type="root" to disk when the
// loaded record has an empty Type. The MCP eligibility gate consults
// Supervisor.Status, which reads from disk via state.ListAgents — so a
// purely in-memory mutation is invisible to the gate and weave-as-caller
// gets rejected.
func TestRegisterRootRuntime_PersistsTypeRootToDisk_WhenLoadedTypeEmpty(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Type:   "",
		Status: "active",
		Branch: "main",
	})
	h := &fakeRootHandle{}
	if _, err := sup.RegisterRootRuntime("weave", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	got, err := state.LoadAgent(tmpDir, "weave")
	if err != nil {
		t.Fatalf("LoadAgent(weave): %v", err)
	}
	if got == nil {
		t.Fatal("LoadAgent(weave) returned nil")
	}
	if got.Type != "root" {
		t.Errorf("on-disk Type = %q, want %q (must be persisted so disk-backed Status() sees it)", got.Type, "root")
	}
}

// QUM-535: when no AgentState exists on disk for the root, the synthesized
// fallback must also be persisted with Type="root" so disk-backed
// eligibility lookups succeed.
func TestRegisterRootRuntime_PersistsSynthesizedRecordToDisk_WhenStateMissing(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	h := &fakeRootHandle{}
	if _, err := sup.RegisterRootRuntime("weave", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	got, err := state.LoadAgent(tmpDir, "weave")
	if err != nil {
		t.Fatalf("LoadAgent(weave): %v", err)
	}
	if got == nil {
		t.Fatal("LoadAgent(weave) returned nil — synthesized record not persisted")
	}
	if got.Type != "root" {
		t.Errorf("on-disk Type = %q, want %q (synthesized record must be persisted)", got.Type, "root")
	}
}

// QUM-535: if the on-disk record already has a non-empty Type (e.g. a
// legacy "weave" type), RegisterRootRuntime must NOT rewrite it — only
// fill in empty Types. This guards against unintended downgrades.
func TestRegisterRootRuntime_DoesNotOverwriteDiskTypeWhenAlreadySet(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Type:   "weave",
		Status: "active",
		Branch: "main",
	})
	h := &fakeRootHandle{}
	if _, err := sup.RegisterRootRuntime("weave", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}
	got, err := state.LoadAgent(tmpDir, "weave")
	if err != nil {
		t.Fatalf("LoadAgent(weave): %v", err)
	}
	if got.Type != "weave" {
		t.Errorf("on-disk Type = %q, want %q (non-empty Type must be preserved on disk)", got.Type, "weave")
	}
}

// QUM-399 / QUM-550 slice 2: when weave is registered as a root runtime and a
// child agent reports status, the child→parent notification path must hit the
// registered handle via the cooperative WakeForDelivery. This guarantees child
// reports drive weave's UnifiedRuntime queue without preempting an in-flight
// turn.
func TestReportStatus_FromChildOfWeave_WakesRootWithoutPreempting(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
		Branch: "dmotles/ratz",
	})

	h := &fakeRootHandle{caps: backendpkg.Capabilities{SupportsInterrupt: true}}
	if _, err := sup.RegisterRootRuntime("weave", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	if _, err := sup.ReportStatus(context.Background(), "ratz", "working", "summary"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	if h.wakeForDeliveryCalls < 1 {
		t.Errorf("registered weave handle's WakeForDelivery calls = %d, want >= 1", h.wakeForDeliveryCalls)
	}
}
