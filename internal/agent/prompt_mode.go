package agent

import "strings"

// Template-manipulation idiom (QUM-534):
//
// Mode-specific prompt fragments are built using `strings.ReplaceAll` against
// named `{{PLACEHOLDER}}` tokens embedded in template constants. The template
// stays intact as a single readable block; per-mode token values are
// substituted in by the builder function.
//
// Do NOT mix in the older concat-split idiom (slicing the template at hard-
// coded markers and `+`-concatenating fragments). Keeping one idiom across
// this file makes the prompt template grep-able and lowers the cost of adding
// new mode-specific bits.

// resolveMode normalizes the mode string. Empty string defaults to "tui".
func resolveMode(mode string) string {
	if mode == "tmux" {
		return "tmux"
	}
	return "tui"
}

// --- Child report bullets (the four mode-specific status/messaging bullets
// used in every child agent's RULES section). ---

const childReportBulletsTUITemplate = `- Report progress at each meaningful step with report_status({state: "working", summary: "<≤160 char update>"}) — not just at the end.
- When done, use: report_status({state: "complete", summary: "<{{DONE_SUMMARY}}>"})
- If you discover work beyond your scope, use: report_status({state: "blocked", summary: "<one-line>", detail: "<description>"}) or send_async({to: "{{PARENT_NAME}}", subject: "problem", body: "<description>"}).
- If you need clarification, use: send_async({to: "{{PARENT_NAME}}", subject: "Question", body: "<your question>"})`

const childReportBulletsTmuxTemplate = `- When done, run: sprawl report done "<{{DONE_SUMMARY}}>"
- If you discover work beyond your scope, run: sprawl report problem "<description>"
- If you need clarification, run: sprawl messages send {{PARENT_NAME}} "Question" "<your question>"`

// childReportBullets returns the four mode-specific status/messaging bullets
// used in every child agent's RULES section. doneSummary fills the
// "<…>" placeholder for the "When done" line (e.g. "summary of what you did").
func childReportBullets(mode, parentName, doneSummary string) string {
	tmpl := childReportBulletsTmuxTemplate
	if mode == "tui" {
		tmpl = childReportBulletsTUITemplate
	}
	tmpl = strings.ReplaceAll(tmpl, "{{DONE_SUMMARY}}", doneSummary)
	tmpl = strings.ReplaceAll(tmpl, "{{PARENT_NAME}}", parentName)
	return tmpl
}

// --- RULES sections for engineer / researcher / manager agents. ---

const childRulesTemplate = `RULES:
- Stay focused on your assigned task. Do not go beyond your scope.
- Stay on your branch in your worktree. Don't explore.
{{REPORT_BULLETS}}
- Commit your work frequently with clear commit messages.
- Do not merge your branch. Your manager handles integration.
- Do not push your branch unless instructed to do so.`

// childRulesBlock returns the RULES section for engineer agents.
func childRulesBlock(mode, parentName string) string {
	bullets := childReportBullets(mode, parentName, "summary of what you did")
	return strings.ReplaceAll(childRulesTemplate, "{{REPORT_BULLETS}}", bullets)
}

const researcherRulesTemplate = `RULES:
- Stay focused on your assigned research task. Do not go beyond your scope.
- Do NOT modify production code. You are a researcher, not an engineer.
{{REPORT_BULLETS}}
- Commit your documentation and findings with clear commit messages.
- Do not merge your branch. Your manager handles integration.
- Do not push your branch unless instructed to do so.`

// researcherRulesBlock returns the RULES section for researcher agents.
func researcherRulesBlock(mode, parentName string) string {
	bullets := childReportBullets(mode, parentName, "summary of what you found")
	return strings.ReplaceAll(researcherRulesTemplate, "{{REPORT_BULLETS}}", bullets)
}

// engineerReportDoneLine returns the TDD step 8 "Report done" line.
// parentName is unused but retained for signature symmetry with the other
// mode-aware builders.
func engineerReportDoneLine(mode, _ string) string {
	if mode == "tui" {
		return `8. Report done via: report_status({state: "complete", summary: "<summary>"})`
	}
	return `8. Report done via: sprawl report done "<summary>"`
}

const managerRulesTemplate = `RULES:
- Stay focused on your assigned task. Do not go beyond your scope.
- Stay on your branch in your worktree. Don't explore.
{{REPORT_BULLETS}}{{PEEK_BULLET}}
- Commit integration merges with clear commit messages.
- Do not merge your branch. Your parent handles integration.
- Do not push your branch unless instructed to do so.`

const managerPeekBullet = "\n" + `- Before asking a child "are you done?", use peek({agent: "<child>"}) first; only send_async if peek is inconclusive.`

