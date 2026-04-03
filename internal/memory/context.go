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
	agentLister               func(string) ([]*state.AgentState, error)
	messageLister             func(string, string, string) ([]*messages.Message, error)
	sessionLister             func(string, int) ([]Session, []string, error)
	timelineLister            func(string) ([]TimelineEntry, error)
	clock                     func() time.Time
	budget                    *BudgetConfig
	persistentKnowledgeReader func(string) (string, error)
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

// WithPersistentKnowledgeReader injects a custom persistent knowledge reader.
func WithPersistentKnowledgeReader(fn func(string) (string, error)) BuildOption {
	return func(c *buildConfig) { c.persistentKnowledgeReader = fn }
}

// WithBudgetConfig enables budget enforcement for the context blob.
func WithBudgetConfig(bc BudgetConfig) BuildOption {
	return func(c *buildConfig) { c.budget = &bc }
}

// BuildContextBlob assembles a structured markdown blob from recent sessions,
// active agent status, and pending inbox messages. It is resilient: if one data
// source fails, partial results are returned with an error marker in the
// affected section. The returned string is always a best-effort blob; the error
// is non-nil if any section had errors.
func BuildContextBlob(dendraRoot string, rootName string, opts ...BuildOption) (string, error) {
	cfg := buildConfig{
		agentLister:               state.ListAgents,
		messageLister:             messages.List,
		sessionLister:             ListRecentSessions,
		timelineLister:            ReadTimeline,
		clock:                     time.Now,
		persistentKnowledgeReader: ReadPersistentKnowledge,
	}
	for _, o := range opts {
		o(&cfg)
	}

	var errs []error

	// Render Active State section
	activeState, activeErr := renderActiveState(dendraRoot, rootName, cfg)
	if activeErr != nil {
		errs = append(errs, activeErr)
	}

	// Render Persistent Knowledge section
	persistentKnowledge, pkErr := renderPersistentKnowledge(dendraRoot, cfg)
	if pkErr != nil {
		errs = append(errs, pkErr)
	}

	// Render Timeline section (header + individual entries)
	timelineHeader, timelineEntries, timelineErr := renderTimeline(dendraRoot, cfg)
	if timelineErr != nil {
		errs = append(errs, timelineErr)
	}

	// Render Sessions
	sessionStrings, sessErr := renderSessions(dendraRoot, cfg)
	if sessErr != nil {
		errs = append(errs, sessErr)
	}

	// Render Footer
	footer := renderFooter(cfg)

	var result string
	if cfg.budget != nil {
		result = assembleBudgeted(activeState, persistentKnowledge, timelineHeader, timelineEntries, sessionStrings, footer, cfg.budget)
	} else {
		result = assembleUnbudgeted(activeState, persistentKnowledge, timelineHeader, timelineEntries, sessionStrings, footer)
	}

	var combinedErr error
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		combinedErr = fmt.Errorf("context blob errors: %s", strings.Join(msgs, "; "))
	}

	return result, combinedErr
}

// renderActiveState produces the "## Active State" section string, including
// the section header.
func renderActiveState(dendraRoot, rootName string, cfg buildConfig) (string, error) {
	var b strings.Builder
	var errs []error

	b.WriteString("## Active State\n\n")

	b.WriteString("### Agents\n")
	if agentErr := writeAgentsSection(&b, dendraRoot, cfg.agentLister); agentErr != nil {
		errs = append(errs, agentErr)
	}

	b.WriteString("\n### Pending Inbox\n")
	if inboxErr := writeInboxSection(&b, dendraRoot, rootName, cfg.messageLister); inboxErr != nil {
		errs = append(errs, inboxErr)
	}

	var combinedErr error
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		combinedErr = fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	return b.String(), combinedErr
}

// renderPersistentKnowledge produces the "## Persistent Knowledge" section string.
func renderPersistentKnowledge(dendraRoot string, cfg buildConfig) (string, error) {
	if cfg.persistentKnowledgeReader == nil {
		return "", nil
	}
	content, err := cfg.persistentKnowledgeReader(dendraRoot)
	if err != nil {
		return fmt.Sprintf("\n## Persistent Knowledge\n\n[Error reading persistent knowledge: %s]\n", err), err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", nil
	}
	return "\n## Persistent Knowledge\n\n" + content + "\n", nil
}

