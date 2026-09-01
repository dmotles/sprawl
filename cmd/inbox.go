// cmd/inbox.go — `sprawl inbox`, the human's side of the event log
// (QUM-1252, M3a slice 7b).
//
// This is NOT the agent maildir. `inbox` is already the word this codebase uses
// for inter-agent messages (internal/messages, `messages_read`), and the two
// have nothing to do with each other: that one is agents talking to agents, this
// one holds `user_question` contracts an agent opened with the `ask_user` tool
// and is answered by a person. The help text says so, because a reader who
// conflates them will come here looking for agent messages and conclude the
// fleet is quiet.
//
// Nothing pokes the human. A question sits here until someone reads it, which is
// the entire difference from `ask_user_question` — and the reason the stall
// sweeper must never treat an open `user_question` as a stalled agent.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/store"
)

// userInbox is the surface this command needs. Declared here rather than in
// store because the consumer defines the interface; *store.Ledger satisfies it.
//
// Enabled and DegradedError are members for the reason GoalSource carries
// Enabled: a nil *store.Ledger IS the disabled store, and a typed-nil in an
// interface is a NON-nil interface value, so a nil check here would read a
// switched-off store as a working one.
type userInbox interface {
	Enabled() bool
	DegradedError() error
	OpenUserQuestions(ctx context.Context) ([]store.UserQuestion, error)
	AnswerUserQuestion(ctx context.Context, questionEventID uuid.UUID, answer, answeredBy string) (uuid.UUID, error)
}

type inboxDeps struct {
	SprawlRoot string
	OpenInbox  func(ctx context.Context, sprawlRoot string) (userInbox, error)
	// Whoami attributes the answer. Taken from the machine rather than a flag:
	// the store refuses an unattributed answer, and a flag people would have to
	// remember is one they would be tempted to make optional.
	Whoami func() string
	Stdout io.Writer
	Stderr io.Writer
}

var defaultInboxDeps *inboxDeps

