package rootinit

import "testing"

func TestModelForAgentType(t *testing.T) {
	tests := []struct {
		agentType string
		want      string
	}{
		{"manager", DefaultManagerModel},
		{"engineer", DefaultAgentModel},
		{"researcher", DefaultAgentModel},
		{"", DefaultAgentModel},
		{"something-new", DefaultAgentModel},
	}
	for _, tt := range tests {
		if got := ModelForAgentType(tt.agentType); got != tt.want {
			t.Errorf("ModelForAgentType(%q) = %q, want %q", tt.agentType, got, tt.want)
		}
	}
}

func TestModelConstants(t *testing.T) {
	if DefaultRootModel != "opus[1m]" {
		t.Errorf("DefaultRootModel = %q, want %q", DefaultRootModel, "opus[1m]")
	}
	if DefaultManagerModel != "opus[1m]" {
		t.Errorf("DefaultManagerModel = %q, want %q", DefaultManagerModel, "opus[1m]")
	}
	if DefaultAgentModel != "opus" {
		t.Errorf("DefaultAgentModel = %q, want %q", DefaultAgentModel, "opus")
	}
}
