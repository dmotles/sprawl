// Package supervisor — question queue (QUM-527 slice 1).
//
// The question queue is the in-process pubsub broker for "ask the user a
// question" flows. An agent calls Supervisor.AskUserQuestion, the question is
// enqueued and registered consumers (e.g. the TUI) are notified, and the
// caller blocks until a consumer Resolves the question (answered), Cancels it
// (user-dismissed), the agent retires (CancelByAgent), or the supervisor
// shuts down (closeAll).
//
// See docs/research/ask-user-question-mcp-design.md for the full design.
package supervisor

import (
	"context"
	"fmt"
	"sync"
)

// Outcome constants returned in QuestionResponse.Outcome.
const (
	OutcomeAnswered       = "answered"
	OutcomeDeclined       = "declined"
	OutcomeSessionEnded   = "session_ended"
	OutcomeAgentRetired   = "agent_retired"
	OutcomeTUIUnavailable = "tui_unavailable"
)

// QOption is one selectable option for a Question.
type QOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one prompt with a fixed set of options. A QuestionRequest may
// carry many. Server-side defaults for AllowCustom / AllowDecline are applied
// in the MCP unmarshal layer (slice 2 / QUM-527).
type Question struct {
	ID           string    `json:"id,omitempty"`
	Header       string    `json:"header,omitempty"`
	Prompt       string    `json:"question"`
	MultiSelect  bool      `json:"multi_select,omitempty"`
	Options      []QOption `json:"options"`
	AllowCustom  bool      `json:"allow_custom,omitempty"`
	AllowDecline bool      `json:"allow_decline,omitempty"`
}

// QuestionRequest is the payload AskUserQuestion accepts. RequestID must be
// non-empty — the caller (MCP tool dispatcher) generates a stable ID.
type QuestionRequest struct {
	RequestID string     `json:"request_id"`
	From      string     `json:"from"`
	Questions []Question `json:"questions"`
}

// QuestionAnswer is the user's per-question response.
type QuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected,omitempty"`
	CustomText string   `json:"custom_text,omitempty"`
	Declined   bool     `json:"declined,omitempty"`
}

// QuestionResponse is delivered to the blocked AskUserQuestion caller.
type QuestionResponse struct {
	RequestID string           `json:"request_id"`
	Outcome   string           `json:"outcome"`
	Answers   []QuestionAnswer `json:"answers,omitempty"`
	Note      string           `json:"note,omitempty"`
}

// PendingQuestion is the consumer-facing handle for a queued question. It
// carries the request and an opaque sequence number assigned at enqueue time.
type PendingQuestion struct {
	Req QuestionRequest
	Seq uint64
}

// QuestionConsumer is the interface that question subscribers (TUI, slack
// bridge, …) implement. Methods are invoked OUTSIDE the queue's mutex so they
// may take their own locks freely.
type QuestionConsumer interface {
	Name() string
	OnEnqueue(*PendingQuestion)
	OnCancel(requestID, reason string)
}

// questionEntry is the queue's internal record. respCh is buffered 1 so the
// resolver (or canceller) never blocks. done guards idempotency under mu.
type questionEntry struct {
	pq     *PendingQuestion
	respCh chan QuestionResponse
	done   bool
}

// questionQueue is the in-process FIFO queue of pending questions. The single
// mu protects all mutable state: entries, consumers, closed. We never block
// on a channel while holding mu — channel sends are done after copying refs
// out under the lock.
type questionQueue struct {
	mu        sync.Mutex
	entries   []*questionEntry
	consumers map[string]QuestionConsumer
	closed    bool
	seq       uint64
	changed   chan struct{}
}

func newQuestionQueue() *questionQueue {
	return &questionQueue{
		consumers: make(map[string]QuestionConsumer),
		changed:   make(chan struct{}, 1),
	}
}

// signalChanged emits a non-blocking notification on the changed channel.
// Must be called WITHOUT holding mu (or with: still non-blocking, but discipline).
func (q *questionQueue) signalChanged() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

func (q *questionQueue) changedCh() <-chan struct{} {
	return q.changed
}

// ask enqueues req and blocks until a response is delivered or ctx cancels.
// Empty RequestID is an error. If the queue is closed, returns OutcomeSessionEnded
// immediately. If no consumers are registered, returns OutcomeTUIUnavailable
// immediately (without enqueueing).
func (q *questionQueue) ask(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	if req.RequestID == "" {
		return QuestionResponse{}, fmt.Errorf("ask: RequestID must not be empty")
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return QuestionResponse{
			RequestID: req.RequestID,
			Outcome:   OutcomeSessionEnded,
			Note:      "supervisor shutdown",
		}, nil
	}
	if len(q.consumers) == 0 {
		q.mu.Unlock()
		return QuestionResponse{
			RequestID: req.RequestID,
			Outcome:   OutcomeTUIUnavailable,
		}, nil
	}

	q.seq++
	entry := &questionEntry{
		pq: &PendingQuestion{
			Req: req,
			Seq: q.seq,
		},
		respCh: make(chan QuestionResponse, 1),
	}
	q.entries = append(q.entries, entry)
	// Snapshot consumers under the lock so we can fan out without it.
	consumers := make([]QuestionConsumer, 0, len(q.consumers))
	for _, c := range q.consumers {
		consumers = append(consumers, c)
	}
	q.mu.Unlock()

	q.signalChanged()
	for _, c := range consumers {
		c.OnEnqueue(entry.pq)
	}

	select {
	case resp := <-entry.respCh:
		return resp, nil
	case <-ctx.Done():
		// Caller's ctx fired without Retire/Kill having drained the queue
		// first (Retire/Kill use cancelByAgent which removes the entry before
		// the caller's ctx can fire). Treat as session-ended; the structured
		// response carries the outcome, so we return a nil error so callers
		// surface the response rather than the bare ctx error.
		q.cancelInternal(req.RequestID, OutcomeSessionEnded, ctx.Err().Error())
		return QuestionResponse{
			RequestID: req.RequestID,
			Outcome:   OutcomeSessionEnded,
			Note:      ctx.Err().Error(),
		}, nil
	}
}

