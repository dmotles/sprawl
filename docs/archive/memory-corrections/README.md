# Memory-store corrections — prepared candidates and rationale

**Prepared, not applied.** Nothing in either memory store was written to, edited,
moved, or deleted. Both stores live outside the repo; `weave` owns them and lands
these.

**Work order:** `memory-store-probe.md` (deleted 2026-08-18 along with the rest of
the 2026-08-06 docs-restructure audit trail; see git history for its content),
branch `dmotles/memory-store-probe`, which measured 16 falsified claims out of 71
checkable standing present-tense claims (23%) across both stores. This document is
the *application* of that report. Findings are cited as `F1`…`F11` from its §2 and
by category from its §3 (unverifiable-as-stated). **Nothing here re-derives the
probe's results**; where a fact needed establishing, the probe's own stated control
is cited rather than re-run.

## Where the corrected files are, and why they are not all here

The corrected candidate files necessarily reproduce store content, and this repo is
public. Two terms that appear in the stores appear **nowhere** in the tracked tree
today — an employer name and a cloud resource-group name — verified by `git grep -l`
returning zero for each, with a positive control (`sprawl` in `README.md`) confirming
the grep fires. Committing the candidates verbatim would introduce both.

So the **full corrected set lives in `vector`'s gitignored findings directory**, in
one place, ready for `weave` to land from:

```
.sprawl/agents/vector/findings/memory-corrections/
├── store-a/persistent.md
├── store-b/{MEMORY.md, persistent_knowledge.md, project_rebrand.md,
│            project_publish_readiness.md, project_liveness_flood_plan.md}
└── *.diff                       # unified diff of each against its original
```

(That directory is confirmed gitignored — `git check-ignore` matches it, and does
*not* match a tracked path, so the check discriminates.) It is also the directory
`retire` deletes, so `weave` should land or copy these before retiring `vector`.

The **index file is reproduced in full below**, because it is the priority
deliverable and it is leak-free. Three of the other four store-B files are also
leak-free but are omitted here to avoid duplicating a source of truth;
`project_rebrand.md` is deliberately withheld from the tracked tree for a different
reason (below).

## Priority order applied

The probe's own §6 and the brief establish the tiering that decides which errors
matter most:

| surface | in every agent's context? | falsification rate |
|---|---|---|
| the index (store B) | **yes, every agent, every turn** | 29% |
| store-B write-once satellites | no — one hop | 42% |
| store-A `persistent.md` | no | 19% |

The index was corrected first. It is the tier that decides *whether an agent follows
a link*, so an error there selects what gets read next — which is exactly how F5
does its damage: an index line labelling a plan "unimplemented" sends an implementer
to rebuild work already on `main`.

---

## Corrections

### 1. `report_status(complete|failure)` "HALTS the agent, must be LAST" — **both stores**

*Was:* terminal reports halt the agent, so `report_status` must be the last action.

*Wrong because:* teardown is deferred to the **end of the current turn**
(`StopAfterTurn`, QUM-866), so a follow-on `send_message` or trailing text in the
same turn survives **by design**. `complete` and `failure` are identical on the
teardown decision — the asymmetry the wording implies does not exist — and neither
is permanent; both are revivable, and only `retire`/`kill` terminate.

*Replaced with:* the deferred-teardown mechanism stated plainly, the
complete/failure equivalence, the revivability, and the note that ordering it last
is a harmless habit rather than a requirement — **plus why the error mattered**:
relayed as a requirement, it teaches agents to drop a final message they could have
sent.

*Control:* F1. The probe's read surfaced the contrasting `working`/`blocked` branch
(no teardown), so it distinguishes teardown from no-teardown rather than reporting
teardown everywhere.

This is the correction with the widest blast radius: it is operating guidance that
is wrong about the mechanism it describes, and it was being relayed to agents all
session.

### 2. `interrupt=true` rejection rule — **both stores**

*Was:* rejected when the caller is a child of the recipient.

*Wrong because:* the rule is that the caller must be a **strict ancestor** of the
recipient. Child→parent is one instance; siblings, peers, unrelated agents and self
are all rejected too. As written it implies sibling interrupts are permitted.

