package sprawlmcp

import (
	"os"

	"github.com/dmotles/sprawl/internal/rootinit"
)

// testToolsEnv is the environment variable that gates internal test-only MCP
// tools (today: `_test_sleep` for the QUM-552 sandbox repro). It is checked
// once per toolDefinitions / dispatchTool call to keep the tool surface
// dynamic in the same process. NEVER set this in production.
const testToolsEnv = "SPRAWL_ENABLE_TEST_TOOLS"

// testToolsEnabled reports whether SPRAWL_ENABLE_TEST_TOOLS=1 is set in the
// current process environment.
func testToolsEnabled() bool {
	return os.Getenv(testToolsEnv) == "1"
}

// toolDefinitions returns the MCP tool definitions for the sprawl MCP server.
// When testToolsEnabled() is true, internal `_test_*` tools are appended.
func toolDefinitions() []map[string]any {
	defs := baseToolDefinitions()
	if testToolsEnabled() {
		defs = append(defs, testToolDefinitions()...)
	}
	// QUM-606: append build-tag-gated test-only tools (`_test_induce_wedge`).
	// injectToolDefinitions returns nil in non-`sprawl_test` builds, so the
	// production tool surface is unaffected.
	defs = append(defs, injectToolDefinitions()...)
	return defs
}

// testToolDefinitions returns the internal `_test_*` MCP tools that are only
// exposed when SPRAWL_ENABLE_TEST_TOOLS=1. These exist for sandbox repros
// (today: QUM-552 interrupt-during-MCP-tool-wait) and MUST NOT be enabled
// outside of sandbox / e2e environments.
func testToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "_test_sleep",
			"description": "Internal test-only tool (gated by SPRAWL_ENABLE_TEST_TOOLS=1). Sleeps for `seconds` seconds, respecting context cancellation. Exists for the QUM-552 sandbox repro of interrupt-during-MCP-tool-wait. DO NOT use in production agent prompts.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"seconds": map[string]any{
						"type":        "integer",
						"description": "Seconds to sleep. Clamped to [0, 60].",
					},
				},
				"required": []string{"seconds"},
			},
		},
	}
}

