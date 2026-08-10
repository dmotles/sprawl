package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// QUM-625 M4: agent state files gain a schema_version and LoadAgent migrates
// pre-versioned (v0) files forward on read.
//
// QUM-1186 changed the TARGET of that migration. The outcome axis
// (LastReportState) is deleted, so the legacy Status tokens map straight onto
// the Status axis instead of being split across two fields:
//
//	"done"    -> StatusComplete
//	"problem" -> StatusFaulted
//	""        -> StatusSuspended
//	"stopped" -> StatusSuspended   (always-on, not version-gated)
//
// The "stopped" row is the one that changed most and matters most: it used to
// split on LastReportState and DEFAULT TO StatusFaulted. Defaulting a legacy
// clean stop to `faulted` would reproduce on the read path exactly the bug
// QUM-1186 D3 removed from the write path — and `faulted` is outside the
// auto-resume accept-set, so the mislabel would cost the agent on next startup.
//
// Every case below drives a GENUINE raw FILE through LoadAgent
// (writeRawAgentJSON), not a hand-built struct: the whole risk in a migration
// is the shape on disk, and a struct literal cannot fail the way a real file
// can — SaveAgent stamps CurrentSchemaVersion, so a struct fixture silently
// skips the version gate.
//
// Most rows omit schema_version entirely and are therefore true v0 files. A few
// carry an EXPLICIT schema_version, deliberately, to exercise the gate from the
// other side — see
// TestLoadAgent_LegacyTokenRewriteIsVersionGated_StoppedIsNot.

// writeRawAgentJSON writes raw JSON bytes directly to the agents dir, so
// LoadAgent sees exactly the shape given — including whatever schema_version
// the caller did or did not supply. Omit the key for a true v0 fixture.
func writeRawAgentJSON(t *testing.T, root, name, rawJSON string) {
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
	writeRawAgentJSON(t, root, "a", `{"name":"a","status":"done","session_id":"s1"}`)

	got, err := LoadAgent(root, "a")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// The legacy token means "finished"; that information is preserved on the
	// Status axis rather than discarded with the outcome field.
	if got.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", got.Status, StatusComplete)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

// TestLoadAgent_MigratesV0StoppedToSuspendedNotFaulted is the load-bearing
// row. A legacy file whose Status is the "stopped" sentinel and which carries
// NO evidence of an outcome must rest at StatusSuspended.
//
// It must NOT become StatusFaulted: that is outside the auto-resume accept-set
// (internal/supervisor RecoverAgents), so a clean legacy stop would silently
// stop coming back after a `sprawl enter` restart.
func TestLoadAgent_MigratesV0StoppedToSuspendedNotFaulted(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "s0", `{"name":"s0","status":"stopped","session_id":"s1"}`)

	got, err := LoadAgent(root, "s0")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != StatusSuspended {
		t.Errorf("Status = %q, want %q (a clean stop nobody labelled is not a fault)", got.Status, StatusSuspended)
	}
}

// TestLoadAgent_MigratesStoppedEvenOnVersionedFile pins that the stopped
// rewrite is ALWAYS-ON rather than gated on schema_version < 1: older v1
// writers still emitted the sentinel, so a version-gated rewrite would leave
// it on disk forever.
func TestLoadAgent_MigratesStoppedEvenOnVersionedFile(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "s1", `{"name":"s1","status":"stopped","schema_version":3}`)

	got, err := LoadAgent(root, "s1")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != StatusSuspended {
		t.Errorf("Status = %q, want %q on an already-versioned file", got.Status, StatusSuspended)
	}
}

// TestLoadAgent_MigratesV0EmptyStatusToSuspended covers the third legacy
// token: no status at all.
func TestLoadAgent_MigratesV0EmptyStatusToSuspended(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "e0", `{"name":"e0"}`)

	got, err := LoadAgent(root, "e0")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != StatusSuspended {
		t.Errorf("Status = %q, want %q", got.Status, StatusSuspended)
	}
}

// TestLoadAgent_DropsLegacyReportKeys pins the ONE-WAY DATA LOSS this schema
// bump intends: the last_report_* keys have no struct fields left, so they are
// dropped from every state file on its next save. Asserted rather than assumed
// — silent field loss should be deliberate.
func TestLoadAgent_DropsLegacyReportKeys(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "lr", `{"name":"lr","status":"active","schema_version":3,`+
		`"last_report_state":"working","last_report_message":"halfway","last_report_at":"2026-06-06T12:00:00Z"}`)

	got, err := LoadAgent(root, "lr")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if err := SaveAgent(root, got); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(AgentsDir(root), "lr.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, key := range []string{"last_report_state", "last_report_message", "last_report_at", "last_report_type", "last_report_detail"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("re-saved state file still contains %q:\n%s", key, raw)
		}
	}
	// Non-report fields must survive the round trip — the loss is scoped.
	if got.Status != "active" || got.Name != "lr" {
		t.Errorf("unrelated fields damaged: Name=%q Status=%q", got.Name, got.Status)
	}
}

func TestLoadAgent_MigratesV0ProblemToFailure(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "b", `{"name":"b","status":"problem"}`)

	got, err := LoadAgent(root, "b")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// QUM-1186: "problem" maps straight to StatusFaulted on the Status axis.
	if got.Status != StatusFaulted {
		t.Errorf("Status = %q, want %q", got.Status, StatusFaulted)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadAgent_MigrateIdempotent(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "a", `{"name":"a","status":"done","session_id":"s1"}`)

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
}

func TestLoadAgent_MigratePreservesValidLiveness(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "c", `{"name":"c","status":"suspended"}`)

	got, err := LoadAgent(root, "c")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("Status = %q, want %q (unchanged)", got.Status, "suspended")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

