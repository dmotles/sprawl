package commands

import (
	"testing"
)

func TestAll_ReturnsFourCommandsInStableOrder(t *testing.T) {
	cmds := All()
	if len(cmds) != 4 {
		t.Fatalf("All() len = %d, want 4", len(cmds))
	}
	want := []string{"/exit", "/help", "/handoff", "/switch"}
	for i, w := range want {
		if cmds[i].Name != w {
			t.Errorf("All()[%d].Name = %q, want %q", i, cmds[i].Name, w)
		}
	}
}

func TestAll_SwitchIsAgentSwitchKind(t *testing.T) {
	var s *Command
	for _, c := range All() {
		if c.Name == "/switch" {
			cc := c
			s = &cc
			break
		}
	}
	if s == nil {
		t.Fatal("/switch not found")
	}
	if s.Kind != KindAgentSwitch {
		t.Errorf("/switch Kind = %v, want KindAgentSwitch", s.Kind)
	}
	if s.Description == "" {
		t.Error("/switch Description is empty")
	}
}

func TestFuzzyMatchAgents(t *testing.T) {
	agents := []string{"weave", "finn", "ghost", "ratz", "oak"}
	cases := []struct {
		query string
		want  []string
	}{
		{"", agents},             // empty returns all in order
		{"fi", []string{"finn"}}, // prefix match
		{"fn", []string{"finn"}}, // subsequence match (issue doc example)
		{"oa", []string{"oak"}},
		{"zz", []string{}},           // no match
		{"WEAVE", []string{"weave"}}, // case-insensitive
		{"at", []string{"ratz"}},     // subsequence in middle
	}
	for _, tc := range cases {
		got := FuzzyMatchAgents(tc.query, agents)
		if len(got) != len(tc.want) {
			t.Errorf("FuzzyMatchAgents(%q) = %v, want %v", tc.query, got, tc.want)
			continue
		}
		for i, w := range tc.want {
			if got[i] != w {
				t.Errorf("FuzzyMatchAgents(%q)[%d] = %q, want %q", tc.query, i, got[i], w)
			}
		}
	}
}

func TestFuzzyMatchAgents_NilNamesReturnsEmpty(t *testing.T) {
	got := FuzzyMatchAgents("fi", nil)
	if len(got) != 0 {
		t.Errorf("FuzzyMatchAgents(_, nil) = %v, want empty", got)
	}
}

func TestAll_EachCommandHasDescription(t *testing.T) {
	for _, c := range All() {
		if c.Description == "" {
			t.Errorf("command %q has empty Description", c.Name)
		}
	}
}

func TestAll_ExitAndHelpAreUIKind(t *testing.T) {
	byName := map[string]Command{}
	for _, c := range All() {
		byName[c.Name] = c
	}
	if byName["/exit"].Kind != KindUI {
		t.Errorf("/exit Kind = %v, want KindUI", byName["/exit"].Kind)
	}
	if byName["/exit"].Action != ActionQuit {
		t.Errorf("/exit Action = %v, want ActionQuit", byName["/exit"].Action)
	}
	if byName["/help"].Kind != KindUI {
		t.Errorf("/help Kind = %v, want KindUI", byName["/help"].Kind)
	}
	if byName["/help"].Action != ActionToggleHelp {
		t.Errorf("/help Action = %v, want ActionToggleHelp", byName["/help"].Action)
	}
}

func TestAll_HandoffIsPromptInjectionWithTemplate(t *testing.T) {
	var h *Command
	for _, c := range All() {
		if c.Name == "/handoff" {
			cc := c
			h = &cc
			break
		}
	}
	if h == nil {
		t.Fatal("/handoff not found")
	}
	if h.Kind != KindPromptInjection {
		t.Errorf("/handoff Kind = %v, want KindPromptInjection", h.Kind)
	}
	if h.PromptTemplate == "" {
		t.Error("/handoff PromptTemplate is empty")
	}
	if h.PromptTemplate != HandoffPromptTemplate {
		t.Error("/handoff PromptTemplate should equal HandoffPromptTemplate const")
	}
}

func TestFilter_EmptyReturnsAll(t *testing.T) {
	cmds := Filter("")
	if len(cmds) != len(All()) {
		t.Errorf("Filter(\"\") len = %d, want %d", len(cmds), len(All()))
	}
}

func TestFilter_PrefixMatchesCaseInsensitive(t *testing.T) {
	cases := []struct {
		filter string
		want   []string
	}{
		{"h", []string{"/help", "/handoff"}},
		{"ha", []string{"/handoff"}},
		{"HA", []string{"/handoff"}},
		{"e", []string{"/exit"}},
		{"x", []string{}},
		{"help", []string{"/help"}},
		{"s", []string{"/switch"}},
		{"sw", []string{"/switch"}},
	}
	for _, tc := range cases {
		got := Filter(tc.filter)
		if len(got) != len(tc.want) {
			t.Errorf("Filter(%q) len = %d (%v), want %d (%v)",
				tc.filter, len(got), names(got), len(tc.want), tc.want)
			continue
		}
		for i, w := range tc.want {
			if got[i].Name != w {
				t.Errorf("Filter(%q)[%d] = %q, want %q", tc.filter, i, got[i].Name, w)
			}
		}
	}
}

func TestFilter_DoesNotMatchLeadingSlash(t *testing.T) {
	// User types `/` then filter, which is passed sans slash. A filter of "/"
	// should be interpreted as a literal character, not the name prefix —
	// but since no command name sans-slash starts with "/", this yields empty.
	got := Filter("/")
	if len(got) != 0 {
		t.Errorf("Filter(\"/\") len = %d, want 0", len(got))
	}
}

func names(cs []Command) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

func TestHandoffPromptTemplate_NonEmptyAndReferencesMCPTool(t *testing.T) {
	if HandoffPromptTemplate == "" {
		t.Fatal("HandoffPromptTemplate is empty")
	}
	if !contains(HandoffPromptTemplate, "sprawl_handoff") {
		t.Error("HandoffPromptTemplate should reference the sprawl_handoff MCP tool")
	}
	if !contains(HandoffPromptTemplate, "/handoff") {
		t.Error("HandoffPromptTemplate should mention /handoff palette invocation")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
