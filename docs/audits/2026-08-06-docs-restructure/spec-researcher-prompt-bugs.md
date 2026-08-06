# SPEC — two researcher/QA prompt bugs (Task 3 of D6)

For an engineer to apply. **Not applied here** — this branch is documentation
only. Both bugs are in `internal/agent/`, both are small, and they are
independent: land them as two commits, in either order.

Line numbers are against `dmotles/docs-audit-skills-behavior` @ `68d2ddc`.
Re-derive them before editing; the file is under active change.

---

## Bug 1 — `BuildResearcherPrompt` never appends the `# Environment` block

### Current behaviour

`envContextBlock` renders the trailing `# Environment` section (working
directory, git repo, git branch, platform, shell). Three of the four child-role
builders call it:

| builder | `internal/agent/prompt.go` | calls `envContextBlock`? |
|---|---|---|
| `BuildEngineerPrompt` | :255 | yes (:272) |
| `BuildResearcherPrompt` | :299 | **no** |
| `BuildQAPrompt` | :319 | yes (:332) |
| `BuildManagerPrompt` | :340 | yes (:365) |

Confirmed in the goldens: `engineer_tui.golden`, `qa_tui.golden`, and
`manager_tui.golden` each contain a `# Environment` heading;
`researcher_tui.golden` does not, and ends abruptly at the final `RULES:`
bullet.

The omission looks accidental rather than deliberate: `BuildResearcherPrompt`
already takes an `env EnvConfig` parameter and already reads `env.Subagent` and
`env.TestMode` from it. Only the environment block is missed. There is no
comment anywhere justifying the difference.

### Impact

A researcher is the one role that must decide **where on disk to write its
output** (see Bug 2), and it is the only role not told its working directory.
It also is not told its branch, so a researcher reconstructs both by running
commands — which is exactly the inference Bug 2 depends on going right.

### Proposed change

`internal/agent/prompt.go`, in `BuildResearcherPrompt`. Current:

```go
	prompt := strings.Join(sections, "\n\n")

	if env.TestMode {
		prompt += testSandboxWarning
	}
	return prompt
}
```

Proposed — match the shape the other three builders already use:

```go
	prompt := strings.Join(sections, "\n\n")

	result := prompt + envContextBlock(branchName, env)
	if env.TestMode {
		result += testSandboxWarning
	}
	return result
}
```

Note the ordering: in the existing three builders the environment block precedes
the test-sandbox warning. Preserve that, so all four roles are identical.

### Verification

- **Re-pin `testdata/researcher_tui.golden`.** It is the only golden this
  changes. Regenerate with the documented command
  (`internal/agent/prompt_test.go:1625`):

  ```bash
  GENERATE_GOLDEN=1 go test ./internal/agent/ -run TestGenerateGoldenFiles
  ```

  That target rewrites **all five** goldens (`prompt_test.go:1648-1653`), so
  review `git diff internal/agent/testdata/` and confirm exactly one file
  changed. A diff touching the other four means something else drifted and
  should be understood before it is committed under this change.

- **Assert the new content, not just golden equality.** A regenerated golden
  agrees with whatever the code now produces, including a wrong result — it
  cannot fail for this bug. Add a positive assertion alongside the existing
  researcher tests, in the shape already used at `prompt_test.go:331`, `:887`,
  and `:1925`: build a researcher prompt with a populated `EnvConfig` and
  require `# Environment`, the working directory, and the branch to be present.

- **Demonstrate it can fail.** Revert the one-line change, confirm the new
  assertion goes red, restore. Record what it printed.

- `make validate`. No e2e row is implicated: `internal/agent/prompt*.go` does
  not appear in the mandatory-test table, and neither does `prompt.go`'s
  directory via any glob row. Derive this yourself from the table at the commit
  you are landing rather than trusting this paragraph.

---

## Bug 2 — the `findings/` path is relative and resolves differently per role

### Current behaviour

Three separate strings tell three different roles about the same directory, in
three different shapes, and every one of them is a **relative** path. Every
agent's working directory is its own worktree, so a relative path means a
different absolute location for each reader.

