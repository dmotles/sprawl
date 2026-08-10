// Package blurb generates and maintains a short, rolling per-agent "capability
// blurb" — 2-3 sentences answering "what does this agent know / what was it
// working on last?". The blurb is produced by a cheap background Claude call,
// mirroring the memory-consolidation dispatch pattern
// (internal/rootinit/bgconsolidate.go) and the context-assemble → build-prompt
// → invoke → empty-result-guard → cap → persist shape of
// internal/memory/persistent.go. Displayed by the status/peek MCP tools so a
// resting agent tree becomes shoppable for reuse. See QUM-899.
package blurb

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/memory"
)

const (
	// DefaultModel is the Claude model used to generate blurbs. A useful
	// capability assessment benefits from a stronger model, so sonnet is the
	// default. Cost lever: swap to ModelHaiku for cheaper/faster (lower-quality)
	// generation if blurb cost ever becomes a concern.
	DefaultModel = "sonnet"
	// ModelHaiku is the documented cheaper cost-lever alternative to DefaultModel.
	ModelHaiku = "haiku"

	// RefreshFloor is the minimum spacing between periodic blurb refreshes: a
	// blurb refreshes at most once per this interval, and only when there is
	// net-new activity since BlurbAt. Idle agents cost nothing.
	RefreshFloor = 15 * time.Minute

	// DefaultInvokeTimeout bounds a single blurb generation Claude call.
	DefaultInvokeTimeout = 90 * time.Second

	// MaxBlurbChars is the hard cap on the generated blurb length. The model is
	// asked for 2-3 sentences; this backstops a runaway response.
	MaxBlurbChars = 400
	// maxSentences caps the number of sentences retained from the model output.
	maxSentences = 3

	// MaxDeltaEntries is a generous cap on how many activity-delta entries are
	// fed into a single generation — far above a normal 15-min delta. When
	// exceeded, the oldest entries are dropped from the window but their count
	// is surfaced (Signals.OmittedDelta) so the prompt notes the omission
	// rather than silently discarding history.
	MaxDeltaEntries = 400
)

// TriggerKind identifies why a blurb generation was requested.
type TriggerKind int

const (
	// TriggerNone means no generation should occur.
	TriggerNone TriggerKind = iota
	// TriggerInitial is the fast at-spawn generation from role + prompt + the
	// first slice of transcript so the agent is findable within seconds.
	TriggerInitial
	// TriggerRefresh is a periodic refresh gated by the dirty-check + floor.
	TriggerRefresh
	// TriggerCompletion is the one-shot regeneration when an agent transitions
	// to a terminal "complete" state — the highest-value moment for the reuse
	// use-case, since the resting blurb should describe finished expertise.
	TriggerCompletion
)

// String renders the trigger kind for logs.
func (k TriggerKind) String() string {
	switch k {
	case TriggerInitial:
		return "initial"
	case TriggerRefresh:
		return "refresh"
	case TriggerCompletion:
		return "completion"
	default:
		return "none"
	}
}

// TriggerInput is the pure decision input for the periodic-refresh path
// (heartbeat-driven). The completion trigger is invoked directly at the
// supervisor layer on the complete-teardown, not through DecideTrigger.
type TriggerInput struct {
	IsRoot         bool
	HasBlurb       bool
	BlurbAt        time.Time
	LastActivityAt time.Time
	Now            time.Time
}

// DecideTrigger is the pure trigger-decision function for the periodic refresh
// path. Root agents never get a blurb (they have the handoff/memory system). An
// agent with no blurb yet is generated immediately (catch-up if the at-spawn
// generation failed). Otherwise a refresh fires only when there is net-new
// activity since BlurbAt AND at least RefreshFloor has elapsed.
func DecideTrigger(in TriggerInput) TriggerKind {
	if in.IsRoot {
		return TriggerNone
	}
	if !in.HasBlurb {
		return TriggerInitial
	}
	if in.LastActivityAt.After(in.BlurbAt) && in.Now.Sub(in.BlurbAt) >= RefreshFloor {
		return TriggerRefresh
	}
	return TriggerNone
}

