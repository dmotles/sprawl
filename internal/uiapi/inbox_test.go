package uiapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// errPgDetail stands in for a pgx error, which names tables, columns and roles.
// Shared by the leak assertions so they all probe the same shape of detail.
var errPgDetail = errors.New(`relation "sprawl_internal_secrets" does not exist`)

// fakeQuestions is a QuestionReader that records what it was asked for.
type fakeQuestions struct {
	questions []Question
	err       error
	gotOpts   ListOptions
}

func (f *fakeQuestions) ListQuestions(_ context.Context, opts ListOptions) ([]Question, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.questions, nil
}

func TestPgQuestionReader_ListQuestions(t *testing.T) {
	var (
		qID    = uuid.New()
		projID = uuid.New()
		wfID   = uuid.New()
		opened = time.Date(2026, 9, 9, 18, 12, 0, 0, time.UTC)
	)
	pool := &queryPool{rows: &rowsStub{rows: [][]any{
		{
			qID, int64(640), opened, projID, "https://github.com/dmotles/sprawl.git", wfID,
			"zone", "Should the drawer trap focus?", "", "0d3f",
		},
	}}}

	got, err := PgQuestionReader{Pool: pool}.ListQuestions(context.Background(), ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	want := Question{
		EventID: qID, Seq: 640, OpenedAt: opened, ProjectID: projID,
		ProjectName: "sprawl", WorkflowInstanceID: wfID,
		Asker: "zone", Question: "Should the drawer trap focus?", Context: "", GoalEventID: "0d3f",
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("question = %+v, want %+v", got, want)
	}
}

// The inbox is the one endpoint sorted OLDEST first: the question waiting
// longest is the one that most needs answering, and newest-first buries it.
// Asserted on the SQL because ordering is the database's job here — a test that
// only checked the returned slice would pass against any ORDER BY, since the
// stub returns rows in the order it was handed them.
func TestPgQuestionReader_OrdersOldestFirst(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgQuestionReader{Pool: pool}).ListQuestions(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if strings.Contains(pool.gotSQL, "e.seq DESC") {
		t.Error("the inbox query orders newest-first, which buries the longest-waiting question")
	}
	if !strings.Contains(pool.gotSQL, "ORDER BY e.seq") {
		t.Errorf("the inbox query does not order by seq at all:\n%s", pool.gotSQL)
	}
}

// Open-ness is the presence of an open_contracts row. A query over `events`
// alone would list every question ever asked, including the answered ones.
func TestPgQuestionReader_ReadsOnlyOpenContracts(t *testing.T) {
	pool := &queryPool{rows: &rowsStub{}}
	if _, err := (PgQuestionReader{Pool: pool}).ListQuestions(context.Background(), ListOptions{Limit: 10}); err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	// Asserted as "DRIVEN BY open_contracts", not merely "mentions it". A
	// bare substring check cannot tell `FROM open_contracts` from
	// `LEFT JOIN open_contracts` — and the latter returns every question ever
	// asked, with the open ones merely annotated. That mutation passed a
	// substring check, which is why this is spelled out.
	if !strings.Contains(pool.gotSQL, "FROM open_contracts") {
		t.Errorf("the inbox query is not driven by open_contracts, so answered questions would be listed too:\n%s", pool.gotSQL)
	}
	if strings.Contains(pool.gotSQL, "LEFT JOIN open_contracts") {
		t.Errorf("open_contracts is OUTER joined, which annotates rather than filters:\n%s", pool.gotSQL)
	}
}

func TestHandleInbox_ServesTheNamedEnvelope(t *testing.T) {
	reader := &fakeQuestions{questions: []Question{}}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: &fakeGoals{}, Inbox: reader}, "/api/inbox?limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"questions":[]`) {
		t.Errorf("empty inbox encoded as %s, want {\"questions\":[]}", strings.TrimSpace(body))
	}
	if reader.gotOpts.Limit != 5 {
		t.Errorf("reader asked for limit %d, want 5", reader.gotOpts.Limit)
	}
}

// Every endpoint owes the same leak guarantee, not just the first one written:
// the 500 body must carry a generic message while the real cause goes to the
// log. Asserted per endpoint because handleList is shared but the wiring is not.
func TestHandleInbox_DoesNotLeakTheDatabaseError(t *testing.T) {
	var logged strings.Builder
	reader := &fakeQuestions{err: errPgDetail}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: &fakeGoals{}, Inbox: reader, Logger: textLogger(&logged)}, "/api/inbox")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "sprawl_internal_secrets") {
		t.Errorf("the response body leaks the database error: %s", strings.TrimSpace(body))
	}
	if !strings.Contains(logged.String(), "sprawl_internal_secrets") {
		t.Errorf("the real error was dropped rather than logged: %s", logged.String())
	}
}