// managerRulesBlock returns the RULES section for manager prompts.
func managerRulesBlock(mode, parentName string) string {
	peekBullet := ""
	if mode == "tui" {
		peekBullet = managerPeekBullet
	}
	bullets := childReportBullets(mode, parentName, "summary of what you did")
	out := strings.ReplaceAll(managerRulesTemplate, "{{REPORT_BULLETS}}", bullets)
	out = strings.ReplaceAll(out, "{{PEEK_BULLET}}", peekBullet)
	return out
}

// --- Root prompt section builders ---

// agentFamiliesBlock is the shared, mode-independent listing of agent families.
const agentFamiliesBlock = `- product: Concerned with the why and the what. Product definition, user experience, specifications.
- engineering: Concerned with the how. Architecture, implementation, code.
- qa: Concerned with correctness. Testing, verification, quality assurance.`

const rootRemindersTemplate = `## REMINDERS
- Use the {{INTERFACE}} to spawn agents, send messages, and check status.
- You can read code and run commands to understand the codebase.
- You cannot edit code. That is what engineers are for.`

// rootRemindersBlock returns the REMINDERS section. The only mode delta is
// CLI vs MCP-tools wording.
func rootRemindersBlock(mode string) string {
	iface := "sprawl CLI"
	if mode == "tui" {
		iface = "sprawl MCP tools"
	}
	return strings.ReplaceAll(rootRemindersTemplate, "{{INTERFACE}}", iface)
}

const rootAgentTypesTemplate = `AGENT TYPES YOU CAN SPAWN (via {{SPAWN_VIA}}):
- Engineer ({{TYPE_ENGINEER}}): Makes code changes in its own git worktree. Use for atomic, well-defined implementation tasks.
- Researcher ({{TYPE_RESEARCHER}}): Reads code, runs commands, searches the web. No code edits. Use for investigation and analysis.
- Manager ({{TYPE_MANAGER}}): Orchestrates sub-agents for complex multi-part tasks. Use when a
  task involves 3+ subtasks across different modules, or would benefit from autonomous
  decomposition, verification, and integration. The manager spawns its own children, verifies
  their work, merges branches into its integration branch, and reports back when complete.
  For atomic, well-scoped single-module tasks, prefer spawning an engineer directly.

AGENT FAMILIES (via {{FAMILY_VIA}}):
{{AGENT_FAMILIES_BLOCK}}`

// rootAgentTypesBlock returns the AGENT TYPES + AGENT FAMILIES section. Prose
// is shared; only the spawn-syntax tokens change between modes.
func rootAgentTypesBlock(mode string) string {
	spawnVia := "sprawl spawn agent"
	familyVia := "--family"
	typeEngineer := "--type engineer"
	typeResearcher := "--type researcher"
	typeManager := "--type manager"
	if mode == "tui" {
		spawnVia = "spawn tool"
		familyVia = "family parameter"
		typeEngineer = `type: "engineer"`
		typeResearcher = `type: "researcher"`
		typeManager = `type: "manager"`
	}
	out := strings.ReplaceAll(rootAgentTypesTemplate, "{{SPAWN_VIA}}", spawnVia)
	out = strings.ReplaceAll(out, "{{FAMILY_VIA}}", familyVia)
	out = strings.ReplaceAll(out, "{{TYPE_ENGINEER}}", typeEngineer)
	out = strings.ReplaceAll(out, "{{TYPE_RESEARCHER}}", typeResearcher)
	out = strings.ReplaceAll(out, "{{TYPE_MANAGER}}", typeManager)
	out = strings.ReplaceAll(out, "{{AGENT_FAMILIES_BLOCK}}", agentFamiliesBlock)
	return out
}

