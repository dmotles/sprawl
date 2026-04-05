# Definitive Naming Research: Dendra/Dendrarchy Replacement

**Compiled:** 2026-04-05
**Sources:** Reed's naming-candidates.md + 5 sci-fi themed research directions (Matrix/Cyberpunk, 90s Hacker Culture, Blade Runner/PKD, Classic AI Sci-Fi, Dune)

---

## 1. Context

The current name "dendra" / "Dendrarchy" (from Greek *dendron* = tree + *-archy* = governance) has a trademark conflict with **Dendra Systems** (dendra.io), a $28M-funded environmental tech company. A new name is needed.

**The tool:** A CLI-based multi-agent orchestration system for Claude Code. It spawns, coordinates, and manages hierarchies of AI coding agents working across git worktrees. The human sits atop a tree of agents that can delegate work to sub-agents.

**Naming requirements:**

1. Unique in the developer tooling space (no conflicts with common CLI tools, package registries)
2. No trademark risk from funded companies or established brands
3. Short for CLI use (6 chars benchmark, 8 max)
4. Memorable and pronounceable
5. Evokes hierarchy, orchestration, trees, swarms, delegation, or multi-agent coordination
6. Tokenizer-friendly (ideally 1 BPE token)
7. Relates to the domain of agent orchestration

---

## 2. All Vetted Candidates

| Name | Chars | Source/Inspiration | Metaphor | Token Est. | Conflicts | Verdict |
|------|-------|--------------------|----------|------------|-----------|---------|
| copse | 5 | English — small dense group of trees | Coordinated cluster of agents | 1 | None found | **Strong** |
| muster | 6 | Military — assemble troops for action | Spawning and coordinating agents | 1 | None found | **Strong** |
| bough | 5 | English — main branch of a tree | Tree hierarchy, git branch connection | 1 | None found | **Strong** |
| liege | 5 | Feudal — lord to whom allegiance is owed | Human->sensei->agent hierarchy | 1 | None found | **Strong** |
| thane | 5 | Feudal/Shakespeare — lord who manages people | Agent hierarchy, land-grant governance | 1 | None found | **Strong** |
| brood | 5 | English — group of offspring; to nurture | Spawning child agents, parent managing brood | 1 | Tiny creative agency in Windsor, UK | **Strong** |
| sprawl | 6 | Gibson's Sprawl trilogy (Neuromancer) | Agents spreading across codebase like The Sprawl | 1 | None found | **Strong** |
| jackin | 6 | Cyberpunk — jacking in to direct agents | Direct connection to agent swarm | 1 | None found | **Strong** |
| phreak | 6 | Phone phreaking culture | System manipulation, hacker orchestration | 1 | None found | **Strong** |
| voight | 6 | Blade Runner — Voight-Kampff test | Human who tests/directs replicants | 1 | None found | **Strong** |
| forbin | 6 | Colossus: The Forbin Project | Human who commands AI | 1 | None found | **Strong** |
| haldane | 7 | HAL 9000 evolution | AI systems lineage | 1 | None found | **Strong** |
| kanly | 5 | Dune — formal orchestrated conflict | Orchestrated multi-party coordination | 1 | None found | **Strong** |
| naib | 4 | Dune / Arabic for "deputy/leader" | Leader who coordinates community | 1 | None found | **Strong** |
| marshal | 7 | Military — commanding officer | Organize and command forces | 1 | npm data serialization libs (unrelated) | **Medium-Strong** |
| regent | 6 | Feudal — governs in place of monarch | AI acting on behalf of human | 1 | Minor crate | **Medium-Strong** |
| cordon | 6 | Military — organized perimeter | Controlled coordination | 1 | kubectl overlap | **Medium** |
| herald | 6 | Medieval — messenger/announcer | Communication coordination | 1 | Small inactive herald-cli | **Medium** |
| rowan | 5 | Celtic/Norse — mountain ash tree | Tree + protection/wisdom | 1 | None found | **Medium** |
| alder | 5 | Botany — collaborative tree genus | Orchestrator that enables agents | 1 | None found | **Medium** |
| steward | 7 | English — manages another's affairs | Delegation and management | 1 | Tiny npm package | **Medium** |
| baton | 5 | English — relay/conductor's stick | Delegation + orchestration | 1 | PHP tool (minor) | **Medium** |
| cohort | 6 | Roman military — tactical unit | Coordinated group | 1 | "cohort analysis" association | **Medium** |
| oper | 4 | Matrix — The Operator | Commanding agents from terminal | 1 | Pronunciation ambiguity | **Medium** |
| coldwire | 8 | Cyberpunk aesthetic | Dark/technical aesthetic | 2 | None found | **Medium** |
| phrak | 5 | Phrack magazine | Hacker culture | 1 | None found | **Medium** |
| warez | 5 | Underground distribution crews | Crew coordination | 1 | Piracy connotation | **Medium** |
| kipple | 6 | PKD — entropy concept | Tool that fights entropy | 1 | Clean registries | **Medium** |
| sietch | 6 | Dune — hidden Fremen community | Coordinated community | 1 | Lightly used on GitHub | **Medium** |
| kwisatz | 7 | Dune — supreme prescience | Supreme coordinator | 2 | Hard to spell/type | **Medium** |
| cr3w | 4 | Leetspeak — crew | Hacker crew coordination | 1 | Verbal awkwardness | **Medium** |
| ubik | 4 | PKD — reality-stabilizing force | Stabilizing force | 1 | npm taken + companies | **Medium** |
| flank | 5 | Military — strategic positioning | Coordinated action | 1 | Firebase test runner (active) | **Medium-Weak** |
| genus | 5 | Biology — taxonomic node | Classification tree | 1 | None found | **Medium-Weak** |
| sylvan | 6 | Latin — relating to woods | Forest of agents | 1 | C library (academic) | **Medium-Weak** |
| phalanx | 7 | Greek military — close formation | Disciplined coordination | 1 | Phalanx Software Ltd. | **Medium-Weak** |

