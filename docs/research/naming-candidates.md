# Naming Candidates for Dendra/Dendrarchy Replacement

**Date:** 2026-04-04
**Researcher:** reed
**Branch:** research/naming

## Context

The current name "dendra" / "Dendrarchy" (from Greek *dendron* = tree + *-archy* = governance) has a trademark conflict with Dendra Systems (dendra.io), a $28M-funded environmental tech company. We need a new name that:

1. Is unique in the developer tooling space (no conflicts with common CLI tools, package registries)
2. Has no trademark risk from funded companies or established brands
3. Is short for CLI use (6 chars benchmark, 8 max)
4. Is memorable and pronounceable
5. Evokes hierarchy, orchestration, trees, swarms, delegation, or multi-agent coordination
6. Is tokenizer-friendly (ideally 1 BPE token)
7. Relates to the domain of agent orchestration

## Vetting Methodology

Each candidate was searched across:
- npm registry
- PyPI
- crates.io
- Homebrew formulae
- GitHub (active projects)
- Company/brand trademark searches
- General web search for conflicts

---

## Tier 1: Top Picks

### 1. copse
- **CLI command:** `copse` (5 chars)
- **Etymology:** A copse is a small, dense group of trees. Directly evokes the tree metaphor while suggesting a coordinated cluster — a group of agents working together like trees in a copse.
- **Token count:** 1 (common English word)
- **Conflicts found:** Domain copse.xyz listed for sale on a marketplace. No software tools, packages, or companies found using this name.
- **Verdict:** **Strong candidate.** Short, evocative, unique, zero conflicts. The "small group of trees" meaning is a perfect metaphor for a coordinated cluster of agents.

### 2. muster
- **CLI command:** `muster` (6 chars)
- **Etymology:** "To assemble troops or forces for inspection or action." Military term for gathering and organizing a force — perfectly captures spawning and coordinating agents.
- **Token count:** 1 (common English word)
- **Conflicts found:** No CLI tools, npm packages, or significant software projects found. No funded startups or tech companies using this name in software.
- **Verdict:** **Strong candidate.** Excellent verb that captures the core action: mustering agents to work. Great for CLI ergonomics ("muster spawn", "muster status"). Zero meaningful conflicts.

### 3. bough
- **CLI command:** `bough` (5 chars)
- **Etymology:** "A main branch of a tree." Directly references tree structure and hierarchy — each agent works on a bough (branch) of the overall tree.
- **Token count:** 1 (common English word)
- **Conflicts found:** No software tools, CLI packages, or companies found. Clean across all registries searched.
- **Verdict:** **Strong candidate.** Tree metaphor is literal and elegant. Short, memorable. Natural connection to git branches ("boughs"). Only downside: pronunciation might not be immediately obvious to non-native speakers (rhymes with "cow").

### 4. liege
- **CLI command:** `liege` (5 chars)
- **Etymology:** "A feudal lord to whom allegiance and service are owed." Captures the hierarchical relationship between the human (liege lord), the root orchestrator, and subordinate agents.
- **Token count:** 1 (common English word)
- **Conflicts found:** No CLI tools, npm packages, or significant software projects found. Liege is also a city in Belgium but no tech brand conflicts.
- **Verdict:** **Strong candidate.** Powerful hierarchy metaphor. Short and memorable. Commands read well: "liege spawn", "liege status", "liege merge". The feudal governance metaphor perfectly maps to the human->sensei->agents hierarchy.

