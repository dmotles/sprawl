#!/usr/bin/env python3
"""Generate the QUM-1155 section-by-section cut verdict table.

WHY THIS SHAPE. A per-line hash over the lines we meant to preserve cannot
detect a line we did not notice we were removing. So the enumeration runs the
other way round: every blank-line-delimited block of the 938-line original
CLAUDE.md at c7093cc is enumerated, and each block must receive a verdict.
Coverage of the original is asserted mechanically (every non-blank line of the
original belongs to exactly one block), so a block cannot be silently omitted.

Matching is normalized and DE-WRAPPED (newlines -> spaces, runs of whitespace
collapsed). A line-wise grep alone falsely reports wrapped prose as missing --
measured by a sibling slice at 15 of 103 phrase assertions matching only
de-wrapped. `tr '\\n' ' '` alone is NOT sufficient because the continuation
line's leading indent survives; the collapse step is what fixes that.

PROBE DISCIPLINE. --self-test asserts the instrument against subjects whose
answer is known before any verdict is believed: a known-present block must
report FOUND, a synthetic never-present block must report MISSING, and a
deliberately wrapped-in-the-original block must be found only because of the
de-wrap. A run whose self-test fails exits non-zero and prints no table -- a
zero from an unvalidated probe is not evidence.
"""

import hashlib
import re
import subprocess
import sys
from pathlib import Path

ORIGINAL_REV = "c7093cc"
REPO = Path(__file__).resolve().parents[3]

# Candidate destinations, in the order a verdict prefers them.
DESTINATIONS = [
    ("CLAUDE.md", "retained"),
    (".claude/skills/e2e-matrix/SKILL.md", "moved:e2e-matrix"),
    (".claude/skills/git-recovery/SKILL.md", "moved:git-recovery"),
    (".claude/skills/sprawl-internals/SKILL.md", "moved:sprawl-internals"),
    (".claude/skills/testing-practices/SKILL.md", "moved:testing-practices"),
    (".claude/skills/tui-testing/SKILL.md", "moved:tui-testing"),
    (".claude/skills/e2e-testing-sandboxing/SKILL.md", "moved:e2e-testing-sandboxing"),
    (".claude/skills/linear-issues/SKILL.md", "moved:linear-issues"),
    (".claude/skills/handoff/SKILL.md", "moved:handoff"),
    (".claude/skills/false-red/SKILL.md", "moved:false-red"),
    (".claude/skills/go-cli-best-practices/SKILL.md", "moved:go-cli-best-practices"),
    (".claude/skills/cli-ux-best-practices/SKILL.md", "moved:cli-ux-best-practices"),
]


