package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTasksDir(t *testing.T) {
	got := TasksDir("/home/user/project", "frank")
	want := "/home/user/project/.sprawl/agents/frank/tasks"
	if got != want {
		t.Errorf("TasksDir = %q, want %q", got, want)
	}
}

func TestEnqueueTask(t *testing.T) {
	dir := t.TempDir()

	task, err := EnqueueTask(dir, "frank", "implement login page")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if task.ID == "" {
		t.Error("expected non-empty task ID")
	}
	if task.Status != "queued" {
		t.Errorf("Status = %q, want %q", task.Status, "queued")
	}
	if task.Prompt != "implement login page" {
		t.Errorf("Prompt = %q, want %q", task.Prompt, "implement login page")
	}
	if task.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt")
	}

	// Verify task persists on disk via ListTasks
	tasks, listErr := ListTasks(dir, "frank")
	if listErr != nil {
		t.Fatalf("ListTasks: %v", listErr)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ID != task.ID {
		t.Errorf("loaded ID = %q, want %q", tasks[0].ID, task.ID)
	}
	if tasks[0].Status != "queued" {
		t.Errorf("loaded Status = %q, want %q", tasks[0].Status, "queued")
	}

	// Verify a JSON file exists in the tasks directory
	entries, dirErr := os.ReadDir(TasksDir(dir, "frank"))
	if dirErr != nil {
		t.Fatalf("reading tasks dir: %v", dirErr)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in tasks dir, got %d", len(entries))
	}
}

func TestEnqueueTask_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()

	tasksPath := TasksDir(dir, "frank")
	if _, err := os.Stat(tasksPath); !os.IsNotExist(err) {
		t.Fatal("tasks dir should not exist before enqueue")
	}

	_, err := EnqueueTask(dir, "frank", "some task")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	info, err := os.Stat(tasksPath)
	if err != nil {
		t.Fatalf("tasks dir should exist after enqueue: %v", err)
	}
	if !info.IsDir() {
		t.Error("tasks path should be a directory")
	}
}