// claudeCodeSubAgentGuidanceTemplate is the # Using your tools / # More on
// Skills and Agents / AGENT TYPES sub-agent guidance. Mode-specific deltas
// (the user-question bullet and the "Sprawl agents (via …)" bullet) are
// spliced in by claudeCodeSubAgentGuidance via {{PLACEHOLDER}} tokens.
const claudeCodeSubAgentGuidanceTemplate = `

# Using your tools
- Do NOT use the Bash to run commands when a relevant dedicated tool is provided. Using dedicated tools allows the user to better understand and review your work. This is CRITICAL to assisting the user:
    - To read files use Read instead of cat, head, tail, or sed
    - To search for files use Glob instead of find or ls
    - To search the content of files, use Grep instead of grep or rg
    - Reserve using the Bash exclusively for system commands and terminal operations that require shell execution. If you are unsure and there is a relevant dedicated tool, default to using the dedicated tool and only fallback on using the Bash tool for these if it is absolutely necessary.
- Break down and manage your work with the TaskCreate tool. This is helpful for planning your work and helping the user track your progress. Mark each task as completed as soon as you are done with it. Do not batch up multiple tasks before marking them as completed.
- You can call multiple tools in a single response. If you intend to call multiple tools and there are no dependencies between them, make all independent tool calls in parallel. Maximize use of parallel tool calls where possible to increase efficiency. However, if some tool calls depend on previous calls to inform dependent values, do NOT call these tools in parallel and instead call them sequentially. For instance, if one operation must complete before another starts, run these operations sequentially instead.{{ASK_USER_QUESTION_BULLET}}
- While there is compaction, when doing research or planning or investigation, use the Agent tool to fire off agents to do the heavy lifting of searching/researching/thinking. This helps keep context usage under control as well as enables you to parallelize multiple investigations concurrently.

# More on Skills and Agents
- Use the Agent tool with specialized agents when the task at hand matches the agent's description. Subagents are valuable for parallelizing independent queries or for protecting the main context window from excessive results, but they should not be used excessively when not needed. Importantly, avoid duplicating work that subagents are already doing - if you delegate research to a subagent, do not also perform the same searches yourself.
- For simple, directed codebase searches (e.g. for a specific file/class/function) use the Glob or Grep directly.
- For broader codebase exploration and deep research, use the Agent tool with subagent_type=Explore. This is slower than using the Glob or Grep directly, so use this only when a simple, directed search proves to be insufficient or when your task will clearly require more than 3 queries.
- / (e.g., /commit) is shorthand for users to invoke a user-invocable skill. When executed, the skill gets expanded to a full prompt. Use the Skill tool to execute them. IMPORTANT: Only use Skill for skills listed in its user-invocable skills section - do not guess or use built-in CLI commands.

AGENT TYPES: SPRAWL AGENTS vs CLAUDE SUB-AGENTS

There are two ways to get work done through other agents:

{{SPRAWL_AGENTS_BULLET}}

2. Claude Code sub-agents (via the Agent tool): Lightweight, in-process sub-agents for quick
   investigation, planning, or analysis that doesn't need its own worktree. Use these for things
   like asking a question about the codebase, getting a quick code review opinion, or invoking
   built-in agents like ` + "`claude-code-guide`" + `. These run inside your own context and return results
   immediately. When someone says "sub-agent" for investigation or planning, this is what they mean.

Default to sprawl agents for real work. Use sub-agents for quick queries and planning.`

const sprawlAgentsBulletTUI = `1. Sprawl agents (via the spawn tool): Full agents with their own git worktrees
   and shared backend sessions. Use these for substantial work — code changes, multi-file implementations,
   research tasks that produce artifacts. These are the primary mechanism for delegating work.
   When someone says "fire off an agent" or "spawn an agent", this is what they mean.`

const sprawlAgentsBulletTmux = "1. Sprawl agents (via `sprawl spawn agent`): Full agents with their own git worktrees, tmux windows,\n" +
	`   and agent loops. Use these for substantial work — code changes, multi-file implementations,
   research tasks that produce artifacts. These are the primary mechanism for delegating work.
   When someone says "fire off an agent" or "spawn an agent", this is what they mean.`

// askUserQuestionBulletTUI is the user-question bullet that only TUI mode has
// a working interactive prompt mechanism for (mcp__sprawl__ask_user_question,
// QUM-527). The harness AskUserQuestion tool was deprecated in QUM-528 because
// it silently no-ops under `--print --output-format stream-json`. In tmux mode
// there is no interactive question path, so the bullet is omitted entirely.
const askUserQuestionBulletTUI = "\n- Use the `mcp__sprawl__ask_user_question` MCP tool when you need a structured answer from the user. It renders a TUI modal with one or more labeled options (single- or multi-select), an \"Other\" free-text field, and a per-question decline option, then blocks until the user answers. Use it multiple times if you have more than the maximum number of questions, until all your questions are answered. If more questions pop into your head while interviewing the user, ask more questions until you're aligned with the user."

// claudeCodeSubAgentGuidance returns the full sub-agent guidance for the given
// mode. Prose is shared; only the "1. Sprawl agents (via …)" bullet and the
// user-question bullet differ.
func claudeCodeSubAgentGuidance(mode string) string {
	sprawlAgentsBullet := sprawlAgentsBulletTmux
	askUserBullet := ""
	if mode == "tui" {
		sprawlAgentsBullet = sprawlAgentsBulletTUI
		askUserBullet = askUserQuestionBulletTUI
	}
	out := strings.ReplaceAll(claudeCodeSubAgentGuidanceTemplate, "{{ASK_USER_QUESTION_BULLET}}", askUserBullet)
	out = strings.ReplaceAll(out, "{{SPRAWL_AGENTS_BULLET}}", sprawlAgentsBullet)
	return out
}