// resolve delivers resp to the request whose RequestID matches id. Returns
// true on first successful resolution, false on duplicate / unknown ID. Safe
// for concurrent callers — only one wins per question.
func (q *questionQueue) resolve(id string, resp QuestionResponse) bool {
	q.mu.Lock()
	entry, idx := q.findLocked(id)
	if entry == nil || entry.done {
		q.mu.Unlock()
		return false
	}
	entry.done = true
	q.removeAtLocked(idx)
	respCh := entry.respCh
	q.mu.Unlock()

	respCh <- resp
	q.signalChanged()
	return true
}

// cancel cancels the request with the given ID and reason. Idempotent. Fires
// OnCancel on every registered consumer (outside the lock).
func (q *questionQueue) cancel(id, reason string) bool {
	return q.cancelInternal(id, OutcomeSessionEnded, reason)
}

// cancelInternal is the shared cancel path used by cancel(), ctx-cancellation,
// and cancelByAgent — each supplies the outcome it wants in the response.
func (q *questionQueue) cancelInternal(id, outcome, reason string) bool {
	q.mu.Lock()
	entry, idx := q.findLocked(id)
	if entry == nil || entry.done {
		q.mu.Unlock()
		return false
	}
	entry.done = true
	q.removeAtLocked(idx)
	respCh := entry.respCh
	consumers := make([]QuestionConsumer, 0, len(q.consumers))
	for _, c := range q.consumers {
		consumers = append(consumers, c)
	}
	q.mu.Unlock()

	respCh <- QuestionResponse{
		RequestID: id,
		Outcome:   outcome,
		Note:      reason,
	}
	for _, c := range consumers {
		c.OnCancel(id, reason)
	}
	q.signalChanged()
	return true
}

// cancelByAgent cancels every pending question whose Req.From == agentName,
// using OutcomeAgentRetired and the supplied reason. Fires OnCancel for each.
func (q *questionQueue) cancelByAgent(agentName, reason string) {
	q.mu.Lock()
	var victims []*questionEntry
	kept := q.entries[:0]
	for _, e := range q.entries {
		if !e.done && e.pq.Req.From == agentName {
			e.done = true
			victims = append(victims, e)
			continue
		}
		kept = append(kept, e)
	}
	q.entries = kept
	consumers := make([]QuestionConsumer, 0, len(q.consumers))
	for _, c := range q.consumers {
		consumers = append(consumers, c)
	}
	q.mu.Unlock()

	for _, v := range victims {
		v.respCh <- QuestionResponse{
			RequestID: v.pq.Req.RequestID,
			Outcome:   OutcomeAgentRetired,
			Note:      reason,
		}
		for _, c := range consumers {
			c.OnCancel(v.pq.Req.RequestID, reason)
		}
	}
	if len(victims) > 0 {
		q.signalChanged()
	}
}

// register adds a consumer. Errors on empty name or duplicate.
func (q *questionQueue) register(c QuestionConsumer) error {
	if c == nil {
		return fmt.Errorf("register: consumer must not be nil")
	}
	name := c.Name()
	if name == "" {
		return fmt.Errorf("register: consumer name must not be empty")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.consumers[name]; exists {
		return fmt.Errorf("register: consumer %q already registered", name)
	}
	q.consumers[name] = c
	return nil
}

// unregister removes a consumer by name. Idempotent — unknown names are silently ignored.
func (q *questionQueue) unregister(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.consumers, name)
}

// peek returns (depth, shallow-copy-of-head). Head is nil when empty.
func (q *questionQueue) peek() (int, *PendingQuestion) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.entries) == 0 {
		return 0, nil
	}
	copyPQ := *q.entries[0].pq
	return len(q.entries), &copyPQ
}

// closeAll marks the queue closed and resolves every pending entry with the
// supplied outcome+reason. After closeAll, ask() returns OutcomeSessionEnded
// immediately.
func (q *questionQueue) closeAll(outcome, reason string) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	victims := q.entries
	q.entries = nil
	for _, e := range victims {
		e.done = true
	}
	q.mu.Unlock()

	for _, v := range victims {
		v.respCh <- QuestionResponse{
			RequestID: v.pq.Req.RequestID,
			Outcome:   outcome,
			Note:      reason,
		}
	}
	if len(victims) > 0 {
		q.signalChanged()
	}
}

// findLocked locates the entry with the given RequestID. Caller MUST hold mu.
// Returns (nil, -1) if not found.
func (q *questionQueue) findLocked(id string) (*questionEntry, int) {
	for i, e := range q.entries {
		if e.pq.Req.RequestID == id {
			return e, i
		}
	}
	return nil, -1
}

// removeAtLocked deletes entries[idx] preserving order. Caller MUST hold mu.
func (q *questionQueue) removeAtLocked(idx int) {
	q.entries = append(q.entries[:idx], q.entries[idx+1:]...)
}