func TestEnqueueTask_MultipleTasksFIFO(t *testing.T) {
	dir := t.TempDir()

	task1, err := EnqueueTask(dir, "frank", "first task")
	if err != nil {
		t.Fatalf("EnqueueTask 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	task2, err := EnqueueTask(dir, "frank", "second task")
	if err != nil {
		t.Fatalf("EnqueueTask 2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	task3, err := EnqueueTask(dir, "frank", "third task")
	if err != nil {
		t.Fatalf("EnqueueTask 3: %v", err)
	}

	// NextTask should return them in FIFO order
	next, err := NextTask(dir, "frank")
	if err != nil {
		t.Fatalf("NextTask 1: %v", err)
	}
	if next.ID != task1.ID {
		t.Errorf("first NextTask ID = %q, want %q", next.ID, task1.ID)
	}

	// Mark first as active so it's skipped
	next.Status = "active"
	if err := UpdateTask(dir, "frank", next); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	next, err = NextTask(dir, "frank")
	if err != nil {
		t.Fatalf("NextTask 2: %v", err)
	}
	if next.ID != task2.ID {
		t.Errorf("second NextTask ID = %q, want %q", next.ID, task2.ID)
	}

	next.Status = "active"
	if err := UpdateTask(dir, "frank", next); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	next, err = NextTask(dir, "frank")
	if err != nil {
		t.Fatalf("NextTask 3: %v", err)
	}
	if next.ID != task3.ID {
		t.Errorf("third NextTask ID = %q, want %q", next.ID, task3.ID)
	}
}

func TestNextTask_EmptyQueue(t *testing.T) {
	dir := t.TempDir()

	// Tasks dir doesn't even exist
	task, err := NextTask(dir, "frank")
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	if task != nil {
		t.Errorf("expected nil task for empty queue, got %+v", task)
	}
}

func TestNextTask_SkipsNonQueued(t *testing.T) {
	dir := t.TempDir()

	task1, err := EnqueueTask(dir, "frank", "first task")
	if err != nil {
		t.Fatalf("EnqueueTask 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	task2, err := EnqueueTask(dir, "frank", "second task")
	if err != nil {
		t.Fatalf("EnqueueTask 2: %v", err)
	}

	// Mark first task as active
	task1.Status = "active"
	if err := UpdateTask(dir, "frank", task1); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// NextTask should skip the active task and return the second
	next, err := NextTask(dir, "frank")
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	if next == nil {
		t.Fatal("expected non-nil task")
	}
	if next.ID != task2.ID {
		t.Errorf("NextTask ID = %q, want %q (should skip active task)", next.ID, task2.ID)
	}
}

func TestNextTask_SkipsDoneAndFailed(t *testing.T) {
	dir := t.TempDir()

	task1, err := EnqueueTask(dir, "frank", "done task")
	if err != nil {
		t.Fatalf("EnqueueTask 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	task2, err := EnqueueTask(dir, "frank", "failed task")
	if err != nil {
		t.Fatalf("EnqueueTask 2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	_, err = EnqueueTask(dir, "frank", "queued task")
	if err != nil {
		t.Fatalf("EnqueueTask 3: %v", err)
	}

	// Mark first as done, second as failed
	task1.Status = "done"
	if err := UpdateTask(dir, "frank", task1); err != nil {
		t.Fatalf("UpdateTask done: %v", err)
	}
	task2.Status = "failed"
	if err := UpdateTask(dir, "frank", task2); err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	next, err := NextTask(dir, "frank")
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	if next == nil {
		t.Fatal("expected non-nil task")
	}
	if next.Prompt != "queued task" {
		t.Errorf("NextTask Prompt = %q, want %q (should skip done and failed)", next.Prompt, "queued task")
	}
}

func TestUpdateTask(t *testing.T) {
	dir := t.TempDir()

	task, err := EnqueueTask(dir, "frank", "some task")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	task.Status = "active"
	task.StartedAt = "2026-03-31T12:00:00Z"
	if err := UpdateTask(dir, "frank", task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// Reload from disk via ListTasks and verify
	tasks, listErr := ListTasks(dir, "frank")
	if listErr != nil {
		t.Fatalf("ListTasks: %v", listErr)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != "active" {
		t.Errorf("Status = %q, want %q", tasks[0].Status, "active")
	}
	if tasks[0].StartedAt != "2026-03-31T12:00:00Z" {
		t.Errorf("StartedAt = %q, want %q", tasks[0].StartedAt, "2026-03-31T12:00:00Z")
	}
}

func TestUpdateTask_NotFound(t *testing.T) {
	dir := t.TempDir()

	task := &Task{
		ID:     "bogus-id-does-not-exist",
		Prompt: "something",
		Status: "active",
	}

	err := UpdateTask(dir, "frank", task)
	if err == nil {
		t.Fatal("expected error for updating nonexistent task")
	}
}

func TestListTasks(t *testing.T) {
	dir := t.TempDir()

	_, err := EnqueueTask(dir, "frank", "task one")
	if err != nil {
		t.Fatalf("EnqueueTask 1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	_, err = EnqueueTask(dir, "frank", "task two")
	if err != nil {
		t.Fatalf("EnqueueTask 2: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	_, err = EnqueueTask(dir, "frank", "task three")
	if err != nil {
		t.Fatalf("EnqueueTask 3: %v", err)
	}

	tasks, err := ListTasks(dir, "frank")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Verify FIFO order (sorted by CreatedAt ascending)
	if tasks[0].Prompt != "task one" {
		t.Errorf("tasks[0].Prompt = %q, want %q", tasks[0].Prompt, "task one")
	}
	if tasks[1].Prompt != "task two" {
		t.Errorf("tasks[1].Prompt = %q, want %q", tasks[1].Prompt, "task two")
	}
	if tasks[2].Prompt != "task three" {
		t.Errorf("tasks[2].Prompt = %q, want %q", tasks[2].Prompt, "task three")
	}
}

func TestEnqueueTask_WritesPromptFile(t *testing.T) {
	dir := t.TempDir()

	task, err := EnqueueTask(dir, "frank", "implement login page")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	// Verify prompt file exists at prompts/<uuid>.md
	promptPath := filepath.Join(PromptsDir(dir, "frank"), task.ID+".md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("reading prompt file: %v", err)
	}
	if string(data) != "implement login page" {
		t.Errorf("prompt file content = %q, want %q", string(data), "implement login page")
	}
}

func TestEnqueueTask_SetsPromptFilePath(t *testing.T) {
	dir := t.TempDir()

	task, err := EnqueueTask(dir, "frank", "some task prompt")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if task.PromptFile == "" {
		t.Fatal("expected PromptFile to be set")
	}

	expectedSuffix := filepath.Join("prompts", task.ID+".md")
	if !strings.HasSuffix(task.PromptFile, expectedSuffix) {
		t.Errorf("PromptFile = %q, want suffix %q", task.PromptFile, expectedSuffix)
	}

	// Verify the file actually exists at that path
	if _, err := os.Stat(task.PromptFile); err != nil {
		t.Errorf("PromptFile path does not exist: %v", err)
	}
}

func TestEnqueueTask_BackwardCompat_PromptStillSet(t *testing.T) {
	dir := t.TempDir()

	task, err := EnqueueTask(dir, "frank", "some task")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	// Prompt field should still be populated for backward compat
	if task.Prompt != "some task" {
		t.Errorf("Prompt = %q, want %q", task.Prompt, "some task")
	}
	// PromptFile should also be populated
	if task.PromptFile == "" {
		t.Error("expected PromptFile to be set alongside Prompt")
	}
}

func TestEnqueueTask_PromptFilePersistsOnDisk(t *testing.T) {
	dir := t.TempDir()

	task, err := EnqueueTask(dir, "frank", "persist me")
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	// Reload task from disk and verify PromptFile is persisted
	tasks, err := ListTasks(dir, "frank")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].PromptFile != task.PromptFile {
		t.Errorf("loaded PromptFile = %q, want %q", tasks[0].PromptFile, task.PromptFile)
	}
}

func TestListTasks_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	tasks, err := ListTasks(dir, "frank")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}
