# `docs/` — what is here, and what may go here

A map, not an essay. If you arrived by grep and are about to act on something you
found under `docs/`, read the authority order and the archive warning first.

## Authority order

    code  >  CLAUDE.md  >  docs/architecture  >  docs/designs  >  docs/archive

The tree is the only thing that cannot be stale. `docs/archive/` is **never**
authority. When a document and the code disagree, the code wins and the document
is a bug.

## The directories

| directory | what belongs here | what does not |
|---|---|---|
| `architecture/` | How a subsystem works **today**. Must name the code path that owns it. | Incidents, decisions, post-mortems, anything about how it *used* to work. |
| `designs/` | Designs that are built, partly built, committed-to-build, or an open decision. New or edited docs **must** carry a status banner: built / partial / design-only / open-decision. Most of the existing ones do not yet — that is a backlog, not a precedent. | Designs that were abandoned or shipped differently — those are archive. |
| `guides/` | Procedural steps a human or an agent runs from this repo. | A task done only *sometimes* — make that a skill under `.claude/skills/`, which is loaded on demand instead of read every turn. |
| `reference/` | External facts the tree cannot re-derive: protocol surfaces, client behaviour, third-party contracts. Say which version you observed. | Anything about *our* code. That is `architecture/`. |
| `audits/` | Dated decision records for an in-flight restructure. Carries an **exit** criterion: when its decisions are executed or superseded, each document either moves to `archive/` (if still cited) or is deleted (git history is the durable record of a completed decision). Currently absent from the tree — no restructure is in flight. | Ongoing work tracking. Linear is the tracker. |
| `archive/` | Was true once. A one-way door — nothing exits. | Anything you want someone to rely on. |
| `research/` | **Transitional — do not add to it.** `open-source-readiness/03-security-audit.md` states part of the security trust model and is held live until QUM-1138 records that model durably; `docs/research/` disappears when that lands. `qum-1111-repro-and-mechanism.md` is an undisposed leftover, not a QUM-1138 dependency — it has no exit criterion yet. | Everything. New investigation goes to `.sprawl/agents/<name>/findings/`, or to `archive/` if it is worth keeping. |

## If you are reading a file under `docs/archive/`

**It was true once and is not now.** Verify every claim against the tree before
acting on it. The quarantine is the path segment itself, deliberately: a label in
a directory README is invisible to an agent that landed mid-file from a grep,
which is how every previous attempt at this failed.

A live document may link into `archive/` **only with an explicit `(archived)`
label** — at the link, or as a banner at the top of the file when one document
links into the archive repeatedly and per-link labels would be noise. The value
in an archived document is usually the *why* —
the causal record of a decision — which the code genuinely cannot supply. That is
worth citing. It is not worth citing silently.

## The entry rule

Apply this to your own draft, before you file it:

> **If a document enumerates call sites, lists the implementers of an interface,
> or mirrors a directory tree, it belongs in `archive/` the day it lands.** It
> cannot be maintained as live truth and must not be filed as such.

Rot enters through **enumeration**, not age. The failure unit is a count followed
by a list of code entities — "three files", "both", "all 11", "the only". These
are not written carelessly; they are made wrong by the next person who *complies*
with the rule they state. A document committed yesterday can already be false
while one untouched for months stays perfect, and which is which is predicted by
this shape rather than by any date.

**Corollary — say what is gone, not what is there.** Claims of absence are
durable; claims of presence expire silently. "X was deleted, do not grep for it"
stays true. "X is implemented in these three files" does not.

**Corollary — name the property, not a countable proxy for it.** Prefer "is this
behaviour tested?" over "does a file with this name exist?". A proxy that happens
to be countable is what lets a wrong claim survive review by being arithmetically
correct.

Any document you add or substantially edit **must** carry a **date and a status
line** at the top. Many existing ones do not — that is a backlog to work off, not
a precedent to copy. Dating is metadata for the reader; it is *not* the control,
because banners rot exactly like the prose they head. Do not judge staleness by
git dates either: a
substantial share of this tree carries a last-modified date from a mechanical
one-word edit or a rebase squash, reflecting no content change at all.

**This rule is applied to its own authors.** The audit documents that produced
this restructure enumerated a 144-file directory tree, so on this restructure's
own terms they had an exit criterion, not a home in `docs/audits/`: on
2026-08-18 they were executed and removed — two relocated to `docs/archive/`
(`budget-resolver.md`, `memory-corrections/README.md`), the rest deleted, since
git history is the durable record of a completed decision. A rule whose authors
exempted themselves is not a rule.

## Deliberately not here

- **Issue tracking.** Linear is the tracker. There is no `todo/`.
- **Machine-generated incident snapshots.** Those go to `.sprawl/incidents/`.
  `docs/archive/` holds human analysis.
- **Agent findings.** Those go to `.sprawl/agents/<name>/findings/`, which is
  gitignored — deliberately, because they routinely contain forensic detail from
  real systems that must not enter a public tree.
