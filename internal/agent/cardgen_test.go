package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mechanical extraction of the Go prompt constants into internal/card seeds.
//
//	GENERATE_CARDS=1 go test ./internal/agent/ -run TestGenerateSeedCards
//
// The bodies are NOT retyped. Each Build*Prompt is called with sentinel
// identity values that cannot occur in prompt prose, and the sentinels are then
// replaced by the card template tokens. That direction matters: substituting
// the REAL values ("root", "engineering") back out would also rewrite the many
// places those words appear as ordinary English.
//
// Env-derived blocks (the "# Environment" section, the sub-agent banner, the
// test-sandbox warning) are excluded from the body on purpose — the renderer
// splices them per the card's render options, so the extraction is run against
// an EnvConfig that produces none of them.
//
// This helper exists only for the duration of the constants-to-cards migration
// and is deleted with the constants it reads.

const (
	sentAgent  = "\x02AGENT\x02"
	sentParent = "\x02PARENT\x02"
	sentBranch = "\x02BRANCH\x02"
	sentFamily = "\x02FAMILY\x02"
)

// tokenize replaces the sentinel identity values with card template tokens.
//
// present names the sentinels this prompt is REQUIRED to contain. Asserting
// they arrived is the half that matters: a builder that ignored an identity
// argument produces a body with no corresponding token, and a tokeniser that
// only checks for survivors calls that a success.
func tokenize(t *testing.T, prompt string, present ...string) string {
	t.Helper()
	for _, sentinel := range present {
		if !strings.Contains(prompt, sentinel) {
			t.Fatalf("sentinel %q never reached the prompt — the builder dropped that identity argument", sentinel)
		}
	}
	for sentinel, token := range map[string]string{
		sentAgent:  "{{AGENT_NAME}}",
		sentParent: "{{PARENT_NAME}}",
		sentBranch: "{{BRANCH_NAME}}",
		sentFamily: "{{FAMILY}}",
	} {
		prompt = strings.ReplaceAll(prompt, sentinel, token)
	}
	if strings.ContainsRune(prompt, '\x02') {
		t.Fatalf("a sentinel survived tokenisation — the sentinel set and the tokenise map have drifted")
	}
	return prompt
}

func TestGenerateSeedCards(t *testing.T) {
	if os.Getenv("GENERATE_CARDS") != "1" {
		t.Skip("set GENERATE_CARDS=1 to regenerate internal/card/seeds/*.md")
	}
	// No worktree, platform or shell: envContextBlock must contribute nothing.
	env := EnvConfig{}

	cards := []struct {
		file        string
		name        string
		agentType   string
		description string
		model       string
		effort      string
		body        string
	}{
		{
			file:        "legacy-engineer.md",
			name:        "legacy-engineer",
			agentType:   "engineer",
			description: "Hands-on builder. Writes code and tests under a mandatory TDD workflow.",
			model:       "opus",
			effort:      "low",
			body:        tokenize(t, legacyBuildEngineerPrompt(sentAgent, sentParent, sentBranch, env), sentAgent, sentParent, sentBranch),
		},
		{
			file:        "legacy-manager.md",
			name:        "legacy-manager",
			agentType:   "manager",
			description: "Decomposes work, dispatches child agents, and integrates their branches.",
			model:       "opus[1m]",
			effort:      "low",
			body:        tokenize(t, legacyBuildManagerPrompt(sentAgent, sentParent, sentBranch, sentFamily, env), sentAgent, sentParent, sentBranch, sentFamily),
		},
		{
			file:        "legacy-researcher.md",
			name:        "legacy-researcher",
			agentType:   "researcher",
			description: "Read-only investigator. Produces findings documents, not code changes.",
			model:       "opus",
			effort:      "low",
			body:        tokenize(t, legacyBuildResearcherPrompt(sentAgent, sentParent, sentBranch, env), sentAgent, sentParent, sentBranch),
		},
		{
			file:        "legacy-qa.md",
			name:        "legacy-qa",
			agentType:   "qa",
			description: "Verifies acceptance criteria against a branch and returns a per-AC verdict.",
			model:       "opus",
			effort:      "low",
			body:        tokenize(t, legacyBuildQAPrompt(sentAgent, sentParent, sentBranch, env), sentAgent, sentParent, sentBranch),
		},
	}

	dir := filepath.Join("..", "card", "seeds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		// The researcher prompt is the one that does not carry an
		// "# Environment" block; every other legacy prompt does.
		appendEnv := c.agentType != "researcher"
		body := c.body
		if appendEnv {
			// envContextBlock emits its header and the two unconditional
			// bullets even for a zero EnvConfig, so the empty-env render still
			// carries a stub block. Strip exactly that stub — TrimSuffix is a
			// no-op if it is absent, so assert instead of trusting it.
			stub := envContextBlock("{{BRANCH_NAME}}", EnvConfig{})
			if !strings.HasSuffix(body, stub) {
				t.Fatalf("%s: expected a stub env block to strip, found none", c.file)
			}
			body = strings.TrimSuffix(body, stub)
		}
		var b strings.Builder
		b.WriteString("---\n")
		fmt.Fprintf(&b, "name: %s\n", c.name)
		fmt.Fprintf(&b, "version: 1\n")
		fmt.Fprintf(&b, "description: %q\n", c.description)
		fmt.Fprintf(&b, "agent_type: %s\n", c.agentType)
		fmt.Fprintf(&b, "model: %s\n", c.model)
		fmt.Fprintf(&b, "effort: %s\n", c.effort)
		b.WriteString("render:\n")
		fmt.Fprintf(&b, "  append_env_context: %t\n", appendEnv)
		b.WriteString("  subagent_banner: true\n")
		b.WriteString("  sandbox_warning: true\n")
		b.WriteString("---\n")
		b.WriteString(body)
		b.WriteString("\n")

		path := filepath.Join(dir, c.file)
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, b.Len())
	}
}
