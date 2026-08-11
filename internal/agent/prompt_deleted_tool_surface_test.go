package agent

import (
	"regexp"
	"strings"
	"testing"
)

// QUM-1186 lane 4. The `delegate` and `report_status` MCP tools are deleted and
// `send_message`'s `interrupt` parameter is renamed `now`. Deleting the
// implementation does not delete the *advertisement*: the agent prompts are the
// single largest place sprawl describes its own tool surface, and a prompt that
// still teaches a deleted tool makes every spawned agent call it and get an
// unknown-tool error.
//
// These assertions run against the BUILT prompts rather than the goldens. The
// goldens are byte-compared against the same builders, and are REGENERATED from
// them — so once the prompts are rewritten the goldens go green having asserted
// nothing about content. Only a builder-level content assertion can carry this.
//
// Every needle here is a string that also occurs in ordinary English, which is
// the shape that produces checks that cannot fail. The scanners are therefore
// pure functions exercised by TestPromptScanners_Controls below against
// defect-present and defect-absent fixtures, so each probe is watched firing
// and watched staying quiet.

// promptRenderCase is one (name, rendered prompt) pair covering the full agent
// type × mode matrix.
type promptRenderCase struct {
	name   string
	render func() string
}

func allPromptRenderCases() []promptRenderCase {
	const (
		agentName  = "zone"
		parentName = "weave"
		branchName = "dmotles/zone-test"
	)

	env := EnvConfig{
		WorkDir:  "/tmp/worktrees/zone",
		Platform: "linux",
		Shell:    "/bin/zsh",
	}
	subEnv := env
	subEnv.Subagent = true
	subEnv.ParentName = parentName

	return []promptRenderCase{
		{"root", func() string {
			return BuildRootPrompt(PromptConfig{
				RootName:    "weave",
				AgentCLI:    "claude-code",
				ContextBlob: "context blob",
			})
		}},
		{"root-no-cli", func() string {
			return BuildRootPrompt(PromptConfig{RootName: "weave", AgentCLI: ""})
		}},
		{"engineer", func() string {
			return BuildEngineerPrompt(agentName, parentName, branchName, env)
		}},
		{"engineer-subagent", func() string {
			return BuildEngineerPrompt(agentName, parentName, branchName, subEnv)
		}},
		{"researcher", func() string {
			return BuildResearcherPrompt(agentName, parentName, branchName, env)
		}},
		{"manager", func() string {
			return BuildManagerPrompt(agentName, parentName, branchName, "engineering", env)
		}},
		{"manager-subagent", func() string {
			return BuildManagerPrompt(agentName, parentName, branchName, "engineering", subEnv)
		}},
		{"qa", func() string {
			return BuildQAPrompt(agentName, parentName, branchName, env)
		}},
	}
}

// --- Scanners. Each returns a finding per defect; empty means clean. ---

