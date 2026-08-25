package card

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantSeedFiles is every card expected to exist in seeds/, BY NAME.
//
// A list rather than a count, because a count is satisfied by exactly the
// corruption it should catch: delete legacy-qa.md, add junk.md, and any
// `len(files) == 8` check still holds while the card that carries QA's whole
// prompt is gone. It is also separate from wantSeeds below because the two axes
// diverged when the slim v2 cards landed — eight files, four agent types — and
// reusing one table for both would have made the file census silently stop
// covering the legacy half.
var wantSeedFiles = []string{
	"legacy-engineer", "legacy-manager", "legacy-qa", "legacy-researcher",
	"slim-engineer", "slim-manager", "slim-qa", "slim-researcher",
}

// wantSeeds pins, PER TYPE, the metadata the card a spawn RESOLVES TO must
// carry. This tracks the current definition of each role, so it names the slim
// v2 cards; the legacy cards are pinned by name, separately, wherever a test
// makes a claim specifically about them (see legacyCards in this package and
// legacySeed in internal/agent). Note the consequence: this table no longer pins
// the legacy cards' effort or agent_type. That is deliberate — legacy is frozen
// evidence on its way out, pinned byte-for-byte by the goldens — not an oversight
// to be "fixed" by widening the table back over both generations.
//
// Per-type rather than an aggregate count, for the same reason as above.
//
// The model each card must carry is NOT here: asserting it needs
// rootinit.ModelForAgentType, and internal/rootinit imports internal/agent,
// which imports this package. That claim lives in internal/agent as
// TestLegacyCards_MetadataMatchesTheBehaviourItReplaces.
var wantSeeds = map[string]struct {
	cardName string
	version  int
	effort   string
}{
	"engineer":   {"slim-engineer", 2, "low"},
	"manager":    {"slim-manager", 2, "low"},
	"researcher": {"slim-researcher", 2, "low"},
	"qa":         {"slim-qa", 2, "low"},
}