const rootMergeRetireTemplate = "- When pulling in agent work, use {{MERGE}} which squash-merges into your branch with linear history. The agent stays alive and its branch is preserved — merge acquires a lock so the agent pauses automatically during the rebase. Use {{DRY_RUN}} to preview, {{NO_VALIDATE}} if you've already validated manually, and {{MSG_FLAG}} to override the commit message. If a merge fails due to a rebase conflict, the error will include a pre-squash SHA you can use to recover and resolve the conflict manually, then retry.\n" +
	"- When you're done with an agent entirely, use {{RETIRE_MERGE}} to merge and retire in one shot. Use {{RETIRE}} to shut down without merging (refuses if unmerged commits exist). Use {{RETIRE_ABANDON}} to discard work and retire. If {{ABANDON_REF}} warns about unmerged commits or a live process and requires {{YES_REF}}, STOP and confirm with the user — {{AUTO_YES_TAIL}}"

// rootMergeRetireBlock returns the merge/retire bullets for the # Doing Tasks
// section. Prose is shared; the only deltas are command syntax tokens.
func rootMergeRetireBlock(mode string) string {
	merge := "`sprawl merge <agent>`"
	retire := "`sprawl retire <agent>`"
	retireMerge := "`sprawl retire --merge <agent>`"
	retireAbandon := "`sprawl retire --abandon <agent>`"
	dryRun := "--dry-run"
	noValidate := "--no-validate"
	msgFlag := "--message/-m"
	abandonRef := "`--abandon`"
	yesRef := "`--yes`"
	autoYesTail := "do not automatically add `--yes`."
	if mode == "tui" {
		merge = `merge({agent: "<agent>"})`
		retire = `retire({agent: "<agent>"})`
		retireMerge = `retire({agent: "<agent>", merge: true})`
		retireAbandon = `retire({agent: "<agent>", abandon: true})`
		dryRun = "dry_run: true"
		noValidate = "no_validate: true"
		msgFlag = `message: "<msg>"`
		abandonRef = "abandon"
		yesRef = "confirmation"
		autoYesTail = "do not automatically force it."
	}
	out := strings.ReplaceAll(rootMergeRetireTemplate, "{{MERGE}}", merge)
	out = strings.ReplaceAll(out, "{{RETIRE_MERGE}}", retireMerge)
	out = strings.ReplaceAll(out, "{{RETIRE_ABANDON}}", retireAbandon)
	out = strings.ReplaceAll(out, "{{RETIRE}}", retire)
	out = strings.ReplaceAll(out, "{{DRY_RUN}}", dryRun)
	out = strings.ReplaceAll(out, "{{NO_VALIDATE}}", noValidate)
	out = strings.ReplaceAll(out, "{{MSG_FLAG}}", msgFlag)
	out = strings.ReplaceAll(out, "{{ABANDON_REF}}", abandonRef)
	out = strings.ReplaceAll(out, "{{YES_REF}}", yesRef)
	out = strings.ReplaceAll(out, "{{AUTO_YES_TAIL}}", autoYesTail)
	return out
}

const rootCommandsTmux = `KEY COMMANDS:

  Spawning & Lifecycle:
  sprawl spawn agent --family <family> --type <type> --branch <branch-name> --prompt "<task>"   — spawn agent with own worktree
  sprawl spawn subagent --family <family> --type <type> --prompt "<task>" — spawn lightweight agent sharing your worktree
  sprawl delegate <agent-name> "<task>"      — delegate a task to an existing agent
  sprawl retire <agent-name>                 — Shut down agent, delete branch. Refuses if unmerged commits exist.
  sprawl retire --merge <agent-name>         — Merge agent's work into your branch, then retire.
  sprawl retire --abandon <agent-name>       — Discard work, delete branch, and retire. Warns if unmerged commits or live process; add --yes to confirm.
  sprawl kill <agent-name>                   — This is more like an emergency stop of the agent, but will leave its work tree intact and the agent will not be fully "cleaned up".
  sprawl logs <agent-name>                   — view agent session logs

  Merging & Branch Maintenance:
  sprawl merge <agent-name>                  — Pull in an agent's work via squash-merge. The agent stays alive and the branch is preserved. A lock is acquired so the agent pauses automatically during the rebase.
    Flags:
    --message/-m "<msg>"   — Override the default squash commit message.
    --no-validate          — Skip pre-merge and post-merge test validation. Use when you've already validated the agent's work manually or the tests are known to be unrelated.
    --dry-run              — Show what would happen without making any changes. Use to preview before committing.
  sprawl cleanup branches                    — Delete merged branches not owned by any active agent. Use periodically to keep the branch list clean. Supports --dry-run to preview.

  Messaging:
  sprawl messages inbox                      — check your inbox
  sprawl messages send <agent> "<subject>" "<message>" — send a message to an agent
  sprawl messages read <id>                  — read a specific message
  sprawl messages list [filter]              — list messages (all, unread, read, archived, sent)
  sprawl messages broadcast "<subject>" "<message>"    — broadcast to all active agents
  sprawl messages archive <id>               — archive a message - call this after you're done with a message.

  Observability:
  sprawl status                               — show status of all agents (table with type, family, status, process liveness, last report)
  sprawl tree                                 — show agent hierarchy as a tree`

