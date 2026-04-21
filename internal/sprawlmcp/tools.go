package sprawlmcp

// toolDefinitions returns the MCP tool definitions for the sprawl-ops server.
func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "sprawl_spawn",
			"description": "Create a new agent subprocess with its own worktree and branch. The agent starts immediately and can receive tasks via sprawl_delegate or messages via sprawl_message.",
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
			"description": "Shut down an agent. Optionally merge its work first or abandon (delete) its branch.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
						"type":        "string",
						"description": "Name of the agent to retire",
					},
					"merge": map[string]any{
						"type":        "boolean",
						"description": "Merge the agent's work before retiring",
					},
					"abandon": map[string]any{
						"type":        "boolean",
						"description": "Discard the agent's work and delete its branch",
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
