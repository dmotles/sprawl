package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Agent status string constants (QUM-372). These enumerate the universe of
// values that may appear in AgentState.Status. They are intentionally not
// enforced by the persistence layer — the field remains a free-form string for
// back-compat with older state files — but every new write-site in the
// codebase should reference one of these constants so the set stays closed.
const (
	StatusActive       = "active"
	StatusRunning      = "running"
	StatusSuspended    = "suspended"
	StatusKilled       = "killed"
	StatusRetired      = "retired"
	StatusRetiring     = "retiring"
	StatusDone         = "done"
	StatusResumeFailed = "resume_failed"
	StatusFaulted      = "faulted"
	// StatusStopped is retained as a parseable string for back-compat with
	// older state files but is NEVER a write target after QUM-787 — the
	// LoadAgent migration rewrites any "stopped" Status on read to
	// StatusSuspended (QUM-1186). Supervisor set-sites no longer stamp it.
	StatusStopped = "stopped"
	// QUM-722: new lifecycle states for pause/death.
	StatusPaused = "paused"
	StatusDied   = "died"
	// QUM-787: StatusComplete is the durable resting state for an agent
	// that reported state=complete and had its runtime torn down. The
	// agent's session_id, worktree, and branch are preserved — it is
	// revivable via wake per the QUM-786 lifecycle arc. This
	// state replaces the previous overload of StatusStopped, which mixed
	// "reported complete and torn down" with "clean subprocess exit
	// without a completion report" (now StatusFaulted).
	StatusComplete = "complete"
	// QUM-1186 (D2): StatusIdle is the durable resting state for an agent
	// whose runtime was reclaimed for INACTIVITY rather than because its work
	// finished. Session, worktree and branch are preserved and it revives on
	// the next message, exactly like StatusSuspended — from which it differs
	// only in why the teardown happened.
	//
	// It is deliberately NOT StatusComplete. Complete claims the agent
	// finished; an idle agent may be mid-task and merely quiet. Conflating
	// them would tell the operator a lie in the TUI and, worse, would put a
	// reaped-but-unfinished agent outside the auto-resume accept-set.
	//
	// It projects onto liveness.Suspended, which is what puts it INSIDE that
	// accept-set (internal/supervisor/real.go RecoverAgents). Pinned by
	// TestRecoverAgents_StatusIdleIsAutoResumeEligible.
	StatusIdle = "idle"
)

// IsTerminal reports whether a Status value names a terminal liveness in
// the QUM-786 lifecycle arc sense: only PARENT-decided permanent states
// are terminal. Everything else — including StatusComplete, StatusFaulted,
// StatusKilled, StatusDied, StatusResumeFailed, StatusPaused — is
// revivable by an explicit `wake` (or auto-wake for `complete`) and must
// not be treated as terminal by liveness checks.
//
// QUM-787 narrowed this from the QUM-739 wide set
// {stopped, faulted, retired, killed, died, resume_failed} down to
// {retired, retiring} only.
//
// NOTE: callers that previously used IsTerminal as a "resolved orphan"
// predicate (children that don't block parent retire/merge cascade)
// should use IsResolvedOrphan instead.
func IsTerminal(status string) bool {
	switch status {
	case StatusRetired, StatusRetiring:
		return true
	}
	return false
}

// IsResolvedOrphan reports whether an agent in this Status is sufficiently
// torn down that it should NOT block a parent's retire/merge cascade. The
// set is strictly broader than IsTerminal — it includes every revivable
// resting state (complete, faulted, killed, died, resume_failed) AND the
// legacy stopped sentinel (for state files that have not yet been
// migrated). This is the predicate that callers in
// internal/agentops/retire.go, internal/agentops/merge.go, and
// internal/supervisor/real.go use to decide which children are "resolved
// orphans" rather than active blockers — a role IsTerminal filled prior
// to QUM-787, before the lifecycle arc narrowed IsTerminal.
func IsResolvedOrphan(status string) bool {
	switch status {
	case StatusRetired, StatusRetiring,
		StatusComplete, StatusStopped, StatusFaulted,
		StatusKilled, StatusDied, StatusResumeFailed:
		return true
	}
	return false
}