// deletedSurfacePatterns are advertisements of tools/parameters that no longer
// exist. Each needle is deliberately narrower than the bare concept word.
var deletedSurfacePatterns = []struct {
	re  *regexp.Regexp
	why string
}{
	// Bare identifier is safe to ban: no legitimate prose use survives.
	{
		regexp.MustCompile(`report_status`),
		"the report_status tool is deleted; agents no longer assert their own state",
	},
	// NOT the bare word `interrupt` — preemption is still a real concept and
	// QUM-549 prose legitimately discusses it. Ban only the parameter shape, in
	// every spelling an example might use (`interrupt: false`, `interrupt=true`,
	// `"interrupt": false`, `interrupt:false`).
	{
		regexp.MustCompile(`interrupt"?\s*[:=]`),
		"send_message's interrupt parameter is renamed `now`",
	},
	// Sprawl ships tracker-agnostic: the per-project CLAUDE.md names the
	// tracker, the prompt must not. Word-boundaried so it is a name and not a
	// substring, but deliberately broad — QUM-1186 carries an explicit
	// acceptance criterion that no prompt names this tracker.
	{
		regexp.MustCompile(`\bLinear\b`),
		"prompts must be tracker-agnostic — the per-project CLAUDE.md names the tracker",
	},
	{
		regexp.MustCompile(`mcp__linear__`),
		"prompts must be tracker-agnostic — do not hardcode one tracker's MCP tools",
	},
	// The deleted report_status habit must not be recreated in prose on top of
	// send_message. Agents record work in the tracker and message when they
	// have something a human or another agent needs; they do not narrate.
	{
		regexp.MustCompile(`(?i)stat(us|e) ping`),
		"agents no longer send state pings — liveness is observed, not claimed",
	},
	{
		regexp.MustCompile(`(?i)announc\w*\s+(your|its|their)\s+(own\s+)?(state|status|progress)`),
		"agents no longer announce their state — liveness is observed, not claimed",
	},
	{
		regexp.MustCompile(`(?i)at each meaningful step`),
		"per-step progress narration was the report_status habit; do not move it onto send_message",
	},
	{
		regexp.MustCompile(`(?i)report (your )?progress`),
		"per-step progress narration was the report_status habit; do not move it onto send_message",
	},
	// Space-spelled, which an identifier grep for `last_report` misses. peek's
	// last_report block is deleted; a prompt promising it sends the reader
	// looking for a field that is not in the response.
	{
		regexp.MustCompile(`(?i)last[_ ]report`),
		"peek no longer returns a last_report block — it reports observed activity only",
	},
	// There is no report channel. A prompt offering "messages OR reports" as a
	// choice describes a fork with one arm deleted.
	{
		regexp.MustCompile(`(?i)(sending|send) reports?\b`),
		"there is no report channel; messages are the only way to reach another agent",
	},
	{
		regexp.MustCompile(`(?i)done report`),
		"there is no done report; the hand-off is a <=300-character message, so anything substantial goes in the tracker or a findings file",
	},
}

// delegateAllowedPhrases are the ordinary-English uses of the word "delegate"
// that must SURVIVE. Anything else on a line containing "delegate" is an
// advertisement of the deleted tool — including the paren-less rule-of-thumb
// form ("if you're telling an agent to *do* something, use delegate"), which a
// call-shape-only ban would miss.
var delegateAllowedPhrases = []string{
	"do not delegate this",
	"delegate research to a sidechain",
}

var delegateWordRe = regexp.MustCompile(`(?i)\bdelegates?\b`)

// scanDeletedSurface returns one finding per advertisement of a deleted tool
// or parameter.
func scanDeletedSurface(prompt string) []string {
	var findings []string
	for _, p := range deletedSurfacePatterns {
		if loc := p.re.FindStringIndex(prompt); loc != nil {
			findings = append(findings, "banned "+p.re.String()+" — "+p.why+"\ncontext: "+contextAround(prompt, loc[0]))
		}
	}
	for _, line := range strings.Split(prompt, "\n") {
		if !delegateWordRe.MatchString(line) {
			continue
		}
		allowed := false
		for _, phrase := range delegateAllowedPhrases {
			if strings.Contains(strings.ToLower(line), phrase) {
				allowed = true
				break
			}
		}
		if !allowed {
			findings = append(findings, "the delegate tool is deleted; send_message is the only way to reach another agent\nline: "+line)
		}
	}
	return findings
}

var (
	// A `now` parameter, not the English adverb. `strings.Contains(s, "now")`
	// would be satisfied by "know" and could never fail.
	nowParamRe = regexp.MustCompile(`now\s*[:=]`)
	// The cap is a number with a unit, not a bare "300" that any future
	// QUM-1300 or 3000ms would satisfy forever.
	//
	// The cap is enforced rune-counted, but the prompts say "300 characters"
	// deliberately: "rune" is a Go word, the two differ only for text an agent
	// is unlikely to be composing a 300-unit message out of, and the failure
	// mode of the vaguer word (an agent budgets slightly conservatively) is
	// harmless while the failure mode of jargon is an agent that does not
	// recognise the limit as applying to it. The regexp accepts either
	// spelling so the prompts and the implementation's own error text are not
	// forced to disagree.
	bodyCapRe = regexp.MustCompile(`300[- ](char|rune)`)
	// Work-record guidance, not a bare token: `s/Linear/tracker/` on one line
	// would otherwise satisfy both the Linear ban and this floor at once.
	trackerGuidanceRe = regexp.MustCompile(`(?im)^.*\btracker\b.*$`)
	trackerVerbRe     = regexp.MustCompile(`(?i)record|comment|issue`)
)

