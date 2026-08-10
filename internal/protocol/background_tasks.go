// QUM-1197 item 2: the CLI's outstanding-background-task set, on the wire.
//
// `system`/`background_tasks_changed` is a CLI-authored protocol frame carrying
// the FULL current set of the agent's outstanding background work — tasks
// started with run_in_background AND live sidechains (Agent-tool spawns). It is
// not a self-report: the model does not author it and cannot decline to send it.
//
// It is LEVEL-TRIGGERED — every frame is the whole truth — so a consumer
// replaces its set rather than incrementing a counter, and a dropped frame
// self-corrects on the next one.
//
// This type lives in its own file rather than types.go on purpose: types.go is a
// literal path entry in e2e matrix rows this change has no business owing.
package protocol

// SubtypeBackgroundTasksChanged is the frame's `subtype`.
const SubtypeBackgroundTasksChanged = "background_tasks_changed"

// The task_type vocabulary, closed at two values by a census over 260,374
// recorded frames (QUM-1197 Lane 2, re-verified independently).
//
// These exist for RENDERING, not for filtering. A consumer deciding whether
// work is outstanding must count every task whatever its type: the vocabulary
// was closed as of a measurement, not forever, and a filter would make a future
// CLI's new task type invisible to the reap predicate — i.e. silently reapable.
const (
	BackgroundTaskLocalBash  = "local_bash"
	BackgroundTaskLocalAgent = "local_agent"
)

// BackgroundTask is one entry of the outstanding set.
type BackgroundTask struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
}

// BackgroundTasksChanged is the frame.
//
// Tasks is a POINTER so an absent `tasks` key is distinguishable from an
// emitted `tasks:[]`. Those are different facts and the reap predicate treats
// them differently: an emitted empty array is a positive statement that nothing
// is outstanding; a missing key is "this frame did not tell us", which must
// never read as good news. Read it through TaskSet rather than touching Tasks.
type BackgroundTasksChanged struct {
	Type    string            `json:"type"`
	Subtype string            `json:"subtype"`
	Tasks   *[]BackgroundTask `json:"tasks"`
}

// TaskSet returns the outstanding set and whether the frame actually carried
// one. present=false means the frame is not evidence about the set in either
// direction — the caller must treat it as "cannot tell", never as "empty".
func (b BackgroundTasksChanged) TaskSet() ([]BackgroundTask, bool) {
	if b.Tasks == nil {
		return nil, false
	}
	return *b.Tasks, true
}