// Signals is the assembled rolling-summary context handed to BuildPrompt.
type Signals struct {
	AgentName    string
	Role         string // e.g. "engineer / engineering"
	Prompt       string // the agent's spawn/input prompt
	PrevBlurb    string // carried forward — the compressed memory of prior work
	Delta        []agentloop.ActivityEntry
	OmittedDelta int // older delta entries summarized out (see MaxDeltaEntries)
	GitDiffStat  string
	LinearKeys   []string
}

var linearKeyPattern = regexp.MustCompile(`\b[A-Z]{2,}-\d+\b`)

// ExtractLinearKeys returns sorted, de-duplicated issue keys (e.g. QUM-899)
// found across the given texts.
func ExtractLinearKeys(texts ...string) []string {
	set := make(map[string]struct{})
	for _, t := range texts {
		for _, m := range linearKeyPattern.FindAllString(t, -1) {
			set[m] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ToolHistogram counts tool_use activity entries by tool name. Entries without
// a tool name are ignored.
func ToolHistogram(entries []agentloop.ActivityEntry) map[string]int {
	hist := make(map[string]int)
	for _, e := range entries {
		if e.Kind != "tool_use" || e.Tool == "" {
			continue
		}
		hist[e.Tool]++
	}
	return hist
}

// ActivityDelta filters entries to those strictly after `since`. A zero `since`
// (initial generation) returns everything. When the filtered result exceeds
// MaxDeltaEntries the newest MaxDeltaEntries are kept and the count of dropped
// (older) entries is returned so BuildPrompt can note the omission rather than
// silently drop history. Entries are assumed oldest-first (activity-log order).
func ActivityDelta(entries []agentloop.ActivityEntry, since time.Time) (delta []agentloop.ActivityEntry, omitted int) {
	for _, e := range entries {
		if since.IsZero() || e.TS.After(since) {
			delta = append(delta, e)
		}
	}
	if len(delta) > MaxDeltaEntries {
		omitted = len(delta) - MaxDeltaEntries
		delta = delta[len(delta)-MaxDeltaEntries:]
	}
	return delta, omitted
}

// BuildPrompt constructs the Claude prompt for a blurb generation. It follows
// the persistent-knowledge template: role framing, the previous blurb (carried
// forward), the activity delta, compact structured signals, then strict output
// instructions.
func BuildPrompt(kind TriggerKind, s Signals) string {
	var b strings.Builder

	b.WriteString("You are writing a SHORT capability blurb for an AI agent in the Sprawl orchestration system. ")
	b.WriteString("Other agents and operators read this blurb to decide whether this agent is worth reviving for related work. ")
	b.WriteString("It must answer, in 2-3 sentences: what does this agent know, and what was it working on last?\n\n")

	switch kind {
	case TriggerInitial:
		b.WriteString("This is the agent's FIRST blurb, written just after spawn — lean on its role and assigned task; there may be little activity yet.\n\n")
	case TriggerCompletion:
		b.WriteString("This agent has just FINISHED its work (reported complete). Write the blurb as a resting summary of its finished expertise.\n\n")
	}

	fmt.Fprintf(&b, "## Agent\n\nName: %s\nRole: %s\n\n", s.AgentName, s.Role)

	if strings.TrimSpace(s.Prompt) != "" {
		b.WriteString("## Assigned task / input prompt\n\n")
		b.WriteString(strings.TrimSpace(s.Prompt))
		b.WriteString("\n\n")
	}

	if strings.TrimSpace(s.PrevBlurb) != "" {
		b.WriteString("## Previous blurb (carry forward — this is the compressed memory of everything before the delta below)\n\n")
		b.WriteString(strings.TrimSpace(s.PrevBlurb))
		b.WriteString("\n\nIf the previous blurb is still accurate, keep it nearly unchanged; only fold in what the delta genuinely adds.\n\n")
	}

	if s.OmittedDelta > 0 {
		fmt.Fprintf(&b, "## Activity since last blurb (showing most recent; %d earlier entries omitted for length)\n\n", s.OmittedDelta)
	} else {
		b.WriteString("## Activity since last blurb\n\n")
	}
	if len(s.Delta) == 0 {
		b.WriteString("(no recorded activity)\n\n")
	} else {
		for _, e := range s.Delta {
			if e.Tool != "" {
				fmt.Fprintf(&b, "- [%s:%s] %s\n", e.Kind, e.Tool, e.Summary)
			} else {
				fmt.Fprintf(&b, "- [%s] %s\n", e.Kind, e.Summary)
			}
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(s.GitDiffStat) != "" {
		b.WriteString("## Files changed on this agent's branch (git diff --stat)\n\n")
		b.WriteString(strings.TrimSpace(s.GitDiffStat))
		b.WriteString("\n\n")
	}

	if hist := renderHistogram(s.Delta); hist != "" {
		b.WriteString("## Tool usage\n\n")
		b.WriteString(hist)
		b.WriteString("\n\n")
	}

	if len(s.LinearKeys) > 0 {
		fmt.Fprintf(&b, "## Referenced issues\n\n%s\n\n", strings.Join(s.LinearKeys, ", "))
	}

	b.WriteString("## Instructions\n\n")
	b.WriteString("Reply with ONLY the blurb — no preamble, no headers, no quotes. ")
	fmt.Fprintf(&b, "Write 2-3 sentences, at most %d characters. ", MaxBlurbChars)
	b.WriteString("Focus on durable expertise (subsystems, techniques, issues) and the last thing worked on. ")
	b.WriteString("Do not speculate beyond the evidence above.\n")

	return b.String()
}

// renderHistogram formats the tool-use histogram as a compact, deterministic
// (name-sorted) line so prompt output is stable across runs.
func renderHistogram(entries []agentloop.ActivityEntry) string {
	hist := ToolHistogram(entries)
	if len(hist) == 0 {
		return ""
	}
	names := make([]string, 0, len(hist))
	for n := range hist {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", n, hist[n]))
	}
	return strings.Join(parts, ", ")
}

// Generate builds the prompt for the given trigger + signals, invokes the
// model, and returns a capped blurb. An empty/whitespace model response yields
// ("", nil) so the caller keeps the previous blurb rather than clobbering it
// (mirrors memory.UpdatePersistentKnowledge's empty-result guard). model=="" and
// timeout<=0 fall back to DefaultModel / DefaultInvokeTimeout.
func Generate(ctx context.Context, invoker memory.ClaudeInvoker, model string, timeout time.Duration, kind TriggerKind, s Signals) (string, error) {
	if model == "" {
		model = DefaultModel
	}
	if timeout <= 0 {
		timeout = DefaultInvokeTimeout
	}
	prompt := BuildPrompt(kind, s)

	invokeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := invoker.Invoke(invokeCtx, prompt, memory.WithModel(model))
	if err != nil {
		return "", fmt.Errorf("invoking claude for blurb: %w", err)
	}
	return EnforceCap(out), nil
}

// EnforceCap normalizes and caps raw model output to a blurb: whitespace is
// collapsed, at most maxSentences sentences are kept, and the result is hard-
// truncated at a word boundary to MaxBlurbChars with an ellipsis. Blank input
// returns "".
func EnforceCap(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	s = firstSentences(s, maxSentences)
	if len([]rune(s)) > MaxBlurbChars {
		runes := []rune(s)[:MaxBlurbChars]
		s = string(runes)
		if idx := strings.LastIndexByte(s, ' '); idx > 0 {
			s = s[:idx]
		}
		s = strings.TrimRight(s, " ,;:") + "…"
	}
	return s
}

// firstSentences returns the prefix of s up to and including the nth sentence
// terminator (. ! ?). If fewer than n terminators are present, s is returned
// unchanged.
func firstSentences(s string, n int) string {
	if n <= 0 {
		return s
	}
	count := 0
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' {
			count++
			if count >= n {
				return strings.TrimSpace(s[:i+1])
			}
		}
	}
	return s
}
