package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The sprawl-internals skill (QUM-1155) holds content RELOCATED out of
// CLAUDE.md, not a rewrite of it. These tests are byte-equality oracles, and
// their oracle is itself pinned by claudeMDRelocatedDigest below: without that
// pin, paraphrasing the source in the same commit would keep every equality test
// green, which is the cheapest possible fake pass.
//
// THIS IS THE CUT SLICE the original comment anticipated. The oracle was the
// live CLAUDE.md, which was sound only while no slice edited that file; the cut
// deleted every one of these sections from it, so the tests are now pointed at
// claudeMDFixture — the pre-cut text, frozen into the tree. They were NOT
// deleted: deleting them silently degrades "relocated verbatim" to
// "paraphrased" with nothing watching, and that fake pass would have been
// available to every slice in this issue.
//
// The digest constant below is UNCHANGED by the cut, and that is the check that
// the freeze was honest: the fixture is byte-identical to the CLAUDE.md that
// produced it. Never regenerate the fixture from a post-cut CLAUDE.md — that
// would make every assertion here vacuous rather than red.

const (
	sprawlInternalsSkill = ".claude/skills/sprawl-internals/SKILL.md"
	sprawlInternalsStub  = ".agents/skills/sprawl-internals/SKILL.md"

	// claudeMDFixture is CLAUDE.md as of c7093cc, the commit the relocation was
	// performed against.
	claudeMDFixture = "cmd/testdata/claude-md-c7093cc.md"
	// claudeMDOracleName is the display name used in failure messages, so a
	// diff never implies the live file still contains this text.
	claudeMDOracleName = "CLAUDE.md@c7093cc"
)

// readClaudeMDOracle returns the frozen pre-cut CLAUDE.md. See the file comment
// above for why this is a fixture and not the live file.
func readClaudeMDOracle(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, filepath.FromSlash(claudeMDFixture))
}

// claudeMDRelocatedDigest pins the CLAUDE.md text this slice relocates. Update
// it ONLY as a deliberate act, alongside the corresponding skill edit — a
// surprise mismatch means the oracle moved under the copy.
const claudeMDRelocatedDigest = "670456633948f2ed892af0a91eabcef06ad8e8e103476918f7af4fa8a20ff0e5"

func readSprawlInternalsSkill(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, filepath.FromSlash(sprawlInternalsSkill))
}

// relocatedSourceTexts returns, in slice order, every span of CLAUDE.md this
// slice relocates. It is both the digest input and the single place the source
// spans are named.
func relocatedSourceTexts(t *testing.T) []string {
	t.Helper()

	claudeMD := readClaudeMDOracle(t)
	return []string{
		mdSection(t, claudeMDOracleName, claudeMD, "Lifecycle model (QUM-786)"),
		mdSection(t, claudeMDOracleName, claudeMD, "Build & Test"),
		mdSection(t, claudeMDOracleName, claudeMD, "Install"),
		mdSection(t, claudeMDOracleName, claudeMD, "Project Configuration"),
		mdSection(t, claudeMDOracleName, claudeMD, "Repo Layout"),
		mdSection(t, claudeMDOracleName, claudeMD, "Code Patterns"),
		mdSection(t, claudeMDOracleName, claudeMD, "Linting & Formatting"),
	}
}

func TestCLAUDEMDRelocationOracleIsPinned(t *testing.T) {
	sum := sha256.Sum256([]byte(strings.Join(relocatedSourceTexts(t), "\n\x00\n")))
	got := hex.EncodeToString(sum[:])
	if got != claudeMDRelocatedDigest {
		t.Fatalf("CLAUDE.md's relocated sections changed: digest %s, pinned %s.\n"+
			"If that change is intended, re-relocate the affected text into %s and update claudeMDRelocatedDigest in the same commit.",
			got, claudeMDRelocatedDigest, sprawlInternalsSkill)
	}
}

// Frontmatter is read with the SAME parser cmd/skills_sync_test.go uses, so the
// two tests cannot return contradictory verdicts on one file (that parser strips
// surrounding quotes; a hand-rolled one here did not).
//
// Note 3 of the 9 pre-existing .claude skills carry no frontmatter at all. This
// test imposes the convention deliberately — frontmatter is what makes the skill
// discoverable in the skill listing — rather than following house style.
func TestSprawlInternalsSkillHasFrontmatter(t *testing.T) {
	meta := readSkillMetadata(t, filepath.Join(repoRootFromTest(t), filepath.FromSlash(sprawlInternalsSkill)))

	if meta.name != "sprawl-internals" {
		t.Errorf("%s: frontmatter name = %q, want %q", sprawlInternalsSkill, meta.name, "sprawl-internals")
	}
	if meta.description == "" {
		t.Errorf("%s: frontmatter description must be non-empty", sprawlInternalsSkill)
	}
}