# MANUAL VERDICTS.
#
# A block reaches this table only when byte-matching could not account for it.
# That is not a defect in the destinations: relocation legitimately re-levels a
# heading, reflows a paragraph, or — twice below — deliberately CORRECTS text
# that was wrong. But it does mean the verdict rests on someone having read the
# block and its destination, so each entry states which, and why.
#
# Two failure directions are both errors: a block with neither an auto-match nor
# an entry here (unaccounted — possibly lost), and an entry here for a block that
# now auto-matches (stale — the justification has quietly stopped applying).
OVERRIDES = {
    # --- reworded in the cut itself; the claim survives in the new CLAUDE.md ---
    (3, 3): ("retained:reworded", "The DESCRIPTION.md pointer. Rewritten so it is a pointer rather than a mandated read: the budget resolver flagged the original as a read-instruction violation, and @-importing DESCRIPTION.md would add 195 lines to the surface being bounded."),
    (707, 707): ("retained:reworded", "'Meta: Developing Sprawl Inside Sprawl' heading; folded into the opening paragraph."),
    (709, 709): ("retained:reworded", "'This repo IS Sprawl' orientation; condensed into the opening paragraph, including the do-not-touch-.sprawl rule."),
    (715, 715): ("retained:reworded", "'Tests required' — every cmd/ and internal/ file has a _test.go. Kept as a bullet under Tests and assertions."),
    (717, 717): ("retained:reworded", "'Every new assertion must demonstrate it CAN fail'. Kept as a one-liner naming all three demonstrations; the long form is in testing-practices."),
    (726, 726): ("retained:reworded", "'No fallback branch may silently succeed'. Kept as a one-liner including the 77-not-0 skip rule and the assertion-count floor."),
    (733, 733): ("retained:reworded", "'Public vs Private Repo Hygiene' heading, kept as '## Public vs private repo hygiene'."),
    (735, 735): ("retained:reworded", "The determine-public-or-private instruction; retained condensed."),
    (737, 738): ("retained:reworded", "The visibility-probe commands and the default-to-PUBLIC rule; retained condensed, both commands kept."),
    (740, 744): ("retained:reworded", "The PUBLIC-repo prohibition list; retained near-verbatim as one paragraph, including the findings/ destination and the forensic-artifact caveat."),
    (746, 747): ("retained:reworded", "The PRIVATE-repo rule; retained as one clause."),
    (749, 750): ("retained:reworded", "'applies to all agents' + the reviewer duty; retained."),
    (752, 752): ("retained:reworded", "Linear is the tracker + CLAUDE.local.md holds the config; retained."),
    (754, 754): ("retained:reworded", "'invoke /linear-issues before creating an issue'; retained."),
    (756, 759): ("retained:reworded", "The three-step issue lifecycle (In Progress + comment / log as you go / Done with summary); retained compressed into one sentence."),
    (761, 761): ("retained:reworded", "'Spawning Agents' heading; folded into the Linear section."),
    (763, 763): ("retained:reworded", "'keep the prompt short, point the agent at the issue'; retained."),
    (765, 765): ("retained:reworded", "'the issue is the source of truth'; retained."),
    (788, 788): ("retained:reworded", "'Validating Changes' heading; folded into '## Build & validate'."),
    (790, 794): ("retained:corrected", "Validate-pipeline item list, lines 788..793 — OUTSIDE the 794..938 byte-identity range wave 1 evidenced, and in no skill. Item 4's mandate ('TUI validation is mandatory for all TUI-related changes') is RETAINED verbatim as a mandate; items 2 and 3 are retained as the smoke-test and sandbox pointers. Item 1 is deliberately NOT carried forward verbatim because it is FALSE: it describes validate as 'build, fmt-check, lint, test' when Makefile:4 runs proto-check and the gate suites too, and runs test-race, not test — which since QUM-972 is the whole point. Replaced by a pointer to the Makefile as authoritative. Item 5 is the union-rule lead-in and is inside the hashed range in e2e-matrix."),
    (593, 593): ("retained:reworded", "'tmux safety' heading; the prohibition became a Prohibitions bullet."),
    (595, 597): ("retained:reworded+corrected", "'Never run bare tmux kill-server' is RETAINED as a prohibition with a pointer to e2e-testing-sandboxing. The block's `_stmux kill-session -t $SPRAWL_NAMESPACE` recommendation is deliberately NOT carried forward: the namespace names the socket, not the session, so the command cannot work. Dropped by the sandbox slice for the same reason; carrying it into the always-loaded surface would propagate a broken command."),
    (767, 767): ("retained:reworded", "'Session Handoff' heading; the handoff skill is a skills-index entry."),
    (769, 769): ("retained:reworded", "'use /handoff at the end of a session'; retained as a skills-index entry marked weave-only."),
    (771, 771): ("retained:reworded", "'Sandbox Testing' heading; became the e2e-testing-sandboxing skills-index entry."),
    (773, 773): ("retained:reworded", "'use the /e2e-testing-sandboxing skill'; retained as a skills-index entry and in Build & validate."),
    (775, 778): ("moved:e2e-testing-sandboxing+corrected", "The sandbox quick-start block (`make build` + `eval \"$(bash scripts/sprawl-test-env.sh)\"`). Present at the destination and deliberately CORRECTED: the relative-path form here fails from inside a .sprawl/worktrees/ path — the script refuses by design — so the skill spells it `cd /tmp` first and invokes by absolute path, and says so explicitly."),

    # --- moved, but re-levelled or reflowed at the destination ---
    (45, 46): ("moved:sprawl-internals", "The lifecycle cross-reference. Deliberately REWRITTEN at the destination to cite the e2e-matrix skill by path instead of CLAUDE.md's own '## Validating Changes' table, which no longer exists. Pinned by TestSprawlInternalsSkillRewritesLifecycleCrossReference, which asserts both halves."),
    (148, 148): ("moved:git-recovery", "'Commit guard (QUM-808)' heading, re-levelled to '## Guards: what stops you landing on `main` (QUM-808)'. Body auto-matched."),
    (210, 210): ("moved:git-recovery", "Heading, re-levelled to '## A commit landed on `main` by mistake'."),
    (237, 237): ("moved:git-recovery", "Heading, re-levelled; the QUM-1083 body auto-matched."),
    (264, 264): ("moved:git-recovery", "'Step 1' bold lead, re-levelled to a '### Step 1 — gate on the base being content-equivalent' heading."),
    (273, 273): ("moved:git-recovery", "'Step 2' bold lead, re-levelled to a heading."),
    (295, 295): ("moved:git-recovery", "'Step 3' bold lead, re-levelled to a heading."),
    (323, 323): ("moved:git-recovery", "'Step 4' bold lead, re-levelled to a heading."),
    (354, 354): ("moved:git-recovery", "Heading, re-levelled; the QUM-1087 body auto-matched."),
    (395, 395): ("moved:git-recovery", "Heading, re-levelled; the QUM-1090 body auto-matched."),
    (448, 448): ("moved:git-recovery", "Heading, re-levelled to '## Never overwrite the thing that tells you where you were'."),
    (512, 512): ("moved:git-recovery", "Heading, re-levelled to '## Staging: never `git add -A` (QUM-989)'."),
    (247, 257): ("moved:git-recovery", "'Both natural checks lie, in opposite directions' — present under its own heading, reflowed. Verified de-wrapped: 'skipped previously applied commit' and the patch-id reasoning are both there."),
    (259, 262): ("moved:git-recovery", "'Prevent, don't recover'. Verified de-wrapped: 'When two branches share a base, either **merge the dependent one first**' is present verbatim."),
    (318, 321): ("moved:git-recovery", "'A clean cherry-pick is not evidence of an identical tree'. Verified de-wrapped: 'The wrong range exits **0** with content silently missing' is present verbatim."),
    (340, 348): ("moved:git-recovery", "'Check that the question the command answers is the question you are claiming' — promoted to its own heading; body verified de-wrapped."),
    (305, 307): ("moved:git-recovery", "The main-has-advanced caveat; verified de-wrapped ('that diff reports `main`'s later commits')."),
    (239, 245): ("moved:git-recovery", "'The precondition'; verified de-wrapped ('Squash-merging a base branch to `main` replaces its commits')."),
    (435, 441): ("moved:git-recovery", "The refs/sprawl/premerge/ ownership rule; verified de-wrapped ('owned **exclusively** by this mechanism'). Reflowed and extended with the rescue/ and manual/ namespaces."),
    (443, 446): ("moved:git-recovery", "The `sprawl gc` retention rule; present at the destination with 'these' reworded to 'those'. --premerge-retention-days, the 14-day default, ageing by the ref-name timestamp, and never-prune-an-unparseable-name all survive."),
    (528, 531): ("moved:git-recovery", "'The two `main` guards do not help here' — the correct-branch/correct-identity/foreign-content point; verified de-wrapped."),

    # --- moved AND deliberately corrected at the destination ---
    (212, 215): ("moved:git-recovery+corrected", "Wrong-tree recovery preamble. The destination deliberately REPLACES the `--soft` advice: in the main checkout HEAD *is* main, so --soft (and a bare update-ref) leaves the stray tree staged, to be silently re-landed by the next commit. The skill prescribes --mixed and says in as many words that the CLAUDE.md wording was wrong and must not be restored. The `reset --hard` prohibition survives intact."),
    (217, 232): ("moved:git-recovery+corrected", "The numbered recovery steps, reformatted into an annotated bash block and corrected per the entry above. Step 3 is now `reset --mixed`, and the verification changed from 'confirm status is clean' (unachievable) to an argument-order-checked `merge-base --is-ancestor <stray-sha> main`."),
    (234, 235): ("moved:git-recovery", "'the guard makes this recovery a rare exception' — the claim survives as the skill's framing of the guards section; the sentence itself was not carried."),

    # --- edited by THIS slice, at the destination ---
    (832, 832): ("moved:e2e-matrix+repointed", "The 'Not logged in' misdiagnosis paragraph. Breakage R1: it said 'see the run-claude shim and .env **above**', where 'above' meant a CLAUDE.md section ~180 lines earlier that went to a DIFFERENT skill — so the sentence telling a misdiagnosing agent how to fix auth pointed at nothing. Repointed by path at e2e-testing-sandboxing. Recorded in that skill's provenance header."),
    (889, 905): ("moved:e2e-matrix+repointed", "The self-falsifying-count paragraph. Breakage R2: 'these paragraphs live inside the corpus they describe' named the wrong corpus after the move, and the recommended `grep -E '^   \\| ' CLAUDE.md` returns zero rows post-cut while looking like an answer. Both halves repointed at the skill file. Recorded in that skill's provenance header."),
}


