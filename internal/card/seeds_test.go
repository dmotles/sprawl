package card

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/rootinit"
)

// wantSeeds pins, PER TYPE, the metadata each legacy card must carry.
//
// Per-type rather than an aggregate count: a tree that dropped legacy-qa and
// gained a junk seed still has four cards, and a `len(cards) >= 4` floor is
// satisfied by exactly the corruption it is supposed to catch.
//
// The models are asserted against rootinit.ModelForAgentType rather than
// written out here. That mapping is what spawn uses today, so the extraction is
// only lossless if the cards agree with it — and hand-copying "opus[1m]" into
// this table would make the assertion agree with itself.
var wantSeeds = map[string]struct {
	cardName string
	version  int
	effort   string
}{
	"engineer":   {"legacy-engineer", 1, "low"},
	"manager":    {"legacy-manager", 1, "low"},
	"researcher": {"legacy-researcher", 1, "low"},
	"qa":         {"legacy-qa", 1, "low"},
}

func TestSeeds_EveryEmbeddedCardParses(t *testing.T) {
	cards, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	for _, c := range cards {
		if c.Description == "" {
			t.Errorf("%s@%d: no description — `sprawl def list` would show a blank row", c.Name, c.Version)
		}
		if c.ContentSHA256 == "" {
			t.Errorf("%s@%d: no content hash", c.Name, c.Version)
		}
	}
}

// TestSeeds_MetadataMatchesTheBehaviourItReplaces is the metadata half of the
// extraction-fidelity claim. The prompt BODY is pinned byte-for-byte by
// internal/agent's golden comparison; nothing there looks at the frontmatter,
// so a card that quietly demoted the manager off its 1M-context model would
// pass every other test in this diff.
func TestSeeds_MetadataMatchesTheBehaviourItReplaces(t *testing.T) {
	for agentType, want := range wantSeeds {
		c, err := SeedForType(agentType)
		if err != nil {
			t.Errorf("SeedForType(%q): %v", agentType, err)
			continue
		}
		if c.Name != want.cardName || c.Version != want.version {
			t.Errorf("%s: got card %s@%d, want %s@%d", agentType, c.Name, c.Version, want.cardName, want.version)
		}
		if wantModel := rootinit.ModelForAgentType(agentType); c.Model != wantModel {
			t.Errorf("%s: card model %q does not match the model spawn uses today (%q) — the extraction changed behaviour",
				agentType, c.Model, wantModel)
		}
		if c.Effort != want.effort {
			t.Errorf("%s: card effort %q, want %q", agentType, c.Effort, want.effort)
		}
	}
}

// TestSeeds_EmbeddedCardsAreTheFilesOnDisk ties the binary to the tree: every
// card SeedForType can serve must hash to a file that is actually in seeds/,
// and that file must be named after the card. It closes the half the
// body-mutation control cannot reach — that control proves Render reads
// Card.Body, not that the embedded bytes came from the checked-in file.
//
// It does NOT prove the agent-type ROUTING is right; a misrouted SeedForType
// still hashes to a real file. That direction is covered by
// TestSeeds_MetadataMatchesTheBehaviourItReplaces, which pins card name per
// type.
func TestSeeds_EmbeddedCardsAreTheFilesOnDisk(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("seeds", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(wantSeeds) {
		t.Fatalf("found %d seed files on disk, want %d — %v", len(files), len(wantSeeds), files)
	}
	byHash := make(map[string]string, len(files))
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(src)
		byHash[hex.EncodeToString(sum[:])] = f
	}
	for agentType := range wantSeeds {
		c, err := SeedForType(agentType)
		if err != nil {
			t.Errorf("SeedForType(%q): %v", agentType, err)
			continue
		}
		f, ok := byHash[c.ContentSHA256]
		if !ok {
			t.Errorf("%s: the embedded card %s@%d matches no file in seeds/ — the binary and the tree disagree",
				agentType, c.Name, c.Version)
			continue
		}
		if want := filepath.Join("seeds", c.Name+".md"); f != want {
			t.Errorf("%s: card %s came from %s, want %s", agentType, c.Name, f, want)
		}
	}
}

// TestSeeds_EveryEmbeddedFileIsGlobbed guards the embed pattern itself: a
// directive that stopped matching would leave every other test in this file
// asserting over an empty set.
func TestSeeds_EveryEmbeddedFileIsGlobbed(t *testing.T) {
	embedded, err := fs.Glob(seedsFS, "seeds/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(wantSeeds) {
		t.Errorf("//go:embed matched %d files, want %d: %v", len(embedded), len(wantSeeds), embedded)
	}
}

func TestSeedForType_RejectsAnUnknownType(t *testing.T) {
	if c, err := SeedForType("no-such-type"); err == nil {
		t.Errorf("SeedForType returned a card for an agent type that has none: %+v", c)
	}
}

// TestSeedForType_PrefersTheHighestVersion is the control for the version pick.
// With only v1 cards embedded today the production path cannot exercise it, so
// a resolver that returned the FIRST match would look correct until the slim
// v2 cards land and silently keep serving legacy.
func TestSeedForType_PrefersTheHighestVersion(t *testing.T) {
	cards := []*Card{
		{Name: "a", Version: 1, AgentType: "engineer"},
		{Name: "a", Version: 7, AgentType: "engineer"},
		{Name: "a", Version: 3, AgentType: "engineer"},
		{Name: "b", Version: 9, AgentType: "manager"},
	}
	if got := pickHighest(cards, "engineer"); got == nil || got.Version != 7 {
		t.Fatalf("pickHighest returned %v, want a@7 — a first-match pick would return a@1", got)
	}
	if got := pickHighest(cards, "qa"); got != nil {
		t.Errorf("pickHighest invented a card for an absent type: %v", got)
	}
}
