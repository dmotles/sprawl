package commands

import (
	"sort"
	"testing"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{name: "no-arg command", input: "/help", wantName: "/help", wantArgs: "", wantOK: true},
		{name: "attach with args", input: `/attach /tmp/x.png "hi"`, wantName: "/attach", wantArgs: `/tmp/x.png "hi"`, wantOK: true},
		{name: "switch with name", input: "/switch weav", wantName: "/switch", wantArgs: "weav", wantOK: true},
		{name: "tab delimiter", input: "/switch\tweav", wantName: "/switch", wantArgs: "weav", wantOK: true},
		{name: "internal multi-space collapses to trimmed args", input: "/attach   /tmp/x.png", wantName: "/attach", wantArgs: "/tmp/x.png", wantOK: true},
		{name: "no-arg command with trailing text still matches", input: "/help extra", wantName: "/help", wantArgs: "extra", wantOK: true},
		{name: "leading/trailing whitespace trimmed", input: "  /help  ", wantName: "/help", wantArgs: "", wantOK: true},
		{name: "case-insensitive token match", input: "/HELP", wantName: "/help", wantArgs: "", wantOK: true},
		{name: "unknown slash command", input: "/nope", wantOK: false},
		{name: "non-slash passthrough", input: "hello world", wantOK: false},
		{name: "slash mid-token passthrough", input: "not/a/command", wantOK: false},
		{name: "bare slash", input: "/", wantOK: false},
		{name: "empty", input: "", wantOK: false},
		{name: "unregistered leading slash prose", input: "/etc/hosts is broken", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, args, ok := Match(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("Match(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if cmd.Name != tt.wantName {
				t.Errorf("Match(%q) name = %q, want %q", tt.input, cmd.Name, tt.wantName)
			}
			if args != tt.wantArgs {
				t.Errorf("Match(%q) args = %q, want %q", tt.input, args, tt.wantArgs)
			}
		})
	}
}

func TestFilterSorted(t *testing.T) {
	// Empty prefix → all commands, alphabetical.
	all := FilterSorted("")
	if len(all) != len(All()) {
		t.Fatalf("FilterSorted(\"\") len = %d, want %d", len(all), len(All()))
	}
	names := make([]string, len(all))
	for i, c := range all {
		names[i] = c.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("FilterSorted(\"\") not alphabetical: %v", names)
	}
	// Prefix filter (name without leading slash), alphabetical.
	h := FilterSorted("h")
	if len(h) < 2 {
		t.Fatalf("FilterSorted(h) = %v, want at least /handoff and /help", h)
	}
	if h[0].Name != "/handoff" || h[1].Name != "/help" {
		t.Errorf("FilterSorted(h) = %q,%q; want /handoff,/help", h[0].Name, h[1].Name)
	}
	// No match → empty.
	if got := FilterSorted("zzz"); len(got) != 0 {
		t.Errorf("FilterSorted(zzz) = %v, want empty", got)
	}
}

func TestTakesArgs(t *testing.T) {
	want := map[string]bool{
		"/exit":    false,
		"/help":    false,
		"/tree":    false,
		"/handoff": false,
		"/usage":   false,
		"/switch":  true,
		"/attach":  true,
	}
	got := make(map[string]bool)
	for _, c := range All() {
		got[c.Name] = c.TakesArgs
	}
	if len(got) != len(want) {
		t.Fatalf("command count = %d, want %d", len(got), len(want))
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s TakesArgs = %v, want %v", name, got[name], w)
		}
	}
}

func TestAllSorted_IsAlphabeticalAndAllUnchanged(t *testing.T) {
	sorted := AllSorted()
	if len(sorted) != len(All()) {
		t.Fatalf("AllSorted() len = %d, want %d", len(sorted), len(All()))
	}
	names := make([]string, len(sorted))
	for i, c := range sorted {
		names[i] = c.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("AllSorted() names not alphabetical: %v", names)
	}
	// All() must remain in registration order (palette depends on it).
	wantOrder := []string{"/exit", "/help", "/tree", "/handoff", "/usage", "/switch", "/attach"}
	got := All()
	for i, w := range wantOrder {
		if got[i].Name != w {
			t.Errorf("All()[%d] = %q, want %q (registration order must be unchanged)", i, got[i].Name, w)
		}
	}
}
