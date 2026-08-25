package card

import (
	"fmt"
	"strings"
)

// Env is the spawn environment a card is rendered against.
//
// Deliberately a local struct rather than internal/agent.EnvConfig: internal/
// agent will render cards, so a dependency the other way would be a cycle.
type Env struct {
	WorkDir    string
	Platform   string
	Shell      string
	TestMode   bool
	Subagent   bool
	ParentName string
}

// Input carries the per-spawn values substituted into a card body.
type Input struct {
	AgentName  string
	ParentName string
	BranchName string
	Family     string
	Env        Env
}

// subagentBanner is prepended to a sub-agent's rendered card. The %q
// placeholder receives the parent agent's name. QUM-709.
const subagentBanner = "# SUB-AGENT BANNER:\nYou are a SPRAWL SUB-AGENT sharing the git worktree and branch of your parent agent %q. Do NOT git checkout, git switch, or git branch — your commits land directly on your parent's working branch. Coordinate file edits with your parent. You retain your own inbox, MCP session, and state file."

// testSandboxWarning is appended when Env.TestMode is set.
const testSandboxWarning = `

# TEST SANDBOX MODE

You are operating in a testing sandbox for sprawl. Take care to:
- Avoid taking any action outside of $SPRAWL_ROOT
- ONLY execute sprawl using $SPRAWL_BIN (do not use bare 'sprawl' from PATH)
- Do not interact with production systems, push to remote repositories, or modify files outside the test directory
- This environment will be torn down after testing`

// tokenBlanker erases every known token from a body, so what remains of a "{{"
// is a token Render cannot substitute. Built from knownTokens rather than from
// its own list, so the two cannot drift.
var tokenBlanker = func() *strings.Replacer {
	pairs := make([]string, 0, len(knownTokens)*2)
	for _, tok := range knownTokens {
		pairs = append(pairs, tok, "")
	}
	return strings.NewReplacer(pairs...)
}()

// Render substitutes in's values into the card body and splices the blocks the
// card's render options ask for.
//
// It returns an error rather than a string alone because a body carrying a
// token nobody substitutes must not reach a model. Parse already refuses
// unknown tokens, so this arm fires when a KNOWN token was left unreplaced —
// which today can only mean Render and knownTokens have drifted apart.
//
// Two properties are load-bearing and both were once absent:
//
//   - The surviving-"{{" scan runs against the BODY, not the output. Family and
//     branch are LLM-supplied at spawn and unvalidated, so a family of
//     "eng{{ineering" used to fail this check — and internal/agent/prompt_cards.go
//     turns a render error into a panic on the supervisor's launch goroutine. A
//     poisoned identity string is not a malformed card.
//   - Substitution is ONE ordered left-to-right pass (strings.Replacer), so a
//     substituted value is never rescanned as a token. This used to range a map
//     literal, which made the result depend on map iteration order.
func (c *Card) Render(in Input) (string, error) {
	if scan := tokenBlanker.Replace(c.Body); strings.Contains(scan, "{{") {
		i := strings.Index(scan, "{{")
		return "", fmt.Errorf("card %q@%d: body contains an unsubstituted token near %q",
			c.Name, c.Version, scan[i:min(i+40, len(scan))])
	}

	out := strings.NewReplacer(
		"{{AGENT_NAME}}", in.AgentName,
		"{{PARENT_NAME}}", in.ParentName,
		"{{BRANCH_NAME}}", in.BranchName,
		"{{FAMILY}}", in.Family,
	).Replace(c.Body)

	if c.Opts.SubagentBanner && in.Env.Subagent {
		out = fmt.Sprintf(subagentBanner, in.Env.ParentName) + "\n\n" + out
	}
	if c.Opts.AppendEnvContext {
		out += envContextBlock(in.BranchName, in.Env)
	}
	if c.Opts.SandboxWarning && in.Env.TestMode {
		out += testSandboxWarning
	}
	return out, nil
}

// envContextBlock renders the trailing "# Environment" section. Pure formatter.
func envContextBlock(branchName string, env Env) string {
	var b strings.Builder
	b.WriteString("\n\n# Environment\n")
	if env.WorkDir != "" {
		fmt.Fprintf(&b, "- Working directory: %s\n", env.WorkDir)
	}
	b.WriteString("- Git repository: yes\n")
	fmt.Fprintf(&b, "- Git branch: %s\n", branchName)
	if env.Platform != "" {
		fmt.Fprintf(&b, "- Platform: %s\n", env.Platform)
	}
	if env.Shell != "" {
		fmt.Fprintf(&b, "- Shell: %s\n", env.Shell)
	}
	return b.String()
}