func resolveInboxDeps() *inboxDeps {
	if defaultInboxDeps != nil {
		return defaultInboxDeps
	}
	return &inboxDeps{
		SprawlRoot: os.Getenv("SPRAWL_ROOT"),
		OpenInbox: func(ctx context.Context, sprawlRoot string) (userInbox, error) {
			return store.Process(ctx, sprawlRoot)
		},
		Whoami: currentUserName,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

// currentUserName names the human answering. It falls back to the login env
// vars and finally to a literal, because a *wrong-but-present* attribution is
// still better than none here: the store refuses an empty one outright, and
// refusing to let someone answer a question because their passwd entry is
// unreadable would be a worse failure than recording "unknown-operator".
func currentUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, k := range []string{"USER", "LOGNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "unknown-operator"
}

// openInbox runs the shared gate: root, then off, then unreachable. The three
// are separate refusals on purpose — an operator told "nothing is waiting" on a
// host where the store is switched off has been told their agents are unblocked
// when nobody is even recording the questions.
func openInbox(ctx context.Context, deps *inboxDeps) (userInbox, error) {
	if deps.SprawlRoot == "" {
		return nil, fmt.Errorf("SPRAWL_ROOT environment variable is not set, so there is no project whose inbox to read\nnext: run this from inside a sprawl session, or set SPRAWL_ROOT to the repo root")
	}
	inbox, err := deps.OpenInbox(ctx, deps.SprawlRoot)
	if err != nil {
		return nil, err
	}
	if !inbox.Enabled() {
		return nil, fmt.Errorf("the event log is disabled on this host, so no agent can leave you a question\nnext: enable it with `sprawl config set event_log.enabled true`, then see docs/event-log-setup.md")
	}
	if derr := inbox.DegradedError(); derr != nil {
		// Refused rather than answered from a spill file. An inbox is read to
		// decide whether anyone is waiting on you, and a partial one answers
		// that question wrongly in the direction that looks fine.
		// No second "next:" line. The wrapped cause usually carries its own, and
		// two competing next steps stacked under one error is how a reader ends
		// up following neither.
		return nil, fmt.Errorf("the event log is unreachable, so your inbox cannot be read (`sprawl store doctor` diagnoses the connection): %w", derr)
	}
	return inbox, nil
}

func runInboxList(ctx context.Context, deps *inboxDeps) error {
	inbox, err := openInbox(ctx, deps)
	if err != nil {
		return err
	}
	questions, err := inbox.OpenUserQuestions(ctx)
	if err != nil {
		return fmt.Errorf("reading your inbox: %w", err)
	}
	if len(questions) == 0 {
		fmt.Fprintln(deps.Stdout, "no questions are waiting for you.")
		return nil
	}

	fmt.Fprintf(deps.Stdout, "%d question(s) waiting, oldest first:\n\n", len(questions))
	for _, q := range questions {
		fmt.Fprintf(deps.Stdout, "  %s\n", q.EventID)
		fmt.Fprintf(deps.Stdout, "    from:  %s (%s ago)\n", q.Asker, humaneAge(time.Since(q.AskedAt)))
		fmt.Fprintf(deps.Stdout, "    asks:  %s\n", q.Question)
		if q.Context != "" {
			fmt.Fprintf(deps.Stdout, "    about: %s\n", q.Context)
		}
		if q.GoalEventID != "" {
			fmt.Fprintf(deps.Stdout, "    goal:  %s\n", q.GoalEventID)
		}
		fmt.Fprintln(deps.Stdout)
	}
	fmt.Fprintln(deps.Stdout, "answer one with: sprawl inbox answer <id> \"your answer\"")
	return nil
}

// humaneAge renders how long someone has been waiting. Coarse on purpose: the
// decision this informs is "has this been sitting too long", and a figure to the
// second invites reading precision into a queue nothing polls.
func humaneAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func runInboxAnswer(ctx context.Context, deps *inboxDeps, id, answer string) error {
	// Parsed and checked BEFORE the store is opened, so a refusal cannot append.
	questionID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("%q is not a uuid; it is the id `sprawl inbox` prints above each question: %w", id, err)
	}
	if answer == "" {
		return fmt.Errorf("the answer is empty; the agent that asked will act on whatever this says")
	}

	inbox, err := openInbox(ctx, deps)
	if err != nil {
		return err
	}
	// Read the open set first so the confirmation can name who is unblocked.
	// This is also what makes "already answered" a clean message rather than a
	// bare store error: the question is simply no longer here.
	questions, err := inbox.OpenUserQuestions(ctx)
	if err != nil {
		return fmt.Errorf("reading your inbox: %w", err)
	}
	var found *store.UserQuestion
	for i := range questions {
		if questions[i].EventID == questionID {
			found = &questions[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("%s is not an open question — it may already have been answered\nnext: run `sprawl inbox` to see what is actually waiting", questionID)
	}

	if _, err := inbox.AnswerUserQuestion(ctx, questionID, answer, deps.Whoami()); err != nil {
		return fmt.Errorf("recording your answer: %w", err)
	}
	fmt.Fprintf(deps.Stdout, "answered %s's question.\n", found.Asker)
	// Said plainly because it is the surprising part: answering does not wake
	// anyone. The agent sees it when it next reads its goal's log.
	fmt.Fprintf(deps.Stdout, "%s is not notified — it will see your answer the next time it reads its goal's log.\n", found.Asker)
	return nil
}

var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Read and answer questions agents have left for you",
	Long: "Read and answer the questions agents have left for you.\n\n" +
		"These are `user_question` contracts opened with the `ask_user` tool: durable " +
		"questions that survive a restart and wait until you get to them. Nothing pokes " +
		"you, and answering does not wake the asker — it sees your answer the next time " +
		"it reads its goal's log.\n\n" +
		"This is NOT agent messages. Inter-agent mail is a separate system entirely — " +
		"agents read it with the `messages_read` tool, and it has no CLI surface at all; " +
		"nothing an agent sends to another agent appears here.\n\n" +
		"Requires the event log (`event_log.enabled`).",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runInboxList(cmd.Context(), resolveInboxDeps())
	},
}

var inboxAnswerCmd = &cobra.Command{
	Use:   "answer <question-id> <answer>",
	Short: "Answer a question an agent left for you",
	Long: "Answer one question, closing it. The id is printed above each question by " +
		"`sprawl inbox`. The answer is attributed to your login name, because an answer " +
		"nobody can trace back to a person cannot be audited in a fleet with more than " +
		"one operator.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInboxAnswer(cmd.Context(), resolveInboxDeps(), args[0], args[1])
	},
}

func init() {
	inboxCmd.AddCommand(inboxAnswerCmd)
	rootCmd.AddCommand(inboxCmd)
}
