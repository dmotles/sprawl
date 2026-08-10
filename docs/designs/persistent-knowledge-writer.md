# Design — the persistent-knowledge writer: loss, caps, and knobs that lie

**Status:** proposed, 2026-08-07. Design only; no code in this change.
**Scope:** `internal/memory`'s persistent-knowledge write path, invoked by `handoff`
at session end.
**Provenance:** follows the memory-store correction passes recorded in
`docs/audits/2026-08-06-docs-restructure/memory-corrections/README.md`, which is
where the incident evidence lives. Read §II.0 there for how the loss was found.

---

## 1. The decomposition — and why "three failure modes" is the wrong count

This was posed as an open question: is *"the writer has three failure modes"* right,
or are the first two the same defect at different granularities? **They are not
three peers.** Getting this wrong is load-bearing, because the natural fix package —
raise the cap, enforce the size cap — **closes none of them** while looking like it
closes two.

The honest structure is **one generator, one policy gap, one epistemic class**:

| | what it is | instances |
|---|---|---|
| **G — loss generator** | the write is a full overwrite computed from a base read earlier: a read-modify-write with no merge | the erased artifact-location rule |
| **P — policy gap** | the caps have no eviction *policy*, only a truncation | `MaxItems` truncation; `MaxSizeChars` unenforced |
| **E — epistemic class** | something asserts enforcement that does not exist, defeating audit rather than data | `MaxSizeChars` declared-and-dead; a forensics doc reasoning from absent functions |

Note `MaxSizeChars` appears in two rows. That is the analysis working, not a
double-count: it is simultaneously a missing policy and a false advertisement of
policy, and those want different remedies.

### Why truncation is not an independent loss generator

Test the counterfactual. **Suppose the write merged with the on-disk base and changed
nothing else.** The curator returns 20 items (truncated from 21). Merging those 20
against the on-disk 21 yields 21. *Nothing is durably lost.* The cap simply stops
capping and the file grows.

So truncation's **loss** is downstream of the overwrite. Truncation alone, under
merge semantics, converts from a data-loss bug into a much milder one — an
ineffective cap. Loss requires the overwrite; the cap only chooses the victim.

That does not make the cap innocent. It remains a real defect of a different kind:
**a cap with no eviction policy.** Its "policy" is *trust the model's output
ordering*, which is not a policy — it is an accident that happens to be
deterministic. But it belongs in a different bucket, and it must be fixed second,
not first.

### The three counterfactuals, which settle the fix order

| ship | concurrent-write loss | truncation loss | growth | verdict |
|---|---|---|---|---|
| **merge-don't-overwrite only** | fixed | fixed (merge restores the evicted item) | unbounded | **all loss closed**; cap inert |
| **raise cap + enforce size cap only** | **unchanged** | rarer, still silent, still lossy | bounded | **closes nothing**, and adds a second silent truncation |
| merge + explicit recorded eviction | fixed | fixed | bounded | target state |

**The middle row is the danger case and it is the package most likely to be
proposed.** It touches two knobs, produces a visibly smaller file, and leaves the
generator fully intact.

### Answer to the question as posed

**Two-and-a-consequence, plus a separate epistemic pair.** Specifically:

- *"Overwrite"* and *"truncate"* are **not** two loss modes. There is one loss
  mechanism (the unmerged write) and one victim-selection mechanism (the cap). The
  cap is not a granularity of the overwrite; it is an orthogonal policy gap whose
  *harm* is routed entirely through the overwrite.
- The `PruneTimeline` doc defect is **not a fourth failure mode of this writer at
  all.** It is a different subsystem (timeline, not persistent knowledge) and a
  different failure kind (documentation). Counting it alongside the others invites a
  fix package that treats a doc edit as progress against data loss. It is listed here
  only because it lies about the same class of caps.

