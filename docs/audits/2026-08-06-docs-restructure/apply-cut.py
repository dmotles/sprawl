#!/usr/bin/env python3
"""Apply the QUM-1155 CLAUDE.md DELETIONS as anchored hunks.

SCOPE, precisely: this removes the relocated sections. It does NOT reproduce
the committed CLAUDE.md — the retained sections were then rewritten by hand
(condensed prohibitions, the skills index, the corrected `make validate` and
`_test.go` claims). Running this against c7093cc yields an 11-line skeleton,
not the 74-line committed file. It is recorded so the deletion boundaries are
reviewable and re-derivable, not as a build step.

NOT a whole-file replacement. Each hunk is a span delimited by two exact,
UNIQUE anchor lines. That is the point: `main` may move under this branch, and
a moved base must CONFLICT rather than silently lose whatever landed. Deleting
by line number would do the opposite — it would apply cleanly to the wrong text.

ALL-OR-NOTHING, and this is the correction of a real defect in the first
version: an absent start anchor used to `continue`, so the script rewrote the
file with the remaining hunks and exited 0. The single failure mode the
paragraph above names — a base that moved, e.g. a heading reworded upstream —
was therefore the one case that silently half-applied and reported success. Now
the hunks are resolved BEFORE anything is written, and a run is valid only if
every hunk resolves (a fresh apply) or none does (already applied). Any mixture
aborts with the offending anchors named and the file untouched.
"""

import sys
from pathlib import Path

TARGET = Path(__file__).resolve().parents[3] / "CLAUDE.md"

# (start_anchor, end_anchor_exclusive_or_None_for_EOF)
DELETIONS = [
    ("## Lifecycle model (QUM-786)", "## Commit guard (QUM-808)"),
    ("## Commit guard (QUM-808)", "## Install"),
    ("## Install", "## Text selection in `sprawl enter` (QUM-653 / QUM-731)"),
    ("## Text selection in `sprawl enter` (QUM-653 / QUM-731)", "## Meta: Developing Sprawl Inside Sprawl"),
    ("## Meta: Developing Sprawl Inside Sprawl", None),
]


def main() -> int:
    lines = TARGET.read_text().split("\n")

    # Resolve every hunk against a scratch copy BEFORE writing anything, so a
    # partial application cannot reach the file. Deleting as we go is what let
    # the original half-apply and exit 0.
    scratch = list(lines)
    resolved, absent = [], []

    for start, end in DELETIONS:
        hits = [i for i, ln in enumerate(scratch) if ln == start]
        if not hits:
            absent.append(start)
            continue
        if len(hits) > 1:
            print(f"ABORT: start anchor is ambiguous ({len(hits)} hits): {start!r}", file=sys.stderr)
            return 1
        s = hits[0]

        if end is None:
            e = len(scratch)
        else:
            ehits = [i for i, ln in enumerate(scratch) if ln == end and i > s]
            if len(ehits) != 1:
                print(f"ABORT: end anchor has {len(ehits)} hits after start: {end!r}", file=sys.stderr)
                return 1
            e = ehits[0]

        resolved.append((start, end, e - s))
        del scratch[s:e]

    # All resolved, or none. A MIXTURE means the base moved under us, which is
    # exactly the case the docstring promises will conflict rather than lose.
    if absent and resolved:
        print("ABORT: the base moved — some hunks resolved and some did not, so applying "
              "would half-cut the file. Nothing was written.", file=sys.stderr)
        for a in absent:
            print(f"  unresolved start anchor: {a!r}", file=sys.stderr)
        for start, _, n in resolved:
            print(f"  would have deleted {n} lines at: {start!r}", file=sys.stderr)
        return 1

    if absent:
        print(f"already applied: all {len(absent)} start anchors are absent; nothing to do.", file=sys.stderr)
        return 0

    for start, end, n in resolved:
        print(f"deleting {n} lines: {start!r} .. {end!r}", file=sys.stderr)
    TARGET.write_text("\n".join(scratch))
    print(f"applied {len(resolved)}/{len(DELETIONS)} hunks; CLAUDE.md now {len(scratch) - 1} lines",
          file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
