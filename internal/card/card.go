// Package card holds agent cards: the prompt, model, effort and tool policy of
// an agent type, as immutable versioned DATA rather than Go string constants.
//
// The point of the move is that a card can be published, diffed and pinned
// without cutting a binary, while the compiled-in seeds keep spawn working when
// the event log is unreachable. Same shape as internal/store's seeded event
// types, and deliberately so — see the FROZEN note on cardNamespace.
package card

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// cardNamespace is the frozen UUIDv5 namespace for derived agent-card ids.
//
// FROZEN, and it is a hard consequence: the derivation inputs are the
// namespace, the "@" separator, the card name and the version. Changing any of
// them changes every id derived from it and strands every card_id already
// recorded in an agent_sessions row. A card gets a new NAME or a new VERSION;
// it never gets a new derivation.
var cardNamespace = uuid.MustParse("3f5b2c81-9d64-4a7e-8b30-6e1c4f92a7d5")

// CardID derives the stable id for a card's (name, version).
func CardID(name string, version int) uuid.UUID {
	return uuid.NewSHA1(cardNamespace, []byte(fmt.Sprintf("%s@%d", name, version)))
}

// RenderOpts are the env-derived blocks a card wants spliced around its body.
//
// They are flags rather than body text because none of them is prompt CONTENT
// the author is free to word: they are derived from the spawn environment, and
// a card that got to spell its own "# Environment" block could disagree with
// the worktree the agent is actually in.
// The json tags carry the same names as the yaml ones because the seed FILE and
// the `agent_cards.render` COLUMN are two encodings of one thing, and an operator
// reading a card in either place should not have to learn two vocabularies.
type RenderOpts struct {
	AppendEnvContext bool `yaml:"append_env_context" json:"append_env_context"`
	SubagentBanner   bool `yaml:"subagent_banner"    json:"subagent_banner"`
	SandboxWarning   bool `yaml:"sandbox_warning"    json:"sandbox_warning"`
}

// renderOptKeys are the wire names of RenderOpts' fields, derived from the struct
// tags by reflection rather than repeated as literals.
//
// Derived, not written down, because the alternative is two lists that nothing
// forces to agree: a renamed field with an un-renamed literal would make
// ParseRenderOpts demand a key that no writer produces, and every round-trip
// test would stay green because it encodes and decodes through the same struct.
var renderOptKeys = func() []string {
	t := reflect.TypeOf(RenderOpts{})
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		// Mirror encoding/json: an absent tag means the field is keyed by its Go
		// NAME. Without this fallback a dropped tag yields "", so the error below
		// reads `do not specify ""` — it still fails, which is what matters, but
		// it names nothing an operator can go and look at.
		if name := f.Tag.Get("json"); name != "" {
			out = append(out, name)
		} else {
			out = append(out, f.Name)
		}
	}
	return out
}()

// ParseRenderOpts decodes a card's render options from their stored JSON form,
// requiring every option to be PRESENT.
//
// Presence is checked separately from value because a struct decode cannot tell
// them apart: an absent key and an explicit `false` both leave the field false.
// That distinction matters here in a way it usually does not — these flags gate
// the sub-agent banner and the TEST SANDBOX MODE warning, so reading "nobody
// said" as "no splices" silently removes safety text from a prompt that still
// renders and still looks plausible. A caller that cannot read the options is
// expected to fall back to the compiled-in seed; one that reads them as all-false
// has no way to know it should.
//
// A `null` document is rejected for the same reason: encoding/json unmarshals it
// into a struct as a no-op, so it would otherwise arrive as all-false.
//
// Unknown keys are deliberately IGNORED. A card published by a newer build may
// carry an option this one does not model, and refusing it would make a forward
// version of the same card unspawnable; the immutability check compares source
// hashes and catches an edit, which is the thing that actually needs catching.
func ParseRenderOpts(raw []byte) (RenderOpts, error) {
	var opts RenderOpts
	if len(raw) == 0 {
		return opts, fmt.Errorf("card: render options are absent")
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return opts, fmt.Errorf("card: render options are not a JSON object: %w", err)
	}
	if present == nil {
		return opts, fmt.Errorf("card: render options are null")
	}
	for _, k := range renderOptKeys {
		if _, ok := present[k]; !ok {
			return opts, fmt.Errorf("card: render options do not specify %q", k)
		}
	}
	if err := json.Unmarshal(raw, &opts); err != nil {
		return opts, fmt.Errorf("card: decoding render options: %w", err)
	}
	return opts, nil
}