def norm(text: str) -> str:
    """De-wrapped, whitespace-collapsed form. See module docstring."""
    return re.sub(r"\s+", " ", text.replace("\n", " ")).strip()


def original() -> str:
    return subprocess.run(
        ["git", "-C", str(REPO), "show", f"{ORIGINAL_REV}:CLAUDE.md"],
        check=True, capture_output=True, text=True,
    ).stdout


def blocks(text: str):
    """Blank-line-delimited blocks as (start_line, end_line, body) 1-indexed."""
    out, start, buf = [], None, []
    for i, line in enumerate(text.split("\n"), start=1):
        if line.strip() == "":
            if start is not None:
                out.append((start, i - 1, "\n".join(buf)))
                start, buf = None, []
        else:
            if start is None:
                start = i
            buf.append(line)
    if start is not None:
        out.append((start, len(text.split("\n")), "\n".join(buf)))
    return out


def load_corpus():
    corpus = {}
    for rel, _ in DESTINATIONS:
        p = REPO / rel
        corpus[rel] = norm(p.read_text()) if p.exists() else None
    return corpus


def destinations_of(body: str, corpus):
    """EVERY destination containing this block, not the first. Reporting only
    the first would hide unintended duplication into the surface being shrunk,
    which is a defect this table exists to surface."""
    hay = norm(body)
    if not hay:
        return []
    return [label for rel, label in DESTINATIONS
            if corpus[rel] is not None and hay in corpus[rel]]


