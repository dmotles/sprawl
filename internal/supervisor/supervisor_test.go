package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
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
	if err := liveRT.Start(context.Background()); err != nil {
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
	if err := stoppedActiveRT.Start(context.Background()); err != nil {
		t.Fatalf("stopped runtime start: %v", err)
	}
	close(stoppedSession.doneCh)
	deadline := time.After(2 * time.Second)
	for stoppedActiveRT.Snapshot().Lifecycle != RuntimeLifecycleStopped {
		select {
		case <-deadline:
			t.Fatalf("stopped-active runtime lifecycle = %q, want %q", stoppedActiveRT.Snapshot().Lifecycle, RuntimeLifecycleStopped)
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

	err := sup.Delegate(context.Background(), "ratz", "implement feature X")
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

	err := sup.Delegate(context.Background(), "nonexistent", "do something")
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

	err := sup.Delegate(context.Background(), "ratz", "do something")
	if err == nil {
		t.Fatal("expected error for killed agent")
	}
}

func TestMessage_SendsMessage(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Type:   "researcher",
		Family: "engineering",
		Parent: "weave",
		Status: "active",
	})

	err := sup.Message(context.Background(), "ghost", "status update", "all done")
	if err != nil {
		t.Fatalf("Message() error: %v", err)
	}

	// Verify message was delivered
	msgs, err := messages.Inbox(tmpDir, "ghost")
	if err != nil {
		t.Fatalf("Inbox() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "status update" {
		t.Errorf("message subject = %q, want 'status update'", msgs[0].Subject)
	}
	if msgs[0].Body != "all done" {
		t.Errorf("message body = %q, want 'all done'", msgs[0].Body)
	}
	if msgs[0].From != "weave" {
		t.Errorf("message from = %q, want weave", msgs[0].From)
	}
}

func TestMessage_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	err := sup.Message(context.Background(), "nonexistent", "hello", "world")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestSendAsync_WritesMaildirAndQueue(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Status: "active",
	})

	result, err := sup.SendAsync(context.Background(), "ghost", "hello", "world", "", []string{"fyi"})
	if err != nil {
		t.Fatalf("SendAsync: %v", err)
	}
	if result.MessageID == "" {
		t.Error("message_id empty")
	}
	if result.QueuedAt == "" {
		t.Error("queued_at empty")
	}

	// Maildir side
	msgs, err := messages.Inbox(tmpDir, "ghost")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Maildir got %d, want 1", len(msgs))
	}
	if msgs[0].Subject != "hello" || msgs[0].Body != "world" || msgs[0].From != "weave" {
		t.Errorf("maildir msg = %+v", msgs[0])
	}

	// Queue side
	entries, err := agentloop.ListPending(tmpDir, "ghost")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("queue got %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Class != agentloop.ClassAsync {
		t.Errorf("class = %q, want async", e.Class)
	}
	if e.From != "weave" || e.Subject != "hello" || e.Body != "world" {
		t.Errorf("entry = %+v", e)
	}
	if len(e.Tags) != 1 || e.Tags[0] != "fyi" {
		t.Errorf("tags = %v", e.Tags)
	}
	if e.ID != result.MessageID {
		t.Errorf("message_id mismatch: result=%q entry=%q", result.MessageID, e.ID)
	}
}

func TestSendAsync_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.SendAsync(context.Background(), "nobody", "s", "b", "", nil)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
}

func TestSendAsync_ValidatesName(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.SendAsync(context.Background(), "../evil", "s", "b", "", nil)
	if err == nil {
		t.Fatal("expected validation error")
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

	res, err := sup.ReportStatus(context.Background(), "", "working", "halfway", "extra detail")
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
	if got.LastReportDetail != "extra detail" {
		t.Errorf("LastReportDetail = %q", got.LastReportDetail)
	}

	// Parent "root" gets both Maildir and queue entries.
	msgs, _ := messages.Inbox(tmpDir, "root")
	if len(msgs) != 1 {
		t.Fatalf("inbox len = %d", len(msgs))
	}
	entries, _ := agentloop.ListPending(tmpDir, "root")
	if len(entries) != 1 {
		t.Fatalf("queue len = %d", len(entries))
	}
	if entries[0].From != "weave" {
		t.Errorf("from = %q", entries[0].From)
	}
}

func TestReportStatus_ExplicitAgentName(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "ratz", Parent: "weave", Status: "active"})

	_, err := sup.ReportStatus(context.Background(), "ratz", "complete", "done", "")
	if err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	got, _ := state.LoadAgent(tmpDir, "ratz")
	if got.Status != "done" {
		t.Errorf("Status = %q, want done", got.Status)
	}
}

