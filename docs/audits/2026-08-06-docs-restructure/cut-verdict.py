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

    rows, missing = [], []
    for start, end, body in bl:
        found = destinations_of(body, corpus)
        first = body.strip().split("\n")[0][:90].replace("|", "\\|")
        if not found:
            missing.append((start, end, first))
            label = "UNACCOUNTED"
        else:
            label = " + ".join(found)
        rows.append((start, end, first, label))

    digest = hashlib.sha256(orig.encode()).hexdigest()
    print(f"# QUM-1155 cut verdict table\n")
    print(f"Original: `git show {ORIGINAL_REV}:CLAUDE.md`, sha256 `{digest}`, "
          f"{len(orig.splitlines())} lines, {nonblank} non-blank, {len(bl)} blocks.\n")
    print(f"Accounted: {len(bl) - len(missing)}/{len(bl)}. Unaccounted: {len(missing)}.\n")
    print("| lines | first line | verdict |")
    print("|---|---|---|")
    for start, end, first, label in rows:
        print(f"| {start}-{end} | {first} | {label} |")

    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main())
