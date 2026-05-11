package tui

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// Compile-time assertion: QuestionConsumer must satisfy supervisor.QuestionConsumer.
var _ supervisor.QuestionConsumer = (*QuestionConsumer)(nil)

func TestQuestionConsumer_Name_IsTUI(t *testing.T) {
	c := NewQuestionConsumer(func(tea.Msg) {})
	if got := c.Name(); got != "tui" {
		t.Errorf("Name() = %q, want %q", got, "tui")
	}
}

func TestQuestionConsumer_OnEnqueue_SendsQuestionsAvailableMsg(t *testing.T) {
	got := make(chan tea.Msg, 4)
	c := NewQuestionConsumer(func(m tea.Msg) { got <- m })

	pq := &supervisor.PendingQuestion{
		Req: supervisor.QuestionRequest{RequestID: "r1", From: "weave"},
		Seq: 1,
	}
	c.OnEnqueue(pq)

	select {
	case msg := <-got:
		qa, ok := msg.(QuestionsAvailableMsg)
		if !ok {
			t.Fatalf("got %T, want QuestionsAvailableMsg", msg)
		}
		if qa.Head != pq {
			t.Errorf("Head = %p, want %p", qa.Head, pq)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for QuestionsAvailableMsg")
	}
}

func TestQuestionConsumer_OnCancel_SendsCancelQuestionMsg(t *testing.T) {
	got := make(chan tea.Msg, 4)
	c := NewQuestionConsumer(func(m tea.Msg) { got <- m })

	c.OnCancel("req-1", "retired")

	select {
	case msg := <-got:
		cq, ok := msg.(CancelQuestionMsg)
		if !ok {
			t.Fatalf("got %T, want CancelQuestionMsg", msg)
		}
		if cq.RequestID != "req-1" {
			t.Errorf("RequestID = %q, want req-1", cq.RequestID)
		}
		if cq.Reason != "retired" {
			t.Errorf("Reason = %q, want retired", cq.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for CancelQuestionMsg")
	}
}

func TestQuestionConsumer_ConcurrentOnEnqueue(t *testing.T) {
	got := make(chan tea.Msg, 64)
	c := NewQuestionConsumer(func(m tea.Msg) { got <- m })

	const N = 16
	pqs := make([]*supervisor.PendingQuestion, N)
	for i := range pqs {
		pqs[i] = &supervisor.PendingQuestion{
			Req: supervisor.QuestionRequest{RequestID: "r", From: "w"},
			Seq: uint64(i),
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(pq *supervisor.PendingQuestion) {
			defer wg.Done()
			<-start
			c.OnEnqueue(pq)
		}(pqs[i])
	}
	close(start)
	wg.Wait()

	deadline := time.After(2 * time.Second)
	seen := make(map[uint64]bool, N)
	for len(seen) < N {
		select {
		case msg := <-got:
			qa, ok := msg.(QuestionsAvailableMsg)
			if !ok {
				t.Fatalf("got %T, want QuestionsAvailableMsg", msg)
			}
			if qa.Head == nil {
				t.Fatalf("QuestionsAvailableMsg.Head is nil")
			}
			seen[qa.Head.Seq] = true
		case <-deadline:
			t.Fatalf("only received %d/%d distinct Seqs before timeout; seen=%v", len(seen), N, seen)
		}
	}
	for i := uint64(0); i < N; i++ {
		if !seen[i] {
			t.Errorf("expected to observe Seq=%d, did not", i)
		}
	}
}
