#!/usr/bin/env bash
# scripts/ops/regenerate-timeline.sh — one-time ops step for the QUM-517 cutover.
#
# Regenerates .sprawl/memory/timeline.md from .sprawl/memory/sessions/ using a
# real `claude -p` LLM call (so this MUST be run from an env with `claude`
# authenticated and on PATH). Output is written to a `.next` sibling so the
# operator can diff before promoting.
#
# This script is intentionally non-destructive: it does NOT overwrite the live
# timeline.md or commit anything. Promote manually after reviewing the diff.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

LIVE=".sprawl/memory/timeline.md"
NEXT=".sprawl/memory/timeline.md.next"

echo "==> Building sprawl"
make build

echo "==> Regenerating timeline → ${NEXT}"
./sprawl memory regenerate-timeline --out "${NEXT}"

echo
echo "==> Diff (live vs candidate):"
if [[ -f "${LIVE}" ]]; then
    diff -u "${LIVE}" "${NEXT}" || true
else
    echo "(no existing ${LIVE} — first generation)"
    cat "${NEXT}"
fi

cat <<EOF

==> Review the diff above.

If it looks correct, promote with:

    mv ${NEXT} ${LIVE}
    git add ${LIVE}
    git commit -m "memory: regenerate timeline.md (QUM-517 cutover)"

If it looks wrong, delete the candidate:

    rm ${NEXT}

and re-run after fixing. The script never modifies the live file directly.
EOF
