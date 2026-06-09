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

// TestAssistantMessage_ParseUsage covers QUM-368: the inline `usage` + `model`
// blob inside AssistantMessage.Content must be extractable via ParseUsage.
func TestAssistantMessage_ParseUsage(t *testing.T) {
	// Verbatim from QUM-368 issue body (the example stream-json line).
	content := `{"model":"claude-opus-4-7","usage":{"input_tokens":6,"cache_creation_input_tokens":14083,"cache_read_input_tokens":12066,"output_tokens":8,"service_tier":"standard"}}`
	m := &AssistantMessage{Content: []byte(content)}
	u, model, err := m.ParseUsage()
	if err != nil {
		t.Fatalf("ParseUsage: %v", err)
	}
	if u == nil {
		t.Fatal("ParseUsage returned nil Usage")
	}
	if model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", model)
	}
	if u.InputTokens != 6 || u.OutputTokens != 8 ||
		u.CacheReadInputTokens != 12066 || u.CacheCreationInputTokens != 14083 {
		t.Errorf("usage = %+v, want input=6 output=8 read=12066 creation=14083", u)
	}
}

func TestAssistantMessage_ParseUsage_EmptyContent(t *testing.T) {
	m := &AssistantMessage{}
	u, model, err := m.ParseUsage()
	if err != nil {
		t.Errorf("expected nil error for empty content, got %v", err)
	}
	if u != nil {
		t.Errorf("expected nil Usage for empty content, got %+v", u)
	}
	if model != "" {
		t.Errorf("expected empty model for empty content, got %q", model)
	}
}

func TestAssistantMessage_ParseUsage_MalformedContent(t *testing.T) {
	m := &AssistantMessage{Content: []byte("not valid json {{{")}
	_, _, err := m.ParseUsage()
	if err == nil {
		t.Fatal("expected non-nil error for malformed Content")
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
