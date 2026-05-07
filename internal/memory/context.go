package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/messages"
)

// canonicalContextFooter is the literal footer string appended to every
// context blob. It points the root agent at the on-disk session index +
// per-session handoff files, replacing the inline timeline/sessions render
// that the old BuildContextBlob emitted.
const canonicalContextFooter = "Read `.sprawl/memory/timeline.md` for the full session index. " +
	"Read `.sprawl/memory/sessions/<id>.md` for the full handoff of any session."

// BuildOption configures BuildContextBlob behavior.
type BuildOption func(*buildConfig)

type buildConfig struct {
	messageLister             func(string, string, string) ([]*messages.Message, error)
	persistentKnowledgeReader func(string) (string, error)
	arcSummarizer             func(ctx context.Context, sprawlRoot string) (string, error)
	sessionLister             func(string, int) ([]Session, []string, error)
	lastSessionIDReader       func(string) (string, error)
}

// WithMessageLister injects a custom message listing function.
func WithMessageLister(fn func(string, string, string) ([]*messages.Message, error)) BuildOption {
	return func(c *buildConfig) { c.messageLister = fn }
}

// WithPersistentKnowledgeReader injects a custom persistent knowledge reader.
func WithPersistentKnowledgeReader(fn func(string) (string, error)) BuildOption {
	return func(c *buildConfig) { c.persistentKnowledgeReader = fn }
}

// WithSessionLister injects a custom session listing function. Default is
// ListRecentSessions. Used by BuildContextBlob to render the verbatim "##
// Last Session" block (QUM-521).
func WithSessionLister(fn func(string, int) ([]Session, []string, error)) BuildOption {
	return func(c *buildConfig) { c.sessionLister = fn }
}

// WithLastSessionIDReader injects a custom last-session-id reader. Default
// is ReadLastSessionID. Used to identify the currently-live session so the
// "## Last Session" block always renders the most recent SEALED session
// (QUM-521).
func WithLastSessionIDReader(fn func(string) (string, error)) BuildOption {
	return func(c *buildConfig) { c.lastSessionIDReader = fn }
}

// WithArcSummarizer injects a custom project-arc summarizer. The default
// returns an empty string; production callers in internal/rootinit wire
// memory.SummarizeProjectArc with a real Claude invoker.
func WithArcSummarizer(fn func(ctx context.Context, sprawlRoot string) (string, error)) BuildOption {
	return func(c *buildConfig) { c.arcSummarizer = fn }
}

// BuildContextBlob assembles a structured markdown blob in the new
// append-only memory model:
//
//  1. ## Project Arc — the multi-session narrative produced by the
//     injected arc summarizer.
//  2. Footer pointing at timeline.md + per-session handoff files.
//  3. ## Pending Inbox (only when there are unread messages) — a single
//     compact sentence; per-message details are intentionally omitted.
//  4. ## Persistent Knowledge — verbatim contents of persistent.md.
//
// The blob is best-effort: section errors are collected and returned as a
// combined error, but the rendered string always includes whatever did
// succeed.
func BuildContextBlob(sprawlRoot string, rootName string, opts ...BuildOption) (string, error) {
	cfg := buildConfig{
		messageLister:             messages.List,
		persistentKnowledgeReader: ReadPersistentKnowledge,
		arcSummarizer: func(_ context.Context, _ string) (string, error) {
			// Production callers in rootinit override this with
			// memory.SummarizeProjectArc; the default keeps BuildContextBlob
			// usable in contexts without an LLM seam.
			return "", nil
		},
		sessionLister:       ListRecentSessions,
		lastSessionIDReader: ReadLastSessionID,
	}
	for _, o := range opts {
		o(&cfg)
	}

	var errs []error
	var b strings.Builder

	// 1. Project Arc.
	arcSummary, arcErr := cfg.arcSummarizer(context.Background(), sprawlRoot)
	if arcErr != nil {
		errs = append(errs, fmt.Errorf("arc summarizer: %w", arcErr))
	}
	b.WriteString("## Project Arc\n\n")
	if strings.TrimSpace(arcSummary) == "" {
		b.WriteString("(no arc summary available)\n")
	} else {
		b.WriteString(strings.TrimRight(arcSummary, "\n"))
		b.WriteString("\n")
	}

	// 2. Footer.
	b.WriteString("\n")
	b.WriteString(canonicalContextFooter)
	b.WriteString("\n")

	// 2b. Last Session (verbatim, most recent SEALED — QUM-521).
	if cfg.sessionLister != nil {
		liveID := ""
		if cfg.lastSessionIDReader != nil {
			id, err := cfg.lastSessionIDReader(sprawlRoot)
			if err != nil {
				errs = append(errs, fmt.Errorf("reading last-session-id: %w", err))
			} else {
				liveID = id
			}
		}
		// N=2: newest sealed, plus one fallback in case the newest file is
		// the currently-live session (id == liveID). N=1 only ships verbatim.
		sessions, bodies, err := cfg.sessionLister(sprawlRoot, 2)
		if err != nil {
			errs = append(errs, fmt.Errorf("listing recent sessions: %w", err))
		} else {
			// ListRecentSessions returns oldest-first; iterate newest-first.
			for i := len(sessions) - 1; i >= 0; i-- {
				if sessions[i].SessionID == liveID {
					continue
				}
				body := ""
				if i < len(bodies) {
					body = bodies[i]
				}
				b.WriteString("\n## Last Session\n\n")
				fmt.Fprintf(&b, "### Session: %s (%s)\n", sessions[i].SessionID, sessions[i].Timestamp.UTC().Format(time.RFC3339))
				b.WriteString(body)
				if !strings.HasSuffix(body, "\n") {
					b.WriteString("\n")
				}
				break
			}
		}
	}

	// 3. Pending inbox (compact).
	if cfg.messageLister != nil {
		msgs, err := cfg.messageLister(sprawlRoot, rootName, "unread")
		if err != nil {
			errs = append(errs, fmt.Errorf("listing inbox: %w", err))
		} else if n := len(msgs); n > 0 {
			b.WriteString("\n## Pending Inbox\n\n")
			fmt.Fprintf(&b, "%d messages in inbox. Recommend archiving stale messages when possible.\n", n)
		}
	}

	// 4. Persistent knowledge.
	if cfg.persistentKnowledgeReader != nil {
		pk, err := cfg.persistentKnowledgeReader(sprawlRoot)
		if err != nil {
			errs = append(errs, fmt.Errorf("reading persistent knowledge: %w", err))
		} else if trimmed := strings.TrimSpace(pk); trimmed != "" {
			b.WriteString("\n## Persistent Knowledge\n\n")
			b.WriteString(trimmed)
			b.WriteString("\n")
		}
	}

	var combined error
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		combined = fmt.Errorf("context blob errors: %s", strings.Join(msgs, "; "))
	}
	return b.String(), combined
}
