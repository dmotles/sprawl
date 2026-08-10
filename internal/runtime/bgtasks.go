// QUM-1197 item 2: the agent's outstanding CLI-managed background work.
//
// The idle reaper had six terms and every one of them could read `idle`
// HONESTLY while the agent had work it intended to return to: an agent that
// backgrounds a tool call or spawns a sidechain and ends its turn is idle by
// every measure the predicate had, and reaping it destroys the work silently —
// the wake notification the agent was waiting for never arrives. Measured across
// the recorded fleet: 263 of 2596 turn closures (10.1%) had work outstanding, 43
// of them a live sidechain.
//
// The source is `system`/`background_tasks_changed`, a CLI-authored frame
// carrying the whole current set. It is not a self-report — the model neither
// writes it nor can decline to send it — and it covers backgrounded Bash and
// sidechains in one signal. Every OS/process-table design was ruled out
// structurally, not by preference: a sidechain is in-process and adds NO pid
// (measured, with the direction stated: a backgrounded Bash task must appear as
// a new child, a sidechain must not), so a process-table term is permanently
// blind to those 43 closures. Earlier eliminations still bind: "has live
// descendants" is permanently true for an agent with a persistent MCP-server
// pair, and a CPU-time delta cannot see a sleeping task.
//
// TWO LIMITS, stated here because discovering them later would be worse:
//
//  1. This covers CLI-MANAGED background work only. A process a Bash call
//     `nohup`ed or `setsid`ed away, or a long-running MCP-side call, carries no
//     task_id and never appears in the set. A large subset, not the whole.
//  2. It protects the REAP DECISION only. An operator still sees an agent
//     rendered idle while three sidechains run; that is QUM-1213, separately
//     tracked, and nothing here changes rendering.
package runtime

import (
	"sort"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// OutstandingTask is one entry of the agent's outstanding set, plus when this
// process first saw it.
//
// FirstSeen is RECORDED AT RECEIPT: the frame carries no timestamp of its own,
// so the age derived from this is "how long we have known about it", not "how
// long it has run". Close enough for the operator-visible wedge signal it feeds,
// and the distinction is stated rather than glossed.
type OutstandingTask struct {
	protocol.BackgroundTask
	FirstSeen time.Time
}

// noteBackgroundTasks folds one background_tasks_changed frame into the set. It
// is the ONLY writer, and runs on the backend reader goroutine (routeFrame's
// synchronous contract), so it takes bgMu purely to order against readers on the
// reaper's sweep goroutine.
//
// The frame is level-triggered — every frame is the whole truth — so the set is
// REPLACED, never merged: a dropped frame self-corrects on the next one instead
// of leaving a counter permanently skewed. FirstSeen is carried forward for a
// task_id already present, because resetting it on every frame would destroy the
// only evidence that a task has been wedged for hours.
//
// A frame we cannot read is NOT an empty set. An unparseable body, or one with
// no `tasks` key, sets bgParseFailed and leaves the known set alone, so the
// accessor reports "cannot tell" and the reap is blocked. This is the shape that
// produced a confident false zero twice during this issue's own investigation (a
// broken jq pipeline reporting no frames in a corpus that contained 1,615 of
// them): a scan that fails is not a scan that passes. The next well-formed frame
// clears the doubt, since it carries the whole truth.
func (rt *UnifiedRuntime) noteBackgroundTasks(msg *protocol.Message) {
	var bg protocol.BackgroundTasksChanged
	tasks, present := []protocol.BackgroundTask(nil), false
	if msg != nil && protocol.ParseAs(msg, &bg) == nil {
		tasks, present = bg.TaskSet()
	}

	rt.bgMu.Lock()
	defer rt.bgMu.Unlock()
	if !present {
		rt.bgParseFailed = true
		return
	}
	now := rt.now()
	next := make(map[string]OutstandingTask, len(tasks))
	for _, task := range tasks {
		entry := OutstandingTask{BackgroundTask: task, FirstSeen: now}
		if prev, ok := rt.bgTasks[task.TaskID]; ok {
			entry.FirstSeen = prev.FirstSeen
		}
		next[task.TaskID] = entry
	}
	rt.bgTasks = next
	rt.bgObserved = true
	rt.bgParseFailed = false
}

// WorkOutstandingObserved reports the agent's outstanding CLI-managed background
// work, and whether that could be observed AT ALL.
//
// The second return is the whole point, and it is the D1a rule this predicate is
// built on: never having seen a background_tasks_changed frame is not the same
// fact as having seen `tasks:[]`. If the CLI renames the subtype or stops
// emitting, a set-valued term would silently read "no work" and the protection
// would evaporate with no error anywhere. So observed=false covers both
// never-observed and "the last frame was unreadable", and the caller must treat
// it as blocking rather than as clean.
//
// The result is sorted (oldest first, then by id) so the refusal record it feeds
// is deterministic.
func (rt *UnifiedRuntime) WorkOutstandingObserved() ([]OutstandingTask, bool) {
	rt.bgMu.Lock()
	defer rt.bgMu.Unlock()
	if !rt.bgObserved || rt.bgParseFailed {
		return nil, false
	}
	out := make([]OutstandingTask, 0, len(rt.bgTasks))
	for _, task := range rt.bgTasks {
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, true
}

// now is the runtime's clock, overridable via RuntimeConfig.Now. Immutable after
// New, so production reads on the reader goroutine cannot race a test's write.
func (rt *UnifiedRuntime) now() time.Time {
	if rt.nowFn != nil {
		return rt.nowFn()
	}
	return time.Now()
}
