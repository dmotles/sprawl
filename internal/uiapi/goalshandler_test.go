package uiapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// fakeGoals records what the handler asked for and returns what it is told to.
type fakeGoals struct {
	goals   []Goal
	err     error
	gotOpts ListOptions
}

func (f *fakeGoals) ListGoals(_ context.Context, opts ListOptions) ([]Goal, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.goals, nil
}

// The default limit must reach the reader. Without this the parseLimit cases
// could all be right while the handler queried a constant.
func TestHandleGoals_PassesOptionsThrough(t *testing.T) {
	id := uuid.New()
	reader := &fakeGoals{goals: []Goal{}}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: reader},
		"/api/goals?limit=7&project_id="+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if reader.gotOpts.Limit != 7 {
		t.Errorf("reader asked for limit %d, want 7", reader.gotOpts.Limit)
	}
	if reader.gotOpts.ProjectID == nil || *reader.gotOpts.ProjectID != id {
		t.Errorf("reader got project %v, want %v", reader.gotOpts.ProjectID, id)
	}
}

// Absent ?project_id= must mean all projects — a nil filter, not a zero uuid,
// which would match nothing and render an empty UI on a populated database.
func TestHandleGoals_NoProjectFilterMeansAllProjects(t *testing.T) {
	reader := &fakeGoals{goals: []Goal{}}
	if rec := get(t, Config{Events: &fakeEvents{}, Goals: reader}, "/api/goals"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if reader.gotOpts.ProjectID != nil {
		t.Errorf("project filter = %v, want nil so every project is listed", *reader.gotOpts.ProjectID)
	}
	if reader.gotOpts.Limit != DefaultEventLimit {
		t.Errorf("limit = %d, want the default %d", reader.gotOpts.Limit, DefaultEventLimit)
	}
}

// A malformed project_id is a 400. Ignoring it would answer a question about
// one project with every project's data.
func TestHandleGoals_RejectsAMalformedProjectID(t *testing.T) {
	reader := &fakeGoals{goals: []Goal{}}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: reader}, "/api/goals?project_id=not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — an unparseable filter must not be silently dropped", rec.Code)
	}
	if reader.gotOpts.Limit != 0 {
		t.Error("the reader was called despite an invalid filter")
	}
	if !strings.Contains(rec.Body.String(), "project_id must be a uuid") {
		t.Errorf("body %s does not say which rule was broken", strings.TrimSpace(rec.Body.String()))
	}
}

// The 500 body must not carry the database's error text. This endpoint is
// browser-reachable with no auth in v1, and a pg error names tables, columns
// and roles.
func TestHandleGoals_DatabaseErrorIsNotReturnedToTheBrowser(t *testing.T) {
	var logged strings.Builder
	const secret = `relation "sprawl_internal_secrets" does not exist`
	reader := &fakeGoals{err: errors.New(secret)}
	rec := get(t, Config{Events: &fakeEvents{}, Goals: reader, Logger: textLogger(&logged)}, "/api/goals")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "sprawl_internal_secrets") {
		t.Errorf("the response body leaks the database error: %s", strings.TrimSpace(body))
	}
	if !strings.Contains(body, "reading goals failed") {
		t.Errorf("body %s does not name the collection that failed", strings.TrimSpace(body))
	}
	// The other half: suppressed from the response but NOT lost. An operator
	// still needs the real cause, so it has to be in the log.
	if !strings.Contains(logged.String(), "sprawl_internal_secrets") {
		t.Errorf("the real error was dropped entirely rather than logged: %s", logged.String())
	}
}

// An empty result encodes as [] under the named key, not as null and not as a
// bare array.
func TestHandleGoals_EmptyEncodesAsNamedArray(t *testing.T) {
	rec := get(t, Config{Events: &fakeEvents{}, Goals: &fakeGoals{goals: []Goal{}}}, "/api/goals")
	if body := rec.Body.String(); !strings.Contains(body, `"goals":[]`) {
		t.Errorf("empty goals encoded as %s, want {\"goals\":[]}", strings.TrimSpace(body))
	}
}