// scanSurvivingSurface is the vacuity floor for scanDeletedSurface: every ban
// above is also satisfied by a prompt with no coordination guidance at all.
// These findings fire if the surface was deleted rather than rewritten.
func scanSurvivingSurface(prompt string) []string {
	var findings []string

	if !strings.Contains(prompt, "send_message(") {
		findings = append(findings, "must still teach send_message( — it is the sole agent-to-agent channel")
	}

	// The `now` parameter must be taught on a send_message call shape, and the
	// examples must show the safe default. A prompt whose every example is
	// `now: true` is worse than the one it replaced.
	sawNowOnCall := false
	for _, line := range strings.Split(prompt, "\n") {
		if strings.Contains(line, "send_message(") && nowParamRe.MatchString(line) {
			sawNowOnCall = true
			break
		}
	}
	if !sawNowOnCall {
		findings = append(findings, "must show send_message( with its `now` parameter on the same line")
	}
	if !strings.Contains(prompt, "now: false") {
		findings = append(findings, "must show `now: false` — the cooperative default is the path of least resistance and the prompt must model it")
	}

	// The 300-rune body cap is a hard error, not truncation. An agent that does
	// not know the cap discovers it by failing a call.
	if !bodyCapRe.MatchString(prompt) {
		findings = append(findings, "must state send_message's 300-character body cap with its unit")
	}

	// Tracker-agnostic work-record guidance, the replacement for the deleted
	// report_status bullets.
	sawGuidance := false
	for _, line := range trackerGuidanceRe.FindAllString(prompt, -1) {
		if trackerVerbRe.MatchString(line) {
			sawGuidance = true
			break
		}
	}
	if !sawGuidance {
		findings = append(findings, "must give tracker-agnostic work-record guidance: a line naming the project's tracker and what to record in it")
	}

	return findings
}

// scanIdleVocabulary checks the prompts of agents that OBSERVE other agents.
// `state.StatusIdle` means the process was reclaimed for inactivity and the
// agent revives on the next message — an observer that reads it as "finished"
// will retire an agent with work outstanding. The three facts must land on one
// line; spread across a 14 KB prompt they do not constitute the claim.
var (
	idleWordRe    = regexp.MustCompile(`(?i)\bidle\b`)
	idleReclaimRe = regexp.MustCompile(`(?i)reclaim`)
	idleReviveRe  = regexp.MustCompile(`(?i)reviv|wakes? (back )?up|next message|not (finished|complete|done)`)
)

func scanIdleVocabulary(prompt string) []string {
	for _, line := range strings.Split(prompt, "\n") {
		if idleWordRe.MatchString(line) && idleReclaimRe.MatchString(line) && idleReviveRe.MatchString(line) {
			return nil
		}
	}
	return []string{"must explain the `idle` agent state on one line: the process was reclaimed for inactivity and the agent revives on the next message — it is NOT complete"}
}

// scanCLIRegistryConfusion checks the child-role prompts (engineer, researcher,
// manager, qa — everything that shares childReportBullets) for QUM-1219: the
// Claude CLI's built-in SendMessage/ListAgents tools are a cross-session
// registry that never contains sprawl agents, so a child that reaches for
// them and doesn't find its parent in ListAgents must NOT conclude the parent
// is gone. The three facts — the CLI names, that they don't reach sprawl
// agents, and the BLOCKED/escalate framing for an agent that thinks its
// parent is unreachable — must land on one line; spread across the prompt
// they do not constitute the claim (same shape as scanIdleVocabulary above).
var (
	cliRegistryNameRe    = regexp.MustCompile(`\bSendMessage\b|\bListAgents\b`)
	cliRegistryScopeRe   = regexp.MustCompile(`(?i)cross-session|does not reach sprawl|not sprawl agents|unrelated to sprawl`)
	cliRegistryBlockedRe = regexp.MustCompile(`(?i)\bBLOCKED\b|escalate`)
)

func scanCLIRegistryConfusion(prompt string) []string {
	for _, line := range strings.Split(prompt, "\n") {
		if cliRegistryNameRe.MatchString(line) && cliRegistryScopeRe.MatchString(line) && cliRegistryBlockedRe.MatchString(line) {
			return nil
		}
	}
	return []string{"must explain on one line that the CLI's SendMessage/ListAgents are a cross-session registry that does not reach sprawl agents, and that an agent who concludes its parent is unreachable is BLOCKED and must escalate — never end the turn with the result stranded only in its own transcript"}
}

