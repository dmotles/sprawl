package memory

import (
	"fmt"
	"strings"
	"time"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
)

// BuildOption configures BuildContextBlob behavior.
type BuildOption func(*buildConfig)

type buildConfig struct {
	agentLister    func(string) ([]*state.AgentState, error)
	messageLister  func(string, string, string) ([]*messages.Message, error)
	sessionLister  func(string, int) ([]Session, []string, error)
	timelineLister func(string) ([]TimelineEntry, error)
	clock          func() time.Time
}

// WithAgentLister injects a custom agent listing function.
func WithAgentLister(fn func(string) ([]*state.AgentState, error)) BuildOption {
	return func(c *buildConfig) { c.agentLister = fn }
}

// WithMessageLister injects a custom message listing function.
func WithMessageLister(fn func(string, string, string) ([]*messages.Message, error)) BuildOption {
	return func(c *buildConfig) { c.messageLister = fn }
}

// WithSessionLister injects a custom session listing function.
func WithSessionLister(fn func(string, int) ([]Session, []string, error)) BuildOption {
	return func(c *buildConfig) { c.sessionLister = fn }
}

// WithTimelineLister injects a custom timeline reading function.
func WithTimelineLister(fn func(string) ([]TimelineEntry, error)) BuildOption {
	return func(c *buildConfig) { c.timelineLister = fn }
}

// WithClock injects a custom time source.
func WithClock(fn func() time.Time) BuildOption {
	return func(c *buildConfig) { c.clock = fn }
}

// BuildContextBlob assembles a structured markdown blob from recent sessions,
// active agent status, and pending inbox messages. It is resilient: if one data
// source fails, partial results are returned with an error marker in the
// affected section. The returned string is always a best-effort blob; the error
// is non-nil if any section had errors.
func BuildContextBlob(dendraRoot string, rootName string, opts ...BuildOption) (string, error) {
	cfg := buildConfig{
		agentLister:    state.ListAgents,
		messageLister:  messages.List,
		sessionLister:  ListRecentSessions,
		timelineLister: ReadTimeline,
		clock:          time.Now,
	}
	for _, o := range opts {
		o(&cfg)
	}

	var b strings.Builder
	var errs []error

	// --- Active State ---
	b.WriteString("## Active State\n\n")

	// Agents section
	b.WriteString("### Agents\n")
	if agentErr := writeAgentsSection(&b, dendraRoot, cfg.agentLister); agentErr != nil {
		errs = append(errs, agentErr)
	}

	// Pending Inbox section
	b.WriteString("\n### Pending Inbox\n")
	if inboxErr := writeInboxSection(&b, dendraRoot, rootName, cfg.messageLister); inboxErr != nil {
		errs = append(errs, inboxErr)
	}

	// --- Session Timeline ---
	if timelineErr := writeTimelineSection(&b, dendraRoot, cfg.timelineLister); timelineErr != nil {
		errs = append(errs, timelineErr)
	}

	// --- Recent Sessions ---
	b.WriteString("\n## Recent Sessions\n")
	if sessErr := writeSessionsSection(&b, dendraRoot, cfg.sessionLister); sessErr != nil {
		errs = append(errs, sessErr)
	}

	// Footer
	now := cfg.clock()
	b.WriteString("\n---\n")
	fmt.Fprintf(&b, "*This system prompt was generated at %s. If this session runs for an extended period, the current time may differ.*\n", now.UTC().Format(time.RFC3339))

	var combinedErr error
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		combinedErr = fmt.Errorf("context blob errors: %s", strings.Join(msgs, "; "))
	}

	return b.String(), combinedErr
}

func writeAgentsSection(b *strings.Builder, dendraRoot string, lister func(string) ([]*state.AgentState, error)) error {
	agents, err := lister(dendraRoot)
	if err != nil {
		fmt.Fprintf(b, "[Error listing agents: %s]\n", err)
		return err
	}

	var active []*state.AgentState
	for _, a := range agents {
		if a.Status != "done" && a.Status != "retired" {
			active = append(active, a)
		}
	}

	if len(active) == 0 {
		b.WriteString("No active agents.\n")
		return nil
	}

	for _, a := range active {
		fmt.Fprintf(b, "- %s | %s | %s | %s | last report: %s\n",
			a.Name, a.Type, a.Family, a.Status, a.LastReportType)
	}
	return nil
}

func writeInboxSection(b *strings.Builder, dendraRoot, rootName string, lister func(string, string, string) ([]*messages.Message, error)) error {
	msgs, err := lister(dendraRoot, rootName, "unread")
	if err != nil {
		fmt.Fprintf(b, "[Error reading inbox: %s]\n", err)
		return err
	}

	if len(msgs) == 0 {
		b.WriteString("No pending messages.\n")
		return nil
	}

	for _, m := range msgs {
		fmt.Fprintf(b, "- From %s: \"%s\" (%s)\n", m.From, m.Subject, m.Timestamp)
	}
	return nil
}

func writeTimelineSection(b *strings.Builder, dendraRoot string, lister func(string) ([]TimelineEntry, error)) error {
	entries, err := lister(dendraRoot)
	if err != nil {
		b.WriteString("\n## Session Timeline\n")
		fmt.Fprintf(b, "[Error reading timeline: %s]\n", err)
		return err
	}

	if len(entries) == 0 {
		return nil
	}

	b.WriteString("\n## Session Timeline\n")
	for _, e := range entries {
		fmt.Fprintf(b, "- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
	}
	return nil
}

func writeSessionsSection(b *strings.Builder, dendraRoot string, lister func(string, int) ([]Session, []string, error)) error {
	sessions, bodies, err := lister(dendraRoot, 3)
	if err != nil {
		fmt.Fprintf(b, "\n[Error reading sessions: %s]\n", err)
		return err
	}

	if len(sessions) == 0 {
		b.WriteString("\nNo previous sessions.\n")
		return nil
	}

	for i, s := range sessions {
		ts := s.Timestamp.UTC().Format(time.RFC3339)
		fmt.Fprintf(b, "\n### Session: %s (%s)\n", s.SessionID, ts)
		if i < len(bodies) {
			b.WriteString(bodies[i])
			b.WriteString("\n")
		}
	}
	return nil
}