**Practical consequence, stated so it can be checked:** of the changes below, exactly
one closes a failure mode. The others change capacity, add policy, or remove a lie.
Any implementation that reports "closed 3 of 4" without landing change **A** has
mis-scored itself.

---

## 2. Evidence

Each row states the control that makes its negative trustworthy.

| # | defect | evidence | positive control |
|---|---|---|---|
| 1 | The write is a full overwrite of freshly generated content, with no merge against the on-disk base. Anything written between the read and the write is lost silently — no diff, no warning. | A rule arrived in the store ~70s after a handoff completed and ~11 minutes after a candidate snapshot was taken; the subsequent whole-file apply erased it. Reconstructing the snapshot and grepping for four distinct phrases of that rule returns zero for all four. | The same grep finds a phrase known to be in that snapshot. And during the follow-up pass the file's mtime moved twice more inside ~7 minutes — the race is the normal condition, not an edge case. |
| 2 | The item cap truncates deterministically by list position after the model has already curated. The evicted item is chosen by the model's output ordering; nothing records what was dropped. | The cap is defaulted at the only live call site (only the model field is overridden) and enforced by a positional slice. The curator prompt separately instructs the model to cap at the same number. | The store reached 21 items against the cap and nothing surfaced it; it was found by reading the writer, not by any signal the system emits. |
| 3 | `MaxSizeChars` is declared, defaulted to 10,000, set in exactly one test, and **read by zero non-test sites**. | Enumerating every occurrence in the tree yields: the field declaration, the default assignment, one test. No enforcement site. | **A live cap of the same kind sits in the same package** — `MaxChars` in the arc summariser is read in real logic at several sites, including fallback truncation. So the probe finds enforced caps where they exist. It finds nothing here because there is nothing. |
| 4 | A forensics doc states that a function enforces timeline caps of 200 entries / 50,000 chars. **Neither the function nor either constant exists.** | Zero Go matches for the named function; zero non-test matches for `MaxEntries` and for the size constant. The same doc line also names a *second* function that does not exist. | A memory-package function that does exist is found by the same probe. |

**Defect 3, sharpened:** the store is currently ~10,590 characters against a nominal
10,000 and nothing noticed, because nothing is watching. The framing that belongs in
the record: **a dead knob sitting beside a live one of the same kind is worse than no
knob, because the live one supplies the credibility.** An auditor who finds
`MaxSizeChars` declared and defaulted, in a package where sibling caps are genuinely
enforced, has every reason to record "size is bounded" and move on.

**Defect 4, worse than first reported.** The brief named one absent function. Line 51
of that document names **two**, and line 99 then *reasons from* the absent one —
asserting that the output timeline is capped while the input is not. That is not a
dangling reference; it is a **false reassurance inside the document someone would
read to understand these very caps**, and it points the reader's attention at the
input while vouching for the output. Both halves of the sentence are wrong in the
same direction. The file is in the archive set and a prior sweep cleared its
workstream as safe to archive as-is, so absent intervention it would be quarantined
with the false claim preserved.

---

## 3. Changes

Each states **which failure mode it closes** — including the ones that close none.

### A. Merge, don't overwrite  ·  closes **G** (the only loss generator)

The write must reconcile against the on-disk file **as it stands at write time**, not
against the base that was read before the model call. Re-read immediately before
writing and reconcile; treat an item present on disk but absent from the model's
output as **retained by default**, not as deleted.

Retention-by-default is the load-bearing choice and it is deliberately asymmetric.
The curator already has an explicit retraction directive and a false-positive
guardrail whose stated position is that stale bullets are preferable to wrong
deletions. Reconciliation must not silently reverse that judgement: a bullet the
model simply did not re-emit — because it never saw it, having arrived after the
read — is indistinguishable at the write boundary from one the model deliberately
retracted. **When those two cases are indistinguishable, retain.**

This is what makes deliberate retraction a thing the curator must do *explicitly*
rather than by omission. If retraction-by-omission is required to keep working, the
model's output needs to carry the retraction as a signal rather than as an absence —
but that is a larger change and is not required to close **G**.

