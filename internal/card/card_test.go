package card

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

const minimalCard = `---
name: sample
version: 3
agent_type: engineer
model: opus
effort: low
render:
  append_env_context: true
---
Hello {{AGENT_NAME}}.
`

func TestParse_ReadsFrontmatterAndBody(t *testing.T) {
	c, err := Parse([]byte(minimalCard))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Name != "sample" || c.Version != 3 || c.AgentType != "engineer" {
		t.Errorf("frontmatter did not reach the struct: %+v", c)
	}
	if c.Model != "opus" || c.Effort != "low" {
		t.Errorf("model/effort did not reach the struct: model=%q effort=%q", c.Model, c.Effort)
	}
	if !c.Opts.AppendEnvContext {
		t.Error("render.append_env_context was set in the source but is false on the card")
	}
	if c.Opts.SubagentBanner || c.Opts.SandboxWarning {
		t.Error("render options absent from the source must stay false, not default to on")
	}
	// The body must be exactly the prompt: no frontmatter, no leading blank
	// line from the closing fence, no trailing newline from the file.
	if c.Body != "Hello {{AGENT_NAME}}." {
		t.Errorf("body = %q, want %q", c.Body, "Hello {{AGENT_NAME}}.")
	}
}

// TestParse_ContentHashCoversEverySourceByte is the immutability check's floor:
// if any edit to the file can leave the hash alone, `def publish` will accept a
// changed card as unchanged.
func TestParse_ContentHashCoversEverySourceByte(t *testing.T) {
	base, err := Parse([]byte(minimalCard))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(base.ContentSHA256) != 64 {
		t.Fatalf("ContentSHA256 = %q, want 64 hex chars", base.ContentSHA256)
	}
	edits := map[string]string{
		"body edited":            strings.Replace(minimalCard, "Hello", "Howdy", 1),
		"description added":      strings.Replace(minimalCard, "model: opus", "description: x\nmodel: opus", 1),
		"frontmatter reordered":  strings.Replace(minimalCard, "model: opus\neffort: low", "effort: low\nmodel: opus", 1),
		"whitespace only change": strings.Replace(minimalCard, "Hello {{AGENT_NAME}}.", "Hello  {{AGENT_NAME}}.", 1),
	}
	for name, src := range edits {
		edited, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: Parse: %v", name, err)
		}
		if edited.ContentSHA256 == base.ContentSHA256 {
			t.Errorf("%s: the content hash did not move — this edit would pass an immutability check", name)
		}
	}
}

// TestCardID_IsDerivedAndStable pins the FROZEN derivation two ways, because
// each catches something the other cannot.
//
// The recomputation restates the namespace, the "@" format and uuid.NewSHA1 on
// this side rather than calling through the subject, so it catches a change to
// any of the three. What it cannot catch is BOTH sides being updated together
// during a refactor, which is the normal way a frozen derivation gets broken.
// The hardcoded literal was computed once and is never recomputed: it is the id
// already written into recorded card_ids, and if the code stops producing it
// those rows are stranded.
func TestCardID_IsDerivedAndStable(t *testing.T) {
	const frozen = "e2b77a9b-3e15-5476-a528-05eb1b45af42" // CardID("legacy-engineer", 1)
	want := uuid.NewSHA1(uuid.MustParse("3f5b2c81-9d64-4a7e-8b30-6e1c4f92a7d5"), []byte("legacy-engineer@1"))
	if got := CardID("legacy-engineer", 1); got != want {
		t.Errorf("CardID drifted from its frozen derivation: got %s, want %s", got, want)
	}
	if got := CardID("legacy-engineer", 1).String(); got != frozen {
		t.Errorf("CardID no longer produces the id already recorded for legacy-engineer@1: got %s, want %s", got, frozen)
	}
	if CardID("legacy-engineer", 1) == CardID("legacy-engineer", 2) {
		t.Error("two versions of one card derive the same id")
	}
	if CardID("legacy-engineer", 1) == CardID("legacy-manager", 1) {
		t.Error("two card names derive the same id")
	}
}

func TestParse_RejectsMalformedSources(t *testing.T) {
	cases := map[string]struct{ src, wantSubstr string }{
		"no frontmatter fence": {"name: x\n---\nbody\n", "frontmatter fence"},
		"unclosed frontmatter": {"---\nname: x\nbody\n", "never closed"},
		"unknown yaml field":   {"---\nname: x\nversion: 1\nagent_type: e\nmodl: opus\n---\nbody\n", "frontmatter"},
		"missing name":         {"---\nversion: 1\nagent_type: e\n---\nbody\n", "name is required"},
		"version zero":         {"---\nname: x\nagent_type: e\n---\nbody\n", "version must be >= 1"},
		"missing agent_type":   {"---\nname: x\nversion: 1\n---\nbody\n", "agent_type is required"},
		"empty body":           {"---\nname: x\nversion: 1\nagent_type: e\n---\n   \n", "body is empty"},
		"unknown token":        {"---\nname: x\nversion: 1\nagent_type: e\n---\nhi {{PARNET_NAME}}\n", "unknown template token"},
	}
	for name, tc := range cases {
		c, err := Parse([]byte(tc.src))
		if err == nil {
			t.Errorf("%s: Parse accepted the source and returned %+v", name, c)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSubstr) {
			t.Errorf("%s: error does not say why: got %v, want it to mention %q", name, err, tc.wantSubstr)
		}
	}
}

// TestUnknownToken_AcceptsEveryKnownToken is the negative control for the
// rejection above: a scanner that flagged all "{{" would satisfy every case in
// TestParse_RejectsMalformedSources while making the real seeds unparseable.
func TestUnknownToken_AcceptsEveryKnownToken(t *testing.T) {
	body := "a " + strings.Join(knownTokens, " b ") + " c"
	if tok := unknownToken(body); tok != "" {
		t.Errorf("a body using only known tokens was flagged: %q", tok)
	}
	if tok := unknownToken("{{AGENT_NAME}} then {{NOPE}}"); tok != "{{NOPE}}" {
		t.Errorf("an unknown token following a known one was missed: got %q", tok)
	}
	if tok := unknownToken("dangling {{OPEN"); tok == "" {
		t.Error("an unterminated {{ was treated as clean")
	}
}