*Replaced with:* the ancestor rule, the enumerated rejected relations, the note that
the gate keys on the original `to` rather than a route-up target — and one thing the
claim never mentioned: an **accepted** interrupt is preemptive only for a *child*
recipient; to the root agent it lands at the next turn boundary (a documented locked
asymmetry). "interrupt=true preempts" is wrong for exactly one recipient.

*Control:* F2 for the gate; the probe's §2 "incidental defects" item 2 for the
priority asymmetry. The same read produced the *accepting* path, so the probe is not
only able to see rejections.

### 3. `retire` "unconditionally `RemoveAll`s the agent dir" — **both stores**

*Was:* `retire` unconditionally `RemoveAll`s `.sprawl/agents/<name>/`.

*Wrong because:* the **conclusion is right** (that path is not durable) but both the
mechanism and the modifier are wrong. The `RemoveAll` in the retire path targets only
a logs subdirectory and merely warns on failure; the whole-directory delete is a
different call. And it is **not unconditional** — several gates return first
(uncommitted changes in a non-subagent worktree without `force`, unmerged commits,
merge-ownership, cascading children, agent-not-found). Messages are *archived*, not
deleted.

*Replaced with:* the durable conclusion kept verbatim in force, the false mechanism
dropped, "unconditional" replaced by the actual gate list, and the mailbox exception
noted.

*Control:* F7. The probe's deletion-site grep returned positives at three separate
sites, so it can find deletion sites and is not silent by construction.

### 4. `messages_list` "has no working `unread_only` filter" — **store B**

*Was:* no working `unread_only` filter or pagination default; can dump 4000+
lines/99K+ chars.

*Wrong because:* the parameter is **`filter`**, and `unread` **works**. The
capability exists under a different name — so the note was steering agents away from
a working tool. The *adjacent* claim (no default limit) is correct and is the real
hazard.

*Replaced with:* the correct parameter name and value set, `unread` marked working,
and the hazard restated as what it actually is — no default limit, no offset, no
cursor, just a hard clamp, so a large mailbox can blow the token limit in one call.
The "4000+ lines / 99K+ chars" figure is a report of one past run against one mailbox
(probe §3) and is replaced by that symptom description. The boundary/library
disagreement on `sent` (probe §2 incidental 1) is recorded.

*Control:* F6, independently re-derived by the probe from schema → handler →
supervisor → library rather than re-run from an earlier query; the same grep surfaced
filter values that do exist plus an asymmetry nobody claimed, so a working `unread`
could not have been missed.

### 5. Liveness-flood plan indexed as "active, unimplemented" — **store B index + topic file**

*Was:* index line "active … unimplemented"; topic file "Pieces 2 and 3 remain
unstarted."

*Wrong because:* **Piece 3 has shipped in full** — all three of its parts are on
`main`, with one deliberate deviation (the heartbeat was *deleted outright* rather
than made an observer). Only Piece 2 is genuinely unstarted. This is the worked
example of index-tier damage: an implementer sent here rebuilds landed work.

*Replaced with:* a dated status block at the top of the topic file with a
piece-by-piece table, an explicit warning that the linked plan documents still carry
the older status, and an index line reading "mostly landed as of 2026-08-06 … only
Piece 2 is unstarted. Read the file's status block before implementing anything from
it."

*Control:* F5 — commits for both halves on `main`, and no heartbeat file under the
supervisor package; the probe's `git log` grep does return hits for the shipped issue
numbers, so it is not silently empty, and the Piece-2 branches are confirmed *not*
ancestors of `main`. **I also spot-checked the two file-level anchors myself** (the
unified drain file present with a policy struct; no heartbeat file) with a
deliberately-absent identifier as a negative arm and the real identifier as the
positive arm — the grep returned 11 and 0 respectively, so it discriminates. This is
the one place I duplicated the probe, because it is the highest-consequence
correction and the check costs one command.

### 6. Index item count — **store B index**

*Was:* the index described the knowledge file as having 21 items.

*Wrong because:* it has 20 (confirmed independently: `grep -c '^- '` returns 20,
and returns 7 on the index itself, matching its 7 links — the probe's own positive
control, reproduced).

