#!/usr/bin/env python3
"""Apply the QUM-1155 CLAUDE.md cut as anchored hunks.

NOT a whole-file replacement. Each hunk is a span delimited by two exact,
UNIQUE anchor lines, and the script refuses to run if either anchor is absent
or ambiguous. That is the point: `main` may move under this branch, and a
moved base must CONFLICT rather than silently lose whatever landed. Deleting by
line number would do the opposite — it would apply cleanly to the wrong text.

Idempotent: re-running after a successful apply is a no-op that reports it.
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

    for start, end in DELETIONS:
        hits = [i for i, ln in enumerate(lines) if ln == start]
        if not hits:
            print(f"skip (already applied?): start anchor absent: {start!r}", file=sys.stderr)
            continue
        if len(hits) > 1:
            print(f"ABORT: start anchor is ambiguous ({len(hits)} hits): {start!r}", file=sys.stderr)
            return 1
        s = hits[0]

        if end is None:
            e = len(lines)
        else:
            ehits = [i for i, ln in enumerate(lines) if ln == end and i > s]
            if len(ehits) != 1:
                print(f"ABORT: end anchor has {len(ehits)} hits after start: {end!r}", file=sys.stderr)
                return 1
            e = ehits[0]

        print(f"deleting {e - s} lines: {start!r} .. {end!r}", file=sys.stderr)
        del lines[s:e]

    TARGET.write_text("\n".join(lines))
    print(f"CLAUDE.md now {len(lines) - 1} lines", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
