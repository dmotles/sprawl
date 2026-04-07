# Fresh Eyes Audit — New User Experience

**Date:** 2026-04-07
**Researcher:** ghost
**Branch:** dmotles/fresh-eyes-audit

## Summary

Simulated the new-user experience: read the README, tried to install, tried to build, explored the docs, and searched for rough edges. The project has a solid architecture document, clean CLI help, and the build works — but there are several blockers that would prevent a new user from getting started, and a significant incomplete rename that undermines credibility.

---

## 🔴 Blockers (would prevent someone from using it)

### B1: No GitHub Releases — both install methods fail

`gh release list` returns empty. There are no tags and no releases published.

**Impact on the curl installer (`install.sh`):** The script calls the GitHub releases API to resolve the latest version. With no releases, it gets an empty response, fails to parse a tag, and exits with:
```
Error: could not resolve latest version. Set VERSION=vX.Y.Z and retry.
```
Even if the user sets a VERSION manually, there are no release assets to download — so it would fail at the download step anyway.

**Impact on `go install`:** `go install github.com/dmotles/sprawl@latest` requires either a tagged release or the module to be publicly accessible. With no tags, Go will try to resolve `latest` and likely fail or grab an arbitrary commit.

**Fix:** Publish a v0.1.0 release (even as a pre-release) with binaries, or at minimum tag a version so `go install` works. The release workflow (`.github/workflows/release.yml`) exists — it just hasn't been run.

### B2: README doesn't mention "build from source" as an install option

The README lists two install methods (curl installer, `go install`) — both of which currently fail (see B1). There is no "build from source" option documented. A determined user could figure it out from CONTRIBUTING.md or CLAUDE.md, but the README is the front door.

**Fix:** Add a "Build from source" section:
```bash
git clone https://github.com/dmotles/sprawl.git
cd sprawl
make build
./sprawl --help
```

### B3: Go 1.25+ requirement may confuse users

The README says "Go 1.25+" and `go.mod` specifies `go 1.25.0`. Go 1.25 is very new (as of this writing, the machine has Go 1.26). Users on older, widely-available Go versions (1.22, 1.23) won't be able to build. This isn't a bug per se, but it's worth noting that the required Go version is bleeding-edge and should be called out prominently, or the minimum version should be lowered if possible.

---

## 🟡 Paper Cuts (annoying/confusing but not blocking)

### P1: Massive incomplete rename — 583 "dendra" references in 59 source files

This is the single biggest credibility issue. The project was renamed from "Dendra/Dendrarchy" to "Sprawl", but the rename is **deeply incomplete**:

- **Source code:** 236 occurrences of `dendraRoot`, `dendraPath`, `findDendra` etc. across 24 files in `cmd/` alone. Another 173 in `internal/`. These are variable names, function names, and comments.
- **CLAUDE.md** (the AI agent instructions file, visible in the repo): Says `make build` "builds `./dendra` binary" — but the Makefile actually builds `./sprawl`. Also tells agents to "test against the locally built `./dendra` binary."
- **.gitignore:** Has a comment "Binary (Makefile still builds as 'dendra' until Phase 3)" and lists `dendra` as an ignored file — but the Makefile builds `sprawl` now.
- **docs/research/:** Multiple files extensively reference Dendra, Dendrarchy, and the old root agent name "sensei." The migration plan document (`sprawl-migration-plan.md`) describes the rename but it's clearly not finished.

A new user cloning this repo would be confused about whether this project is called "Sprawl" or "Dendra." The variable names throughout the Go source suggest the rename was cosmetic (binary name, README, module path) but the code internals were never updated.

**Severity note:** This doesn't *break* anything — the binary works, tests pass. But it makes the codebase look unfinished and would be jarring for any contributor. Upgrading to "blocker" if the goal is to make a good impression on open-source contributors.

### P2: CLAUDE.md references incorrect binary name

CLAUDE.md line 10 says:
```
make build        # builds ./dendra binary
```
And line 23 says:
```
test against the locally built ./dendra binary
```

The Makefile builds `./sprawl`. This would confuse any AI agent (or human) following the instructions.

### P3: .gitignore has stale comment and entry

