// QUM-1197 item 2: the wire type for the CLI's outstanding-background-task set.
//
// Every fixture here is a VERBATIM frame copied out of a real session log
// (`.sprawl/logs/sessions/tally/746726a4-….ndjson`), not a hand-written guess at
// the shape. The census behind this slice found exactly two `task_type` values
// in 260,374 recorded frames, but the parser deliberately does not enforce that
// — the vocabulary was closed as of a measurement, not forever.
package protocol

import "testing"

// A verbatim non-empty frame and a verbatim drain frame, both as emitted.
const (
	wireBGOneTask = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"bsspuvme4","task_type":"local_bash","description":"Run make validate"}],"uuid":"9a0e5d92-9877-4809-8375-cebfd84352a7","session_id":"746726a4-6608-45a7-9e03-6cdf94d347e7"}`
	wireBGDrain   = `{"type":"system","subtype":"background_tasks_changed","tasks":[],"uuid":"47b75211-28c0-442e-b1dc-a6a734330a03","session_id":"746726a4-6608-45a7-9e03-6cdf94d347e7"}`
)

// TestBackgroundTasksChanged_ParsesAVerbatimFrame is the POSITIVE control for
// the whole type: a real frame MUST yield a present, one-element set with every
// field the refusal record renders (id and type) populated.
func TestBackgroundTasksChanged_ParsesAVerbatimFrame(t *testing.T) {
	msg := &Message{Type: "system", Subtype: SubtypeBackgroundTasksChanged, Raw: []byte(wireBGOneTask)}

	var bg BackgroundTasksChanged
	if err := ParseAs(msg, &bg); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	tasks, present := bg.TaskSet()
	if !present {
		t.Fatalf("TaskSet present = false for a frame that carries a tasks array")
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1: %+v", len(tasks), tasks)
	}
	if tasks[0].TaskID != "bsspuvme4" {
		t.Errorf("TaskID = %q, want bsspuvme4", tasks[0].TaskID)
	}
	if tasks[0].TaskType != BackgroundTaskLocalBash {
		t.Errorf("TaskType = %q, want %q", tasks[0].TaskType, BackgroundTaskLocalBash)
	}
	if tasks[0].Description != "Run make validate" {
		t.Errorf("Description = %q, want %q", tasks[0].Description, "Run make validate")
	}
}

// TestBackgroundTasksChanged_DrainFrameIsPresentAndEmpty pins the distinction
// the whole tri-state rests on at the wire layer: an emitted `tasks:[]` is a
// POSITIVE statement that nothing is outstanding, and must be reported as
// present-and-empty rather than as absent.
func TestBackgroundTasksChanged_DrainFrameIsPresentAndEmpty(t *testing.T) {
	msg := &Message{Type: "system", Subtype: SubtypeBackgroundTasksChanged, Raw: []byte(wireBGDrain)}

	var bg BackgroundTasksChanged
	if err := ParseAs(msg, &bg); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	tasks, present := bg.TaskSet()
	if !present {
		t.Fatalf("TaskSet present = false for an emitted tasks:[] drain frame; observed-empty is a FACT, not an absence")
	}
	if len(tasks) != 0 {
		t.Errorf("len(tasks) = %d for a drain frame, want 0: %+v", len(tasks), tasks)
	}
}

// TestBackgroundTasksChanged_MissingTasksKeyIsNotEmpty is the arm that keeps a
// malformed or renamed frame from reading as good news. A frame with no `tasks`
// key at all tells us nothing about the set; reporting it as empty is the
// confident-false-zero this slice's standing requirement forbids.
//
// Direction: MUST report present=false. Positive control for the assertion is
// the drain-frame test above, which reports present=true off the same method.
func TestBackgroundTasksChanged_MissingTasksKeyIsNotEmpty(t *testing.T) {
	msg := &Message{
		Type:    "system",
		Subtype: SubtypeBackgroundTasksChanged,
		Raw:     []byte(`{"type":"system","subtype":"background_tasks_changed","uuid":"x"}`),
	}

	var bg BackgroundTasksChanged
	if err := ParseAs(msg, &bg); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	if tasks, present := bg.TaskSet(); present {
		t.Errorf("TaskSet present = true with no tasks key (tasks=%+v); an absent key is 'we do not know', not 'nothing outstanding'", tasks)
	}
}

// TestBackgroundTasksChanged_UnknownTaskTypeIsPreserved: the census closed the
// vocabulary at local_bash and local_agent as of one measurement. A parser that
// dropped anything else would make a future CLI's new task type invisible to
// the reap predicate — silently reapable work. Direction: MUST be preserved.
func TestBackgroundTasksChanged_UnknownTaskTypeIsPreserved(t *testing.T) {
	msg := &Message{
		Type:    "system",
		Subtype: SubtypeBackgroundTasksChanged,
		Raw:     []byte(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"z1","task_type":"local_something_new"}]}`),
	}

	var bg BackgroundTasksChanged
	if err := ParseAs(msg, &bg); err != nil {
		t.Fatalf("ParseAs: %v", err)
	}
	tasks, present := bg.TaskSet()
	if !present || len(tasks) != 1 || tasks[0].TaskType != "local_something_new" {
		t.Errorf("unknown task_type was not preserved: present=%v tasks=%+v", present, tasks)
	}
}

// TestBackgroundTasksChanged_NilRawIsAnError: ParseAs on a Message with no Raw
// must fail rather than yield a zero value that reads as an empty set. This is
// the same defect one layer down.
func TestBackgroundTasksChanged_NilRawIsAnError(t *testing.T) {
	var bg BackgroundTasksChanged
	if err := ParseAs(&Message{Type: "system", Subtype: SubtypeBackgroundTasksChanged}, &bg); err == nil {
		t.Error("ParseAs with nil Raw returned nil error; a frame that could not be read must not present as an empty set")
	}
}

// TestBackgroundTaskTypeConstants_MatchTheWire keeps the constants honest
// against the verbatim frames above rather than against memory.
func TestBackgroundTaskTypeConstants_MatchTheWire(t *testing.T) {
	if SubtypeBackgroundTasksChanged != "background_tasks_changed" {
		t.Errorf("SubtypeBackgroundTasksChanged = %q", SubtypeBackgroundTasksChanged)
	}
	if BackgroundTaskLocalBash != "local_bash" {
		t.Errorf("BackgroundTaskLocalBash = %q", BackgroundTaskLocalBash)
	}
	if BackgroundTaskLocalAgent != "local_agent" {
		t.Errorf("BackgroundTaskLocalAgent = %q", BackgroundTaskLocalAgent)
	}
}