// contextAround renders a rune-safe window around a byte offset.
func contextAround(s string, idx int) string {
	start := idx - 60
	if start < 0 {
		start = 0
	}
	for start > 0 && !isRuneStart(s[start]) {
		start--
	}
	end := idx + 120
	if end > len(s) {
		end = len(s)
	}
	for end < len(s) && !isRuneStart(s[end]) {
		end++
	}
	return s[start:end]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// --- Tests over the real prompts ---

func TestPromptRenderers_NoDeletedToolSurface(t *testing.T) {
	for _, tc := range allPromptRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range scanDeletedSurface(tc.render()) {
				t.Errorf("%s prompt: %s", tc.name, f)
			}
		})
	}
}

func TestPromptRenderers_TeachSurvivingMessagingSurface(t *testing.T) {
	for _, tc := range allPromptRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range scanSurvivingSurface(tc.render()) {
				t.Errorf("%s prompt: %s", tc.name, f)
			}
		})
	}
}

// TestPromptRenderers_ChildPromptsTeachCLIRegistryDistinction pins QUM-1219:
// every child-role prompt (engineer, researcher, manager, qa — the roles that
// share childReportBullets) must name the CLI's SendMessage/ListAgents
// registry, say it does not reach sprawl agents, and frame an agent's belief
// that its parent is unreachable as BLOCKED (escalate), not a reason to end
// the turn silently. Root is excluded: it never renders childReportBullets.
func TestPromptRenderers_ChildPromptsTeachCLIRegistryDistinction(t *testing.T) {
	children := map[string]bool{
		"engineer": true, "engineer-subagent": true,
		"researcher": true,
		"manager":    true, "manager-subagent": true,
		"qa": true,
	}

	for _, tc := range allPromptRenderCases() {
		if !children[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range scanCLIRegistryConfusion(tc.render()) {
				t.Errorf("%s prompt: %s", tc.name, f)
			}
		})
	}
}

// TestPromptRenderers_RootDoesNotRenderCLIRegistryBullet is the negative
// direction of the test above: root never renders childReportBullets, so the
// QUM-1219 bullet must NOT appear in the root prompt. Without this, a future
// refactor that accidentally pulled the bullet into the root builder would go
// unnoticed. Keys on a phrase unique to the bullet (not the bare "SendMessage"/
// "ListAgents" words, which legitimately appear elsewhere in this package,
// e.g. wake_prompts.go's WakeReasonSendMessage) so the assertion cannot be
// satisfied by an unrelated mention.
func TestPromptRenderers_RootDoesNotRenderCLIRegistryBullet(t *testing.T) {
	roots := map[string]bool{"root": true, "root-no-cli": true}
	const uniquePhrase = "mangled CLI session name"

	for _, tc := range allPromptRenderCases() {
		if !roots[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			if prompt := tc.render(); strings.Contains(prompt, uniquePhrase) {
				t.Errorf("%s prompt: unexpectedly contains the QUM-1219 child-only CLI-registry bullet (%q) — root never renders childReportBullets", tc.name, uniquePhrase)
			}
		})
	}
}

func TestPromptRenderers_ObserverPromptsTeachIdle(t *testing.T) {
	observers := map[string]bool{"root": true, "root-no-cli": true, "manager": true, "manager-subagent": true}

	for _, tc := range allPromptRenderCases() {
		if !observers[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range scanIdleVocabulary(tc.render()) {
				t.Errorf("%s prompt: %s", tc.name, f)
			}
		})
	}
}

// --- Controls: every scanner is watched firing and watched staying quiet. ---