*Replaced with:* **the count is gone.** A counted roster on an always-loaded surface
drifts by construction; the index now says what each file *is for* and whether it is
current, which is the only thing a reader needs in order to decide whether to follow
the link.

*Control:* F9.

### 7. Rebrand note names the current root agent wrongly — **store B topic file**

*Was:* the root agent is the name introduced by the 2026-04-07 rename.

*Wrong because:* the root agent was renamed **again** afterwards and is now `weave`.
The note was accurate the day it was written; carrying it forward in the present
tense is the over-compression.

*Replaced with:* the file is retitled and framed as historical, with an explicit
"superseded" paragraph naming `weave` as current. The "zero stale references" half is
kept but scoped correctly: a surviving old-brand reference **in Go code** is a bug,
while prose and forensic documents may legitimately quote the old names — as of
2026-08-06 exactly one such reference survived, deliberately, and none in Go files.
That count is explicitly dated rather than stated as standing.

*Control:* F10 — the root-agent constant read directly from the entry command (I
re-read this line myself: it is `weave`), the rename commit, and a single-hit grep;
the probe's control is that the same grep engine returns many hits for the current
brand name in `README.md`, so it is not silently empty.

**This corrected file is deliberately NOT committed to the tracked tree.** To be
useful it must name the old brand, its old CLI name, and its old root-agent name —
and the publish-readiness work scrubbed exactly those strings from the tree. Adding
them back in a doc would manufacture false positives for anyone grepping for the
scrub. The candidate is in the gitignored findings directory.

### 8. Publish-readiness note lists three steps as remaining — **store B topic file**

*Was:* three manual steps remain — tag `v0.1.0` and push, set a social-preview
image, flip the repo public.

*Wrong because:* **two of the three are long done.** The tag exists and the repo is
public; the project is many releases past `v0.1.x`. Only the social-preview image is
unsettled — and note it is not observable from the tree or the API, so "unknown" is
more accurate than "pending".

*Replaced with:* the file reframed as historical and superseded, each item annotated
with its 2026-08-06 status, and a "do not quote this file — read the current tag and
visibility instead" instruction. The index line pointing at it was already correct
and is kept correct.

*Control:* F11 — `git tag` / `git describe` / repo visibility; `git tag` returns
both `v0.1.0` and the current series, so the probe can see tags that exist and a
genuinely untagged release would have shown as absent.

That the index was **right where its target was wrong** is itself the argument in
§"Flattening" below.

### 9. "budget over 2 minutes or the commit gets SIGTERMed" — **store B**

*Was:* committing on `main` runs the full `make validate` via the pre-commit hook —
budget over 2 minutes or it gets SIGTERMed mid-run.

*Wrong because:* the "full `make validate`" half is verified; the "2 minutes /
SIGTERM" half is **not a property of the hook or of any repo script**. There is no
timeout or kill mechanism in `scripts/`. It is an inference about an *external
caller's* timeout stated as a repo mechanism — which is what makes it un-actionable,
because it sends you looking for a knob that does not exist.

*Replaced with:* "it takes minutes, budget generously; nothing in the repo imposes a
timeout, so if a commit dies mid-run the deadline belongs to your caller — raise the
timeout you pass." The stale-`COMMIT_EDITMSG` consequence is kept.

*Control:* F8. The probe's grep pattern hits in other repo scripts (session-kill
calls in the e2e harnesses), so zero hits in the hook is a real negative rather than
an inert pattern.

### 10. Race count "9 real races across 2 packages" — **store A**

*Was:* `make validate`'s race gate found 9 real races across 2 packages.

*Wrong because:* the repo's own documentation exists specifically to retire that
figure — a race count is **run-dependent**; the detector reports what it witnesses.
The "9" traces to a commit subject line whose figure omits two reports.

*Replaced with:* "races in two packages — the count varies run to run, so never
quote a bare total", the production defect kept (it is verified) but with the
counted roster removed, the +23% cost figure kept with its measurement conditions,
and the guarantee stated accurately: a green `validate` means no race was
*observed*.

*Control:* F3. The probe's wiring check can go negative — the bare `test:` target
exists in the `Makefile` and is visibly not a `validate` dependency.

### 11. Wrong Opus model ID — **store A**

*Was:* `opus` = `claude-opus-4-8`.