// CurrentSchemaVersion is the schema version stamped onto agent state files by
// the current code. LoadAgent migrates older (v0/v1) files forward on read and
// SaveAgent stamps this value (QUM-625 M4; bumped to v2 for the QUM-851
// Model / SystemPromptAppend fields).
const CurrentSchemaVersion = 4

// AgentState holds the persistent metadata for a spawned agent.
type AgentState struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Family    string `json:"family"`
	Parent    string `json:"parent"`
	Prompt    string `json:"prompt"`
	Branch    string `json:"branch"`
	Worktree  string `json:"worktree"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	SessionID string `json:"session_id,omitempty"`
	Subagent  bool   `json:"subagent,omitempty"`
	TreePath  string `json:"tree_path,omitempty"`

	// Model, when non-empty, is the resolved `claude --model` string this
	// agent launches with, overriding rootinit.ModelForAgentType(Type). Empty
	// means "use the type default" (QUM-851).
	Model string `json:"model,omitempty"`
	// SystemPromptAppend, when non-empty, is custom operator instructions
	// appended onto the built-in role system prompt under a delimited header.
	// Empty means "no append" (QUM-851). It never replaces the base prompt.
	SystemPromptAppend string `json:"system_prompt_append,omitempty"`

	// SchemaVersion records the persisted schema version. Files written before
	// QUM-625 M4 lack this field and unmarshal as 0 (v0); LoadAgent migrates
	// them forward and stamps CurrentSchemaVersion.
	SchemaVersion int `json:"schema_version,omitempty"`

	// Blurb is a short (2-3 sentence) auto-generated capability summary
	// answering "what does this agent know / what was it working on last?".
	// Maintained in the background by internal/blurb and displayed in the
	// status/peek tools (QUM-899). Empty until the first generation completes.
	Blurb string `json:"blurb,omitempty"`
	// BlurbAt is the generation watermark for Blurb: the RFC3339 time the
	// current Blurb was produced. Used as the dirty-check baseline (refresh
	// only when new activity postdates it). Zero until first generation.
	BlurbAt time.Time `json:"blurb_at,omitempty"`
}

// AgentsDir returns the path to the agents state directory under the given sprawl root.
func AgentsDir(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "agents")
}

// migrate brings a freshly-unmarshaled AgentState forward to the current
// schema version. Returns true if any field was rewritten so LoadAgent can
// persist the normalized form back to disk.
//
// The v0 -> v1 migration splits the legacy combined Status axis. Pre-M4 code
// overloaded Status with outcome tokens ("done"/"problem") that are not
// livenesses, so those are rewritten to a pure liveness.
//
// QUM-1186 changed what the legacy tokens map ONTO. The outcome axis
// (LastReportState) is deleted, so there is no second field to record an
// outcome in and nothing to consult when re-classifying:
//
//   - "done"     -> StatusComplete. The token means the agent finished; that
//     information is preserved on the Status axis rather than discarded.
//   - "problem"  -> StatusFaulted.
//   - ""         -> StatusSuspended.
//   - "stopped"  -> StatusSuspended (always-on, not version-gated). It
//     PREVIOUSLY split on LastReportState and defaulted to StatusFaulted.
//     There is no longer any evidence to split on, and defaulting a legacy
//     clean stop to `faulted` would reproduce on the READ path exactly the
//     bug QUM-1186 D3 removed from the WRITE path: a clean exit nobody
//     labelled is not a fault, and `faulted` is outside the auto-resume
//     accept-set, so the mislabel would cost the agent on next startup.
//
// The SessionID-dependent branch is gone with it: a session cookie told us
// whether the agent could be resumed, which is now decided purely by the
// Status the tokens above map to.
//
// The v1 -> v2 (QUM-851) and v2 -> v3 (QUM-899) migrations are version-stamps
// only; their added fields' zero values are the correct legacy behaviour.
//
// The v3 -> v4 migration (QUM-1186) is likewise a stamp. It marks state files
// written after the report axis was deleted. NOTE the ONE-WAY DATA LOSS this
// implies and which is intended: the last_report_type / _message / _at /
// _state / _detail JSON keys no longer have struct fields, so they are dropped
// silently from every existing state file on its next SaveAgent. There is no
// consumer left to read them.
func migrate(a *AgentState) bool {
	mutated := false

	if a.SchemaVersion < 1 {
		// Rewrite Status only when it holds a non-liveness legacy token.
		// QUM-1186: mapped straight onto the Status axis — there is no
		// outcome axis left to record the token in.
		switch a.Status {
		case StatusDone:
			a.Status = StatusComplete
		case "problem":
			a.Status = StatusFaulted
		case "":
			a.Status = StatusSuspended
		}

		a.SchemaVersion = 1
		mutated = true
	}

	// v1 -> v2 (QUM-851): Model and SystemPromptAppend are additive fields
	// whose zero value ("") is exactly the correct legacy behavior — type
	// default model, no prompt append. No field rewrite is needed; just stamp
	// the new version so future writers/readers agree on the schema.
	if a.SchemaVersion < 2 {
		a.SchemaVersion = 2
		mutated = true
	}

	// v2 -> v3 (QUM-899): Blurb and BlurbAt are additive fields whose zero
	// values ("" / zero time) are exactly the correct legacy behavior — no
	// blurb yet. Stamp-only, mirroring the v1→v2 step above.
	if a.SchemaVersion < 3 {
		a.SchemaVersion = 3
		mutated = true
	}

	// v3 -> v4 (QUM-1186): stamp-only. See the doc comment for the one-way
	// removal of the last_report_* keys this version marks.
	if a.SchemaVersion < 4 {
		a.SchemaVersion = 4
		mutated = true
	}

	// QUM-1186: re-classify the legacy "stopped" sentinel. ALWAYS-ON, not
	// version-gated, because older v1 writers still emit it. It maps to
	// StatusSuspended — see the doc comment for why NOT StatusFaulted.
	if a.Status == StatusStopped {
		a.Status = StatusSuspended
		mutated = true
	}

	return mutated
}

// SaveAgent writes the agent state to a JSON file in the agents directory.
// The write is atomic: data is marshaled and written to a sibling .tmp file
// first, then renamed into place. On marshal failure no disk write occurs.
func SaveAgent(sprawlRoot string, agent *AgentState) error {
	// Stamp the schema version on fresh (never-versioned) states so they
	// persist at CurrentSchemaVersion (QUM-625 M4; v2 as of QUM-851).
	if agent.SchemaVersion == 0 {
		agent.SchemaVersion = CurrentSchemaVersion
	}

	data, err := json.MarshalIndent(agent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent state: %w", err)
	}

	dir := AgentsDir(sprawlRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable agents dir is intentional
		return fmt.Errorf("creating agents directory: %w", err)
	}

	path := filepath.Join(dir, agent.Name+".json")
	// Best-effort clean any stale literal `<name>.json.tmp` file so it does
	// not leak into the agents directory (QUM-372).
	staleTmp := path + ".tmp"
	_ = os.Remove(staleTmp)
	tmp, err := os.CreateTemp(dir, agent.Name+".json.tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp agent state: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing agent state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing agent state: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil { //nolint:gosec // G302: world-readable state file is intentional
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod agent state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming agent state: %w", err)
	}
	return nil
}

// LoadAgent reads the agent state from a JSON file.
func LoadAgent(sprawlRoot string, name string) (*AgentState, error) {
	path := filepath.Join(AgentsDir(sprawlRoot), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agent state for %q: %w", name, err)
	}

	var agent AgentState
	if err := json.Unmarshal(data, &agent); err != nil {
		return nil, fmt.Errorf("parsing agent state for %q: %w", name, err)
	}
	// Migrate older (v0) files forward on read. QUM-787: when migrate
	// rewrites a field (e.g. stopped→complete/faulted) persist back to
	// disk so subsequent loads are normalized. Best-effort — a failed
	// save is logged but the in-memory migrated value is still returned
	// to the caller (preserves LoadAgent's read contract). Note: this
	// means every read-only consumer (status, peek, listing tools) will
	// trigger a disk write on first load of a legacy file; the write is
	// idempotent once the file is normalized.
	if migrate(&agent) {
		if sErr := SaveAgent(sprawlRoot, &agent); sErr != nil {
			slog.Warn("state: migrate persist-back failed", "agent", name, "err", sErr)
		}
	}
	return &agent, nil
}

// ListAgents returns all agent states from the agents directory.
func ListAgents(sprawlRoot string) ([]*AgentState, error) {
	dir := AgentsDir(sprawlRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing agents directory: %w", err)
	}

	var agents []*AgentState
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		agent, err := LoadAgent(sprawlRoot, name)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

// DeleteAgent removes the agent state file and the agent's directory under
// .sprawl/agents/<name>/, freeing the name. Removing the directory prevents
// orphaned per-agent artifacts (SYSTEM.md, prompts, tasks, activity logs)
// from accumulating across spawn/retire cycles and from being silently
// inherited when a name is reused (QUM-404).
func DeleteAgent(sprawlRoot string, name string) error {
	path := filepath.Join(AgentsDir(sprawlRoot), name+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing agent state for %q: %w", name, err)
	}
	dirPath := filepath.Join(AgentsDir(sprawlRoot), name)
	if err := os.RemoveAll(dirPath); err != nil {
		return fmt.Errorf("removing agent directory for %q: %w", name, err)
	}
	return nil
}

// StateDir returns the path to the .sprawl/state directory under the given root.
func StateDir(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "state")
}

// WriteAccentColor persists the accent color to .sprawl/state/accent-color.
func WriteAccentColor(sprawlRoot, color string) error {
	dir := StateDir(sprawlRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable state dir is intentional
		return fmt.Errorf("creating state directory: %w", err)
	}
	path := filepath.Join(dir, "accent-color")
	return os.WriteFile(path, []byte(color), 0o644) //nolint:gosec // G306: world-readable state file is intentional
}

// ReadAccentColor reads the persisted accent color from .sprawl/state/accent-color.
// Returns empty string if the file doesn't exist.
func ReadAccentColor(sprawlRoot string) string {
	path := filepath.Join(StateDir(sprawlRoot), "accent-color")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadNamespace reads the persisted namespace from .sprawl/namespace.
// Returns empty string if the file doesn't exist.
func ReadNamespace(sprawlRoot string) string {
	path := filepath.Join(sprawlRoot, ".sprawl", "namespace")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ReadRootName reads the persisted root name from .sprawl/root-name.
// Returns empty string if the file doesn't exist.
func ReadRootName(sprawlRoot string) string {
	path := filepath.Join(sprawlRoot, ".sprawl", "root-name")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteSystemPrompt writes the system prompt to .sprawl/agents/{agentName}/SYSTEM.md
// and returns the absolute path to the file.
func WriteSystemPrompt(sprawlRoot, agentName, content string) (string, error) {
	dir := filepath.Join(AgentsDir(sprawlRoot), agentName)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable agent dir is intentional
		return "", fmt.Errorf("creating agent directory: %w", err)
	}
	path := filepath.Join(dir, "SYSTEM.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: world-readable prompt file is intentional
		return "", fmt.Errorf("writing system prompt: %w", err)
	}
	return path, nil
}