// QUM-1186: TestLoadAgent_MigrateDoesNotClobberExistingReportState was
// removed here. It pinned that an explicit last_report_state on disk was
// preserved rather than re-derived from the legacy Status token. There is no
// longer a field to preserve; TestLoadAgent_DropsLegacyReportKeys above pins
// the replacement behaviour (the keys are dropped, deliberately).

func TestLoadAgent_MigratePreservesActiveCrashSurvivor(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "f", `{"name":"f","status":"active","session_id":"s1"}`)

	got, err := LoadAgent(root, "f")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// "active" is a valid liveness value; only done/problem/empty get
	// rewritten. It must NOT be demoted to "suspended".
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q (not demoted)", got.Status, "active")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

// TestCurrentSchemaVersion_IsV4 pins QUM-1186: the schema version was bumped
// to 4 when the last_report_* fields were removed from AgentState. The stamp
// is what marks a file as written after the report axis was deleted.
func TestCurrentSchemaVersion_IsV4(t *testing.T) {
	if CurrentSchemaVersion != 4 {
		t.Errorf("CurrentSchemaVersion = %d, want 4 (QUM-1186 bump)", CurrentSchemaVersion)
	}
}

// TestLoadAgent_MigratesV2ToV3_EmptyBlurb pins QUM-899: a genuine v2 state file
// (schema_version=2, no blurb/blurb_at keys) loads cleanly, migrates forward to
// the current schema version, and yields the legacy behavior — empty Blurb and
// zero BlurbAt. Other fields must be preserved.
func TestLoadAgent_MigratesV2ToV3_EmptyBlurb(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "b3", `{"name":"b3","status":"running","model":"opus","schema_version":2}`)

	got, err := LoadAgent(root, "b3")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Blurb != "" {
		t.Errorf("Blurb = %q, want empty (legacy)", got.Blurb)
	}
	if !got.BlurbAt.IsZero() {
		t.Errorf("BlurbAt = %v, want zero (legacy)", got.BlurbAt)
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want %q (unchanged)", got.Status, "running")
	}
	if got.Model != "opus" {
		t.Errorf("Model = %q, want %q (unchanged)", got.Model, "opus")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
	}
}

// TestLoadAgent_MigratesV1ToV2_EmptyModelAndAppend pins QUM-851: a genuine v1
// state file (schema_version=1, no model/system_prompt_append keys) loads
// cleanly, migrates forward to the current schema version, and yields the
// legacy behavior — empty Model and empty SystemPromptAppend (i.e. type-default
// model, no prompt append). The pre-existing liveness must be preserved.
func TestLoadAgent_MigratesV1ToV2_EmptyModelAndAppend(t *testing.T) {
	root := t.TempDir()
	writeRawAgentJSON(t, root, "g", `{"name":"g","status":"complete","last_report_state":"complete","schema_version":1}`)

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
	writeRawAgentJSON(t, root, "h", `{"name":"h","status":"done","session_id":"s1"}`)

	got, err := LoadAgent(root, "h")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	// QUM-1186: v0→v1 maps "done" straight to StatusComplete.
	if got.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", got.Status, StatusComplete)
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

// TestLoadAgent_LegacyTokenRewriteIsVersionGated_StoppedIsNot pins the
// asymmetry between the two legacy-token rewrites, because that asymmetry is
// what decides how "done" may be classified elsewhere in the tree.
//
// "stopped" is rewritten unconditionally; "done" is rewritten only below
// schema_version 1. So "done" is NOT unreachable the way "stopped" is: any file
// at v1 or above carrying "done" survives the read verbatim — and that is
// exactly what a SaveAgent-built fixture is, since SaveAgent stamps
// CurrentSchemaVersion, so the gate never fires.
//
// This matters at internal/agentops merge precondition 4, where a reader might
// otherwise conclude "done" belongs in the unreachable bucket beside "stopped"
// and write a proof aimed at a gate that does not hold. "done" IS unreachable
// in production, but for a different reason: QUM-615 (bd024ab) introduced
// CurrentSchemaVersion and deleted the last writer of "done" (report.go) in the
// SAME commit, so no binary has ever emitted "done" at schema_version >= 1, and
// every file that could carry it is v0 and is migrated below.
//
// Driven through genuine raw files and LoadAgent, per this file's header — not
// migrate() directly. The v1 row is the boundary the classification argument
// actually rests on; it is written as a literal 1 rather than a constant so it
// cannot drift away from the gate on the next schema bump.
func TestLoadAgent_LegacyTokenRewriteIsVersionGated_StoppedIsNot(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"v0 done is rewritten", `{"name":"n","status":"done"}`, StatusComplete},
		{"v0 stopped is rewritten", `{"name":"n","status":"stopped"}`, StatusSuspended},
		{
			"v1 done SURVIVES — the gate is version-scoped, and v1 is its boundary",
			`{"name":"n","status":"done","schema_version":1}`, StatusDone,
		},
		{
			"v-current done SURVIVES",
			fmt.Sprintf(`{"name":"n","status":"done","schema_version":%d}`, CurrentSchemaVersion), StatusDone,
		},
		{
			"v-current stopped is STILL rewritten — that rewrite is always-on",
			fmt.Sprintf(`{"name":"n","status":"stopped","schema_version":%d}`, CurrentSchemaVersion), StatusSuspended,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeRawAgentJSON(t, root, "n", tc.raw)

			got, err := LoadAgent(root, "n")
			if err != nil {
				t.Fatalf("LoadAgent: %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("LoadAgent(%s).Status = %q, want %q", tc.raw, got.Status, tc.want)
			}
		})
	}
}
