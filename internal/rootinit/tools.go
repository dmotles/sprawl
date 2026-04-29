// Package rootinit holds the mode-agnostic pre-launch (Phase A) and
// post-launch (Phase D) housekeeping for the root weave agent. Both the
// tmux-based root loop (`cmd/rootloop.go`) and the TUI session
// (`cmd/enter.go`) call into this package so that memory/handoff behavior
// stays consistent across launch modes.
package rootinit

// RootTools is the set of tools available to the root agent. It is the
// single source of truth for both the tmux root loop and the TUI session.
var RootTools = []string{
	"Bash", "Read", "Glob", "Grep", "WebSearch", "WebFetch",
	"Agent", "Task", "TaskOutput", "TaskStop", "ToolSearch",
	"Skill", "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet",
	"AskUserQuestion", "EnterPlanMode", "ExitPlanMode",
}

// DisallowedTools is the set of tools the root agent is explicitly denied.
// The root agent does not edit files directly — it delegates to child agents.
var DisallowedTools = []string{"Edit", "Write", "NotebookEdit"}

// DefaultModel is the shared Claude model used for the root weave session and
// child agent sessions. Memory distillation uses internal/memory.DefaultMemoryModel.
const DefaultModel = "claude-opus-4-6"
