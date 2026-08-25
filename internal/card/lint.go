package card

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Card-lint: the prompt-safety rules, as a production surface (QUM-1251, AC5).
//
// These scanners were test-only helpers in internal/agent. They moved here
// unchanged in behaviour so that `sprawl def publish` and the prompt tests share
// ONE implementation: a second copy behind the publish path would let a card be
// admitted by rules that had quietly diverged from the ones the tests pin, and
// both sides would stay green while doing it.
//
// Every rule is section- or line-scoped rather than a whole-prompt substring
// search. That is the point of QUM-1129's original design: asserting a needle
// against the whole assembled prompt passes even when the needle sits somewhere
// irrelevant, so the vacuity demonstrations in lint_test.go show what a bare
// strings.Contains would have waved through.
//
// What these rules do NOT cover is stated plainly because it is easy to
// over-read: they check that a card TELLS the agent the safety-critical things,
// not that the prose is good, and they check the four seeded roles only.

// ExecutingActionsHeading is the section every child card must carry.
const ExecutingActionsHeading = "# Executing actions with care"

// ExtractHeadingSection returns the substring of prompt starting at the given
// "# Heading" marker up to (but not including) the next heading line, or the end
// of the prompt. ok is false if heading does not occur at the start of a line —
// matching mid-line prose that happens to contain the literal heading text would
// scope the extraction to the wrong span.
//
// The terminator is ANY line beginning with "#", not the next "# " at the same
// depth. Markdown would nest a "## Later" under a preceding "# Section", and
// under that reading a card could satisfy a section-scoped rule with text in an
// unrelated SUBsection — the silent direction of this function's original
// flat-heading assumption, and unreachable while only the seeds were scanned but
// reachable the moment `def publish` lints an arbitrary body. Ending at the next
// heading of any depth can only NARROW the span, so it can only make a rule
// stricter; a rule that then fires on a real card is a card to fix, not a false
// alarm to suppress. TestLint_ADemotedFollowingHeadingDoesNotWidenTheSection is
// the control.
func ExtractHeadingSection(prompt, heading string) (string, bool) {
	idx := -1
	if strings.HasPrefix(prompt, heading) {
		idx = 0
	} else if lineIdx := strings.Index(prompt, "\n"+heading); lineIdx != -1 {
		idx = lineIdx + 1
	}
	if idx == -1 {
		return "", false
	}
	rest := prompt[idx:]
	afterHeading := rest[len(heading):]
	if nextIdx := strings.Index(afterHeading, "\n#"); nextIdx != -1 {
		return rest[:len(heading)+nextIdx], true
	}
	return rest, true
}

// ScanExecutingActionsGuardrail returns a finding if the prompt lacks the
// "Executing actions with care" section entirely, or has the section but it does
// not carry the destructive-var `rm -rf "$VAR"` guardrail.
func ScanExecutingActionsGuardrail(prompt string) []string {
	section, ok := ExtractHeadingSection(prompt, ExecutingActionsHeading)
	if !ok {
		return []string{"missing the \"" + ExecutingActionsHeading + "\" section entirely"}
	}
	if !strings.Contains(section, `rm -rf "$VAR"`) {
		return []string{"has the \"" + ExecutingActionsHeading + "\" section but it does not carry the destructive-var rm -rf \"$VAR\" guardrail"}
	}
	return nil
}

var (
	promptInjectionRe        = regexp.MustCompile(`(?i)prompt injection`)
	promptInjectionEscalates = regexp.MustCompile(`manager and weave`)
)

// MentionsPromptInjection reports whether the prompt mentions prompt injection
// at all.
//
// Exported for one reason: internal/agent's root negative control has to prove
// its subject is a REAL one — root does discuss prompt injection, it just
// escalates to the user rather than to the manager — before asserting that
// ScanPromptInjectionEscalation fires on it. Without that precondition the
// control is satisfied by a subject that never mentions the topic, which is
// absent-subject vacuity rather than a negative control. Exported as a predicate
// rather than as the *regexp.Regexp so no caller can mutate the pattern.
func MentionsPromptInjection(prompt string) bool { return promptInjectionRe.MatchString(prompt) }

// ScanPromptInjectionEscalation returns a finding unless some single line both
// mentions prompt injection AND names the escalation target (manager and weave).
// Co-occurrence must land on ONE line — spread across the prompt, the two facts
// do not constitute the instruction.
func ScanPromptInjectionEscalation(prompt string) []string {
	for _, line := range strings.Split(prompt, "\n") {
		if promptInjectionRe.MatchString(line) && promptInjectionEscalates.MatchString(line) {
			return nil
		}
	}
	return []string{"must state on one line that a suspected prompt injection gets escalated via a message to your manager and weave"}
}

var (
	qaConcurrencyContentionRe = regexp.MustCompile(`(?i)contention|wall-clock`)
	qaConcurrencyDiagnoseRe   = regexp.MustCompile(`(?i)diagnose|mechanism`)
	qaConcurrencyFlakeRefRe   = regexp.MustCompile(`QUM-1126|(?i)\bflake\b`)
)