func TestSeeds_EveryEmbeddedCardParses(t *testing.T) {
	cards, err := Seeds()
	if err != nil {
		t.Fatalf("Seeds: %v", err)
	}
	if len(cards) == 0 {
		t.Fatal("Seeds returned nothing — every assertion below would pass over zero iterations")
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
	assertCardNames(t, "seeds/ on disk", files)
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
	assertCardNames(t, "//go:embed", embedded)
}

func TestSeedForType_RejectsAnUnknownType(t *testing.T) {
	if c, err := SeedForType("no-such-type"); err == nil {
		t.Errorf("SeedForType returned a card for an agent type that has none: %+v", c)
	}
}

// TestSeedForType_PrefersTheHighestVersion is the control for the version pick,
// driven through fixtures so it does not depend on how many generations of card
// happen to be embedded. A resolver that returned the FIRST match would serve
// legacy forever — legacy sorts before slim — while looking correct.
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

// TestPickNamed_IsExactOnBothNameAndVersion.
//
// Exactness is the whole point of this accessor, and it is why it cannot just
// call pickHighest with a name filter. Its callers are the legacy-fidelity
// goldens, which must stay pinned to legacy@1 no matter how many later versions
// of the same card get embedded — a "highest version of this name" lookup would
// follow a legacy@2 and turn those goldens back into the moving target the
// name-keyed accessor exists to stop.
//
// Driven through fixtures rather than the embedded seeds because the tree still
// has exactly one version of each NAME (legacy-x@1 and slim-x@2 are different
// names), so nothing in production can tell an exact lookup from a
// highest-of-name one.
func TestPickNamed_IsExactOnBothNameAndVersion(t *testing.T) {
	cards := []*Card{
		{Name: "a", Version: 1, AgentType: "engineer"},
		{Name: "a", Version: 7, AgentType: "engineer"},
		{Name: "b", Version: 1, AgentType: "manager"},
	}
	got := pickNamed(cards, "a", 1)
	if got == nil {
		t.Fatalf("pickNamed found no a@1")
	}
	if got.Version != 1 {
		t.Errorf("pickNamed(a, 1) returned a@%d — a highest-of-name pick would return a@7", got.Version)
	}
	// The version argument in the OTHER direction too. Without this leg an
	// implementation that always returns the LOWEST version of a name whenever
	// any match exists satisfies every other assertion here.
	if got := pickNamed(cards, "a", 7); got == nil || got.Version != 7 {
		t.Errorf("pickNamed(a, 7) returned %v, want a@7", got)
	}
	// Both halves of "exact", because a lookup that ignored the version and one
	// that ignored the name each satisfy the assertion above.
	if got := pickNamed(cards, "a", 2); got != nil {
		t.Errorf("pickNamed(a, 2) invented %s@%d for a version that is not embedded", got.Name, got.Version)
	}
	if got := pickNamed(cards, "c", 1); got != nil {
		t.Errorf("pickNamed(c, 1) returned %s@%d for a name that is not embedded", got.Name, got.Version)
	}
}

// legacyCards is the table this file's legacy pinning asserts against.
//
// Deliberately NOT wantSeeds, which is keyed by agent type and pins whatever
// SeedForType RESOLVES to — so when the slim v2 cards land, wantSeeds becomes
// {"slim-engineer", 2, …} by design. A legacy pin that read wantSeeds would
// follow that edit, silently start asserting SeedByName("slim-engineer", 2), and
// go green while measuring the exact opposite of its name. A local table is the
// only version a slim landing cannot reach.
var legacyCards = map[string]string{
	"engineer":   "legacy-engineer",
	"manager":    "legacy-manager",
	"researcher": "legacy-researcher",
	"qa":         "legacy-qa",
}

// TestSeedByName_ServesEveryLegacyCardUnderItsOwnName is the accessor the
// legacy-fidelity tests re-point onto, asserted against the real embedded set.
//
// The AgentType leg is what makes this more than a name echo: an accessor that
// fabricated &Card{Name: name, Version: version} would satisfy a name comparison
// against its own argument.
func TestSeedByName_ServesEveryLegacyCardUnderItsOwnName(t *testing.T) {
	for agentType, name := range legacyCards {
		c, err := SeedByName(name, 1)
		if err != nil {
			t.Errorf("SeedByName(%q, 1): %v", name, err)
			continue
		}
		if c.Name != name || c.Version != 1 {
			t.Errorf("SeedByName(%q, 1) returned %s@%d", name, c.Name, c.Version)
		}
		if c.AgentType != agentType {
			t.Errorf("%s: SeedByName(%q) returned a card whose agent_type is %q", agentType, name, c.AgentType)
		}
		if c.Body == "" {
			t.Errorf("%s: SeedByName(%q) returned a card with an empty body — the goldens would compare against the render template alone", agentType, name)
		}
	}
}

// TestSeedByName_RefusesWhatIsNotEmbedded. An accessor that fell back to
// "something close" would let a golden silently re-point at a different card,
// which is precisely the failure the name pinning prevents.
func TestSeedByName_RefusesWhatIsNotEmbedded(t *testing.T) {
	if c, err := SeedByName("legacy-engineer", 99); err == nil {
		t.Errorf("SeedByName returned %s@%d for a version that is not embedded", c.Name, c.Version)
	}
	if c, err := SeedByName("no-such-card", 1); err == nil {
		t.Errorf("SeedByName returned %s@%d for a name that is not embedded", c.Name, c.Version)
	}
}

// TestSeeds_MetadataMatchesTheTable pins the card name, version and effort each
// agent type resolves to. It is the routing half of the metadata claim: a
// misrouted SeedForType still returns a real, well-formed card, so every
// body-level test in this package passes. The AgentType assertion covers the
// second way that can go wrong — a card whose frontmatter agent_type disagrees
// with the type it was served for, which the card-name check alone would miss
// if the seed file itself carried the wrong type.
func TestSeeds_MetadataMatchesTheTable(t *testing.T) {
	for agentType, want := range wantSeeds {
		c, err := SeedForType(agentType)
		if err != nil {
			t.Errorf("SeedForType(%q): %v", agentType, err)
			continue
		}
		if c.Name != want.cardName || c.Version != want.version {
			t.Errorf("%s: got card %s@%d, want %s@%d", agentType, c.Name, c.Version, want.cardName, want.version)
		}
		if c.AgentType != agentType {
			t.Errorf("%s: resolved a card whose agent_type is %q", agentType, c.AgentType)
		}
		if c.Effort != want.effort {
			t.Errorf("%s: card effort %q, want %q", agentType, c.Effort, want.effort)
		}
	}
}

// assertCardNames compares a set of seeds/*.md paths against wantSeedFiles by
// NAME, in both directions: a missing card and an unexpected extra card are
// distinct failures with distinct messages. Set comparison rather than a length
// check, so "eight files" cannot stand in for "these eight files".
func assertCardNames(t *testing.T, where string, paths []string) {
	t.Helper()
	got := make(map[string]bool, len(paths))
	for _, p := range paths {
		got[strings.TrimSuffix(filepath.Base(p), ".md")] = true
	}
	for _, want := range wantSeedFiles {
		if !got[want] {
			t.Errorf("%s: %s.md is missing", where, want)
		}
		delete(got, want)
	}
	for extra := range got {
		t.Errorf("%s: unexpected card %s.md — add it to wantSeedFiles if it is intended", where, extra)
	}
}

// TestSeedTables_CannotDrift ties the two tables in this file together.
//
// They are separate on purpose (eight files, four agent types), which means a
// third generation of cards can be added to one and forgotten in the other, and
// nothing else in the package would notice: the file census would pass over the
// new file as "unexpected" only if wantSeedFiles were the one left behind, and
// the type table would pass unchanged if it were the other.
func TestSeedTables_CannotDrift(t *testing.T) {
	files := make(map[string]bool, len(wantSeedFiles))
	for _, n := range wantSeedFiles {
		files[n] = true
	}
	for agentType, want := range wantSeeds {
		if !files[want.cardName] {
			t.Errorf("wantSeeds says %s resolves to %s, but %s.md is not in wantSeedFiles", agentType, want.cardName, want.cardName)
		}
	}
}