def verdict_for(body: str, corpus):
    found = destinations_of(body, corpus)
    return (found[0] if found else None), None


def self_test(orig, corpus) -> bool:
    """Validate the instrument on subjects whose answer is known. Any failure
    is fatal: an unvalidated probe's zero is not evidence."""
    ok = True

    def check(name, got, want):
        nonlocal ok
        if got != want:
            print(f"SELF-TEST FAIL: {name}: got {got!r}, want {want!r}", file=sys.stderr)
            ok = False
        else:
            print(f"self-test ok: {name}", file=sys.stderr)

    # 1. Known-present: the Terminology definition is retained in CLAUDE.md.
    check("known-present block is FOUND",
          verdict_for("- **agent** — a sprawl-spawned process with its own worktree and its own\n  Claude session.", corpus)[0] is not None,
          True)

    # 2. Known-absent: a synthetic string no corpus file can contain.
    check("synthetic never-present block is MISSING",
          verdict_for("zzz-qum1155-probe-control-never-present-zzz", corpus)[0],
          None)

    # 3. The de-wrap is load-bearing, proved on a subject whose answer is known
    #    both ways. Take a block that IS present in a destination and re-wrap it
    #    at a different column with an indented continuation — exactly the
    #    transformation a relocation-with-reflow performs. Line-wise matching
    #    must MISS it (proving the naive probe would report a false absence) and
    #    the de-wrapped matching must FIND it. Note `tr '\n' ' '` alone would
    #    also miss this, because the continuation indent survives; the
    #    whitespace-collapse step is what closes it.
    subject = None
    for _, _, body in blocks(orig):
        if "IsTerminal(status)" in body and len(body) > 120:
            subject = body
            break
    if subject is None:
        print("SELF-TEST FAIL: no control subject found — the control is stale", file=sys.stderr)
        return False
    words = subject.split()
    rewrapped = "\n    ".join(" ".join(words[i:i + 7]) for i in range(0, len(words), 7))
    if rewrapped == subject:
        print("SELF-TEST FAIL: re-wrap was a no-op — the control asserts nothing", file=sys.stderr)
        return False
    linewise = any(
        (REPO / rel).exists() and rewrapped in (REPO / rel).read_text()
        for rel, _ in DESTINATIONS
    )
    check("re-wrapped control is MISSED line-wise (a naive probe reports a false absence)", linewise, False)
    check("re-wrapped control is FOUND de-wrapped", verdict_for(rewrapped, corpus)[0] is not None, True)

    # Reported, not asserted: how many real blocks are found ONLY de-wrapped.
    only_dewrapped = 0
    for _, _, body in blocks(orig):
        if verdict_for(body, corpus)[0] is None:
            continue
        if not any((REPO / rel).exists() and body in (REPO / rel).read_text() for rel, _ in DESTINATIONS):
            only_dewrapped += 1
    print(f"self-test note: {only_dewrapped} accounted blocks match ONLY de-wrapped", file=sys.stderr)

    return ok


