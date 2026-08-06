# D6 cover note — decisions, corrections, and open flags

Companion to the three D6 deliverables. Read before landing any of them.

| deliverable | path | state |
|---|---|---|
| Task 1 — false-RED catalogue | `.claude/skills/false-red/SKILL.md` | at its real path, ready to land |
| Task 2 — researcher/QA safety text | `docs/audits/2026-08-06-docs-restructure/draft-claude-md-agent-safety.md` | draft; CLAUDE.md untouched |
| Task 3 — prompt-bug spec | `docs/audits/2026-08-06-docs-restructure/spec-researcher-prompt-bugs.md` | spec only; no code changed |

---

## Skill, not a `docs/` page

A skill, and the deciding property is not proceduralness — it is that a skill's
description is loaded into every agent's context automatically, whereas a
`docs/` page is reachable only by an agent that already knows it exists. The
audience is an agent that has just seen red and is *about to* revert or bypass;
that agent is not going to go looking, and it has no reason to suspect the
failure is environmental — which is precisely the belief the document has to
interrupt. A skill description does the interrupting for free and cannot be
skipped, so the discovery problem is solved by the container rather than by a
CLAUDE.md line competing for space in a 75-line budget. This is checkable rather
than argued: writing the file caused it to appear in this agent's own skill
listing mid-session, unprompted. The concession to the weak prior against it is
real — `handoff` is procedural and this is a lookup table — but that cuts against
skills being *only* for procedures, not against this one being a skill. The
CLAUDE.md breadcrumb in Task 2 should still land: it is what tells an agent the
catalogue is authoritative rather than optional.

---

## Corrections to the brief

Three, all found by verifying against the tree rather than transcribing.

**1. "ENOSPC from the Go build cache" is itself a misdiagnosis** — and it is a
third known-bad claim in the memory store, of the same shape as the other two.
QUM-1118 records that the incident *was* attributed to build-cache pressure on
one filesystem while that filesystem had ample space free and a **different**
filesystem was the one at capacity. The catalogue entry now names the property
(*a filesystem filled*) rather than the proxy (*the build cache*), and tells the
reader to check both devices. This is the P4 correction applied to itself: the
build cache is the countable, convenient-to-check thing; it is not the thing that
was true.

**2. "QUM-1118 gives ENOSPC a distinct exit code" — present tense is wrong.** The
issue is Todo. `grep -rn QUM-1118` and `git log --all --grep=QUM-1118` both
return zero. The issue *specifies* the behaviour and does **not** assign a
number. The entry says so and explicitly warns against coding against a value.

**3. "QA is told nothing about concurrency" — not quite.**
`qaVerificationProtocolSection` step 4 already says *"If validate reports lock
contention, retry with backoff — do NOT bypass."* So the lint lock specifically
is covered for QA. The real gap is the other classes, plus the fact that the one
hint QA gets is invisible to every other role. Task 2's breadcrumb generalises it
rather than introducing it — the dependency you identified holds, for a slightly
different reason.

---

## Response to the mid-task rigor instruction

Task 1 was built from a tree-and-issue verification sweep rather than from
memory, which is how correction 1 surfaced before anything shipped. On receiving
the instruction I re-audited each entry and **weakened two diagnosis-halves that
overreached**:

- The lint entry asserted *why* the lock exists (an unsynchronised shared cache).
  Unverified, and exactly the failure mode described. Removed; the entry now says
  do not disable the guard, without claiming to know what disabling it does.
- The `TempDir` entry's mechanism is secondhand from QUM-1070 and is now labelled
  as such. Its symptom, the absent-assertion tell, and the remedy stand on their
  own; the cause is marked reported-not-confirmed.

Every entry carries a positive control in a **Provenance** table at the foot of
the skill, distinguishing *observed*, *verified against the tree*, and *reported
only* — separately for the symptom and for the mechanism. One entry
(`TempDir`) rests entirely on a secondhand report and is flagged as the file's
known gap, with an instruction to correct it on first real encounter.

The merge-recovery procedure was also rewritten. My first draft reasoned out an
`fsck --lost-found` excavation; it now leads with pin-a-rescue-ref then
`reset --soft`, which is what was actually done successfully twice, with an
explicit warning that `--hard` at any step destroys the thing being recovered.

**Property-before-probe, applied to the shlint claim.** Corrected in the main
audit at §6.1. The sentence I could support is *no such path has ever existed*;
the probe (`git log --all -- internal/shlint`) matches that sentence exactly. A
different probe (`-S'shlint'`, 5 commits) has a different subject — the string,
not the path — and would make the original phrasing look false to anyone
re-deriving it. Both are now stated, with the scope named.

---

## Open flags

**`refs/sprawl/rescue/` is a namespace I invented.** The skill tells an agent
recovering an orphaned merge to pin a rescue ref there before moving anything.
`sprawl gc` prunes `refs/sprawl/premerge/` on a retention window; it prunes
nothing under `refs/sprawl/rescue/`, so these accumulate indefinitely. I kept it
deliberately — an orphaned ref is a trivial cost against a lost commit, and the
pin is what makes the recovery safe — but whoever lands QUM-1090 should either
fold this namespace into the same retention sweep or redirect the instruction at
the premerge namespace. It should not stay unmanaged by accident.

**The catalogue will rot in one specific place.** The symptom strings are
durable; the issue states (`QUM-1118` open, `QUM-1070` unfixed, `QUM-1090`
in flight) are not, and three entries reference them. When the fixed merge engine
installs, the last entry's recovery section is the part to revisit — it already
tells the reader how to detect which engine they are on, which is the cheapest
hedge available, but it is a hedge and not a fix.

**Not verified, and out of scope:** whether the `TempDir` race reproduces, and
whether any e2e row is genuinely implicated by the Task 3 changes. The spec tells
the implementing engineer to derive the row set from the table rather than
trusting my reading of it.
