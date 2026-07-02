package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// QUM-625 M4: agent state files gain a schema_version and LoadAgent migrates
// pre-versioned (v0) files forward on read. The legacy Status="done"/"problem"
// axis is split: outcome moves to LastReportState and Status is reduced to a
// pure liveness. These tests FAIL today because no migration runs and
// SaveAgent does not stamp the schema version.

// writeRawV0Agent writes raw JSON bytes (genuinely lacking schema_version)
// directly to the agents dir so LoadAgent sees a true v0 file.
func writeRawV0Agent(t *testing.T, root, name, rawJSON string) {
	t.Helper()
	dir := AgentsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, []byte(rawJSON), 0o644); err != nil {
		t.Fatalf("write raw v0 fixture: %v", err)
	}
}

func TestLoadAgent_MigratesV0DoneToComplete(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "a", `{"name":"a","status":"done","session_id":"s1"}`)

	got, err := LoadAgent(root, "a")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.LastReportState != "complete" {
		t.Errorf("LastReportState = %q, want %q", got.LastReportState, "complete")
	}
	// SessionID present => suspended, not stopped.
	if got.Status != "suspended" {
		t.Errorf("Status = %q, want %q", got.Status, "suspended")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadAgent_MigratesV0ProblemToFailure(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "b", `{"name":"b","status":"problem"}`)

	got, err := LoadAgent(root, "b")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.LastReportState != "failure" {
		t.Errorf("LastReportState = %q, want %q", got.LastReportState, "failure")
	}
	// No session_id => the v0→v1 step writes "stopped", then QUM-787's
	// always-on stopped→{complete,faulted} re-classification rewrites it
	// to "faulted" (LastReportState="failure" is not "complete").
	if got.Status != StatusFaulted {
		t.Errorf("Status = %q, want %q", got.Status, StatusFaulted)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadAgent_MigrateIdempotent(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "a", `{"name":"a","status":"done","session_id":"s1"}`)

	first, err := LoadAgent(root, "a")
	if err != nil {
		t.Fatalf("first LoadAgent: %v", err)
	}
	if err := SaveAgent(root, first); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	second, err := LoadAgent(root, "a")
	if err != nil {
		t.Fatalf("second LoadAgent: %v", err)
	}

	if second.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("second.SchemaVersion = %d, want %d", second.SchemaVersion, CurrentSchemaVersion)
	}
	if second.Status != first.Status {
		t.Errorf("Status unstable: first=%q second=%q", first.Status, second.Status)
	}
	if second.LastReportState != first.LastReportState {
		t.Errorf("LastReportState unstable: first=%q second=%q", first.LastReportState, second.LastReportState)
	}
}

func TestLoadAgent_MigratePreservesValidLiveness(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "c", `{"name":"c","status":"suspended"}`)

	got, err := LoadAgent(root, "c")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("Status = %q, want %q (unchanged)", got.Status, "suspended")
	}
	if got.LastReportState != "" {
		t.Errorf("LastReportState = %q, want empty (suspended is not an outcome)", got.LastReportState)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadAgent_MigrateDoesNotClobberExistingReportState(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "e", `{"name":"e","status":"done","last_report_state":"working","session_id":"s1"}`)

	got, err := LoadAgent(root, "e")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// Pre-existing last_report_state must be PRESERVED, not derived to
	// "complete" — the migration guards on empty.
	if got.LastReportState != "working" {
		t.Errorf("LastReportState = %q, want %q (preserved, not derived)", got.LastReportState, "working")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadAgent_MigratePreservesActiveCrashSurvivor(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "f", `{"name":"f","status":"active","session_id":"s1"}`)

	got, err := LoadAgent(root, "f")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// "active" is a valid liveness value; only done/problem/empty get
	// rewritten. It must NOT be demoted to "suspended".
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q (not demoted)", got.Status, "active")
	}
	if got.LastReportState != "" {
		t.Errorf("LastReportState = %q, want empty (active is not an outcome)", got.LastReportState)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

// TestCurrentSchemaVersion_IsV2 pins QUM-851: the schema version was bumped to
// 2 when Model + SystemPromptAppend were added to AgentState.
func TestCurrentSchemaVersion_IsV2(t *testing.T) {
	if CurrentSchemaVersion != 2 {
		t.Errorf("CurrentSchemaVersion = %d, want 2 (QUM-851 bump)", CurrentSchemaVersion)
	}
}

// TestLoadAgent_MigratesV1ToV2_EmptyModelAndAppend pins QUM-851: a genuine v1
// state file (schema_version=1, no model/system_prompt_append keys) loads
// cleanly, migrates forward to the current schema version, and yields the
// legacy behavior — empty Model and empty SystemPromptAppend (i.e. type-default
// model, no prompt append). The pre-existing liveness must be preserved.
func TestLoadAgent_MigratesV1ToV2_EmptyModelAndAppend(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "g", `{"name":"g","status":"complete","last_report_state":"complete","schema_version":1}`)

	got, err := LoadAgent(root, "g")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Model != "" {
		t.Errorf("Model = %q, want empty (legacy = type default)", got.Model)
	}
	if got.SystemPromptAppend != "" {
		t.Errorf("SystemPromptAppend = %q, want empty (legacy = no append)", got.SystemPromptAppend)
	}
	if got.Status != "complete" {
		t.Errorf("Status = %q, want %q (unchanged)", got.Status, "complete")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

// TestLoadAgent_MigrateV0ToV2Stepwise pins QUM-851: a genuine v0 file (no
// schema_version key) still migrates correctly through BOTH the v0→v1 Status/
// report re-classification and the v1→v2 step, ending at the current schema
// version. Guards against the version bump skipping the v0→v1 body.
func TestLoadAgent_MigrateV0ToV2Stepwise(t *testing.T) {
	root := t.TempDir()
	writeRawV0Agent(t, root, "h", `{"name":"h","status":"done","session_id":"s1"}`)

	got, err := LoadAgent(root, "h")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// v0→v1: done + session_id => suspended, LastReportState derived to complete.
	if got.Status != "suspended" {
		t.Errorf("Status = %q, want %q", got.Status, "suspended")
	}
	if got.LastReportState != "complete" {
		t.Errorf("LastReportState = %q, want %q", got.LastReportState, "complete")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestSaveAgent_StampsSchemaVersion(t *testing.T) {
	root := t.TempDir()
	if err := SaveAgent(root, &AgentState{Name: "d"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(AgentsDir(root), "d.json"))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if probe.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("persisted schema_version = %d, want %d", probe.SchemaVersion, CurrentSchemaVersion)
	}
}
