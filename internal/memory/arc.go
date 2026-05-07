// Package memory — arc.go implements the project-arc summarizer (QUM-516
// slice 3). Given the regenerated timeline, it produces a short
// milestone-level narrative of the project's development arc, suitable for
// embedding in the system context blob alongside per-session summaries.
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArcConfig configures the LLM summarization step of project-arc generation.
type ArcConfig struct {
	// Model is the Claude model name passed to the invoker. If empty,
	// SummarizeProjectArc substitutes a haiku-class default.
	Model string
	// MaxLines bounds the output line count. Zero falls back to 10.
	MaxLines int
	// MaxChars bounds the output character count. Zero falls back to 1200.
	MaxChars int
	// InvokeTimeout bounds each individual claude -p call. Zero falls
	// back to DefaultInvokeTimeout.
	InvokeTimeout time.Duration
}

// ArcOptions bundles all dependencies + flags for SummarizeProjectArc.
type ArcOptions struct {
	// SprawlRoot is the project root. Used to derive the default
	// timeline path when TimelinePath is empty.
	SprawlRoot string
	// TimelinePath, when non-empty, overrides
	// `<SprawlRoot>/.sprawl/memory/timeline.md`.
	TimelinePath string
	// Invoker is the LLM seam. Required.
	Invoker ClaudeInvoker
	// Cfg holds tunable summarization parameters.
	Cfg ArcConfig
}

// ValidateArcSummary enforces the line/char budget on a candidate arc
// summary string.
func ValidateArcSummary(text string, maxLines, maxChars int) error {
	if n := len(strings.Split(text, "\n")); n > maxLines {
		return fmt.Errorf("arc summary has %d lines, max %d", n, maxLines)
	}
	if n := len(text); n > maxChars {
		return fmt.Errorf("arc summary has %d chars, max %d", n, maxChars)
	}
	return nil
}

// arcPrompt builds the prompt sent to the model. The strict variant
// prepends a RETRY marker that tells the model its previous response
// blew the budget.
func arcPrompt(timeline string, strict bool) string {
	var b strings.Builder
	if strict {
		b.WriteString("RETRY: your previous response exceeded the budget. Be more concise.\n\n")
	}
	b.WriteString("Summarize this development arc in 5–10 lines focused on milestones, direction shifts, and major architectural changes. Do NOT enumerate individual sessions.\n\n")
	b.WriteString("<timeline>\n")
	b.WriteString(timeline)
	b.WriteString("\n</timeline>\n")
	return b.String()
}

// truncateForFallback returns a best-effort truncation of timeline content
// such that the literal "summarization failed\n" prefix plus the truncated
// body stays under maxChars total. The body itself is also bounded so
// strings.HasPrefix(result, "summarization failed") holds.
func truncateForFallback(timeline string, maxChars int) string {
	prefix := "summarization failed\n"
	budget := maxChars - len(prefix)
	if budget <= 0 {
		// Pathological; clip the prefix.
		if len(prefix) > maxChars {
			return prefix[:maxChars]
		}
		return prefix
	}
	body := timeline
	if len(body) > budget {
		body = body[:budget]
	}
	return prefix + body
}

// SummarizeProjectArc reads the regenerated timeline and asks the model to
// produce a short milestone-level arc summary. On budget overflow it
// retries once with a strict marker; if the second attempt also overflows,
// it returns a deterministic fallback that always starts with the literal
// "summarization failed" prefix and respects the char budget.
func SummarizeProjectArc(ctx context.Context, opts ArcOptions) (string, error) {
	if opts.Invoker == nil {
		return "", fmt.Errorf("SummarizeProjectArc: Invoker is required")
	}
	if opts.SprawlRoot == "" && opts.TimelinePath == "" {
		return "", fmt.Errorf("SummarizeProjectArc: SprawlRoot or TimelinePath is required")
	}
	if opts.Cfg.Model == "" {
		opts.Cfg.Model = "haiku"
	}
	if opts.Cfg.MaxLines == 0 {
		opts.Cfg.MaxLines = 10
	}
	if opts.Cfg.MaxChars == 0 {
		opts.Cfg.MaxChars = 1200
	}
	if opts.Cfg.InvokeTimeout == 0 {
		opts.Cfg.InvokeTimeout = DefaultInvokeTimeout
	}

	tlPath := opts.TimelinePath
	if tlPath == "" {
		tlPath = filepath.Join(opts.SprawlRoot, ".sprawl", "memory", "timeline.md")
	}
	raw, err := os.ReadFile(tlPath) //nolint:gosec // G304: timeline path is caller-controlled by design
	if err != nil {
		return "", fmt.Errorf("reading timeline %s: %w", tlPath, err)
	}
	timeline := string(raw)

	tryOnce := func(strict bool) (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, opts.Cfg.InvokeTimeout)
		defer cancel()
		out, err := opts.Invoker.Invoke(callCtx, arcPrompt(timeline, strict), WithModel(opts.Cfg.Model))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}

	row, err := tryOnce(false)
	if err != nil {
		return "", err
	}
	if ValidateArcSummary(row, opts.Cfg.MaxLines, opts.Cfg.MaxChars) == nil {
		return row, nil
	}

	row, err = tryOnce(true)
	if err != nil {
		return "", err
	}
	if ValidateArcSummary(row, opts.Cfg.MaxLines, opts.Cfg.MaxChars) == nil {
		return row, nil
	}

	return truncateForFallback(timeline, opts.Cfg.MaxChars), nil
}