// TestSprawlInternalsSkillRelocatesSectionsVerbatim is the fidelity oracle: byte
// equality against CLAUDE.md's section bodies. A summary or a reflow passes any
// presence check and fails this one, which is the whole point — the QUM-1155
// governing principle is that moving content audits it and rewriting it does not.
//
// The floor below bounds THIS test, not the slice: 7 spans are relocated and only
// these 4 are whole-section copies. Note also that a section body includes the
// blank line before the next heading, so a stray extra trailing newline at the
// end of the skill fails here with a diff that looks empty.
func TestSprawlInternalsSkillRelocatesSectionsVerbatim(t *testing.T) {
	sections := []string{
		"Install",
		"Project Configuration",
		"Repo Layout",
		"Linting & Formatting",
	}
	if len(sections) < 4 {
		t.Fatalf("assertion-count floor: expected at least 4 whole-section copies, have %d", len(sections))
	}

	claudeMD := readClaudeMDOracle(t)
	skill := readSprawlInternalsSkill(t)

	for _, heading := range sections {
		want := mdSection(t, claudeMDOracleName, claudeMD, heading)
		got := mdSection(t, sprawlInternalsSkill, skill, heading)
		if got != want {
			t.Errorf("%s: section %q is not byte-identical to CLAUDE.md's\n--- CLAUDE.md ---\n%q\n--- skill ---\n%q",
				sprawlInternalsSkill, heading, want, got)
		}
	}
}

// The lifecycle section's closing cross-reference must be repointed at the
// e2e-matrix skill. Both halves are asserted: without the negative half,
// appending the new pointer while leaving the stale "under **Validating
// Changes**" wording would pass.
//
// QUM-1186 removed this test's byte-equality arm on the CONTRACT text, and the
// removal is the point rather than a concession. The arm required the skill's
// lifecycle contract to stay byte-identical to `CLAUDE.md@c7093cc` — a fixture
// frozen in 2026 — so it kept passing only while the contract never changed.
// The moment the contract DID change, the fixture stopped being an oracle and
// started being an anti-correctness gate: it demanded the shipped skill go on
// documenting `delegate(<state>, task)` gates for a tool that no longer exists,
// and it would have gone red at whoever fixed that. A test that fails BECAUSE
// you corrected the documentation is describing the past, not guarding the
// present.
//
// What replaces it has a live subject: TestSprawlInternalsSkillLifecycleNamesRealStatuses
// below checks the section against `internal/state/state.go`, which is the
// thing the contract is supposed to be true of. The relocation-fidelity
// oracles that are still meaningful — the four whole-section copies, the
// Build & Test block, the Code Patterns subset, and the digest pin on the
// fixture itself — are untouched.
func TestSprawlInternalsSkillRewritesLifecycleCrossReference(t *testing.T) {
	const heading = "Lifecycle model (QUM-786)"
	const crossRefMarker = "Touched-file matrix-row mapping"
	const staleWording = "under **Validating Changes**"

	claudeSection := mdSection(t, claudeMDOracleName, readClaudeMDOracle(t), heading)
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), heading)

	// Positive control for the negative assertion below: the banned wording must
	// be findable in the source, or its absence from the skill proves nothing.
	// (It survives a line wrap in CLAUDE.md at "the table\nunder **Validating".)
	if !strings.Contains(claudeSection, staleWording) {
		t.Fatalf("%s %q: control failed — %q is not present in the source, so asserting its absence is vacuous",
			claudeMDOracleName, heading, staleWording)
	}

	_, skillCrossRef, ok := strings.Cut(skillSection, crossRefMarker)
	if !ok {
		t.Fatalf("%s: %q section is missing the %q paragraph", sprawlInternalsSkill, heading, crossRefMarker)
	}
	if !strings.Contains(skillCrossRef, ".claude/skills/e2e-matrix/SKILL.md") {
		t.Errorf("%s: cross-reference must cite `.claude/skills/e2e-matrix/SKILL.md` by path, got: %q",
			sprawlInternalsSkill, skillCrossRef)
	}
	// The row name is what makes the pointer actionable; citing the skill file
	// alone sends the reader to a table with ~30 rows.
	if !strings.Contains(skillCrossRef, "complete-lifecycle") {
		t.Errorf("%s: cross-reference must still name the `complete-lifecycle` row, got: %q",
			sprawlInternalsSkill, skillCrossRef)
	}
	if strings.Contains(skillSection, staleWording) {
		t.Errorf("%s: stale cross-reference %q to CLAUDE.md's own table survives", sprawlInternalsSkill, staleWording)
	}
}