// Card is one immutable, versioned agent definition.
type Card struct {
	Name        string     `yaml:"name"`
	Version     int        `yaml:"version"`
	Description string     `yaml:"description"`
	AgentType   string     `yaml:"agent_type"`
	Families    []string   `yaml:"families"`
	Model       string     `yaml:"model"`
	Effort      string     `yaml:"effort"`
	Opts        RenderOpts `yaml:"render"`

	// Body is the prompt template: the frontmatter's trailing "---" line is not
	// part of it, and neither is the newline that terminates that line.
	Body string `yaml:"-"`

	// ContentSHA256 is the hash of the SOURCE BYTES the card was parsed from.
	//
	// Hashing the raw file rather than a canonicalised projection of the parsed
	// struct is the conservative direction: canonicalisation drops whatever it
	// does not model, so a field added to the file but not yet to Card would
	// hash identically to the file without it, and the immutability check would
	// wave through a real edit. A raw hash can only be over-sensitive, and an
	// over-sensitive immutability check fails loudly.
	ContentSHA256 string `yaml:"-"`
}

// ID returns the card's derived id.
func (c *Card) ID() uuid.UUID { return CardID(c.Name, c.Version) }

var frontmatterDelim = []byte("---\n")

// Parse reads a card from its `.md` source: YAML frontmatter fenced by `---`
// lines, then the prompt body.
func Parse(src []byte) (*Card, error) {
	if !bytes.HasPrefix(src, frontmatterDelim) {
		return nil, fmt.Errorf("card: source does not begin with a %q frontmatter fence", "---")
	}
	rest := src[len(frontmatterDelim):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return nil, fmt.Errorf("card: frontmatter is never closed by a %q line", "---")
	}
	head, body := rest[:end+1], rest[end+len("\n---\n"):]

	var c Card
	dec := yaml.NewDecoder(bytes.NewReader(head))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("card: frontmatter: %w", err)
	}
	// Exactly one trailing newline is stripped: a text file conventionally ends
	// with one and the prompt body does not, so keeping it would put a stray
	// blank line before every appended block. Only one, so a body that
	// deliberately ends blank can still say so with two.
	c.Body = strings.TrimSuffix(string(body), "\n")
	sum := sha256.Sum256(src)
	c.ContentSHA256 = hex.EncodeToString(sum[:])

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// knownTokens is the CLOSED set of substitutions Render performs.
//
// Closed on purpose. Rendering ends by refusing any surviving "{{", so an
// unknown token is a load-time error in card-lint and a hard render error at
// spawn — never a literal "{{PARNET_NAME}}" shipped to a model, which is the
// failure mode a typo produces if unknown tokens are simply left alone.
var knownTokens = []string{
	"{{AGENT_NAME}}",
	"{{PARENT_NAME}}",
	"{{BRANCH_NAME}}",
	"{{FAMILY}}",
}

// Validate checks the card is internally coherent. Called by Parse.
func (c *Card) Validate() error {
	switch {
	case c.Name == "":
		return fmt.Errorf("card: name is required")
	case c.Version < 1:
		return fmt.Errorf("card %q: version must be >= 1, got %d", c.Name, c.Version)
	case c.AgentType == "":
		return fmt.Errorf("card %q: agent_type is required", c.Name)
	case strings.TrimSpace(c.Body) == "":
		return fmt.Errorf("card %q: body is empty", c.Name)
	}
	if tok := unknownToken(c.Body); tok != "" {
		return fmt.Errorf("card %q: body uses unknown template token %s; known tokens are %s",
			c.Name, tok, strings.Join(knownTokens, " "))
	}
	return nil
}

// unknownToken returns the first "{{...}}" in s that is not in knownTokens, or
// "" if there is none.
func unknownToken(s string) string {
	for i := 0; ; {
		j := strings.Index(s[i:], "{{")
		if j < 0 {
			return ""
		}
		j += i
		k := strings.Index(s[j:], "}}")
		if k < 0 {
			return s[j:min(j+32, len(s))]
		}
		tok := s[j : j+k+2]
		if !contains(knownTokens, tok) {
			return tok
		}
		i = j + k + 2
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