### 5. thane
- **CLI command:** `thane` (5 chars)
- **Etymology:** "A feudal lord or baron in Scottish/Anglo-Saxon hierarchy." A thane held land granted by the king and managed people under them — directly parallels the agent hierarchy.
- **Token count:** 1 (common English word, also famous from Shakespeare's Macbeth — "Thane of Cawdor")
- **Conflicts found:** Thane is a city in India with software companies, but no software product/tool named "thane" was found. Clean across all package registries.
- **Verdict:** **Strong candidate.** Evocative, short, literary. Strong hierarchy connotation. The Macbeth connection ("Thane of Glamis, Thane of Cawdor") gives it cultural memorability.

### 6. brood
- **CLI command:** `brood` (5 chars)
- **Etymology:** "A group of young animals (especially birds) produced at one hatching; to nurture offspring." Evokes spawning child agents and the parent managing its brood.
- **Token count:** 1 (common English word)
- **Conflicts found:** A creative agency called "Brood" in Windsor, UK (not tech/software). No CLI tools or packages found.
- **Verdict:** **Strong candidate.** Great metaphor for spawning and managing child agents. Short, punchy, memorable. "brood spawn" is almost redundantly perfect. Minor concern: "brood" also means "to think anxiously" which could create a slight negative connotation.

---

## Tier 2: Solid Alternatives

### 7. marshal
- **CLI command:** `marshal` (7 chars)
- **Etymology:** "To arrange or organize; a high-ranking military officer who commands forces." Directly evokes orchestration and command.
- **Token count:** 1 (common English word)
- **Conflicts found:** Several npm packages for data serialization (marshal, marshall, @marcj/marshal) but none are CLI tools. No competing developer tools or companies.
- **Verdict:** **Medium-strong candidate.** Excellent meaning, familiar word. Slightly long at 7 chars. The data serialization npm packages are unrelated and low-profile, but the name overlap could cause minor confusion in some contexts.

### 8. regent
- **CLI command:** `regent` (6 chars)
- **Etymology:** "One who governs in place of a monarch." Perfectly describes the AI orchestrator that acts on behalf of the human leader.
- **Token count:** 1 (common English word)
- **Conflicts found:** `regent-internals` crate on crates.io (proc macros, very minor). No CLI tools, no major projects.
- **Verdict:** **Medium-strong candidate.** Elegant governance metaphor. Good length. The "governing on behalf of" meaning perfectly maps to the AI agents acting on behalf of the human.

### 9. cordon
- **CLI command:** `cordon` (6 chars)
- **Etymology:** "A line of people or things enclosing or guarding an area; to form a protective barrier around." Suggests organized coordination and controlled perimeters.
- **Token count:** 1 (common English word)
- **Conflicts found:** No CLI tools, packages, or companies found. Also used in Kubernetes (`kubectl cordon`) to mark a node as unschedulable — related but different context.
- **Verdict:** **Medium candidate.** Clean and short. The kubectl association could cause some confusion for Kubernetes users, though the context is quite different. The meaning doesn't as directly evoke hierarchy/orchestration as other candidates.

### 10. herald
- **CLI command:** `herald` (6 chars)
- **Etymology:** "A messenger or announcer; one who precedes and announces." Evokes the communication/messaging aspect of agent coordination.
- **Token count:** 1 (common English word)
- **Conflicts found:** herald-app/herald-cli on GitHub (small AWS deployment tool, low adoption). No major conflicts.
- **Verdict:** **Medium candidate.** Good word, evocative of communication. Existing herald-cli is tiny and inactive, but the name overlap exists. Meaning leans more toward messaging than hierarchy/orchestration.

### 11. rowan
- **CLI command:** `rowan` (5 chars)
- **Etymology:** A rowan is a type of mountain ash tree, historically associated with protection and wisdom in Celtic/Norse mythology.
- **Token count:** 1 (common English word/name)
- **Conflicts found:** No CLI tools or software packages found. Clean across all registries searched.
- **Verdict:** **Medium candidate.** Short, pleasant to type. Tree connection is present but subtle — most people know rowan as a name rather than a tree. Less immediately evocative of orchestration/hierarchy.

### 12. alder
- **CLI command:** `alder` (5 chars)
- **Etymology:** A genus of trees (Alnus) that grow in wet habitats, known for forming groves and improving soil for other plants — a collaborative tree.
- **Token count:** 1 (common English word)
- **Conflicts found:** No CLI tools or software packages found. Clean across all registries.
- **Verdict:** **Medium candidate.** Short, easy to type. The collaborative nature of alder trees (nitrogen fixation, improving soil for other trees) is a nice metaphor for an orchestrator that enables other agents. But the connection is obscure — most people just think "tree."

### 13. steward
- **CLI command:** `steward` (7 chars)
- **Etymology:** "One who manages another's property or affairs." Directly captures the delegation and management metaphor.
- **Token count:** 1 (common English word)
- **Conflicts found:** `repo-steward` on npm (1 weekly download, inactive). No major conflicts.
- **Verdict:** **Medium candidate.** Meaning is excellent — perfectly captures management/delegation. At 7 chars it's on the longer side. Very clean namespace.

### 14. baton
- **CLI command:** `baton` (5 chars)
- **Etymology:** "A staff passed from one runner to the next in a relay; a conductor's stick used to direct an orchestra." Evokes both delegation (passing work) and orchestration (conducting).
- **Token count:** 1 (common English word)
- **Conflicts found:** A PHP Composer dependency analytics tool (webfactory/baton). No npm/CLI conflicts.
- **Verdict:** **Medium candidate.** Dual metaphor of relay (delegation) and conducting (orchestration) is elegant. Short and memorable. The PHP tool is unrelated and minor.

### 15. cohort
- **CLI command:** `cohort` (6 chars)
- **Etymology:** "A group of soldiers; a band of companions or supporters." Roman military term for a tactical unit.
- **Token count:** 1 (common English word)
- **Conflicts found:** No CLI tools or developer packages found. Common in analytics ("cohort analysis") but no product conflicts.
- **Verdict:** **Medium candidate.** Good group/military metaphor. Clean namespace. Concern: heavily associated with "cohort analysis" in analytics/product contexts, which could create confusion about what the tool does.

### 16. flank
- **CLI command:** `flank` (5 chars)
- **Etymology:** "The side of a military formation; to position forces on the side of." Suggests strategic positioning and coordinated action.
- **Token count:** 1 (common English word)
- **Conflicts found:** Flank is a Firebase Test Lab test runner (github.com/Flank/flank). Niche but active project.
- **Verdict:** **Medium-weak candidate.** Short and punchy, but the Firebase test runner is an active project in the developer tooling space. The meaning is more about positioning than hierarchy/orchestration.

---

## Tier 3: Creative Long Shots

### 17. clade
- **CLI command:** `clade` (5 chars)
- **Etymology:** "A group of organisms descended from a common ancestor." From biology — a branch of the tree of life. Evokes the tree hierarchy and lineage of agents.
- **Token count:** 1 (common English word)
- **Conflicts found:** `clade` on PyPI — a build command interception tool for C projects. Active (v4.1).
- **Verdict:** **Weak candidate.** Great metaphor (evolutionary tree branches) but the PyPI package is active and in the developer tools space.

### 18. genus
- **CLI command:** `genus` (5 chars)
- **Etymology:** "A principal taxonomic category in biological classification, ranking above species." A node in the tree of life.
- **Token count:** 1 (common English word)
- **Conflicts found:** No CLI tools or packages found across registries.
- **Verdict:** **Medium-weak candidate.** Clean namespace and short, but the taxonomic meaning is abstract. Doesn't immediately suggest orchestration or coordination — sounds more like it could be a database or classification tool.

### 19. arbor
- **CLI command:** `arbor` (5 chars)
- **Etymology:** Latin for "tree." Direct and classical.
- **Token count:** 1 (common English word)
- **Conflicts found:** `arbor-cli` and `arbor` on crates.io. npm's internal `@npmcli/arborist` package (though scoped). Multiple small packages.
- **Verdict:** **Weak candidate.** The Latin "tree" meaning is perfect but there are too many existing packages in the arbor/arborist namespace, especially npm's official arborist.

### 20. sylvan
- **CLI command:** `sylvan` (6 chars)
- **Etymology:** "Relating to or inhabiting woods or forests." Evokes a woodland/forest setting — agents as trees in a forest.
- **Token count:** 1 (common English word)
- **Conflicts found:** Sylvan is a C library for binary decision diagrams (BDDs) from University of Twente. Academic project, active.
- **Verdict:** **Medium-weak candidate.** Beautiful word, good length. The BDD library is niche/academic and wouldn't likely cause real-world confusion, but it is an active computer science project.

### 21. echelon
- **CLI command:** `echelon` (7 chars)
- **Etymology:** "A level or rank in an organization; a formation of troops in parallel rows." Directly evokes hierarchical levels.
- **Token count:** 1 (common English word)
- **Conflicts found:** Echelon SDK (npm), Echelon (terminal progress bars from cirruslabs), Echelon.com (IoT company). Multiple conflicts.
- **Verdict:** **Weak candidate.** Perfect meaning but too many existing uses across the tech landscape.

### 22. canopy
- **CLI command:** `canopy` (6 chars)
- **Etymology:** "The uppermost layer of branches in a forest." The human/orchestrator sits at the canopy level overseeing everything below.
- **Token count:** 1 (common English word)
- **Conflicts found:** Canopy PEG parser compiler (npm installable), Canopy Python IDE (Enthought), CanopySimulations (GitHub org). Multiple conflicts.
- **Verdict:** **Weak candidate.** Great metaphor but namespace is crowded.

### 23. phalanx
- **CLI command:** `phalanx` (7 chars)
- **Etymology:** "A body of troops in close formation." Greek military formation suggesting coordinated, disciplined action.
- **Token count:** 1 (common English word)
- **Conflicts found:** Phalanx Software Ltd. (GitHub org, software development company). Minor conflict.
- **Verdict:** **Medium-weak candidate.** Strong military/coordination metaphor. At 7 chars it's longer. The software company is small but shares the exact name.

### 24. flock
- **CLI command:** `flock` (5 chars)
- **Etymology:** "A group of birds; to gather together." Swarm/group metaphor.
- **Token count:** 1 (common English word)
- **Conflicts found:** `flock-cli` on npm (messaging platform CLI, inactive). Also `flock` is a Linux filesystem lock command (`man flock`). The Linux utility conflict is significant.
- **Verdict:** **Weak candidate.** The Linux `flock` command is a real conflict — developers use it for file locking in scripts.

### 25. horde
- **CLI command:** `horde` (5 chars)
- **Etymology:** "A large group, especially of people." Evokes a coordinated mass of agents.
- **Token count:** 1 (common English word)
- **Conflicts found:** The Horde Project (PHP framework with CLI tools, horde.org). Active open-source project.
- **Verdict:** **Weak candidate.** The Horde PHP framework is well-established and uses the exact name.

---

## Summary Comparison Table

| Rank | Name | CLI Chars | Token Est. | Conflicts | Metaphor Fit | Verdict |
|------|------|-----------|------------|-----------|-------------|---------|
| 1 | copse | 5 | 1 | None found | Small group of trees | **Strong** |
| 2 | muster | 6 | 1 | None found | Assemble forces | **Strong** |
| 3 | bough | 5 | 1 | None found | Main tree branch | **Strong** |
| 4 | liege | 5 | 1 | None found | Feudal lord/governance | **Strong** |
| 5 | thane | 5 | 1 | None found | Feudal lord (Scottish) | **Strong** |
| 6 | brood | 5 | 1 | Tiny creative agency | Spawn and nurture offspring | **Strong** |
| 7 | marshal | 7 | 1 | npm data serialization libs | Organize/command forces | **Medium-strong** |
| 8 | regent | 6 | 1 | Minor crate | Governs for a monarch | **Medium-strong** |
| 9 | cordon | 6 | 1 | kubectl overlap | Organized perimeter | **Medium** |
| 10 | herald | 6 | 1 | Small inactive CLI | Messenger/announcer | **Medium** |
| 11 | rowan | 5 | 1 | None found | Mountain ash tree | **Medium** |
| 12 | alder | 5 | 1 | None found | Collaborative tree genus | **Medium** |
| 13 | steward | 7 | 1 | Tiny npm package | Manages affairs | **Medium** |
| 14 | baton | 5 | 1 | PHP tool | Relay/conductor's stick | **Medium** |
| 15 | cohort | 6 | 1 | None found | Military unit | **Medium** |

---

## Recommendations

**If you want the strongest tree metaphor:** `copse` or `bough`
- "copse" is a small group of trees — perfectly describes a coordinated cluster of agents
- "bough" is a main branch — ties beautifully to git branches and tree hierarchy

**If you want the strongest orchestration/command metaphor:** `muster` or `marshal`
- "muster" means to assemble forces — the core action of the tool
- "marshal" means to organize and command — slightly longer but very clear

**If you want the strongest hierarchy/governance metaphor:** `liege` or `thane`
- "liege" captures the feudal lord relationship perfectly
- "thane" has the Shakespeare connection and is very short

**If you want the most unique/distinctive:** `copse` or `brood`
- Both are common English words that are almost never used in tech
- Both have delightfully specific meanings that map to the tool's purpose

**My top 3 personal recommendations:**
1. **copse** — unique, short, perfect tree metaphor, zero conflicts
2. **muster** — matches the benchmark length, perfect action verb, zero conflicts
3. **liege** — short, powerful hierarchy metaphor, zero conflicts

---

## Candidates Explicitly Rejected (and Why)

| Name | Reason for Rejection |
|------|---------------------|
| grove | Multiple CLI tools (git worktree managers, PyPI AI toolkit) |
| hive | Apache Hive (massive, well-known data warehouse) |
| swarm | OpenAI Swarm, multiple AI agent orchestration tools |
| nexus | Sonatype Nexus Repository Manager |
| foreman | theforeman.org (server lifecycle management) |
| trunk | Trunk.io (well-funded developer experience toolkit) |
| helm | Kubernetes Helm (CNCF graduated project) |
| consul | HashiCorp Consul (service networking) |
| bower | Bower package manager (deprecated but well-known) |
| envoy | Envoy proxy (CNCF project) |
| cortex | Snowflake Cortex Code, Cortex.io |
| apex | Salesforce Apex (massive ecosystem) |
| rally | Rally Software (Broadcom, project management) |
| trellis | Multiple tools including AI coding framework |
| cadre | Cadre AI — funded AI startup ($3-5M revenue, OpenAI partner) |
| overstory | Direct competitor (multi-agent orchestration for Claude Code) |
| flock | Linux `flock` command (filesystem locking utility) |
| roost | Roost.ai ($4M funded), ROOST nonprofit (Mozilla-backed) |
| strata | Multiple active projects on PyPI |

---

## Open Questions

1. **Tokenizer verification:** Token counts above are estimates based on word commonality. Before finalizing, the actual token count should be verified against Claude's tokenizer.
2. **Domain availability:** None of the candidates have been checked for domain availability (.com, .dev, .io).
3. **Social media handles:** No checking was done for GitHub org names, Twitter handles, etc.
4. **International considerations:** Some names may have unintended meanings in other languages (e.g., "bough" pronunciation issues for non-native English speakers).
5. **Community perception:** The feudal metaphors (liege, thane, regent) could be seen as either charmingly evocative or pretentiously archaic depending on audience.