*Wrong because:* that is a **different, older model**. Current Opus is
`claude-opus-5`. Store B has this right, so the two stores disagreed and A was the
wrong one. The tree's own spawn-validator test lists `claude-opus-4-8` among strings
the validator must **reject**.

*Replaced with:* the correct ID, plus an explicit note that `claude-opus-4-8` is a
real-but-different model (so a future reader does not "restore" it), plus a date
stamp on the whole reference since model IDs are inherently time-bound.

*Control:* F4. The same harness reference returns the correct value for the three
IDs store A got right and lists the wrong one as a separate real model — so the
probe distinguishes "not a model" from "wrong model".

### 12. Go build cache blamed for ENOSPC — **store B**

*Was:* ENOSPC from the Go build cache filling the disk; clearing `GOCACHE` is the
least-destructive fix.

*Wrong because:* that attribution was falsified earlier the same day — the
filesystem named had ample space, and a **different device** was full.

*Replaced with:* a symptom→diagnosis pair rather than a cause — "run `df` against
the device the failing path actually lives on **before** clearing `GOCACHE`" — with
the false attribution recorded so nobody re-derives it.

*Control:* **none of mine, and none of the probe's.** This is the one falsification
the probe explicitly **carried rather than re-derived** (its §2 closing note). I did
not re-derive it either. **This is the weakest correction in the set** — `weave`
should treat it as second-hand. It is safe in the sense that the replacement text is
a diagnostic procedure that is correct either way, but the historical claim inside it
rests on one unreproduced report.

### 13–17. `unverifiable-as-stated` rewrites

The brief's rule: these are not soft falsifications; a claim that cannot be checked
as written is unfit for an always-loaded surface **regardless of whether it happens
to be true**. Each was rewritten to the checkable part or cut.