func TestReportStatus_InvalidState(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})
	_, err := sup.ReportStatus(context.Background(), "", "bogus", "x", "")
	if err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestSendInterrupt_WritesMaildirAndQueueWithInterruptClass(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	// weave → ghost direct child.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Parent: "weave",
		Status: "active",
	})

	result, err := sup.SendInterrupt(context.Background(), "ghost", "urgent", "stop what you're doing", "you were refactoring foo")
	if err != nil {
		t.Fatalf("SendInterrupt: %v", err)
	}
	if result.MessageID == "" {
		t.Error("message_id empty")
	}
	if result.DeliveredAt == "" {
		t.Error("delivered_at empty")
	}
	if !result.Interrupted {
		t.Error("interrupted = false, want true (advisory)")
	}

	// Maildir side
	msgs, err := messages.Inbox(tmpDir, "ghost")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "urgent" || msgs[0].Body != "stop what you're doing" {
		t.Errorf("maildir msg = %+v", msgs)
	}

	// Queue side
	entries, err := agentloop.ListPending(tmpDir, "ghost")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("queue got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Class != agentloop.ClassInterrupt {
		t.Errorf("class = %q, want interrupt", e.Class)
	}
	if e.From != "weave" || e.Subject != "urgent" || e.Body != "stop what you're doing" {
		t.Errorf("entry = %+v", e)
	}
	// resume_hint travels as a tag so the harness can render §4.5.2 frames
	// without re-parsing the body.
	var gotHint string
	for _, tag := range e.Tags {
		if len(tag) > len("resume_hint:") && tag[:len("resume_hint:")] == "resume_hint:" {
			gotHint = tag[len("resume_hint:"):]
		}
	}
	if gotHint != "you were refactoring foo" {
		t.Errorf("resume_hint tag = %q, want 'you were refactoring foo' (tags=%v)", gotHint, e.Tags)
	}
	if e.ID != result.MessageID {
		t.Errorf("message_id mismatch: result=%q entry=%q", result.MessageID, e.ID)
	}
}

func TestSendInterrupt_BlocksNonAncestorCaller(t *testing.T) {
	// peer (non-ancestor) attempts to interrupt ghost. caller = "weave" in
	// test fixture, so set up a tree where weave is NOT ghost's ancestor.
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "other-root", // different root
		Parent: "",
		Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Parent: "other-root", // ghost's ancestor is other-root, not weave
		Status: "active",
	})

	_, err := sup.SendInterrupt(context.Background(), "ghost", "s", "b", "")
	if err == nil {
		t.Fatal("expected error for non-ancestor caller")
	}
	// Ensure nothing was enqueued.
	entries, _ := agentloop.ListPending(tmpDir, "ghost")
	if len(entries) != 0 {
		t.Errorf("queue got %d, want 0 (gate should block)", len(entries))
	}
}

func TestSendInterrupt_AllowsAncestorChain(t *testing.T) {
	// weave → midmgr → ghost. weave should be allowed to interrupt ghost
	// via the grandparent path.
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "midmgr",
		Parent: "weave",
		Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Parent: "midmgr",
		Status: "active",
	})

	_, err := sup.SendInterrupt(context.Background(), "ghost", "s", "b", "")
	if err != nil {
		t.Fatalf("SendInterrupt grandparent: %v", err)
	}
}

func TestSendInterrupt_RejectsSelfInterrupt(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})

	_, err := sup.SendInterrupt(context.Background(), "weave", "s", "b", "")
	if err == nil {
		t.Fatal("expected error for self-interrupt")
	}
}

func TestSendInterrupt_AgentNotFound(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.SendInterrupt(context.Background(), "nobody", "s", "b", "")
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
}

func TestSendInterrupt_ValidatesName(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.SendInterrupt(context.Background(), "../evil", "s", "b", "")
	if err == nil {
		t.Fatal("expected validation error")
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
	if err := messages.Send(sprawlRoot, from, "weave", subject, body); err != nil {
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
	if err := messages.Send(tmpDir, "weave", "ratz", "fyi", "private"); err != nil {
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