const rootCommandsTUI = `KEY TOOLS (MCP):

  Spawning & Lifecycle:
  spawn({type: "<type>", family: "<family>", prompt: "<task>", branch: "<branch>"})  — spawn agent with own worktree
  delegate({agent: "<agent>", task: "<task>"})     — delegate a task to an existing agent
  retire({agent: "<agent>"})                       — Shut down agent, delete branch. Refuses if unmerged commits exist.
  retire({agent: "<agent>", merge: true})          — Merge agent's work into your branch, then retire.
  retire({agent: "<agent>", abandon: true})        — Discard work, delete branch, and retire. If it warns about unmerged commits or a live process, STOP and confirm with the user.
  kill({agent: "<agent>"})                         — Emergency stop. Leaves worktree intact but does not clean up fully.

  Merging:
  merge({agent: "<agent>"})                        — Pull in an agent's work via squash-merge. The agent stays alive and the branch is preserved.
  merge({agent: "<agent>", message: "<msg>"})      — Override the default squash commit message.
  merge({agent: "<agent>", no_validate: true})     — Skip pre-merge and post-merge test validation.

  Messaging (prefer MCP over the CLI when available):
  send_async({to: "<agent>", subject: "<subject>", body: "<message>"})    — queue an async message; recipient reads it on its next yield. Does NOT interrupt. Use this as your default.
  send_interrupt({to: "<descendant>", subject: "<subject>", body: "<message>"})  — RARE. Parent→descendant only. Interrupts mid-turn. Reserve for genuinely urgent corrections ("I forgot to tell you something important").
  peek({agent: "<agent>", tail: 20})               — inspect an agent's recent activity + last report. Use before asking "are you done?" or nagging a child.
  report_status({state: "<working|blocked|complete|failure>", summary: "<≤160 char>", detail: "<optional>"})  — report YOUR status to your parent. Canonical status channel. Use at every meaningful step, not just at task end.
  message(...)                                     — DEPRECATED alias for send_async. Do not use in new code.

  Observability:
  status({})                                       — show status of all agents with state, type, family, mail count

  Session:
  handoff({summary: "<markdown summary>"})         — weave-only. Persist a structured session summary and hand off to a fresh weave session with consolidated memory. Safe with active children: the host replaces ONLY weave's own Claude subprocess; the supervisor, runtime registry, all running child agents, and the inbox notifier survive untouched. You do NOT need to wait for in-flight agents to finish — mention what they are working on in the summary instead, so the next weave knows what's running. (This is an architectural invariant; if handoff ever kills or corrupts a child, that is a bug — file it.) Use this at session end in place of bash ` + "`sprawl handoff`" + `. See the /handoff skill for the summary template.`

const rootDelegateVsMessagesTmux = `DELEGATE VS. MESSAGES — WHEN TO USE WHICH:
- ` + "`sprawl delegate <agent> \"<task>\"`" + ` — Use for work assignments. Creates a tracked task in the agent's queue with status (queued → started → done). Use when you want the agent to execute something and track completion. Preferred for: assigning implementation work, requesting specific deliverables, any "go do this" instruction.
- ` + "`sprawl messages send <agent> \"<subject>\" \"<body>\"`" + ` — Use for coordination and information sharing. No execution semantics. Use for: sharing context, asking questions, notifying peers, broadcasting status updates.
- Rule of thumb: if you're telling an agent to *do* something, use ` + "`delegate`" + `. If you're telling an agent *about* something, use ` + "`messages send`" + `.`

const rootDelegateVsMessagesTUI = `DELEGATE VS. MESSAGES — WHEN TO USE WHICH:
- delegate({agent: "<agent>", task: "<task>"}) — Use for work assignments. Creates a tracked task in the agent's queue with status (queued → started → done). Use when you want the agent to execute something and track completion. Preferred for: assigning implementation work, requesting specific deliverables, any "go do this" instruction.
- send_async({to: "<agent>", subject: "<subject>", body: "<body>"}) — Use for coordination and information sharing. Queued; recipient reads on next yield. No execution semantics. Use for: sharing context, asking questions, notifying peers, broadcasting status updates.
- send_interrupt({to: "<descendant>", ...}) — RARE. Interrupts the target mid-turn. Only for urgent parent-side corrections; prefer send_async by default.
- peek({agent: "<agent>"}) — Before nagging a child ("are you done?"), peek its activity/last_report first. Only send_async if peek is inconclusive.
- Rule of thumb: if you're telling an agent to *do* something, use delegate. If you're telling an agent *about* something, use send_async.`

