package usage

import (
	"encoding/json"
	"testing"
)

// TestRecord_AllKeysPresentOnZeroValue asserts every field of Record is
// emitted on JSON marshal even when the value is the type's zero — i.e.
// none of the tags use omitempty. Downstream tooling relies on stable
// column presence (QUM-368).
func TestRecord_AllKeysPresentOnZeroValue(t *testing.T) {
	var r Record
	b, err := json.Marshal(&r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	required := []string{
		"timestamp",
		"agent_name",
		"agent_type",
		"agent_family",
		"parent_name",
		"session_id",
		"branch",
		"model",
		"input_tokens",
		"output_tokens",
		"cache_read_input_tokens",
		"cache_creation_input_tokens",
		"total_cost_usd",
	}
	for _, key := range required {
		if _, ok := got[key]; !ok {
			t.Errorf("required key %q missing from zero-value record JSON: %s", key, string(b))
		}
	}
}
