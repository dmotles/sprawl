package blurb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/memory"
)

// fakeInvoker is a recording memory.ClaudeInvoker for unit tests — no real
// subprocess is ever launched.
type fakeInvoker struct {
	resp       string
	err        error
	calls      int
	lastPrompt string
	lastOpts   int
}

func (f *fakeInvoker) Invoke(_ context.Context, prompt string, opts ...memory.InvokeOption) (string, error) {
	f.calls++
	f.lastPrompt = prompt
	f.lastOpts = len(opts)
	return f.resp, f.err
}

var _ memory.ClaudeInvoker = (*fakeInvoker)(nil)

func TestDecideTrigger(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   TriggerInput
		want TriggerKind
	}{
		{
			name: "root never gets a blurb",
			in:   TriggerInput{IsRoot: true, HasBlurb: false, Now: base},
			want: TriggerNone,
		},
		{
			name: "no blurb yet triggers initial (catch-up)",
			in:   TriggerInput{HasBlurb: false, Now: base},
			want: TriggerInitial,
		},
		{
			name: "dirty and floor elapsed triggers refresh",
			in: TriggerInput{
				HasBlurb:       true,
				BlurbAt:        base.Add(-20 * time.Minute),
				LastActivityAt: base.Add(-1 * time.Minute),
				Now:            base,
			},
			want: TriggerRefresh,
		},
		{
			name: "dirty and exactly at floor triggers refresh (>= boundary)",
			in: TriggerInput{
				HasBlurb:       true,
				BlurbAt:        base.Add(-RefreshFloor),
				LastActivityAt: base.Add(-1 * time.Minute),
				Now:            base,
			},
			want: TriggerRefresh,
		},
		{
			name: "dirty but within floor does not refresh",
			in: TriggerInput{
				HasBlurb:       true,
				BlurbAt:        base.Add(-5 * time.Minute),
				LastActivityAt: base.Add(-1 * time.Minute),
				Now:            base,
			},
			want: TriggerNone,
		},
		{
			name: "floor elapsed but idle (no new activity) does not refresh",
			in: TriggerInput{
				HasBlurb:       true,
				BlurbAt:        base.Add(-30 * time.Minute),
				LastActivityAt: base.Add(-40 * time.Minute),
				Now:            base,
			},
			want: TriggerNone,
		},
		{
			name: "activity exactly at blurb watermark is not dirty",
			in: TriggerInput{
				HasBlurb:       true,
				BlurbAt:        base.Add(-30 * time.Minute),
				LastActivityAt: base.Add(-30 * time.Minute),
				Now:            base,
			},
			want: TriggerNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideTrigger(tc.in); got != tc.want {
				t.Errorf("DecideTrigger = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestToolHistogram(t *testing.T) {
	entries := []agentloop.ActivityEntry{
		{Kind: "tool_use", Tool: "Edit"},
		{Kind: "tool_use", Tool: "Edit"},
		{Kind: "tool_use", Tool: "Bash"},
		{Kind: "assistant_text", Summary: "thinking"},
		{Kind: "tool_use", Tool: ""}, // no tool name — skipped
	}
	got := ToolHistogram(entries)
	if got["Edit"] != 2 {
		t.Errorf("Edit = %d, want 2", got["Edit"])
	}
	if got["Bash"] != 1 {
		t.Errorf("Bash = %d, want 1", got["Bash"])
	}
	if _, ok := got[""]; ok {
		t.Errorf("empty tool name should not be counted")
	}
	if len(got) != 2 {
		t.Errorf("histogram size = %d, want 2", len(got))
	}
}

func TestExtractLinearKeys(t *testing.T) {
	got := ExtractLinearKeys(
		"working on QUM-899 and QUM-786",
		"also touched QUM-899 again",
		"no keys here",
	)
	want := []string{"QUM-786", "QUM-899"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ExtractLinearKeys = %v, want %v (sorted, deduped)", got, want)
	}
}

func TestActivityDelta_FiltersBySince(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	entries := []agentloop.ActivityEntry{
		{TS: t0, Summary: "old"},
		{TS: t0.Add(5 * time.Minute), Summary: "boundary"},
		{TS: t0.Add(10 * time.Minute), Summary: "new"},
	}
	delta, omitted := ActivityDelta(entries, t0.Add(5*time.Minute))
	if omitted != 0 {
		t.Errorf("omitted = %d, want 0", omitted)
	}
	if len(delta) != 1 || delta[0].Summary != "new" {
		t.Errorf("delta = %+v, want only the entry strictly after since", delta)
	}
}

func TestActivityDelta_ZeroSinceReturnsAll(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	entries := []agentloop.ActivityEntry{
		{TS: t0, Summary: "a"},
		{TS: t0.Add(time.Minute), Summary: "b"},
	}
	delta, omitted := ActivityDelta(entries, time.Time{})
	if omitted != 0 || len(delta) != 2 {
		t.Errorf("ActivityDelta(zero) = %d entries omitted=%d, want 2/0", len(delta), omitted)
	}
}

func TestActivityDelta_GenerousCapOmitsOldest(t *testing.T) {
	t0 := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	n := MaxDeltaEntries + 25
	entries := make([]agentloop.ActivityEntry, n)
	for i := range entries {
		entries[i] = agentloop.ActivityEntry{TS: t0.Add(time.Duration(i) * time.Second), Summary: "e"}
	}
	delta, omitted := ActivityDelta(entries, time.Time{})
	if len(delta) != MaxDeltaEntries {
		t.Errorf("delta len = %d, want %d (capped)", len(delta), MaxDeltaEntries)
	}
	if omitted != 25 {
		t.Errorf("omitted = %d, want 25", omitted)
	}
	// The retained window must be the most-recent entries, not the oldest.
	if !delta[len(delta)-1].TS.Equal(entries[n-1].TS) {
		t.Errorf("expected newest entry retained")
	}
}

func TestEnforceCap_LimitsSentences(t *testing.T) {
	got := EnforceCap("One thing. Two thing. Three thing. Four thing. Five thing.")
	if got != "One thing. Two thing. Three thing." {
		t.Errorf("EnforceCap sentences = %q", got)
	}
}

func TestEnforceCap_HardCharCap(t *testing.T) {
	long := strings.Repeat("word ", 200) // ~1000 chars, no sentence terminator
	got := EnforceCap(long)
	// +2 tolerance accounts for the trailing ellipsis rune added after the
	// word-boundary truncation.
	if len([]rune(got)) > MaxBlurbChars+2 {
		t.Errorf("EnforceCap len = %d runes, want <= %d", len([]rune(got)), MaxBlurbChars+2)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected truncation ellipsis, got %q", got)
	}
}

func TestEnforceCap_EmptyStaysEmpty(t *testing.T) {
	if got := EnforceCap("   \n  "); got != "" {
		t.Errorf("EnforceCap(blank) = %q, want empty", got)
	}
}

func TestBuildPrompt_InitialIncludesRoleAndPrompt(t *testing.T) {
	s := Signals{
		AgentName: "ratz",
		Role:      "engineer / engineering",
		Prompt:    "Implement QUM-899 blurb feature",
	}
	p := BuildPrompt(TriggerInitial, s)
	for _, want := range []string{"ratz", "engineer / engineering", "Implement QUM-899 blurb feature"} {
		if !strings.Contains(p, want) {
			t.Errorf("initial prompt missing %q\n---\n%s", want, p)
		}
	}
}

func TestBuildPrompt_RefreshIncludesPrevBlurbDeltaAndSignals(t *testing.T) {
	s := Signals{
		AgentName:    "ratz",
		Role:         "engineer / engineering",
		PrevBlurb:    "Previously wired the heartbeat.",
		Delta:        []agentloop.ActivityEntry{{Kind: "tool_use", Tool: "Edit", Summary: "Edit state.go"}},
		OmittedDelta: 12,
		GitDiffStat:  " internal/blurb/blurb.go | 40 ++++",
		Tasks:        []string{"implement blurb (in_progress)"},
		LinearKeys:   []string{"QUM-899"},
	}
	p := BuildPrompt(TriggerRefresh, s)
	for _, want := range []string{
		"Previously wired the heartbeat.",
		"Edit state.go",
		"internal/blurb/blurb.go",
		"implement blurb (in_progress)",
		"QUM-899",
		"12", // omitted-count note, not a silent drop
	} {
		if !strings.Contains(p, want) {
			t.Errorf("refresh prompt missing %q\n---\n%s", want, p)
		}
	}
}

func TestGenerate_CapsOutputAndPassesModel(t *testing.T) {
	inv := &fakeInvoker{resp: "One. Two. Three. Four. Five."}
	got, err := Generate(context.Background(), inv, "haiku", 0, TriggerInitial, Signals{AgentName: "x", Role: "engineer"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "One. Two. Three." {
		t.Errorf("Generate = %q, want capped to 3 sentences", got)
	}
	if inv.calls != 1 {
		t.Errorf("invoker calls = %d, want 1", inv.calls)
	}
	// The resolved model must be threaded through as a WithModel option.
	if inv.lastOpts != 1 {
		t.Errorf("invoke opts = %d, want 1 (WithModel)", inv.lastOpts)
	}
}

func TestGenerate_EmptyResponseYieldsEmptyNoError(t *testing.T) {
	inv := &fakeInvoker{resp: "   "}
	got, err := Generate(context.Background(), inv, "", 0, TriggerRefresh, Signals{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "" {
		t.Errorf("Generate = %q, want empty (caller keeps previous blurb)", got)
	}
}

func TestGenerate_PropagatesInvokerError(t *testing.T) {
	inv := &fakeInvoker{err: errors.New("boom")}
	_, err := Generate(context.Background(), inv, "", 0, TriggerRefresh, Signals{})
	if err == nil {
		t.Fatalf("expected error propagation")
	}
}