---

## 3. Rejected Candidates

| Name | Source | Reason for Rejection |
|------|--------|---------------------|
| dendra | Greek — tree | Dendra Systems (dendra.io, $28M funded) |
| sensei | Japanese — teacher | Homebrew formula, npm/PyPI/crates taken, USPTO trademarks, Automattic project |
| grove | English — group of trees | Multiple CLI tools (git worktree managers, PyPI AI toolkit) |
| hive | English — insect colony | Apache Hive (massive data warehouse) |
| swarm | English — coordinated group | OpenAI Swarm, multiple AI agent tools |
| nexus | Latin — connection | Sonatype Nexus (trademark), $27M Nexus Labs, saturated |
| foreman | English — supervisor | theforeman.org (server lifecycle management) |
| trunk | English — tree trunk | Trunk.io (well-funded dev toolkit) |
| helm | English — ship's wheel | Kubernetes Helm (CNCF graduated) |
| consul | English — official | HashiCorp Consul |
| bower | English — tree shelter | Bower package manager |
| envoy | English — messenger | Envoy proxy (CNCF) |
| cortex | Latin — brain region | Snowflake Cortex, Cortex.io |
| apex | English — peak | Salesforce Apex |
| rally | English — gathering | Rally Software (Broadcom) |
| trellis | English — plant structure | Multiple tools including AI framework |
| cadre | French — core group | Cadre AI (funded, OpenAI partner) |
| overstory | English — forest canopy | Direct competitor (multi-agent Claude Code tool) |
| flock | English — group of birds | Linux `flock` command |
| roost | English — bird perch | Roost.ai ($4M funded) |
| strata | Latin — layers | Multiple PyPI projects |
| clade | Biology — ancestor group | Active PyPI tool (v4.1) |
| arbor | Latin — tree | crates.io + npm arborist |
| echelon | Military — level | Echelon SDK, Echelon IoT company |
| canopy | English — forest top | Canopy PEG parser, Canopy IDE |
| horde | English — large group | Horde PHP framework |
| deck | Cyberpunk — cyberdeck | crates.io, Steam Deck trademark |
| cabal | English — secret group | Haskell build tool, funded startup |
| coloss | Truncated Colossus | Crowded + looks incomplete |
| positrn | Asimov — positronic | Posit's Positron IDE |
| mono | 2001 — monolith | Mono .NET runtime |
| mercer | PKD — shared consciousness | Mercer HR consulting (global firm) |
| melange | Dune — the spice | Chainguard's melange (Homebrew, $300M company) |

---

## 4. Top Contenders — Shortlist

### Tier 1: Strongest Candidates

**sprawl** (6 chars) — From William Gibson's Sprawl trilogy (Neuromancer, Count Zero, Mona Lisa Overdrive). The Boston-Atlanta Metropolitan Axis — a massive, interconnected, organic network. Agents spread across a codebase like the Sprawl spreads across the eastern seaboard. Also a plain English word ("urban sprawl") so it's not anyone's IP. Zero conflicts. Excellent CLI feel: `sprawl spawn`, `sprawl status`, `sprawl merge`. Potential thematic verb pairing: `sprawl expand` / `sprawl contract`.

**voight** (6 chars) — From the Voight-Kampff test in Blade Runner. The human who administers the test sits above the replicant, directing and evaluating. Maps perfectly to a human directing AI agents. Zero conflicts. Less immediately recognizable as a word to non-Blade Runner fans.

**naib** (4 chars) — Fremen leader in Dune; also a real Arabic word meaning "deputy" or "leader." The shortest candidate at 4 characters. Dual origin gives it standalone legitimacy beyond Dune IP. Zero conflicts.

**forbin** (6 chars) — Dr. Charles Forbin from Colossus: The Forbin Project. The human who built and commands the AI. Thematically precise: the tool is named after the human controller, not the AI. Zero conflicts. More obscure reference.

**kanly** (5 chars) — Formal orchestrated conflict between Great Houses in Dune. Structured, rule-bound coordination. Zero conflicts. Short and distinctive.

**copse** (5 chars) — A small, dense group of trees. The single best tree metaphor: a coordinated cluster. Zero conflicts. Short, common English word. Less sci-fi energy.

**muster** (6 chars) — "To assemble troops for action." The core verb of the tool. Zero conflicts. Matches the 6-char benchmark. Less sci-fi, more military/practical.

### Tier 2: Strong Alternatives

**liege** (5) — Feudal lord. Powerful hierarchy metaphor.
**thane** (5) — Scottish feudal lord. Shakespeare connection.
**brood** (5) — Spawn and nurture offspring. Perfect metaphor, slight negative connotation.
**phreak** (6) — Phone phreaking. Strong hacker culture energy.
**jackin** (6) — Jacking in. Cyberpunk verb energy.
**haldane** (7) — HAL evolution. Completely clean but longer and subtle.

---

## 5. Open Questions

1. **Tokenizer verification:** Token counts are estimates. Verify against Claude's BPE tokenizer before finalizing.
2. **Domain availability:** No candidates checked for .com, .dev, .io availability.
3. **GitHub org availability:** Not checked.
4. **Social media handles:** Not checked.
5. **International considerations:** Some names may have unintended meanings in other languages.