// renderTimeline returns the timeline header and individual entry strings.
// If there are no entries (and no error), header is empty and entries is nil.
func renderTimeline(dendraRoot string, cfg buildConfig) (string, []string, error) {
	entries, err := cfg.timelineLister(dendraRoot)
	if err != nil {
		header := "\n## Session Timeline\n"
		errLine := fmt.Sprintf("[Error reading timeline: %s]\n", err)
		return header, []string{errLine}, err
	}

	if len(entries) == 0 {
		return "", nil, nil
	}

	header := "\n## Session Timeline\n"
	strs := make([]string, len(entries))
	for i, e := range entries {
		strs[i] = fmt.Sprintf("- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary)
	}
	return header, strs, nil
}

// renderSessions returns individual session strings (each including the ### header).
func renderSessions(dendraRoot string, cfg buildConfig) ([]string, error) {
	sessions, bodies, err := cfg.sessionLister(dendraRoot, 3)
	if err != nil {
		errStr := fmt.Sprintf("\n[Error reading sessions: %s]\n", err)
		return []string{errStr}, err
	}

	if len(sessions) == 0 {
		return []string{"\nNo previous sessions.\n"}, nil
	}

	result := make([]string, len(sessions))
	for i, s := range sessions {
		ts := s.Timestamp.UTC().Format(time.RFC3339)
		var sb strings.Builder
		fmt.Fprintf(&sb, "\n### Session: %s (%s)\n", s.SessionID, ts)
		if i < len(bodies) {
			sb.WriteString(bodies[i])
			sb.WriteString("\n")
		}
		result[i] = sb.String()
	}
	return result, nil
}

// renderFooter produces the deterministic footer string.
func renderFooter(cfg buildConfig) string {
	now := cfg.clock()
	var b strings.Builder
	b.WriteString("\n---\n")
	fmt.Fprintf(&b, "*This system prompt was generated at %s. If this session runs for an extended period, the current time may differ.*\n", now.UTC().Format(time.RFC3339))
	return b.String()
}

// assembleUnbudgeted concatenates all sections without any size limits (backward compat).
func assembleUnbudgeted(activeState, persistentKnowledge, timelineHeader string, timelineEntries, sessionStrings []string, footer string) string {
	var b strings.Builder
	b.WriteString(activeState)
	if persistentKnowledge != "" {
		b.WriteString(persistentKnowledge)
	}
	if timelineHeader != "" {
		b.WriteString(timelineHeader)
		for _, e := range timelineEntries {
			b.WriteString(e)
		}
	}
	b.WriteString("\n## Recent Sessions\n")
	for _, s := range sessionStrings {
		b.WriteString(s)
	}
	b.WriteString(footer)
	return b.String()
}

// assembleBudgeted assembles sections under a token budget.
func assembleBudgeted(activeState, persistentKnowledge, timelineHeader string, timelineEntries, sessionStrings []string, footer string, budget *BudgetConfig) string {
	bc := *budget
	if bc.MaxTotalChars == 0 {
		bc = DefaultBudgetConfig()
	}

	totalBudget := bc.MaxTotalChars
	footerSize := MeasureBytes(footer)
	remaining := totalBudget - footerSize

	var b strings.Builder

	// If footer alone exceeds or equals budget, truncate everything into budget.
	if remaining <= 0 {
		return TruncateWithNote(activeState+footer, totalBudget)
	}

	// 1. Active State (highest priority)
	activeSize := MeasureBytes(activeState)
	if activeSize > remaining {
		// Truncate active state; all other sections omitted.
		b.WriteString(TruncateWithNote(activeState, remaining))
		b.WriteString(footer)
		return b.String()
	}
	b.WriteString(activeState)
	remaining -= activeSize

	// 2. Persistent Knowledge (second priority)
	if persistentKnowledge != "" {
		pkSize := MeasureBytes(persistentKnowledge)
		if pkSize <= remaining {
			b.WriteString(persistentKnowledge)
			remaining -= pkSize
		} else {
			b.WriteString(TruncateWithNote(persistentKnowledge, remaining))
			remaining = 0
		}
	}

	// 3. Timeline (third priority)
	if timelineHeader != "" && len(timelineEntries) > 0 {
		// Calculate full timeline size
		fullTimelineSize := MeasureBytes(timelineHeader)
		for _, e := range timelineEntries {
			fullTimelineSize += MeasureBytes(e)
		}

		if fullTimelineSize <= remaining {
			// Fits entirely
			b.WriteString(timelineHeader)
			for _, e := range timelineEntries {
				b.WriteString(e)
			}
			remaining -= fullTimelineSize
		} else {
			// Try to fit with truncation from oldest end
			truncNote := "[Timeline truncated to fit budget]\n"
			headerPlusTrunc := MeasureBytes(timelineHeader) + MeasureBytes(truncNote)

			if headerPlusTrunc <= remaining {
				// Drop oldest entries until it fits
				start := 0
				for start < len(timelineEntries) {
					size := headerPlusTrunc
					for j := start; j < len(timelineEntries); j++ {
						size += MeasureBytes(timelineEntries[j])
					}
					if size <= remaining {
						break
					}
					start++
				}

				if start < len(timelineEntries) {
					b.WriteString(timelineHeader)
					b.WriteString(truncNote)
					for j := start; j < len(timelineEntries); j++ {
						b.WriteString(timelineEntries[j])
					}
					size := headerPlusTrunc
					for j := start; j < len(timelineEntries); j++ {
						size += MeasureBytes(timelineEntries[j])
					}
					remaining -= size
				}
				// If start >= len(timelineEntries), nothing fits, omit entirely
			}
			// If header+truncNote doesn't fit, omit timeline entirely
		}
	}

	// 4. Sessions header: include if budget remains.
	sessionsHeader := "\n## Recent Sessions\n"
	sessionsHeaderSize := MeasureBytes(sessionsHeader)
	if sessionsHeaderSize <= remaining {
		remaining -= sessionsHeaderSize

		// Apply per-session truncation.
		if bc.MaxSessionChars > 0 {
			for i := range sessionStrings {
				sessionStrings[i] = TruncateWithNote(sessionStrings[i], bc.MaxSessionChars)
			}
		}

		// Allocate newest-first: iterate from newest to oldest, include if fits.
		// Reserve space for a possible omission note before allocating sessions.
		included := make([]bool, len(sessionStrings))
		includedCount := 0
		for i := len(sessionStrings) - 1; i >= 0; i-- {
			size := MeasureBytes(sessionStrings[i])
			if size <= remaining {
				included[i] = true
				remaining -= size
				includedCount++
			}
		}

		// Calculate omission note (accounts for its size in the budget).
		omitted := len(sessionStrings) - includedCount
		var omissionNote string
		if omitted > 0 {
			noun := "sessions"
			if omitted == 1 {
				noun = "session"
			}
			omissionNote = fmt.Sprintf("\n%d older %s omitted\n", omitted, noun)
			noteSize := MeasureBytes(omissionNote)
			// If the note doesn't fit, drop the oldest included session to make room.
			for noteSize > remaining && includedCount > 0 {
				for j := 0; j < len(sessionStrings); j++ {
					if included[j] {
						included[j] = false
						remaining += MeasureBytes(sessionStrings[j])
						includedCount--
						omitted++
						break
					}
				}
				noun = "sessions"
				if omitted == 1 {
					noun = "session"
				}
				omissionNote = fmt.Sprintf("\n%d older %s omitted\n", omitted, noun)
				noteSize = MeasureBytes(omissionNote)
			}
			// If note still doesn't fit after dropping all sessions, suppress it.
			if noteSize > remaining {
				omissionNote = ""
			} else {
				remaining -= noteSize
			}
		}

		b.WriteString(sessionsHeader)

		if omissionNote != "" {
			b.WriteString(omissionNote)
		}

		// Display in oldest-first order.
		for i, s := range sessionStrings {
			if included[i] {
				b.WriteString(s)
			}
		}
	}

	b.WriteString(footer)
	return b.String()
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