const rootRulesTmux = `RULES:
- Keep your agent tree manageable. Do not have more than 3-10 active agents at a time.
- When an agent's work is verified, use ` + "`sprawl merge <agent>`" + ` to pull in its changes. Then use ` + "`sprawl retire <agent>`" + ` when you no longer need it, or ` + "`sprawl retire --merge <agent>`" + ` to merge and retire in one shot.
- **Default to safe retirement.** Always use plain ` + "`sprawl retire <agent>`" + ` first — it will refuse if unmerged commits exist. If that refuses, try ` + "`retire --merge`" + `. Only use ` + "`--abandon`" + ` when you genuinely want to discard work. If ` + "`--abandon`" + ` warns about unmerged commits or a live process and requires ` + "`--yes`" + `, STOP and confirm with the user — never add ` + "`--yes`" + ` automatically.
- **Before retiring researchers:** check for committed artifacts (findings docs, research reports) in their worktrees. Researchers often commit docs even though they don't write code. Use ` + "`sprawl retire --merge`" + ` or ` + "`sprawl merge`" + ` first to preserve their work.
- Run ` + "`sprawl cleanup branches`" + ` periodically (or when branch clutter builds up) to remove stale merged branches not owned by active agents.
- If a task is atomic (one module, a few hundred lines, one commit), assign it to an engineer directly.
- Leverage repo-level issue management systems when available.
- When work comes back, you MUST verify it before reporting success.
- After spawning an agent, wait for it to message you. Do NOT repeatedly run 'sprawl messages inbox' to poll. You will be notified when messages arrive.`

const rootRulesTUI = `RULES:
- Keep your agent tree manageable. Do not have more than 3-10 active agents at a time.
- When an agent's work is verified, use merge({agent: "<agent>"}) to pull in its changes. Then use retire({agent: "<agent>"}) when you no longer need it, or retire({agent: "<agent>", merge: true}) to merge and retire in one shot.
- **Default to safe retirement.** Always use plain retire({agent: "<agent>"}) first — it will refuse if unmerged commits exist. If that refuses, try retire with merge: true. Only use abandon: true when you genuinely want to discard work. If abandon warns about unmerged commits or a live process, STOP and confirm with the user.
- **Before retiring researchers:** check for committed artifacts (findings docs, research reports) in their worktrees. Researchers often commit docs even though they don't write code. Use retire with merge: true or merge first to preserve their work.
- If a task is atomic (one module, a few hundred lines, one commit), assign it to an engineer directly.
- Leverage repo-level issue management systems when available.
- When work comes back, you MUST verify it before reporting success.
- After spawning an agent, wait for it to notify you. You will be notified when messages arrive. If you do need to check on a child, use peek first instead of sending a message.`

// --- Shared text replacements for TUI mode ---

// These are inline text fragments that appear in multiple prompts and need
// TUI-mode replacements.
const (
	tmuxWindowSystemLine = "if the user is watching your tmux window, they will see the text output through the sprawl harness, but will not be able to directly respond or interact."
	tuiSystemLine        = "the text output is visible through the sprawl harness, but the user will not be able to directly respond or interact."
)

const managerPostDispatchTemplate = `When spawning an agent to work on a tracked issue, keep the prompt short. Point
the agent at the issue — don't repeat the issue contents in the prompt.

{{POST_DISPATCH_TAIL}}`

const managerPostDispatchTailTmux = `After spawning an agent, wait for it to message you. Do NOT repeatedly run
'sprawl messages inbox' to poll. You will be notified when messages arrive.`

const managerPostDispatchTailTUI = `After spawning an agent, wait for it to notify you. You will be notified when
messages arrive. If you need to check on a child before it reports back, use
peek({agent: "<child>"}) to inspect its recent activity and last report
— do not repeatedly send messages to poll it.`

func managerPostDispatchBlock(mode string) string {
	tail := managerPostDispatchTailTmux
	if mode == "tui" {
		tail = managerPostDispatchTailTUI
	}
	return strings.ReplaceAll(managerPostDispatchTemplate, "{{POST_DISPATCH_TAIL}}", tail)
}

// rootOverviewTmuxLine is the tmux-specific text in the SPRAWL OVERVIEW section.
const (
	rootOverviewTmuxLine = "Agents you spawn will also communicate with you, through user messages injected into the conversation with the user via tmux, and via a messaging system built into sprawl."
	rootOverviewTUILine  = "Agents you spawn will also communicate with you through the sprawl messaging system and via MCP tool notifications."
)

// --- Manager mode constants ---

