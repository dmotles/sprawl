package state

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Task represents a queued unit of work for an agent.
type Task struct {
	ID         string `json:"id"`
	Prompt     string `json:"prompt"`
	PromptFile string `json:"prompt_file,omitempty"`
	Status     string `json:"status"`
	CreatedAt string `json:"created_at"`
	StartedAt string `json:"started_at,omitempty"`
	DoneAt    string `json:"done_at,omitempty"`
}

// TasksDir returns the path to the tasks directory for a given agent.
func TasksDir(dendraRoot, agentName string) string {
	return filepath.Join(dendraRoot, ".dendra", "agents", agentName, "tasks")
}

// GenerateUUID creates a random UUID v4 string using crypto/rand.
func GenerateUUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating UUID: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// EnqueueTask creates a new task with status "queued" and writes it to disk.
func EnqueueTask(dendraRoot, agentName, prompt string) (*Task, error) {
	dir := TasksDir(dendraRoot, agentName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating tasks directory: %w", err)
	}

	id, err := GenerateUUID()
	if err != nil {
		return nil, err
	}

	promptPath, err := WritePromptFile(dendraRoot, agentName, id, prompt)
	if err != nil {
		return nil, fmt.Errorf("writing prompt file: %w", err)
	}

	now := time.Now().UTC()
	task := &Task{
		ID:         id,
		Prompt:     prompt,
		PromptFile: promptPath,
		Status:     "queued",
		CreatedAt:  now.Format(time.RFC3339),
	}

	filename := now.Format("20060102T150405.000000000Z") + "-" + id + ".json"
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling task: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, filename), data, 0644); err != nil {
		return nil, fmt.Errorf("writing task file: %w", err)
	}

	return task, nil
}

// NextTask returns the first task with status "queued" in FIFO order, or nil if none.
func NextTask(dendraRoot, agentName string) (*Task, error) {
	tasks, err := ListTasks(dendraRoot, agentName)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.Status == "queued" {
			return t, nil
		}
	}
	return nil, nil
}

// UpdateTask updates an existing task file on disk. Returns an error if the task is not found.
func UpdateTask(dendraRoot, agentName string, task *Task) error {
	dir := TasksDir(dendraRoot, agentName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found", task.ID)
		}
		return fmt.Errorf("reading tasks directory: %w", err)
	}

	suffix := "-" + task.ID + ".json"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		data, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling task: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0644); err != nil {
			return fmt.Errorf("writing task file: %w", err)
		}
		return nil
	}

	return fmt.Errorf("task %q not found", task.ID)
}

// ListTasks returns all tasks for an agent in FIFO order (sorted by filename).
func ListTasks(dendraRoot, agentName string) ([]*Task, error) {
	dir := TasksDir(dendraRoot, agentName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading tasks directory: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	var tasks []*Task
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("reading task file %q: %w", name, err)
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			return nil, fmt.Errorf("parsing task file %q: %w", name, err)
		}
		tasks = append(tasks, &task)
	}
	return tasks, nil
}
