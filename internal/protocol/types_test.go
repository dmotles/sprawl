package protocol

import (
	"encoding/json"
	"testing"
)

func TestUsage_UnmarshalJSON(t *testing.T) {
	raw := `{"input_tokens":1500,"output_tokens":300,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if u.InputTokens != 1500 {
		t.Errorf("InputTokens = %d, want 1500", u.InputTokens)
	}
	if u.OutputTokens != 300 {
		t.Errorf("OutputTokens = %d, want 300", u.OutputTokens)
	}
	if u.CacheCreationInputTokens != 100 {
		t.Errorf("CacheCreationInputTokens = %d, want 100", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 50 {
		t.Errorf("CacheReadInputTokens = %d, want 50", u.CacheReadInputTokens)
	}
}

func TestUsage_UnmarshalJSON_MissingFields(t *testing.T) {
	raw := `{"input_tokens":2000,"output_tokens":500}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if u.InputTokens != 2000 {
		t.Errorf("InputTokens = %d, want 2000", u.InputTokens)
	}
	if u.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens = %d, want 0", u.CacheReadInputTokens)
	}
}

func TestUsage_UnmarshalJSON_Empty(t *testing.T) {
	raw := `{}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if u.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", u.InputTokens)
	}
	if u.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", u.OutputTokens)
	}
}
