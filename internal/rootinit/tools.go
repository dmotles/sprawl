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
	"EnterPlanMode", "ExitPlanMode",
}

// DisallowedTools is the set of tools the root agent is explicitly denied.
// The root agent does not edit files directly — it delegates to child agents.
// AskUserQuestion is denied because the harness implementation silently
// no-ops in `--print` (stream-json) mode (QUM-528); use the
// `mcp__sprawl__ask_user_question` MCP tool (QUM-527) instead.
var DisallowedTools = []string{"Edit", "Write", "NotebookEdit", "AskUserQuestion"}

// ChildDisallowedTools is the set of harness-tied tools that silently no-op
// when claude runs in `--print` (stream-json) mode, which is how all sprawl
// child agents are launched. Without this denylist, children can ToolSearch
// these names and issue tool calls that "succeed" without doing anything —
// e.g. ScheduleWakeup queues a wake that never fires because no idle session
// loop exists in --print mode. See QUM-470 for the wake-loss footgun.
//
// AskUserQuestion belongs here too (QUM-528): the harness version returns
// inputs to the model but never renders a prompt to the user under
// `--print --output-format stream-json`. Weave and managers should call
// `mcp__sprawl__ask_user_question` (QUM-527) instead; other agents must
// escalate to their parent.
//
// Children should use `Bash run_in_background: true` plus synchronous
// `mcp__sprawl__*` waits instead.
var ChildDisallowedTools = []string{
	"ScheduleWakeup",
	"Monitor",
	"PushNotification",
	"RemoteTrigger",
	"CronCreate",
	"CronDelete",
	"CronList",
	"EnterWorktree",
	"ExitWorktree",
	"TaskStop",
	"AskUserQuestion",
}

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