// statusIdentRe matches a backticked Go status identifier as the lifecycle
// section writes them, e.g. `StatusComplete`.
var statusIdentRe = regexp.MustCompile("`(Status[A-Za-z]+)`")

// TestSprawlInternalsSkillLifecycleNamesRealStatuses replaces the byte-equality
// arm removed above with an assertion whose subject is the code, not a frozen
// copy of a document. Every `StatusXxx` the lifecycle contract names must be a
// real constant in internal/state/state.go, and the contract must cover the
// statuses that exist — so both a stale name left behind by a deletion and a
// newly added status nobody documented are caught.
//
// This is deliberately narrower than byte equality. It cannot detect a
// paraphrase, which byte equality could; that trade is the price of a gate that
// tracks the code instead of forbidding the code to change.
func TestSprawlInternalsSkillLifecycleNamesRealStatuses(t *testing.T) {
	const heading = "Lifecycle model (QUM-786)"

	stateSrc := readRepoFile(t, filepath.FromSlash("internal/state/state.go"))
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), heading)

	// Control for the whole test: the oracle must actually contain status
	// declarations, or every check below passes by finding nothing.
	declared := regexp.MustCompile(`(?m)^\s*(Status[A-Za-z]+)\s*=`).FindAllStringSubmatch(stateSrc, -1)
	if len(declared) < 5 {
		t.Fatalf("control failed: internal/state/state.go declares %d Status constants, expected at least 5 — the oracle is stale or the regexp stopped matching", len(declared))
	}
	declaredSet := map[string]bool{}
	for _, m := range declared {
		declaredSet[m[1]] = true
	}

	named := statusIdentRe.FindAllStringSubmatch(skillSection, -1)
	if len(named) < 3 {
		t.Fatalf("control failed: the %q section names %d `StatusXxx` identifiers, expected at least 3 — the section was gutted or the regexp stopped matching", heading, len(named))
	}
	for _, m := range named {
		if !declaredSet[m[1]] {
			t.Errorf("%s %q: names %q, which is not declared in internal/state/state.go — the contract documents a status that does not exist",
				sprawlInternalsSkill, heading, m[1])
		}
	}

	// The statuses whose gate behaviour the contract exists to state. StatusIdle
	// (QUM-1186 D2) is the one most likely to be missed: an undocumented resting
	// state reads to an operator as "complete", which is the exact confusion the
	// separate constant was added to prevent.
	for _, must := range []string{"StatusComplete", "StatusIdle"} {
		if !declaredSet[must] {
			t.Fatalf("control failed: %q is not declared in internal/state/state.go, so requiring the skill to document it is vacuous", must)
		}
		if !strings.Contains(skillSection, "`"+must+"`") {
			t.Errorf("%s %q: does not document %q, a resting state agents and operators must be able to tell apart",
				sprawlInternalsSkill, heading, must)
		}
	}

	// The contract must not describe the gate in terms of tools QUM-1186 deleted.
	for _, banned := range []string{"delegate(", "report_status"} {
		if strings.Contains(skillSection, banned) {
			t.Errorf("%s %q: still describes the gate via the deleted tool %q; send_message is the only entry point",
				sprawlInternalsSkill, heading, banned)
		}
	}
}