// ScanQAConcurrencyGuidance returns a finding unless some single line explains
// that a wall-clock-sensitive failure under load is more likely contention than a
// regression, that the correct response is to diagnose the mechanism, and points
// at the flake-vs-wrong-signal distinction (QUM-1126).
func ScanQAConcurrencyGuidance(prompt string) []string {
	for _, line := range strings.Split(prompt, "\n") {
		if qaConcurrencyContentionRe.MatchString(line) && qaConcurrencyDiagnoseRe.MatchString(line) && qaConcurrencyFlakeRefRe.MatchString(line) {
			return nil
		}
	}
	return []string{"must explain on one line that a wall-clock-sensitive failure under load is more likely contention than a regression, and that the response is to diagnose the mechanism rather than rerun (see QUM-1126)"}
}

// LintableAgentTypes are the agent types card-lint has rules for.
//
// A closed set, and the closure is load-bearing rather than tidiness: a card
// whose agent_type is not here matches NO rule, Lint reports zero applied, and
// `def publish` refuses it. The alternative — rules that apply to everything —
// admits a card for an unrecognised type having been checked by nothing, and a
// spawn of that type WOULD launch it (cardresolve looks the type up by name
// before it ever reaches the unknown-type fallback). "Published unchecked" is
// the failure this closed set exists to make impossible.
var LintableAgentTypes = []string{"engineer", "manager", "qa", "researcher"}

func lintable(agentType string) bool {
	for _, t := range LintableAgentTypes {
		if t == agentType {
			return true
		}
	}
	return false
}

// Finding is one rule violation.
type Finding struct {
	Rule    string
	Message string
}

// Rule is one card-lint rule.
type Rule struct {
	Name string
	// AppliesTo decides whether this rule has anything to say about the card.
	AppliesTo func(c *Card) bool
	// Scan reads the RENDERED prompt, not the raw body. Render splices the
	// sub-agent banner, the sandbox warning and the "# Environment" block, so a
	// section-scoped rule evaluated pre-render is answering a question no
	// agent's prompt ever poses.
	Scan func(prompt string) []string
}

// SafetyRules returns the card-lint rule set.
func SafetyRules() []Rule {
	anyLintableRole := func(c *Card) bool { return lintable(c.AgentType) }
	return []Rule{
		{Name: "executing-actions-guardrail", AppliesTo: anyLintableRole, Scan: ScanExecutingActionsGuardrail},
		{Name: "prompt-injection-escalation", AppliesTo: anyLintableRole, Scan: ScanPromptInjectionEscalation},
		{
			Name: "qa-concurrency-guidance",
			// QA-specific by design at QUM-1129: QA is the role ordered to run
			// `make validate` and judge red/green under fleet load.
			AppliesTo: func(c *Card) bool { return c.AgentType == "qa" },
			Scan:      ScanQAConcurrencyGuidance,
		},
	}
}

// Report is the outcome of a lint run.
//
// Applied exists so "clean" cannot be reported by a run that checked nothing. It
// is the assertion-count floor, in PRODUCTION rather than only in a test: a
// caller must treat len(Applied) == 0 as a refusal, because zero findings out of
// zero rules is not evidence of anything. Skipped records why each other rule
// stood down, so an operator reading a clean report can see what was not asked.
type Report struct {
	Applied  []string
	Skipped  map[string]string
	Findings []Finding
}

// Clean reports whether the card passed every rule that applied to it — and that
// at least one did.
func (r Report) Clean() bool { return len(r.Applied) > 0 && len(r.Findings) == 0 }

// Lint runs the safety rules against a card and its rendered prompt.
func Lint(c *Card, rendered string) Report {
	rep := Report{Skipped: map[string]string{}}
	for _, rule := range SafetyRules() {
		if !rule.AppliesTo(c) {
			rep.Skipped[rule.Name] = fmt.Sprintf("does not apply to agent_type %q", c.AgentType)
			continue
		}
		rep.Applied = append(rep.Applied, rule.Name)
		for _, msg := range rule.Scan(rendered) {
			rep.Findings = append(rep.Findings, Finding{Rule: rule.Name, Message: msg})
		}
	}
	sort.Strings(rep.Applied)
	return rep
}

// LintInput is the identity and environment a card is rendered under for
// linting.
//
// Fixed rather than derived from the caller's environment, so a lint verdict is
// reproducible: a rule that fired on one operator's machine and not another's
// because SPRAWL_TEST_MODE differed would be worse than no rule. Subagent and
// TestMode are both true because those are the paths that SPLICE extra text —
// linting the widest render means no spliced block can hide a finding.
func LintInput() Input {
	return Input{
		AgentName:  "lint-subject",
		ParentName: "lint-parent",
		BranchName: "lint/branch",
		Family:     "lint-family",
		Env: Env{
			WorkDir:    "/lint/worktree",
			Platform:   "linux",
			Shell:      "/bin/zsh",
			TestMode:   true,
			Subagent:   true,
			ParentName: "lint-parent",
		},
	}
}