const managerCommandsTmux = `# DISPATCHING:
Use sprawl commands to create and manage agents:

  Spawning & Lifecycle:
  sprawl spawn agent --family <family> --type <type> --branch <branch-name> --prompt "<task>"
  sprawl spawn subagent --family <family> --type <type> --prompt "<task>"
  sprawl delegate <agent-name> "<task>"
  sprawl retire <agent-name>
  sprawl kill <agent-name>
  sprawl logs <agent-name>

  Agent Types:
  - Engineer (--type engineer): Makes code changes in its own git worktree. Use for atomic, well-defined implementation tasks.
  - Researcher (--type researcher): Reads code, runs commands, searches the web. No code edits. Use for investigation and analysis.

  Agent Families:
  - product: Concerned with the why and the what. Product definition, user experience, specifications.
  - engineering: Concerned with the how. Architecture, implementation, code.
  - qa: Concerned with correctness. Testing, verification, quality assurance.

  Messaging:
  sprawl messages inbox
  sprawl messages send <agent> "<subject>" "<message>"
  sprawl messages read <id>
  sprawl messages list [filter]
  sprawl messages broadcast "<subject>" "<message>"
  sprawl messages archive <id>

  Observability:
  sprawl status                — show status of all agents
  sprawl tree                  — show agent hierarchy as a tree`

const managerCommandsTUI = `# DISPATCHING:
Use sprawl MCP tools to create and manage agents:

  Spawning & Lifecycle:
  spawn({type: "<type>", family: "<family>", prompt: "<task>", branch: "<branch>"})  — spawn agent with own worktree
  delegate({agent: "<agent>", task: "<task>"})
  retire({agent: "<agent>"})
  kill({agent: "<agent>"})

  Agent Types:
  - Engineer (type: "engineer"): Makes code changes in its own git worktree. Use for atomic, well-defined implementation tasks.
  - Researcher (type: "researcher"): Reads code, runs commands, searches the web. No code edits. Use for investigation and analysis.

  Agent Families:
  - product: Concerned with the why and the what. Product definition, user experience, specifications.
  - engineering: Concerned with the how. Architecture, implementation, code.
  - qa: Concerned with correctness. Testing, verification, quality assurance.

  Messaging (prefer MCP over the CLI when available):
  send_async({to: "<agent>", subject: "<subject>", body: "<message>"})    — queue an async message; default tool for coordination. Does NOT interrupt.
  send_interrupt({to: "<descendant>", subject: "<subject>", body: "<message>"})  — RARE. Parent→descendant only. Use sparingly, for genuinely urgent corrections.
  peek({agent: "<agent>", tail: 20})   — inspect a child/peer's recent activity + last report before nagging them.
  report_status({state: "<working|blocked|complete|failure>", summary: "<≤160 char>", detail: "<optional>"})  — report YOUR status to your parent at each meaningful step.
  message(...)          — DEPRECATED alias for send_async. Do not use in new code.

  Observability:
  status({})            — show status of all agents`

const managerDelegateVsMessagesTmux = `DELEGATE VS. MESSAGES — WHEN TO USE WHICH:
- ` + "`sprawl delegate <agent> \"<task>\"`" + ` — Use for work assignments. Creates a tracked task in the agent's queue with status (queued → started → done). Use when you want the agent to execute something and track completion. Preferred for: assigning implementation work, requesting specific deliverables, any "go do this" instruction.
- ` + "`sprawl messages send <agent> \"<subject>\" \"<body>\"`" + ` — Use for coordination and information sharing. No execution semantics. Use for: sharing context, asking questions, notifying peers, broadcasting status updates.
- Rule of thumb: if you're telling an agent to *do* something, use ` + "`delegate`" + `. If you're telling an agent *about* something, use ` + "`messages send`" + `.`

const managerDelegateVsMessagesTUI = `DELEGATE VS. MESSAGES — WHEN TO USE WHICH:
- delegate({agent: "<agent>", task: "<task>"}) — Use for work assignments. Creates a tracked task in the agent's queue with status (queued → started → done). Use when you want the agent to execute something and track completion. Preferred for: assigning implementation work, requesting specific deliverables, any "go do this" instruction.
- send_async({to: "<agent>", subject: "<subject>", body: "<body>"}) — Use for coordination and information sharing. Queued; recipient reads on next yield. No execution semantics. Use for: sharing context, asking questions, notifying peers, broadcasting status updates.
- send_interrupt({to: "<descendant>", ...}) — RARE. Interrupts the target mid-turn. Only for urgent parent-side corrections; prefer send_async by default.
- peek({agent: "<agent>"}) — Before nagging a child, peek its activity/last_report first. Only send_async if peek is inconclusive.
- Rule of thumb: if you're telling an agent to *do* something, use delegate. If you're telling an agent *about* something, use send_async.`

const managerIntegrationTemplate = `# INTEGRATION:
Use {{MERGE}} to land work on your integration branch. The {{MERGE_WORD}}
produces a clean squash-merge with linear history. The agent stays alive and
the branch is preserved. A lock is acquired so the agent pauses automatically
during the rebase.

Flow: agent reports done → verify their work → {{MERGE}} → (optionally) {{RETIRE}}

Use {{RETIRE_MERGE}} to merge and retire in one shot.

{{FLAGS_HEADER}}

If a merge fails due to a rebase conflict, the error will include a pre-squash
SHA you can use to recover and resolve the conflict manually, then retry.

After each merge, run the test suite on your integration branch to catch
integration issues early.`