// The Build & Test command block is copied in full. It is asserted with Contains
// rather than section equality because CLAUDE.md's section also carries the
// out-of-slice `### What make validate guarantees…` subsection — which the
// negative half below keeps out. Both halves are scoped to the skill's OWN
// `## Build & Test` section, so pasting the block under some other heading fails.
func TestSprawlInternalsSkillCopiesBuildTestCommandBlock(t *testing.T) {
	claudeSection := mdSection(t, claudeMDOracleName, readClaudeMDOracle(t), "Build & Test")
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Build & Test")

	_, rest, ok := strings.Cut(claudeSection, "```bash\n")
	if !ok {
		t.Fatalf("CLAUDE.md ## Build & Test: no ```bash block found — the oracle is stale")
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatalf("CLAUDE.md ## Build & Test: unterminated ```bash block")
	}

	// Spot-check the oracle really carries the lines the slice calls out, so a
	// truncated extraction cannot make this test vacuously easy to satisfy.
	for _, must := range []string{
		"scripts/smoke-test-memory.sh",
		"scripts/sprawl-test-env.sh",
		"race-gate runs BEFORE test-race on purpose",
	} {
		if !strings.Contains(block, must) {
			t.Fatalf("CLAUDE.md ## Build & Test block is missing %q — the oracle is stale", must)
		}
	}

	if !strings.Contains(skillSection, block) {
		t.Errorf("%s ## Build & Test: CLAUDE.md's command block is not reproduced verbatim\n--- want ---\n%s",
			sprawlInternalsSkill, block)
	}

	for _, banned := range []string{
		"guarantees about data races",
		"atomicDuration",
	} {
		if !strings.Contains(claudeSection, banned) {
			t.Fatalf("CLAUDE.md ## Build & Test: control failed — %q absent from the source, so asserting its absence is vacuous", banned)
		}
		if strings.Contains(skillSection, banned) {
			t.Errorf("%s ## Build & Test: carries %q from the out-of-slice race-detector subsection, which belongs to the testing-practices slice",
				sprawlInternalsSkill, banned)
		}
	}
}

// Code Patterns is a PARAGRAPH SUBSET: the dependency-injection paragraph and the
// two skill pointers travel here; the testing-rigor paragraphs are routed to
// testing-practices by a sibling slice. The negative half carries the claim — a
// lazy copy-the-whole-section implementation passes the positive half alone.
func TestSprawlInternalsSkillCodePatternsIsParagraphSubset(t *testing.T) {
	const where = claudeMDOracleName + " ## Code Patterns"

	claudeSection := mdSection(t, claudeMDOracleName, readClaudeMDOracle(t), "Code Patterns")
	skillSection := mdSection(t, sprawlInternalsSkill, readSprawlInternalsSkill(t), "Code Patterns")

	keep := []string{
		"**Dependency injection**",
		"**Read `/go-cli-best-practices`",
		"**Read `/cli-ux-best-practices`",
	}
	for _, lead := range keep {
		// Edge whitespace is trimmed off each paragraph; the interior — which is
		// where a paraphrase would show — is compared byte for byte.
		para := paragraphStartingWith(t, where, claudeSection, lead)
		if !strings.Contains(skillSection, para) {
			t.Errorf("%s ## Code Patterns: paragraph starting %q is not reproduced verbatim\n--- want ---\n%s",
				sprawlInternalsSkill, lead, para)
		}
	}

	notThere := []string{
		"**Tests required**",
		"Every new assertion must demonstrate it CAN fail",
		"A watched failure proves the instrument works",
		"No fallback branch may silently succeed",
	}
	for _, banned := range notThere {
		// Positive control: a typo here, or a CLAUDE.md rewording, would make the
		// absence check below silently vacuous.
		if !strings.Contains(claudeSection, banned) {
			t.Fatalf("%s: control failed — %q is not present in the source, so asserting its absence is vacuous", where, banned)
		}
		if strings.Contains(skillSection, banned) {
			t.Errorf("%s ## Code Patterns: carries %q, which belongs to the testing-practices slice",
				sprawlInternalsSkill, banned)
		}
	}
}

// paragraphStartingWith returns the blank-line-delimited paragraph of body whose
// first line begins with lead. It fatals unless there is exactly one, so a
// duplicated lead cannot silently pick the wrong paragraph.
func paragraphStartingWith(t *testing.T, where, body, lead string) string {
	t.Helper()

	var found []string
	for _, para := range strings.Split(body, "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(para), lead) {
			found = append(found, strings.TrimSpace(para))
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: found %d paragraphs starting %q, want exactly 1 — the oracle is stale", where, len(found), lead)
	}
	return found[0]
}

// The stub's pointer is RESOLVED, not just string-matched: a typo duplicated
// between the test and the stub would otherwise pass.
func TestSprawlInternalsAgentsStubPointsAtClaudeSkill(t *testing.T) {
	const target = "../../../.claude/skills/sprawl-internals/SKILL.md"

	if !strings.Contains(readRepoFile(t, filepath.FromSlash(sprawlInternalsStub)), target) {
		t.Fatalf("%s: must name %q as the source of truth", sprawlInternalsStub, target)
	}

	stubDir := filepath.Join(repoRootFromTest(t), ".agents", "skills", "sprawl-internals")
	resolved := filepath.Join(stubDir, filepath.FromSlash(target))
	if _, err := os.Stat(resolved); err != nil {
		t.Errorf("%s: pointer %q does not resolve to a file (%s): %v", sprawlInternalsStub, target, resolved, err)
	}
}