// baseToolDefinitions returns the canonical MCP tool definitions always
// exposed to claude. testToolDefinitions are appended dynamically when
// SPRAWL_ENABLE_TEST_TOOLS=1.
func baseToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "spawn",
			"description": "Create a new worktree-backed child agent under the current sprawl enter session. The child starts immediately and can receive tasks via delegate or messages via send_message. The new worktree is based on the caller's current worktree HEAD (committed only — uncommitted changes do NOT propagate). Commit before spawning if you want the child to see in-flight work.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"family": map[string]any{
						"type":        "string",
						"description": "Agent family: engineering, product, or qa",
						"enum":        []string{"engineering", "product", "qa"},
					},
					"type": map[string]any{
						"type":        "string",
						"description": "Agent type: engineer, researcher, manager, or qa",
						"enum":        []string{"engineer", "researcher", "manager", "qa"},
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Task description for the agent",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "Git branch name for the agent's worktree",
					},
					"subagent": map[string]any{
						"type":        "boolean",
						"description": "If true, the child shares the caller's worktree and branch instead of getting its own. `branch` must be omitted. Subject to depth limit (3) and capability gate (caller must be manager/engineer/researcher/qa).",
					},
					"model": map[string]any{
						"type": "string",
						"enum": rootinit.ValidSpawnModels,
						"description": "Optional. Model tier for the spawned agent. Omit to use the type default (manager→opus[1m], engineer/researcher/qa→opus). Guidance: " +
							"`haiku` = cheap/fast, straightforward work only (classify, summarize; mid tool-calling); " +
							"`sonnet` = solid, fine for easy tasks; " +
							"`opus` = default smart general-purpose; " +
							"`fable` = savant, only for hard research/planning needing deep thinking & analysis (expensive); " +
							"`opus[1m]`/`sonnet[1m]` = same tiers with 1M context, use ONLY when a very large context window is genuinely needed.",
					},
					"system_prompt": map[string]any{
						"type":        "string",
						"description": "Optional. Custom instructions, identity, personality, and operating mantra for this agent. APPENDED to the agent's built-in role prompt under a delimited '## Operator Instructions' header — it does not replace it.",
					},
				},
				"required": []string{"family", "type", "prompt"},
			},
		},
		{
			"name":        "status",
			"description": "List all agents with their current state, type, family, and branch. Each entry includes `subprocess_alive` (whether a live claude handle is attached) and `eventbus_subscribed` (whether the agent's per-runtime EventBus has any live subscribers); both are false in steady state for terminal-Status agents (stopped/faulted/killed/retired). `eventbus_sub_count` reports the exact subscriber count when nonzero. Does NOT wake the agent.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "delegate",
			"description": "Assign a tracked work item (task) to an existing agent. The task is queued and the agent picks it up when ready. Agents that reported `complete` are revivable — delegate AUTO-WAKES them; no flag required. Delegating to a `retired`/`retiring` agent returns a terminal-agent error.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Name of the target agent",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "Task description to delegate",
					},
					"wake_if_offline": map[string]any{
						"type":        "boolean",
						"description": "Required to revive paused/killed/died/faulted/resume_failed agents — wakes the target first and delivers this task as its first inbox item. NOT required for `complete` agents (auto-revives). Default false (delivery errors with an actionable message if target is in one of the offline fault classes and the flag is unset).",
					},
				},
				"required": []string{"agent", "task"},
			},
		},
		{
			"name":        "send_message",
			"description": "The preferred channel for substantive communication with another agent (parent, peer, or descendant). Use this for questions, context-sharing, findings, hand-offs, and anything you want to be retrievable later via `messages_read`. Durable: the message lands in the recipient's inbox, increments their unread count, and can be listed/read/archived. Default is async (interrupt=false) — strictly cooperative: the message is enqueued at the recipient's queue and delivered at the next turn boundary; preserves the recipient's prompt cache. Set interrupt=true ONLY for genuinely urgent corrections that must preempt the recipient mid-turn (parent→descendant only, rare). interrupt=true jumps to the front of the recipient's queue AND requests preemption — honored immediately when the recipient is streaming or thinking; if the recipient is currently awaiting a sprawl-side MCP tool response, the interrupt JSON is written to claude's stdin but its effect is not observable until that MCP call returns (QUM-549). For hard recovery from a wedged MCP call, use kill. For routine status updates (\"started X\", \"finished X\", \"blocked on Y\"), use `report_status` instead — it's lighter weight and doesn't clutter the recipient's inbox. The first line of `body` serves as the subject-equivalent in the recipient's inbox.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":   map[string]any{"type": "string", "description": "Target agent name"},
					"body": map[string]any{"type": "string", "description": "Markdown body. The first line is rendered as the subject-equivalent in inbox/banner displays."},
					"interrupt": map[string]any{
						"type":        "boolean",
						"description": "Default false. true = jump the recipient's queue and request preemption. Best-effort during MCP-tool-waits: honored for streaming/thinking but observable only after the wait returns. Use `kill` to hard-recover a wedged MCP call. See QUM-549.",
					},
					"wake_if_offline": map[string]any{
						"type":        "boolean",
						"description": "Required to revive paused/killed/died/faulted/resume_failed agents — wakes the target first and delivers this message as its first inbox item. NOT required for `complete` agents (auto-revives). Default false (delivery errors with an actionable message if target is in one of the offline fault classes and the flag is unset).",
					},
				},
				"required": []string{"to", "body"},
			},
		},
		{
			"name":        "peek",
			"description": "Inspect a child or peer agent's recent activity. Returns the agent's status, its last report, the last N protocol events (tool calls, text, results), and `in_turn` (true when the target's backend session is mid-turn; `in_autonomous_turn` is emitted as a deprecated alias for one release — QUM-692). Use to answer \"what is this agent doing?\" before sending a message. Does NOT wake the agent. Peek is introspectable for `complete` and the offline fault classes (paused/killed/died/faulted/resume_failed); only `retired`/`retiring` short-circuit to a terminal-agent error.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Target agent name",
					},
					"tail": map[string]any{
						"type":        "integer",
						"description": "Activity entries to return (default 20, max 200)",
					},
				},
				"required": []string{"agent"},
			},
		},
		{
			"name":        "report_status",
			"description": "Updates this agent's global state (what you're working on, blocked status, completion, failures). Visible to the parent and surfaced in the `status` / `peek` tools. REQUIRED: call when you start a task, when you complete it, and when you hit blockers or failures — use at every meaningful step, not just at task end. The parent is notified asynchronously; this never preempts them. This is NOT a message: it does NOT appear in the parent's inbox, does NOT increment unread counts, and CANNOT be read back later via `messages_read` / `messages_list`. The notification is ephemeral — only the latest state+summary is persisted to the agent's state on disk. Use `send_message` for anything you need to convey in detail or for anything you want the parent to be able to retrieve later.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"state": map[string]any{
						"type":        "string",
						"description": "Current work state",
						"enum":        []string{"working", "blocked", "complete", "failure"},
					},
					"summary": map[string]any{
						"type":        "string",
						"description": "≤160 char one-line summary (coder_report_task-compatible)",
					},
				},
				"required": []string{"state", "summary"},
			},
		},
		{
			"name":        "merge",
			"description": "Squash-merge an agent's branch into the current branch. The agent is NOT retired — it stays alive and can continue working. Acquires a per-sprawl-root lock; concurrent merges queue and run sequentially.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Name of the agent whose branch to merge",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Override the squash commit message",
					},
					"no_validate": map[string]any{
						"type":        "boolean",
						"description": "Skip post-merge test validation",
					},
				},
				"required": []string{"agent"},
			},
		},
		{
			"name":        "retire",
			"description": "Shut down an agent. Optionally merge its work first, abandon (delete) its branch, or cascade through descendants. Default refuses if the agent has unmerged commits or active children; pass `merge`, `abandon`, or `cascade` to override.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Name of the agent to retire",
					},
					"merge": map[string]any{
						"type":        "boolean",
						"description": "Squash-merge the agent's work into the caller's branch before retiring. Mutually exclusive with abandon.",
					},
					"abandon": map[string]any{
						"type":        "boolean",
						"description": "Discard the agent's work and delete its branch, even if commits are unmerged. Mutually exclusive with merge.",
					},
					"cascade": map[string]any{
						"type":        "boolean",
						"description": "If the agent has descendants, retire them bottom-up applying the same flags. Without this, retire refuses when children exist.",
					},
					"validate": map[string]any{
						"type":        "boolean",
						"description": "Run project validate after merge (default true). Only meaningful with merge=true.",
					},
				},
				"required": []string{"agent"},
			},
		},
		{
			"name":        "handoff",
			"description": "Weave-only: persist a structured session summary and hand off to a fresh weave session. The host tears down the current subprocess and starts a new one with consolidated memory. Call this at the end of a session. See the /handoff skill for the summary template.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{
						"type":        "string",
						"description": "Structured session summary (markdown). See the /handoff skill for the template.",
					},
				},
				"required": []string{"summary"},
			},
		},
		{
			"name":        "messages_list",
			"description": "List durable messages in the caller's mailbox (messages sent via `send_message`). Scoped to the caller's agent identity — cannot read other agents' mailboxes. Returns newest-first summaries (id, from, subject, timestamp, read-state). `report_status` updates from children do NOT appear here — they are ephemeral state notifications, not messages; use `status` or `peek` to inspect them. Example: {\"filter\":\"unread\",\"limit\":20}.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":        "string",
						"description": "Mailbox filter. Default \"all\" (new+cur). \"status\" surfaces only status_change envelopes (hidden from default views).",
						"enum":        []string{"all", "unread", "read", "archived", "status"},
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max messages to return (newest-first). 0 or omitted = no limit; clamped to 500.",
					},
				},
			},
		},
		{
			"name":        "messages_read",
			"description": "Fetch the full body of a durable message by ID (short or long prefix). Auto-marks the message read if it was unread. Scoped to the caller's mailbox. Only messages sent via `send_message` are retrievable here — `report_status` updates are not messages and cannot be read back; use `status` or `peek` to see a child's latest reported state. Example: {\"id\":\"abc\"}.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Message ID (short ID preferred; long-ID prefix accepted).",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			"name":        "messages_archive",
			"description": "Archive messages. Pass {\"id\":\"abc\"} to archive a single message, or {\"all\":true} to archive all messages in the mailbox (new + read). Scoped to the caller's mailbox.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Message ID to archive. Required unless 'all' is true.",
					},
					"all": map[string]any{
						"type":        "boolean",
						"description": "Archive all messages (new + read). Ignores 'id' when true.",
					},
				},
			},
		},
		{
			"name":        "messages_peek",
			"description": "Cheap \"do I have mail?\" probe — returns the caller's unread count and up to 5 newest-first preview summaries. Counts only durable messages (sent via `send_message`); `report_status` updates do not contribute to unread. Scoped to the caller's mailbox. Takes no arguments.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "ask_user_question",
			"description": "Use this when you feel a question must be directly escalated to the human user. Engineers and researchers: escalate to your parent manager instead.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"questions": map[string]any{
						"type":        "array",
						"description": "One or more multiple-choice questions to surface to the user. The tool blocks until the user answers, declines, or the session ends.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id": map[string]any{
									"type":        "string",
									"description": "Optional stable identifier for the question (echoed back in the response).",
								},
								"header": map[string]any{
									"type":        "string",
									"description": "Short chip-style label rendered above the question in the modal.",
								},
								"question": map[string]any{
									"type":        "string",
									"description": "The full question text shown to the user.",
								},
								"multi_select": map[string]any{
									"type":        "boolean",
									"description": "When true, the user may pick zero or more options. Default false (pick exactly one).",
								},
								"options": map[string]any{
									"type":        "array",
									"description": "Pre-baked options the user can pick from. Always rendered alongside an \"Other\" free-text field and a per-question decline.",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"label": map[string]any{
												"type":        "string",
												"description": "Option label shown to the user.",
											},
											"description": map[string]any{
												"type":        "string",
												"description": "Optional extended description rendered under the label.",
											},
										},
										"required": []string{"label"},
									},
								},
							},
							"required": []string{"question", "options"},
						},
					},
				},
				"required": []string{"questions"},
			},
		},
		{
			"name":        "kill",
			"description": "Emergency stop an agent process. Preserves state and worktree for inspection.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Name of the agent to kill",
					},
				},
				"required": []string{"agent"},
			},
		},
		{
			"name":        "pause",
			"description": "Politely stop an agent at its next turn boundary, preserving transcript+worktree so it can be `wake`d later. Escalates to `kill` after a configurable timeout (default 30s). Cascades to descendants by default. (QUM-722)",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Name of the agent to pause",
					},
					"timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Seconds to wait for a turn boundary before escalating to kill. Defaults to 30.",
					},
					"cascade": map[string]any{
						"type":        "boolean",
						"description": "Default true. Set false to pause only this agent (errors if children exist).",
					},
				},
				"required": []string{"agent"},
			},
		},
		{
			"name":        "wake",
			"description": "Bring an offline or completed agent back online. Accepts any agent that is not retired/retiring, including paused/killed/died/faulted/resume_failed and the post-report `complete` resting state. Attempts to resume the prior claude session by session-id; falls back to a fresh session if the session cookie is rejected or no session exists. No-op success if the agent is already running. Errors if the agent is retired or retiring. Does NOT inject any task — combine with `delegate` or `send_message` (with `wake_if_offline: true`) for wake-with-work.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{
						"type":        "string",
						"description": "Name of the agent to wake",
					},
				},
				"required": []string{"agent"},
			},
		},
	}
}