| claim | store | disposition |
|---|---|---|
| "seen ~11 times" / "~12 times" in one session | A, B | **Tally cut.** The two stores gave different numbers for what reads as the same phenomenon, so at most one was right and nothing adjudicates. Replaced with the recurrence itself, which is the actionable part; store A's version now notes the two-stores disagreement as evidence that the tally was never real. |
| corpus-dated measurements (ack-set percentages, "0 early settles across N wire logs", the flood's byte/count figures) | B | **Kept, but dated and scoped** as one-time measurements of a snapshot, with the note that the corpus grows so a re-run answers a different question — and the flood figures pointed at the commit that records them, because the commit that recorded them also deleted the mechanism that produced them. The *conclusion* they support ("complexity debt, not an active bug") is what the reader needs and it stands. |
| "coined by \<agent\>" attribution on the canonical framing | B | **Attribution cut.** The framing is verifiably in `CLAUDE.md`; authorship of a phrase is not a tree property. The file now says so explicitly, so nobody re-adds it. |
| "`[1m]` is the long-context alias suffix" | A, B | **Narrowed to the checkable half** — sprawl passes it through verbatim, which is in the tree. What it means upstream is not, and the note now flags that it may be vestigial since the models it is applied to are natively long-context. |
| "delegate and async `send_message` don't order against each other" | A | **Premise kept, implication marked.** The `later`/`next` split is verified; what it implies for interleaving is a property of the CLI's queue scheduler and is not checkable here. The observed symptom (a follow-up arriving before the task it references) is kept as an observation with its issue number. |
| "live testing is the gold standard" | A, B | **Value judgement replaced by the written mandate**, correctly scoped: the mandate covers **TUI code** and gates *reporting done*, not "new interactive features" and not marking an issue Done. The practice is kept as a recommendation, clearly labelled as such. The `capture-pane -e` advice is narrowed — most capture sites deliberately omit `-e`; it belongs only where attributes are asserted. |
| "spec-first saves 30+ min"; "clean across ~10-issue epics"; "bitten 3 different agents" | A, B | **Fake precision removed**, heuristic kept. These read as measurements and are not. |

---

## What I deliberately left alone

**Every claim about dmotles — byte-identical, verified mechanically.** Three bullets
in store A and three in store B are personal-working-style observations. The probe
correctly excluded them as unfalsifiable by its method; an agent rewriting an
observation about the user from evidence it does not have is the worst available
version of this work. I hashed each of those six lines before and after and confirmed
identity — **with a positive control**: the same comparison run against two lines I
*did* change reports DIFFERS, so the check discriminates rather than always saying
"identical".

**`user_dmotles.md` — untouched, not even a candidate.** The probe flagged its branch
convention as *under-specified* (the loose form, where both refreshed files carry the
tighter issue-keyed form for tracked work) — explicitly **not false**. It sits in the
user-profile file, and the rule is to leave anything I am unsure is about the user.
I am unsure, so I left it. Recommend `weave` decide: it is a one-line tightening and
it is the only satellite drift left unaddressed.

**`feedback_linear_skill.md` and `feedback_keep_cli_priority_queue.md` — untouched.**
The probe falsified nothing in either. The first is purely normative. The second is
recent, self-consistent, and its code-entity references are pointers to *where to
aim*, not standing structural claims — the least rot-prone form such a reference can
take.

**Nothing in `timeline.md`, `sessions/`, or `timeline.md.legacy-stalled`.** Historical
by construction and out of scope. (The probe's separate recommendation to archive the
stalled legacy timeline — dead since 2026-04-22, referenced nowhere in the tree —
still stands and is `weave`'s call.)

**Bullet count and line ordering preserved in both refreshed files** (20 bullets each,
before and after) so the diffs read as line-for-line replacements.

---

## Flattening the tiering — the proposal, in one paragraph

**Delete the tier.** The 42% satellite rate is not an editorial problem, it is what
"write-once, beside a file rewritten every handoff" produces: nothing re-reads the
satellites, so nothing corrects them, and each states its stale content in the
**present tense**, which is the step that converts an accurate historical note into a
false current one — three of the four falsified satellite claims were accurate on the
day they were written. The concrete move is to fold each satellite's still-live
content into `persistent_knowledge.md` (the file that actually gets refreshed), keep
the historical files only as dated, past-tense records that the index labels as
history, and let the index shrink to a pointer at the one current file. Until that
happens, the corrections above do the next-best thing structurally rather than
editorially: **every retained satellite now opens with a dated status block and is
titled as historical**, so the tense itself stops making the false claim, and the
index states which files are current and which are not — because the index is what
decides whether the link gets followed at all.

## Is the index now fit to be an always-loaded surface?

**Yes, with one caveat.** Both of its falsifications are fixed at the root rather than
patched: the drifting count is **gone** rather than corrected (a count on an
always-loaded surface will drift again), and the plan line now carries a dated status
plus an explicit instruction to read the file's own status block before implementing.
Every line now also declares whether its target is current or historical, which is the
property the index actually needs to carry, since its job is to gate what gets read
next. The caveat: the index's remaining time-bound content is now *explicitly dated*
rather than eliminated, so a reader can judge staleness — but the flattening above is
what removes the underlying generator, and until it lands the index will need
re-checking whenever a satellite's status changes.

## What I found that the probe did not

1. **"Write-once" is not the same as "old", and the probe's framing conflates them.**
   Its §6 attributes the 42% to satellite modification times of 100–121 days. But
   **F5 — the most consequential satellite falsification — is in a file written two
   days before the audit.** Its staleness has nothing to do with age; the plan it
   describes shipped underneath it. The mechanism is *write-once with no refresh
   trigger*, and age is only a proxy. This matters for the remedy: a
   staleness-by-mtime heuristic would have ranked that file as the freshest satellite
   in the store and skipped it entirely.

2. **The probe says "five write-once topic files"; the index links six.** The
   100–121-day mtime band covers four of them, and the two most recent are the pair
   from 2026-08-04. The count is a minor slip, but combined with (1) it means the
   satellite denominator and the ageing argument are describing slightly different
   sets.

3. **The index's error density is understated by counting lines.** Two of seven lines
   were falsified, but a third line (the publish-readiness one) is *correct while the
   file it summarises is wrong*. Judged as a navigation aid the index is 2/7; judged
   as what it functionally is — a second store that can disagree with its targets in
   either direction — three of its seven entries were out of step with their targets.
   That is the strongest single argument for the flattening.

## Disagreements with the probe

**None on the substance of the 16.** I applied all of them. Three qualifications,
recorded rather than silently absorbed:

- **F12 (the carried disk-full falsification) is the weakest link in the chain** and
  I have flagged it as such above. Neither the probe nor I established it
  independently. The replacement text is written so that it is correct regardless,
  but `weave` should know the difference.
- **F7 is scored as a falsification and its conclusion is true.** The probe marks
  this ("conclusion only") and I agree with the scoring — the sentence a reader acts
  on was wrong about both mechanism and conditionality — but the operational advice
  it produced (don't stash findings in the agent dir) was always correct and should
  not be softened when the mechanism is fixed. It isn't.
- **The 23% headline depends on a stated judgement call** (§5: counting
  over-compressed claims whose core is true as falsified; the outright-false rate is
  near 8%). The probe says so plainly and I agree with its choice for this purpose:
  promotion ships the sentence, not the core. Anyone citing the 23% should cite the
  judgement call with it.

## Leak read-through

This file was re-read in full before committing, specifically for the two leak
classes prior agents found that no regex caught. It contains no employer name, no
cloud resource identifiers, no absolute home paths, no session identifiers, no
user quotations, and no former-brand strings. Store content is paraphrased
throughout; the only file reproduced verbatim is the corrected index, below, which
was checked against all of the above. No store was pasted wholesale.

---

## Appendix — the corrected index, verbatim

This is the full proposed replacement for the store-B index file.

```markdown
Index of this project's memory. **Only `persistent_knowledge.md` is refreshed each
handoff. Every other file below is write-once: it was accurate on the date it states
and has had nothing to correct it since. Check a file's date before acting on it.**

- [Persistent Knowledge](persistent_knowledge.md) — distilled facts, conventions, and patterns; the only file here kept current
- [User Profile](user_dmotles.md) — dmotles is the primary developer, uses dmotles/ branch prefix
- [Linear Skill](feedback_linear_skill.md) — invoke the /linear-issues skill before any save_issue create
- [Keep CLI Priority Queue](feedback_keep_cli_priority_queue.md) — don't take message scheduling back from the Claude CLI; target the ack bookkeeping instead
- [Liveness Flood Plan](project_liveness_flood_plan.md) — **mostly landed as of 2026-08-06.** Pieces 1 and 3 are on `main`; only per-turn ack attribution (Piece 2) is unstarted. Read the file's status block before implementing anything from it
- [Project Rebrand](project_rebrand.md) — historical (2026-04-07). Note the root agent was renamed *again* after that effort; the current root agent is `weave`
- [Publish Readiness](project_publish_readiness.md) — historical (2026-04-07) and superseded: the repo is public and well past v0.1.x
```

Ordering is deliberate: current-first, historical-last. The header sentence carries
the tier distinction that the flattening will eventually make unnecessary.

---

# Part II — follow-up package (2026-08-07)

Second pass, after the first was applied. Four items were commissioned; **two
survived contact with measurement, one turned out to be a code change rather than a
file edit, and one premise was wrong — my own.** All store changes here are
delivered as **hunks against the live base**, never whole files, for the reason in
§II.0.

## II.0 Why hunks, and the writer we found

The rule dropped during the first apply was **never in the snapshot I edited** — I
established that by reconstructing my own base and grepping it (four probe phrases,
all zero, with a firing positive control). Weave then identified the writer: it was
**`handoff`**, which writes the project's persistent-knowledge file at session end.
The backup's preserved mtime puts the rule's arrival 70 seconds after the handoff
completed and 11 minutes after my snapshot.

That makes it structural rather than careless. **There is no window to be careful
in**: any agent that snapshots the store, works for more than a few minutes, and
delivers a whole file will silently erase whatever the intervening handoff wrote.
The remedy is three-way, not more care — a moved base must *conflict*, not lose. Both
patches in this package are context-bearing unified diffs, dry-run verified against
the live base, with a negative control confirming a drifted base is **rejected**
rather than silently accepted.

Worth recording that during this pass the base moved **twice more** while I worked
(mtimes 00:26, 00:33). The hazard is not hypothetical or rare.

## II.1 Where the `report_status` error came from — a template, not a misreading

**This is the item that matters most, and the answer is: it will come back.**

No agent-facing surface says "HALTS" or "must be LAST" — that wording is nowhere in
the prompt templates or the tool descriptions. But the *ordering imperative* is
authored, in three places, and reinforced structurally:

1. A helper that emits the engineer TDD workflow's report step is numbered **last**,
   and its own Go doc comment describes it as the "**final**" step.
2. The QA verification protocol ends with `report_status(complete)` as step **8 of
   8** — and step **7 is `send_message`**. The canonical ordering the template
   teaches *is* message-then-report, which is exactly the behaviour "must be LAST"
   prescribes.
3. The shared child-report template says "**When done**, use:
   `report_status({state: "complete"})`".

**The decisive finding is an absence, not a sentence.** Nothing in any agent-facing
surface states what actually happens after a terminal report: that teardown is
deferred to end-of-turn and a follow-on `send_message` survives by design. The tool
description covers what `report_status` *is* — ephemeral, not an inbox message — and
says nothing about lifecycle. Given three "do this last" cues and zero statements of
the mechanism, "complete HALTS the agent" is the natural compression. An agent will
re-derive it, and the next handoff will write it back into the store.

*Control for the absence:* lifecycle-consequence prose **does** exist in this exact
surface for other tools — `retire` is described as shutting an agent down, `kill` as
an emergency stop leaving the worktree intact. So the probe finds post-tool lifecycle
prose where it exists; it returns nothing for `report_status` because there is
nothing to find. This is a property control, not a token control — which matters,
because my first three probes tonight were token-scoped and one of them could not
have fired.

**Recommendation, and a caution about the obvious fix.** Do **not** change the
templates' ordering. Message-then-report is good practice and the QA protocol has it
right. The fix is one sentence of mechanism in the `report_status` tool description —
teardown is deferred to end of turn, a follow-on message in the same turn survives,
`complete` and `failure` are identical on that decision, and neither is permanent.
Without it the store correction is a patch on the symptom: the source keeps emitting.

**Fourth instance of the wrong-scope failure, and I hit it here.** My first search for
this used keyword patterns — `must be.*last`, `halt`, `terminat`, `ends the agent` —
and returned clean negatives. Those patterns would not have matched "When done, use"
or a numbered final step, which is what the source actually looks like. I was probing
for the *wording I expected* rather than the *property*. The result only became
trustworthy after re-scoping to "enumerate every agent-facing mention and read them
all," with no keyword filter.

## II.2 The A/B merge — my own premise was over-compressed

I reported "14 of 20 bullets duplicated." **That was wrong, and wrong in the
characteristic direction.** It came from matching bullets on a six-word opening key,
which measures *how a bullet starts*, not what it contains. A clause-level audit of
the same pairs, with a positive control, gives a different picture:

| | count |
|---|---|
| bullets sharing an opening | 12 |
| — of those, genuine near-duplicates | 6 |
| — of those, carrying substantial content the other store lacks | **6** |
| bullets unique to the satellite store outright | **8** |

So roughly **6 of 20 are true duplicates**, not 14. The other 14 hold content that
exists in one store only — including, in a bullet that *looked* duplicated, the
entire `messages_list` pagination guidance that this very audit corrected.

**A deletion patch built on my original claim would have destroyed it** — the same
defect class as the rule lost in the first apply, and I generated it myself, in the
finding I filed *about* that defect. I am not delivering the merge as patches.

**And the merge as specified cannot be executed anyway, for a reason nobody had
visibility into.** The canonical store is machine-maintained, which is exactly why it
was chosen — but the writer enforces a **hard cap of 20 items with deterministic
truncation** (`items[:MaxItems]`), and the curator prompt instructs the model to cap
at 20. Consolidating ~14 unique bullets into a store that is capped at 20 is not a
merge; it is a forced eviction whose victims an LLM selects. The correct sequence is
to raise or remove the cap first, then merge. That is a code change and a decision
for weave, not something to work around at the file level.

## II.3 The truncation is about to re-delete the restored rule

**Flagging this as the most time-sensitive item in the package.** The canonical store
currently holds **21 bullets against a cap of 20** — it went to 21 when the dropped
artifact-location rule was restored by appending it.

At the next handoff the curator is told to cap at 20, and the writer hard-truncates
anything longer. One of those 21 bullets will not survive, chosen by a model that has
no record of which one was just recovered, and the write is a full overwrite with no
diff and no warning. **The rule we spent this evening recovering is a plausible
casualty of the very next session end.**

This is the same boundary as QUM-1149 and, I think, materially strengthens it: the
issue was filed for `handoff` truncating the root agent's self-knowledge, and this is
the same mechanism truncating the store. Two distinct properties of one writer are
implicated, and they compound:

- **it overwrites** (whole-file write of only bullet lines, no merge, no diff) — the
  first apply's lost rule; and
- **it truncates** (hard cap, LLM-selected victim, silent) — the risk now live.

Answering forge's question directly: I did not find a *third* thing `handoff`
overwrites, and my pass was not designed to look for one. What I found instead is a
second failure mode of the same writer. Whether that is an AC on QUM-1149 or its own
issue is weave's call — but the overwrite and the truncation want separate acceptance
criteria, because a fix for either leaves the other live.

## II.4 Store A cannot hold a date field

Commissioned as item 3; **not deliverable as a file edit, and a patch would have
silently evaporated.**

The writer emits bullet lines only, and its parser discards every line that does not
begin with `- `. The curator prompt closes the loop from the other side: "Output ONLY
the bullet lines. No headers, no explanation, no other text." So YAML frontmatter or
a date header survives exactly until the next handoff and then vanishes without
comment. *Control:* the file currently contains **zero** non-bullet lines, while the
satellite store — written by a different mechanism — contains seven, so the check
distinguishes "this file has no header" from "my check cannot see headers."

The requirement is right and the diagnosis behind it is right: an undated store is
the property that turned four satellite notes into false present-tense claims, and it
matters more now that this store is canonical. But it has to be implemented in the
writer — stamp the generation time when the file is written — not in the file. Noted
for weave as a code change; I have not made it.

## II.5 Delivered patches

Both are in the hunks directory alongside an `APPLY.md` carrying base hashes and
verification steps. Both dry-run cleanly against the live base; a drifted base is
rejected.

- **Hub bullet, de-leaked.** Reduced to the architectural invariant — the local
  binary is network-only with respect to the hub. Deployment topology, resource
  identifiers, and region removed, with a short note recording *why* they are absent
  so a future curator does not helpfully restore them. The original text is not
  quoted anywhere in this tracked ledger, deliberately: that line is the specific
  content that must not reach the public tree.
- **`make install`, scoped.** Now explicitly root-only, naming the CLAUDE.md warning
  it contradicts for every other agent. It was correct advice in a root agent's
  private store and becomes dangerous the moment the store is read by all agents,
  which is precisely what promotion would do.

Both are promotion blockers and both are now cleared.

## II.6 Standing note on my own error rate this pass

Two of my own claims failed under checking in this pass: the "14 of 20 duplicated"
figure (§II.2) and the first keyword-scoped search for the template source (§II.1).
Both were the same defect — a probe scoped to a convenient proxy rather than to the
property being claimed — and both were caught only by building a control that could
fire. That is four instances tonight across three agents, every one of them found by
the control rather than by re-reading the work. The practice that keeps working:
**name the property, then build a probe that must produce a positive on a case you
know exists, before trusting any negative.**

---

# Part III — writer-side design doc (2026-08-07)

The writer-side changes this audit surfaced are specced in
**[`docs/designs/persistent-knowledge-writer.md`](../../../designs/persistent-knowledge-writer.md)**
— placed there rather than here in accordance with the artifact-location rule this
audit accidentally deleted and then recovered: designs and durable artifacts go in
the tracked tree under `docs/designs/`; raw findings stay gitignored.

It covers four defects (the unmerged whole-file write; the item cap's positional
truncation; `MaxSizeChars` declared-and-never-read; a forensics doc reasoning from
functions that do not exist) and answers the decomposition question posed against
this work: **they are not three peer failure modes.** There is one loss generator,
one policy gap whose harm routes entirely through it, and one epistemic class. The
practical consequence is recorded there as a scoring rule — of the five proposed
changes, exactly one closes a failure mode, and the natural fix package closes none
while appearing to close two.

§5 of that document carries the "14 of 20" near-miss as the standing evidence for
hunks-only delivery.
