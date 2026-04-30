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

// Per-role model constants. The root weave session and manager agents use
// extended-thinking opus; engineer and researcher agents use standard opus.
// Memory distillation uses internal/memory.DefaultMemoryModel.
const (
	DefaultRootModel    = "opus[1m]" // root weave session
	DefaultManagerModel = "opus[1m]" // manager child agents
	DefaultAgentModel   = "opus"     // engineer, researcher, etc.
)

// ModelForAgentType returns the model string for the given agent type.
func ModelForAgentType(agentType string) string {
	if agentType == "manager" {
		return DefaultManagerModel
	}
	return DefaultAgentModel
}
