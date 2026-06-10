// Package supervisortest provides test helpers for code that depends on the
// supervisor.Supervisor interface.
//
// NoopSupervisor is a do-nothing implementation that test mocks can embed and
// selectively override. When the Supervisor interface grows, only this file
// needs updating — embedding mocks continue to compile. See QUM-531.
package supervisortest

import (
	"context"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// NoopSupervisor implements supervisor.Supervisor with no-op / zero-value
// returns for every method. Embed it in test mocks and override only the
// methods the test cares about.
type NoopSupervisor struct{}

// Compile-time guarantee that NoopSupervisor satisfies supervisor.Supervisor.
// Any new method added to the interface surfaces as a build error here, in one
// place, instead of breaking every test file that embeds a mock.
var _ supervisor.Supervisor = (*NoopSupervisor)(nil)

func (*NoopSupervisor) Spawn(context.Context, supervisor.SpawnRequest) (*supervisor.AgentInfo, error) {
	return nil, nil
}

func (*NoopSupervisor) Status(context.Context) ([]supervisor.AgentInfo, error) {
	return nil, nil
}

func (*NoopSupervisor) Delegate(context.Context, string, string, bool) error { return nil }

func (*NoopSupervisor) Merge(context.Context, string, string, string, bool) (*supervisor.MergeOutcome, error) {
	return nil, nil
}

func (*NoopSupervisor) Retire(context.Context, string, string, bool, bool, bool, bool) error {
	return nil
}

func (*NoopSupervisor) Kill(context.Context, string) error { return nil }

func (*NoopSupervisor) Pause(context.Context, string, supervisor.PauseOptions) (*supervisor.PauseResult, error) {
	return nil, nil
}

func (*NoopSupervisor) Wake(context.Context, string, agent.WakeReason, string) (*supervisor.WakeResult, error) {
	return nil, nil
}

func (*NoopSupervisor) InduceTerminalFault(context.Context, string, error) error { return nil }

func (*NoopSupervisor) RecoverAgents(context.Context) (int, int, []error) { return 0, 0, nil }

func (*NoopSupervisor) Shutdown(context.Context) error { return nil }

func (*NoopSupervisor) Handoff(context.Context, string) error { return nil }

func (*NoopSupervisor) HandoffRequested() <-chan struct{} { return nil }

func (*NoopSupervisor) PeekActivity(context.Context, string, int) ([]agentloop.ActivityEntry, error) {
	return nil, nil
}

func (*NoopSupervisor) SendMessage(context.Context, string, string, bool, bool) (*supervisor.SendMessageResult, error) {
	return nil, nil
}

func (*NoopSupervisor) Peek(context.Context, string, int) (*supervisor.PeekResult, error) {
	return nil, nil
}

func (*NoopSupervisor) ReportStatus(context.Context, string, string, string) (*supervisor.ReportStatusResult, error) {
	return nil, nil
}

func (*NoopSupervisor) MessagesList(context.Context, string, int) (*supervisor.MessagesListResult, error) {
	return nil, nil
}

func (*NoopSupervisor) MessagesRead(context.Context, string) (*supervisor.MessagesReadResult, error) {
	return nil, nil
}

func (*NoopSupervisor) MessagesArchive(context.Context, string) (*supervisor.MessagesArchiveResult, error) {
	return nil, nil
}

func (*NoopSupervisor) MessagesArchiveAll(context.Context, string) (*supervisor.MessagesArchiveAllResult, error) {
	return nil, nil
}

func (*NoopSupervisor) MessagesPeek(context.Context) (*supervisor.MessagesPeekResult, error) {
	return nil, nil
}

func (*NoopSupervisor) RuntimeRegistry() *supervisor.RuntimeRegistry { return nil }

func (*NoopSupervisor) RegisterRootRuntime(string, supervisor.RuntimeHandle, *state.AgentState) (*supervisor.AgentRuntime, error) {
	return nil, nil
}

func (*NoopSupervisor) AskUserQuestion(context.Context, supervisor.QuestionRequest) (supervisor.QuestionResponse, error) {
	return supervisor.QuestionResponse{}, nil
}

func (*NoopSupervisor) RegisterQuestionConsumer(supervisor.QuestionConsumer) error { return nil }

func (*NoopSupervisor) UnregisterQuestionConsumer(string) {}

func (*NoopSupervisor) ResolveQuestion(string, supervisor.QuestionResponse) bool { return false }

func (*NoopSupervisor) CancelQuestion(string, string) bool { return false }

func (*NoopSupervisor) CancelByAgent(string, string) {}

func (*NoopSupervisor) QuestionsChanged() <-chan struct{} { return nil }

func (*NoopSupervisor) PeekQuestions() (int, *supervisor.PendingQuestion) { return 0, nil }