Under this change alone, all loss stops and the file grows unboundedly. That is the
correct intermediate state: **unbounded growth is a visible, recoverable problem;
silent loss is neither.** Ship A before B.

*Closes:* G. *Does not close:* P (the cap stops evicting durably but still has no
policy), E.

### B. One eviction path, explicit and recorded  ·  closes **P**

Replace positional truncation with a single eviction decision that is computed once,
applied once, and **recorded**. Requirements:

1. **One path, not two.** Count and size must feed the *same* decision. Enforcing
   `MaxSizeChars` as a second independent slice builds the same defect twice — which
   is the concrete answer to "two silent truncations are not obviously better than
   one." They are worse: two independent truncations have an ordering, the ordering
   affects the outcome, and nobody will have specified it.
2. **Never silent.** Every eviction emits a record — at minimum the count and the
   evicted items' first line, to the same place the handoff's other phase warnings
   go. An eviction nobody can observe is the defect, not the eviction.
3. **Deterministic and inspectable victim selection.** Anything is better than
   *whatever the model put last*. Oldest-unmodified is the obvious candidate; the
   specific rule matters less than that it is written down and stable.
4. **Overflow behaviour must be stated, not implied.** On exceeding either bound:
   evict per the rule, record, and write. Do **not** fail the write — a handoff that
   refuses to persist is a worse outcome than one that persists a bounded subset and
   says so.

*Closes:* P. *Does not close:* G (a recorded eviction over an unmerged write still
loses the concurrent update), E.

### C. Raise the item cap  ·  closes **nothing** — this is capacity, not correctness

Stated plainly because it was commissioned as item 1 and it is the change most likely
to be mistaken for a fix. Raising the cap reduces how often eviction fires. It does
not make eviction visible, does not make it policy, and does not touch the overwrite.

It is nonetheless a genuine **prerequisite for the A/B store merge**: consolidating
the satellite store's unique bullets into a store bounded at 20 is a forced eviction
with a model adjudicating, not a merge. Sequence it **after A and B**, so the merge
lands into a writer that cannot lose and cannot evict silently.

*Closes:* nothing. *Unblocks:* the A/B merge.

### D. A written-at stamp the writer emits  ·  closes **nothing** — it is instrumentation

An undated store is the property that turned four satellite notes into false
present-tense claims, and it matters more now that this store is canonical. But it
**cannot be a hand-authored header**: the writer emits bullet lines only, the parser
discards every line not beginning with the bullet marker, and the curator prompt
instructs the model to output only bullet lines. A header survives until the next
handoff and then vanishes without comment.

So the writer must own it. Two properties make this safe, and both must hold:

- **Emit the stamp as a non-bullet trailing line.** The parser's existing lenient
  behaviour — silently skipping non-bullet lines — is a hazard for a human-authored
  header and is exactly what makes a writer-owned one safe: it round-trips inertly
  and cannot be mistaken for content.
- **Strip non-bullet lines when composing the curator prompt.** The existing prompt
  builder embeds the file's raw contents, so without this the model sees the stamp,
  may edit it, and may "update" it to a date it invented. Feeding the prompt the
  parsed bullets rather than the raw file also makes the prompt deterministic.

Content: the write timestamp and the writing mechanism. Enough for a reader to judge
staleness — which is the entire requirement.

*Closes:* nothing directly. *Enables:* judging staleness, and detecting a stalled
writer, which is currently unobservable.

### E. `MaxSizeChars` — enforce it as part of B, or delete the field  ·  closes **E** (one instance)

Do not leave it declared and dead. Either it participates in the single eviction path
from **B**, or the field, its default, and its one test reference are removed.

Deletion is a legitimate outcome and should not be treated as the lesser option: an
absent knob is honest, and the size bound can be reintroduced with the eviction path
when someone needs it. What is not acceptable is the current state, where the field's
presence is doing active epistemic work against anyone auditing this code.

