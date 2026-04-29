package sprawlmcp

// toolDefinitions returns the MCP tool definitions for the sprawl-ops server.
func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "sprawl_spawn",
			"description": "Create a new worktree-backed child agent under the current sprawl enter session. The child starts immediately and can receive tasks via sprawl_delegate or messages via sprawl_send_async.",
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
			"name":        "sprawl_status",
			"description": "List all agents with their current state, type, family, and branch.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "sprawl_delegate",
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
			"name":        "sprawl_send_async",
			"description": "Queue an asynchronous message for a peer or child agent. The recipient reads it on its next yield (between turns). Does not interrupt. Persisted; survives crashes. Returns the queue message_id and queued_at.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to": map[string]any{
						"type":        "string",
						"description": "Target agent name",
					},
					"subject": map[string]any{
						"type":        "string",
						"description": "≤80 char human-readable label",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Markdown body (no length cap)",
					},
					"reply_to": map[string]any{
						"type":        "string",
						"description": "Optional message ID this replies to (threading)",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional labels, e.g. [\"status\", \"question\", \"fyi\"]",
					},
				},
				"required": []string{"to", "subject", "body"},
			},
		},
		{
			"name":        "sprawl_send_interrupt",
			"description": "Interrupt a descendant agent mid-turn and inject this message as a user turn. Gated to parent→descendants only (§8.5). Use sparingly — this is the \"I forgot to tell you something important\" channel. Target resumes its previous work unless the body directs it to stop.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to": map[string]any{
						"type":        "string",
						"description": "Target descendant agent name",
					},
					"subject": map[string]any{
						"type":        "string",
						"description": "≤80 char human-readable label",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Markdown body of the interrupt message",
					},
					"resume_hint": map[string]any{
						"type":        "string",
						"description": "Optional free-form hint the target can quote back to itself after reading (e.g. \"you were implementing X\")",
					},
				},
				"required": []string{"to", "subject", "body"},
			},
		},
		{
			"name":        "sprawl_peek",
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
			"name":        "sprawl_report_status",
			"description": "Report this agent's status to its parent. Canonical status channel (replaces ad-hoc `sprawl report`). Persists to agent state and delivers an async notification to the parent. Use at every meaningful step — not just at task end.",
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
					"detail": map[string]any{
						"type":        "string",
						"description": "Optional markdown detail (no length cap)",
					},
				},
				"required": []string{"state", "summary"},
			},
		},
		{
			"name":        "sprawl_message",
			"description": "DEPRECATED: use sprawl_send_async. Kept as an alias that writes to the recipient's Maildir and harness queue, then returns a short acknowledgement.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
						"type":        "string",
						"description": "Name of the target agent",
					},
					"subject": map[string]any{
						"type":        "string",
						"description": "Message subject",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Message body",
					},
				},
				"required": []string{"agent_name", "subject", "body"},
			},
		},
		{
			"name":        "sprawl_merge",
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
			"name":        "sprawl_retire",
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
			"name":        "sprawl_handoff",
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
			"name":        "sprawl_messages_list",
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
			"name":        "sprawl_messages_read",
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
			"name":        "sprawl_messages_archive",
			"description": "Archive a single message by ID (short or long prefix). Moves the file from new/ or cur/ into archive/. Scoped to the caller's mailbox. Example: {\"id\":\"abc\"}.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Message ID to archive.",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			"name":        "sprawl_messages_peek",
			"description": "Cheap \"do I have mail?\" probe — returns the caller's unread count and up to 5 newest-first preview summaries. Scoped to the caller's mailbox. Takes no arguments.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "sprawl_kill",
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
