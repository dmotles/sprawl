package sprawlmcp

// toolDefinitions returns the MCP tool definitions for the sprawl MCP server.
func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "spawn",
			"description": "Create a new worktree-backed child agent under the current sprawl enter session. The child starts immediately and can receive tasks via delegate or messages via send_message.",
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
						"description": "Agent type: engineer, researcher, or manager",
						"enum":        []string{"engineer", "researcher", "manager"},
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Task description for the agent",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "Git branch name for the agent's worktree",
					},
				},
				"required": []string{"family", "type", "prompt", "branch"},
			},
		},
		{
			"name":        "status",
			"description": "List all agents with their current state, type, family, and branch.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "delegate",
			"description": "Assign a tracked work item (task) to an existing agent. The task is queued and the agent picks it up when ready.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
						"type":        "string",
						"description": "Name of the target agent",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "Task description to delegate",
					},
				},
				"required": []string{"agent_name", "task"},
			},
		},
		{
			"name":        "send_message",
			"description": "Canonical messaging tool (QUM-550). Sends `body` to agent `to`. interrupt=false (default): strictly cooperative — the message is enqueued at the recipient's queue and delivered at the next turn boundary; preserves the recipient's prompt cache. interrupt=true: jumps to the front of the recipient's queue AND requests preemption. Honored immediately when the recipient is streaming or thinking. If the recipient is currently awaiting a sprawl-side MCP tool response, the interrupt JSON is written to claude's stdin but its effect is not observable until that MCP call returns (QUM-549). For hard recovery from a wedged MCP call, use kill. Use interrupt=true sparingly for truly urgent context. The first line of `body` serves as the subject-equivalent in the recipient's inbox.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":   map[string]any{"type": "string", "description": "Target agent name"},
					"body": map[string]any{"type": "string", "description": "Markdown body. The first line is rendered as the subject-equivalent in inbox/banner displays."},
					"interrupt": map[string]any{
						"type":        "boolean",
						"description": "Default false. true = jump the recipient's queue and request preemption. Best-effort during MCP-tool-waits: honored for streaming/thinking but observable only after the wait returns. Use `kill` to hard-recover a wedged MCP call. See QUM-549.",
					},
				},
				"required": []string{"to", "body"},
			},
		},
		{
			"name":        "peek",
			"description": "Inspect a child or peer agent's recent activity. Returns the agent's status, its last report, and the last N protocol events (tool calls, text, results). Use to answer \"what is this agent doing?\" before sending a message.",
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
			"description": "Report this agent's status to its parent. Canonical status channel (replaces ad-hoc `sprawl report`). Persists state+summary to agent state on disk; the parent receives a strictly cooperative inbox notification (never preempts the parent). Use at every meaningful step — not just at task end.",
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
			"description": "Squash-merge an agent's branch into the current branch. The agent is NOT retired — it stays alive and can continue working.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
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
				"required": []string{"agent_name"},
			},
		},
		{
			"name":        "retire",
			"description": "Shut down an agent. Optionally merge its work first, abandon (delete) its branch, or cascade through descendants. Default refuses if the agent has unmerged commits or active children; pass `merge`, `abandon`, or `cascade` to override.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
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
				"required": []string{"agent_name"},
			},
		},
		{
			"name":        "handoff",
			"description": "Weave-only: persist a structured session summary and hand off to a fresh weave session. The host tears down the current subprocess and starts a new one with consolidated memory. Call this at the end of a session instead of `sprawl handoff` via bash.",
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
			"description": "List messages in the caller's mailbox. Scoped to the caller's agent identity — cannot read other agents' mailboxes. Returns newest-first summaries (id, from, subject, timestamp, read-state). Example: {\"filter\":\"unread\",\"limit\":20}.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":        "string",
						"description": "Mailbox filter. Default \"all\" (new+cur).",
						"enum":        []string{"all", "unread", "read", "archived"},
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
			"description": "Fetch the full body of a message by ID (short or long prefix). Auto-marks the message read if it was unread (mirrors `sprawl messages read`). Scoped to the caller's mailbox. Example: {\"id\":\"abc\"}.",
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
			"description": "Cheap \"do I have mail?\" probe — returns the caller's unread count and up to 5 newest-first preview summaries. Scoped to the caller's mailbox. Takes no arguments.",
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
					"agent_name": map[string]any{
						"type":        "string",
						"description": "Name of the agent to kill",
					},
				},
				"required": []string{"agent_name"},
			},
		},
	}
}