| # | file:line | text | resolves to |
|---|---|---|---|
| 1 | `prompt_child_sections.go:183` (`researcherDocumentingSection`) | `write to .sprawl/agents/%s/findings/` | `<researcher-worktree>/.sprawl/agents/<name>/findings/` |
| 2 | `prompt_child_sections.go:213` (`qaVerificationProtocolSection`) | `Longer artifacts go in findings/%s/ in your worktree` | `<qa-worktree>/findings/<qa-name>/` |
| 3 | `prompt.go:188` and `prompt_child_sections.go:298` (root/manager review guidance) | `check .sprawl/agents/<name>/findings/` | `<manager-worktree>/.sprawl/agents/<name>/findings/` |

Rows 1 and 3 are the *same string* handed to two roles with different working
directories, so they name two different directories — and the one the manager is
sent to has never existed. Row 2 is a third shape entirely: the agent name is a
subdirectory of a top-level `findings/`, not a component under `.sprawl/`.

The intended location is neither of the worktree-relative ones. It is
`<repoRoot>/.sprawl/agents/<name>/findings/` — the agent's real state directory,
sibling to `SYSTEM.md`, `queue/`, and `tasks/`.

### Evidence, and the reason this is currently masked

On this host, every findings directory that exists is at the repo-root location
(`ghost`, `ratz`, `trace`); **no worktree contains one**, at any depth. Since
each of those agents' working directory was its own worktree, each must have
silently declined to follow the instruction literally and used the repo-root path
instead.

State this carefully: the observed pass rate is 3 of 3, so today the bug produces
no wrong files. That is not evidence the text is fine — it is evidence that the
text is wrong and that agents are currently compensating by inference. The defect
is real and latent, and the failure mode when the inference misses is quiet:
findings land in a gitignored directory inside a worktree that is deleted when
the agent is retired, while the manager sent to read them looks somewhere they
never were and finds nothing.

Not a leak risk, and worth recording so nobody re-derives it: an unanchored
`findings/` rule already covers row 2 (`.gitignore:51`, QUM-989), and `.sprawl/*`
covers rows 1 and 3. All three are ignored. This bug is about *findability and
survival*, not exposure.

### Proposed change

Make the path absolute at the point where the absolute path is known, and make
all three strings agree.

The prompt builders do not currently receive a repo root. `EnvConfig` already
carries `WorkDir` (`envContextBlock`, `prompt.go:283-286`), which is the
worktree, not the repo root — so it is **not** the right field and must not be
reused for this. Prefer adding an explicit `StateDir` (or `RepoRoot`) to
`EnvConfig`, populated at the existing call sites, and interpolating it.

Row 1, `prompt_child_sections.go:183`:

```
- For research reports or findings: write to <StateDir>/agents/<name>/findings/ with a descriptive filename.
```

Row 2, `prompt_child_sections.go:213` — change the shape as well as the root, so
QA and researcher artifacts land in one predictable place:

```
6. ... Longer artifacts go in <StateDir>/agents/<name>/findings/.
```

Row 3, `prompt.go:188` and `prompt_child_sections.go:298` — the same absolute
path, so the manager is sent where the other two actually wrote.

If threading a root through turns out to be more invasive than it looks, the
minimum acceptable fix is to state the anchor in the prose of all three strings
— "under the repository root, not your worktree" — which removes the ambiguity
without a signature change. Prefer the absolute path; it removes the inference
rather than documenting it.

### Verification

- **Re-pin all four child-role goldens.** Unlike Bug 1 this touches researcher
  (row 1), QA (row 2), and manager (row 3), and the root prompt via
  `prompt.go:188`. Expect `researcher_tui.golden`, `qa_tui.golden`,
  `manager_tui.golden`, and `golden_tui_claude_code.txt` all to change;
  `engineer_tui.golden` should **not**. Same regeneration command as Bug 1.

- Update the two existing literal-path assertions, which will otherwise fail:
  `prompt_test.go:92` (`".sprawl/agents/birch/findings/"`) and
  `prompt_test.go:520` (`".sprawl/agents/<name>/findings/"`).

- **Add one assertion the old text would fail**: that the researcher, QA, and
  manager prompts all contain the *same* findings path, derived from the same
  value. That is the property — three strings agreeing — rather than three
  independent string checks, which is what let them drift apart.

- Confirm no e2e row is implicated by deriving from the mandatory-test table at
  your commit. `prompt_child_sections.go` **is** named in the table, under
  `subagent-model` — but for `engineer reviewer-spawn prose`. Read the row and
  decide; per CLAUDE.md, when a row's applicability is unclear, include it. If
  you run it and it skips, say so — a skip does not discharge the obligation.