```
# Binary (Makefile still builds as 'dendra' until Phase 3)
dendra
```

The Makefile builds `sprawl` now. The comment is wrong and the `dendra` entry is vestigial (though `sprawl` is also listed, so the current binary is properly ignored).

### P4: docs/research/ contains internal development history

The `docs/research/` directory contains 16 files of internal research including:
- **naming-candidates.md** — discusses trademark conflicts with Dendra Systems (a $28M company), reveals the rename was forced by trademark risk
- **sprawl-migration-plan.md** — detailed internal migration plan with references to "sensei" (old root agent name)
- **stream-json-prototype/** — prototype Go code for the Claude Code stream-json protocol
- **open-source-readiness/** — 7 detailed readiness reports (licensing, secrets scan, security audit, etc.)

None of this is *embarrassing*, but it's a lot of internal process documentation that a new user doesn't need. The naming-candidates file in particular reveals business-sensitive reasoning about why the rename happened. The open-source-readiness reports are fine — they show diligence.

**Recommendation:** Consider whether `docs/research/` should be in the public repo. If kept, add a README explaining these are historical research documents from development.

### P5: No CI badge or status indicator in README

There's a release workflow but no CI/test workflow visible, and no badge in the README showing build status. New users and contributors can't quickly tell if the project is in a healthy state.

---

## 🟢 Cosmetic (nice to fix but not important)

### C1: DESCRIPTION.md agent type table says Researchers "cannot spawn agents" but also says they get "Own worktree"

This is accurate (researchers do get worktrees for isolation) but the table layout might suggest worktrees are only for agents that spawn others. Minor — the text below the table explains it.

### C2: Banner image is AI-generated cyberpunk cityscape

The banner (`assets/banner.jpg`) is a cool cyberpunk cityscape that fits the Gibson/Sprawl theme. No issue per se, but if there are concerns about AI-generated imagery attribution in open-source projects, this should be noted.

### C3: The `sprawl` CLI help text is clean but could link to docs

`sprawl --help` output is well-organized with clear command descriptions. Could benefit from a "Documentation: https://github.com/dmotles/sprawl" footer, but this is very minor.

### C4: CONTRIBUTING.md is minimal but functional

It covers the basics (bug reports, dev setup, PRs, code style). Could benefit from a section on architecture overview (pointing to DESCRIPTION.md) and how the agent system works for contributors who aren't familiar with it. But it's not missing anything critical.

---

## What's Good

- **The build works.** `make build` succeeds cleanly and produces a working binary.
- **CLI help is excellent.** `sprawl --help` is well-organized, commands are clearly named, and the overall UX feels polished.
- **DESCRIPTION.md is outstanding.** The architecture document is thorough, well-written, and genuinely explains what the system does and why. This is one of the best architecture docs I've seen in a project of this size.
- **install.sh is well-written.** Good error handling, checksum verification, platform detection, sudo handling, PATH warning. It just needs releases to download.
- **The .gitignore correctly handles .sprawl/.** It ignores everything under `.sprawl/` except `config.yaml`, which is the right behavior.
- **Apache 2.0 license is properly in place.**
- **The open-source readiness research is thorough.** The team clearly did their homework on licensing, security, and distribution.

---

## Reflections

**Surprising:** The sheer scale of the incomplete rename. 583 references to "dendra" across 59 tracked files in production code. The external-facing rename (README, module path, binary name) was done, but the internal code is still largely unrenamed. This suggests the rename was prioritized by visibility rather than completeness.

**Open questions:**
- Is there a plan to complete the internal rename (variable names like `dendraRoot`, `dendraPath`, etc.)? The migration plan document mentions phases but it's unclear which have been completed.
- Will there be a v0.1.0 release soon? The release workflow exists but hasn't been triggered.
- Is the Go 1.25 minimum version intentional, or could it be lowered to support more users?

**What I'd investigate next:**
- Run the full `make validate` to check if tests pass (I only ran `make build`)
- Check if the release workflow would actually produce working artifacts
- Audit the agent prompt templates in `internal/state/prompts.go` for any remaining old-name references that would leak into agent behavior
- Check whether `go install github.com/dmotles/sprawl@latest` would work once a tag is published (module proxy behavior)