// TestPromptScanners_Controls pairs each probe with a defect-present subject it
// MUST fire on and a defect-absent subject it MUST stay quiet on. Without this
// the probes are claims: each needle above ("now", "300", "tracker", "delegate",
// "Linear") also occurs in ordinary English, so a naive spelling of any of them
// would be green forever.
func TestPromptScanners_Controls(t *testing.T) {
	// A minimal prompt fragment that is correct on every axis. Both scanners
	// must be silent on it, and the surviving-surface scanner must be silent
	// on it too — otherwise the positive controls below prove nothing.
	const clean = `KEY TOOLS:
  send_message({to: "<agent>", body: "<markdown>", now: false})  — sole agent-to-agent channel. body is capped at 300 characters; over the cap it is a hard error, never a truncated message.
- Record your work in the project's tracker if it has one: pick the issue up, comment decisions as you go, close it with a summary.
- An agent shown as idle had its process reclaimed for inactivity; it is NOT complete and revives on the next message.
- Write the failing test yourself — do not delegate this.`

	t.Run("clean-subject-stays-quiet", func(t *testing.T) {
		for _, f := range scanDeletedSurface(clean) {
			t.Errorf("scanDeletedSurface fired on a clean subject: %s", f)
		}
		for _, f := range scanSurvivingSurface(clean) {
			t.Errorf("scanSurvivingSurface fired on a clean subject: %s", f)
		}
		for _, f := range scanIdleVocabulary(clean) {
			t.Errorf("scanIdleVocabulary fired on a clean subject: %s", f)
		}
	})

	// Positive controls for scanDeletedSurface: direction = MUST fire.
	deletedDefects := []struct {
		name    string
		subject string
	}{
		{"report_status tool", clean + "\nreport_status({state: \"complete\", summary: \"done\"})"},
		{"interrupt param, spaced", clean + "\n  send_message({to: \"x\", body: \"y\", interrupt: false})"},
		{"interrupt param, unspaced", clean + "\n  send_message({to: \"x\", body: \"y\", interrupt:false})"},
		{"interrupt param, json-quoted", clean + "\n  send_message({\"to\": \"x\", \"interrupt\": true})"},
		{"interrupt param, equals", clean + "\n  interrupt=true is RARE."},
		{"delegate call shape", clean + "\n  delegate({agent: \"<agent>\", task: \"<task>\"})"},
		// The paren-less form a call-shape-only ban would miss.
		{"delegate rule of thumb", clean + "\n- If you're telling an agent to *do* something, use delegate."},
		{"tracker named", clean + "\n- For Linear issue work, spawn a manager."},
		{"tracker MCP hardcoded", clean + "\n1. Enumerate the ACs via mcp__linear__get_issue."},
		{"state ping in prose", clean + "\n- Send a state ping to your parent when you start."},
		{"announce state in prose", clean + "\n- Use send_message to announce your status to your parent."},
		{"per-step narration", clean + "\n- Message your parent at each meaningful step, not just at the end."},
		{"report progress", clean + "\n- Report progress to your manager as you go."},
	}
	for _, d := range deletedDefects {
		t.Run("fires/"+d.name, func(t *testing.T) {
			if got := scanDeletedSurface(d.subject); len(got) == 0 {
				t.Errorf("scanDeletedSurface stayed quiet on a subject where the defect IS present (%s); subject=%q", d.name, d.subject)
			}
		})
	}

	// Positive controls for scanSurvivingSurface: direction = MUST fire. Each
	// subject is `clean` with exactly one required element removed or
	// weakened, so a finding can only come from that element.
	survivingDefects := []struct {
		name    string
		subject string
	}{
		{"no send_message at all", "- Coordinate with your manager somehow."},
		// The vacuous-`now` trap: the word "now" and "know" are both present,
		// the PARAMETER is absent. A `strings.Contains(s, "now")` probe passes
		// this; nowParamRe must not.
		{
			"now the adverb, not the parameter",
			`  send_message({to: "x", body: "<markdown>"}) — you now know the recipient is notified; it is known to be async. body is capped at 300 characters.
- Record your work in the project's tracker if it has one; comment as you go.`,
		},
		{
			"now shown only as true",
			strings.Replace(clean, "now: false", "now: true", 1),
		},
		{
			"cap without unit",
			strings.Replace(clean, "300 characters", "300", 1),
		},
		{
			"tracker named but nothing recorded",
			strings.Replace(clean,
				"Record your work in the project's tracker if it has one: pick the issue up, comment decisions as you go, close it with a summary.",
				"For tracker work, default to spawning a manager.", 1),
		},
	}
	for _, d := range survivingDefects {
		t.Run("fires/"+d.name, func(t *testing.T) {
			if got := scanSurvivingSurface(d.subject); len(got) == 0 {
				t.Errorf("scanSurvivingSurface stayed quiet on a subject where the defect IS present (%s); subject=%q", d.name, d.subject)
			}
		})
	}

	// The naive spelling of the `now` probe must be demonstrably vacuous —
	// this is what makes the strengthened form above worth having.
	t.Run("naive-now-probe-cannot-fail", func(t *testing.T) {
		vacuous := `send_message({to: "x", body: "y"}) — you now know it is async.`
		if !strings.Contains(vacuous, "now") {
			t.Fatal("fixture no longer contains the English word `now`; the demonstration is broken")
		}
		if nowParamRe.MatchString(vacuous) {
			t.Errorf("nowParamRe matched a subject with no `now` parameter — the probe is as vacuous as strings.Contains(s, %q)", "now")
		}
	})

	// Positive controls for scanIdleVocabulary: direction = MUST fire when the
	// three facts are split across lines rather than stated together.
	t.Run("fires/idle-facts-split-across-lines", func(t *testing.T) {
		subject := "- Wake an idle agent when you need it.\n- Its worktree may have been reclaimed.\n- Revive it with a message."
		if got := scanIdleVocabulary(subject); len(got) == 0 {
			t.Error("scanIdleVocabulary stayed quiet when the idle/reclaimed/revives facts were split across unrelated lines")
		}
	})
	t.Run("fires/idle-absent", func(t *testing.T) {
		if got := scanIdleVocabulary("- Agents are either active or complete."); len(got) == 0 {
			t.Error("scanIdleVocabulary stayed quiet on a subject that never mentions the idle state")
		}
	})

	// scanCLIRegistryConfusion (QUM-1219): a clean subject with all three
	// required facts on one line must stay quiet.
	const cleanCLIRegistry = `- Do NOT use the CLI's built-in SendMessage or ListAgents tools — they are a cross-session registry and do not reach sprawl agents. If you conclude your parent is unreachable you are BLOCKED: escalate, never end your turn silently.`

	t.Run("clean-cli-registry-stays-quiet", func(t *testing.T) {
		if got := scanCLIRegistryConfusion(cleanCLIRegistry); len(got) != 0 {
			t.Errorf("scanCLIRegistryConfusion fired on a clean subject: %v", got)
		}
	})

	t.Run("fires/cli-registry-no-blocked-language", func(t *testing.T) {
		subject := `- Do NOT use the CLI's built-in SendMessage or ListAgents tools — they are a cross-session registry and do not reach sprawl agents.`
		if got := scanCLIRegistryConfusion(subject); len(got) == 0 {
			t.Error("scanCLIRegistryConfusion stayed quiet on a subject missing the BLOCKED/escalate framing")
		}
	})

	t.Run("fires/cli-registry-names-absent", func(t *testing.T) {
		subject := `- If you conclude your parent is unreachable you are BLOCKED: escalate, never end your turn silently.`
		if got := scanCLIRegistryConfusion(subject); len(got) == 0 {
			t.Error("scanCLIRegistryConfusion stayed quiet on a subject that never names SendMessage/ListAgents")
		}
	})

	t.Run("fires/cli-registry-facts-split-across-lines", func(t *testing.T) {
		subject := "- Do NOT use the CLI's built-in SendMessage or ListAgents tools.\n- They are a cross-session registry and do not reach sprawl agents.\n- If you conclude your parent is unreachable you are BLOCKED: escalate."
		if got := scanCLIRegistryConfusion(subject); len(got) == 0 {
			t.Error("scanCLIRegistryConfusion stayed quiet when the name/scope/blocked facts were split across unrelated lines")
		}
	})

	// Vacuity demonstration: a bare strings.Contains on "ListAgents" would pass
	// forever even when the word appears in an unrelated sentence that never
	// explains the distinction. scanCLIRegistryConfusion must still fire.
	t.Run("naive-listagents-contains-cannot-fail", func(t *testing.T) {
		vacuous := "- ListAgents is deprecated in v2 of some unrelated tool, totally different topic."
		if !strings.Contains(vacuous, "ListAgents") {
			t.Fatal("fixture no longer contains the literal ListAgents; the demonstration is broken")
		}
		if got := scanCLIRegistryConfusion(vacuous); len(got) == 0 {
			t.Errorf("scanCLIRegistryConfusion stayed quiet on a subject where ListAgents appears with no scope/blocked framing — the probe is as vacuous as strings.Contains(s, %q)", "ListAgents")
		}
	})
}