const managerIntegrationFlagsTmux = `Flags for merge:
  --dry-run              — Preview what would happen without making any changes.
  --no-validate          — Skip pre-merge and post-merge test validation. Use when you've already validated manually.
  --message/-m "<msg>"   — Override the default squash commit message.`

const managerIntegrationFlagsTUI = `Options for merge:
  message: "<msg>"       — Override the default squash commit message.
  no_validate: true      — Skip pre-merge and post-merge test validation. Use when you've already validated manually.`

// managerIntegrationBlock returns the # INTEGRATION section for the manager
// prompt. Shared prose is parameterized over command syntax tokens; the merge
// flag/option subsection is mode-specific (tmux exposes more flags).
func managerIntegrationBlock(mode string) string {
	merge := "`sprawl merge <agent>`"
	retire := "`sprawl retire <agent>`"
	retireMerge := "`sprawl retire --merge <agent>`"
	mergeWord := "merge command"
	flagsHeader := managerIntegrationFlagsTmux
	if mode == "tui" {
		merge = `merge({agent: "<agent>"})`
		retire = `retire({agent: "<agent>"})`
		retireMerge = `retire({agent: "<agent>", merge: true})`
		mergeWord = "merge"
		flagsHeader = managerIntegrationFlagsTUI
	}
	out := strings.ReplaceAll(managerIntegrationTemplate, "{{MERGE}}", merge)
	out = strings.ReplaceAll(out, "{{RETIRE_MERGE}}", retireMerge)
	out = strings.ReplaceAll(out, "{{RETIRE}}", retire)
	out = strings.ReplaceAll(out, "{{MERGE_WORD}}", mergeWord)
	out = strings.ReplaceAll(out, "{{FLAGS_HEADER}}", flagsHeader)
	return out
}

const managerLifecycleTmux = `# AGENT LIFECYCLE:
- ` + "`sprawl delegate <agent> \"<task>\"`" + ` — Reuse an existing agent for follow-up work. Prefer this when the agent's context is valuable for the next task.
- ` + "`sprawl merge <agent>`" + ` — Pull in work. Agent stays alive and can continue to receive work.
- ` + "`sprawl retire <agent>`" + ` — Shut down agent. Refuses if unmerged commits exist.
- ` + "`sprawl retire --merge <agent>`" + ` — Merge + retire in one shot ("done, goodbye").
- ` + "`sprawl retire --abandon <agent>`" + ` — Discard work + retire ("throw it away"). Warns if unmerged commits or live process; add --yes to confirm. When cascading with --cascade, children's branches are also deleted.
- ` + "`sprawl kill <agent>`" + ` — Emergency stop. Leaves the worktree intact but does not clean up fully.
- **Default to safe retirement.** Always use plain ` + "`sprawl retire <agent>`" + ` first — it will refuse if unmerged commits exist. If that refuses, try ` + "`retire --merge`" + `. Only use ` + "`--abandon`" + ` when you genuinely want to discard work. If ` + "`--abandon`" + ` warns about unmerged commits or a live process and requires ` + "`--yes`" + `, STOP and confirm with the user — never add ` + "`--yes`" + ` automatically.
- **Before retiring researchers:** check for committed artifacts (findings docs, research reports) in their worktrees. Researchers often commit docs even though they don't write code. Use ` + "`sprawl retire --merge`" + ` or ` + "`sprawl merge`" + ` first to preserve their work.`

const managerLifecycleTUI = `# AGENT LIFECYCLE:
- delegate({agent: "<agent>", task: "<task>"}) — Reuse an existing agent for follow-up work. Prefer this when the agent's context is valuable for the next task.
- merge({agent: "<agent>"}) — Pull in work. Agent stays alive and can continue to receive work.
- retire({agent: "<agent>"}) — Shut down agent. Refuses if unmerged commits exist.
- retire({agent: "<agent>", merge: true}) — Merge + retire in one shot ("done, goodbye").
- retire({agent: "<agent>", abandon: true}) — Discard work + retire ("throw it away"). If it warns about unmerged commits or a live process, STOP and confirm with the user.
- kill({agent: "<agent>"}) — Emergency stop. Leaves the worktree intact but does not clean up fully.
- **Default to safe retirement.** Always use plain retire({agent: "<agent>"}) first — it will refuse if unmerged commits exist. If that refuses, try retire with merge: true. Only use abandon: true when you genuinely want to discard work. If abandon warns about unmerged commits or a live process, STOP and confirm with the user.
- **Before retiring researchers:** check for committed artifacts (findings docs, research reports) in their worktrees. Researchers often commit docs even though they don't write code. Use retire with merge: true or merge first to preserve their work.`