*Closes:* E, for this instance. *Does not close:* G, P (unless implemented inside B).

---

## 4. Sequencing

**A → B → (C, D, E in any order).**

A first, because it is the only change that closes loss, and because it changes what
B has to do: under merge semantics, B's job is bounding growth, whereas under
overwrite semantics B's job would be *choosing what to destroy*. Those are different
specifications and only one of them is worth implementing.

C explicitly last of the three, so the A/B store merge lands into a writer that
cannot lose and cannot evict silently. Landing C first would raise the ceiling on a
writer that still drops concurrent writes — more content at risk, same mechanism.

---

## 5. Why the store merge stays hunks-only — the near-miss that establishes it

Recorded as evidence, not as an apology.

The A/B merge was scoped from my own measurement that **14 of 20 bullets were
duplicated across the two stores**. That figure was produced by matching bullets on a
six-word opening key — a probe that measures *how a bullet starts*, not what it
contains. A clause-level re-audit, with a control, gives ~6 true duplicates: of the
12 that share an opening, half carry substantial content the other store lacks, and 8
further bullets exist in one store only.

**A deletion patch built on the original figure would have destroyed the
`messages_list` pagination guidance that this same audit had just corrected** — it
sat inside a bullet that only *looked* duplicated. That is the identical defect class
as the rule the overwrite erased, reproduced by the author of the finding about that
erasure, in the same evening.

Two rules follow, and they are the standard for anything touching this store until
change **A** lands:

1. **Hunks only. Never whole files.** Deliveries are context-bearing unified diffs,
   dry-run verified against the live base, with a **negative control proving a
   drifted base is rejected** rather than silently accepted. A moved base must
   conflict, not lose.
2. **Diff for deletions as a check separate from verifying the corrections.** They
   are different questions. A clean correctness review answers only the first —
   verifying that replacements are present and false claims absent says nothing about
   what the base contained.

The near-miss is the argument for both. It was caught by building a probe that could
produce a positive, not by reviewing the work more carefully; the original probe was
already careful and already returned a true answer to the wrong question.

---

## 6. Related fixes, not in this change

Listed so the dependencies are visible.

- **`report_status` tool description — approved, owned elsewhere. SUPERSEDED: the
  tool was deleted in QUM-1186, so this item has no subject matter left; it is
  retired by deletion, not fixed.** No agent-facing
  surface states that terminal-report teardown is deferred to end of turn and that a
  follow-on message in the same turn survives. Three surfaces author the *ordering*
  (a report step whose own doc comment calls it the "final" step; a verification
  protocol ending at step 8 with `send_message` at step 7; a "when done, use" line),
  and nothing states the mechanism — so the false "it halts the agent" reading
  regenerates. **The templates' ordering must not change:** message-then-report is
  correct, and the QA protocol has it right at 7→8. The fix is one sentence of
  mechanism in the tool description.
- **The forensics doc's absent functions — flag only, another agent's territory.**
  Whoever owns that document should know that line 51 names two functions that do not
  exist and line 99 reasons from one of them to a false reassurance about caps. Not
  fixed here; it is in the archive set.

---

## 7. Acceptance

A change satisfies this design when, for each item it claims:

- **A** — a write issued against a base that moved after the read preserves the
  intervening content, demonstrated by a test that mutates the file between the read
  and the write. The test must be shown to fail without the change.
- **B** — an over-cap input produces a recorded eviction, and the record names what
  was dropped. Count and size overflow exercise the *same* path.
- **C** — capacity only; claims no failure mode.
- **D** — the stamp survives a full write→read→prompt→write round trip, and the
  curator prompt provably does not contain it.
- **E** — either an enforcement site exists and a test drives it, or the field is
  gone from declaration, default, and test.

And the scoring rule, from §1: **any report claiming more than one closed failure
mode without change A is mis-scored.**