def main() -> int:
    orig = original()
    bl = blocks(orig)

    # Coverage assertion: every non-blank line of the original is in exactly one
    # block. A block cannot be silently dropped from the enumeration.
    covered = sum(e - s + 1 for s, e, _ in bl)
    nonblank = sum(1 for line in orig.split("\n") if line.strip())
    if covered != nonblank:
        print(f"COVERAGE FAIL: blocks cover {covered} lines, original has {nonblank} non-blank", file=sys.stderr)
        return 1
    print(f"coverage ok: {len(bl)} blocks cover all {nonblank} non-blank lines of {ORIGINAL_REV}:CLAUDE.md", file=sys.stderr)

    corpus = load_corpus()
    if not self_test(orig, corpus):
        return 1

    if "--self-test" in sys.argv:
        return 0

    rows, missing, stale = [], [], []
    for start, end, body in bl:
        found = destinations_of(body, corpus)
        first = body.strip().split("\n")[0][:90].replace("|", "\\|")
        override = OVERRIDES.get((start, end))
        if found:
            label, why = " + ".join(found), "byte-match against the destination"
            if override:
                stale.append((start, end, first))
                why = "STALE OVERRIDE: this block now byte-matches; delete its OVERRIDES entry"
        elif override:
            label, why = override
        else:
            missing.append((start, end, first))
            label, why = "UNACCOUNTED", "no destination contains this text and no verdict was recorded"
        rows.append((start, end, first, label, why))

    digest = hashlib.sha256(orig.encode()).hexdigest()
    print(f"# QUM-1155 cut verdict table\n")
    print(f"Original: `git show {ORIGINAL_REV}:CLAUDE.md`, sha256 `{digest}`, "
          f"{len(orig.splitlines())} lines, {nonblank} non-blank, {len(bl)} blocks.\n")
    auto = sum(1 for r in rows if r[4] == "byte-match against the destination")
    print(f"Verdicts: {len(bl) - len(missing)}/{len(bl)} accounted "
          f"({auto} by byte-match against the destination, {len(bl) - len(missing) - auto} by recorded manual verdict). "
          f"Unaccounted: {len(missing)}. Stale overrides: {len(stale)}.\n")
    print("Every non-blank line of the original is inside exactly one row, asserted "
          "mechanically above, so a block cannot be dropped from this enumeration "
          "without the generator failing.\n")
    print("| lines | first line | verdict | basis |")
    print("|---|---|---|---|")
    for start, end, first, label, why in rows:
        print(f"| {start}-{end} | {first} | {label} | {why} |")

    if missing:
        print("\n## UNACCOUNTED — content that may have been lost\n", file=sys.stderr)
        for start, end, first in missing:
            print(f"  {start}-{end}: {first}", file=sys.stderr)
    if stale:
        print("\n## STALE OVERRIDES\n", file=sys.stderr)
        for start, end, first in stale:
            print(f"  {start}-{end}: {first}", file=sys.stderr)

    return 1 if (missing or stale) else 0


if __name__ == "__main__":
    sys.exit(main())
