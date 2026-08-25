package agent

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/card"
)

// Card-backed system prompts (QUM-1251, M2).
//
// Build*Prompt keeps the signature its callers already use and renders the
// embedded legacy card for the role instead of assembling Go constants. Keeping
// the signature is what lets every prose test, golden and safety scanner in this
// package carry over unedited — they now measure card-derived text, which is the
// whole point of the migration, and TestPromptScanners_MutatedSeedReachesTheScanners
// is the control proving they do.
//
// The DB-backed lookup and the compiled-in fallback land at the launch seam, not
// here: these helpers deliberately read only the embedded seeds, so a caller of
// Build*Prompt can never depend on a database being reachable.

// cardInput adapts this package's EnvConfig to the renderer's input.
func cardInput(agentName, parentName, branchName, family string, env EnvConfig) card.Input {
	return card.Input{
		AgentName:  agentName,
		ParentName: parentName,
		BranchName: branchName,
		Family:     family,
		Env: card.Env{
			WorkDir:    env.WorkDir,
			Platform:   env.Platform,
			Shell:      env.Shell,
			TestMode:   env.TestMode,
			Subagent:   env.Subagent,
			ParentName: env.ParentName,
		},
	}
}

// renderCard renders one card.
//
// It is a var rather than a func so it can be substituted: the control that
// licenses deleting the Go prompt constants has to drive a mutated card through
// Build*Prompt itself, not merely alongside it. Calling c.Render directly from a
// test would leave "the builders quietly kept using the constants" untested,
// which is the one hypothesis that control exists to exclude.
var renderCard = func(c *card.Card, in card.Input) (string, error) {
	return c.Render(in)
}

// mustRenderSeedPrompt renders the embedded seed card for an agent type.
//
// It panics rather than returning an error because Build*Prompt's callers have
// no error path and the alternative failure mode is worse: an empty or partial
// system prompt is a silently unsafe agent, while a panic is loud and local.
// Neither failure is reachable from a well-formed tree — the seeds are embedded,
// parsed and pinned to the goldens by tests in this package — so a panic here
// means the binary was built from seeds that never passed those tests.
func mustRenderSeedPrompt(agentType string, in card.Input) string {
	return mustRenderCard(nil, agentType, in)
}

// BuildCardPrompt renders a RESOLVED card into a system prompt, falling back to
// the embedded seed for agentType when c is nil.
//
// This is the launch path's entry point (QUM-1251). It shares mustRenderCard —
// and therefore the renderCard choke point — with Build*Prompt on purpose: if
// the launch path rendered cards through its own helper, this package's prose
// tests, goldens and safety scanners would all measure a function nothing
// launches, and TestPromptScanners_MutatedSeedReachesTheScanners would stop
// bounding production while staying green.
//
// The scanners bound the EMBEDDED SEEDS. A card published to the database is not
// covered by them; that is what `sprawl def publish`'s card-lint is for.
func BuildCardPrompt(c *card.Card, agentType, agentName, parentName, branchName, family string, env EnvConfig) string {
	return mustRenderCard(c, agentType, cardInput(agentName, parentName, branchName, family, env))
}

// mustRenderCard renders c, or the seed for agentType when c is nil.
func mustRenderCard(c *card.Card, agentType string, in card.Input) string {
	if c == nil {
		seed, err := card.SeedForType(agentType)
		if err != nil {
			panic(fmt.Sprintf("agent: no embedded card for agent type %q: %v", agentType, err))
		}
		c = seed
	}
	prompt, err := renderCard(c, in)
	if err != nil {
		panic(fmt.Sprintf("agent: rendering card %s@%d: %v", c.Name, c.Version, err))
	}
	return prompt
}

// BuildEngineerPrompt constructs the system prompt for an engineer agent.
func BuildEngineerPrompt(agentName, parentName, branchName string, env EnvConfig) string {
	return mustRenderSeedPrompt("engineer", cardInput(agentName, parentName, branchName, "", env))
}

// BuildResearcherPrompt constructs the system prompt for a researcher agent.
func BuildResearcherPrompt(agentName, parentName, branchName string, env EnvConfig) string {
	return mustRenderSeedPrompt("researcher", cardInput(agentName, parentName, branchName, "", env))
}

// BuildQAPrompt constructs the system prompt for a QA agent.
func BuildQAPrompt(agentName, parentName, branchName string, env EnvConfig) string {
	return mustRenderSeedPrompt("qa", cardInput(agentName, parentName, branchName, "", env))
}

// BuildManagerPrompt constructs the system prompt for a manager agent.
func BuildManagerPrompt(agentName, parentName, branchName, family string, env EnvConfig) string {
	return mustRenderSeedPrompt("manager", cardInput(agentName, parentName, branchName, family, env))
}
