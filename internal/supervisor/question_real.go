package supervisor

import (
	"context"
	"fmt"
)

// AskUserQuestion implements Supervisor.AskUserQuestion. See QUM-527 and
// docs/research/ask-user-question-mcp-design.md. The MCP tool dispatcher is
// responsible for generating req.RequestID — an empty value is an error.
func (r *Real) AskUserQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	if req.RequestID == "" {
		return QuestionResponse{}, fmt.Errorf("AskUserQuestion: RequestID must not be empty")
	}
	return r.questions.ask(ctx, req)
}

// RegisterQuestionConsumer implements Supervisor.RegisterQuestionConsumer.
func (r *Real) RegisterQuestionConsumer(c QuestionConsumer) error {
	return r.questions.register(c)
}

// UnregisterQuestionConsumer implements Supervisor.UnregisterQuestionConsumer.
func (r *Real) UnregisterQuestionConsumer(name string) {
	r.questions.unregister(name)
}

// ResolveQuestion implements Supervisor.ResolveQuestion.
func (r *Real) ResolveQuestion(id string, resp QuestionResponse) bool {
	return r.questions.resolve(id, resp)
}

// CancelQuestion implements Supervisor.CancelQuestion.
func (r *Real) CancelQuestion(id, reason string) bool {
	return r.questions.cancel(id, reason)
}

// CancelByAgent implements Supervisor.CancelByAgent.
func (r *Real) CancelByAgent(agentName, reason string) {
	r.questions.cancelByAgent(agentName, reason)
}

// QuestionsChanged implements Supervisor.QuestionsChanged.
func (r *Real) QuestionsChanged() <-chan struct{} {
	return r.questions.changedCh()
}

// PeekQuestions implements Supervisor.PeekQuestions.
func (r *Real) PeekQuestions() (int, *PendingQuestion) {
	return r.questions.peek()
}
