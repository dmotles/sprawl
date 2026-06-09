package tui

// QUM-721 — failing tests for the /usage slash command modal.
//
// These tests are red on current code (the implementation does not yet exist)
// and define the public contract that the implementer must satisfy.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/usage"
)

const usageDisclaimer = `API-reported; doesn't reflect subscription credits (Claude Max etc.)`

func newTestUsageModalModel(t *testing.T) UsageModalModel {
	t.Helper()
	theme := NewTheme("colour212")
	m := NewUsageModalModel(&theme)
	m = m.SetSize(120, 40)
	return m
}

func usageFixture() map[string]usage.TokenTotals {
	return map[string]usage.TokenTotals{
		"weave": {
			InputTokens:              421000,
			OutputTokens:             12300,
			CacheReadInputTokens:     980000,
			CacheCreationInputTokens: 34000,
			TotalCostUsd:             0.21,
		},
		"finn": {
			InputTokens:              298000,
			OutputTokens:             8900,
			CacheReadInputTokens:     2100000,
			CacheCreationInputTokens: 24000,
			TotalCostUsd:             0.43,
		},
	}
}

func TestUsageModal_InitiallyHidden(t *testing.T) {
	m := newTestUsageModalModel(t)
	if m.Visible() {
		t.Error("new usage modal should be hidden")
	}
}

func TestUsageModal_ShowMakesVisible(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Show()
	if !m.Visible() {
		t.Error("Show() should set visible")
	}
}

func TestUsageModal_HideClearsVisible(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Show()
	m = m.Hide()
	if m.Visible() {
		t.Error("Hide() should clear visible")
	}
}

func TestUsageModal_InstallSetsDefaultViewTokens(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture())
	if m.CurrentView() != usageViewTokens {
		t.Errorf("default view after Install = %v, want usageViewTokens", m.CurrentView())
	}
}

func TestUsageModal_KeyCyclesViewTokens(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	// First switch to cost view.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'c'})
	if m.CurrentView() != usageViewCost {
		t.Fatalf("setup: 'c' did not switch to cost, got %v", m.CurrentView())
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 't'})
	if m.CurrentView() != usageViewTokens {
		t.Errorf("'t' should select tokens view, got %v", m.CurrentView())
	}
}

func TestUsageModal_KeyCyclesViewCost(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'c'})
	if m.CurrentView() != usageViewCost {
		t.Errorf("'c' should select cost view, got %v", m.CurrentView())
	}
}

func TestUsageModal_KeyCyclesViewAll(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a'})
	if m.CurrentView() != usageViewAll {
		t.Errorf("'a' should select all view, got %v", m.CurrentView())
	}
}

func TestUsageModal_EscEmitsDismiss(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc should emit a cmd")
	}
	if _, ok := cmd().(DismissUsageMsg); !ok {
		t.Errorf("Esc returned %T, want DismissUsageMsg", cmd())
	}
}

func TestUsageModal_QEmitsDismiss(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q'})
	if cmd == nil {
		t.Fatal("q should emit a cmd")
	}
	if _, ok := cmd().(DismissUsageMsg); !ok {
		t.Errorf("q returned %T, want DismissUsageMsg", cmd())
	}
}

func TestUsageModal_RenderHiddenIsEmpty(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture())
	v := m.View()
	if strings.TrimSpace(v) != "" {
		t.Errorf("View() hidden should be empty, got %q", v)
	}
}

func TestUsageModal_RenderTokensListsAgents(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	v := stripAnsi(m.View())
	for _, want := range []string{"weave", "finn", "TOTAL"} {
		if !strings.Contains(v, want) {
			t.Errorf("View() missing %q\n%s", want, v)
		}
	}
}

func TestUsageModal_TokensSortedByInputTokensDesc(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	v := stripAnsi(m.View())
	wi := strings.Index(v, "weave")
	fi := strings.Index(v, "finn")
	if wi < 0 || fi < 0 {
		t.Fatalf("setup: missing agent names in view: weave=%d finn=%d\n%s", wi, fi, v)
	}
	if wi >= fi {
		t.Errorf("tokens view should sort by input_tokens desc; weave(%d) should appear before finn(%d)", wi, fi)
	}
}

func TestUsageModal_CostSortedByTotalCostDesc(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'c'})
	v := stripAnsi(m.View())
	wi := strings.Index(v, "weave")
	fi := strings.Index(v, "finn")
	if wi < 0 || fi < 0 {
		t.Fatalf("setup: missing agent names in cost view: weave=%d finn=%d\n%s", wi, fi, v)
	}
	if fi >= wi {
		t.Errorf("cost view should sort by total_cost_usd desc; finn(%d) should appear before weave(%d)", fi, wi)
	}
}

func TestUsageModal_CostViewIncludesDisclaimer(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'c'})
	v := stripAnsi(m.View())
	if !strings.Contains(v, usageDisclaimer) {
		t.Errorf("cost view should contain disclaimer %q\n%s", usageDisclaimer, v)
	}
}

func TestUsageModal_AllViewIncludesDisclaimer(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a'})
	v := stripAnsi(m.View())
	if !strings.Contains(v, usageDisclaimer) {
		t.Errorf("all view should contain disclaimer %q\n%s", usageDisclaimer, v)
	}
}

func TestUsageModal_EmptyDataPath(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(map[string]usage.TokenTotals{}).Show()
	v := stripAnsi(m.View())
	if !strings.Contains(v, "no usage records yet") {
		t.Errorf("empty-data view should contain 'no usage records yet'\n%s", v)
	}
	if !strings.Contains(v, "QUM-368") {
		t.Errorf("empty-data view should reference QUM-368\n%s", v)
	}
}

func TestUsageModal_OtherKeysReturnNoCmd(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	for _, k := range []rune{'x', 'z', '1'} {
		before := m.CurrentView()
		updated, cmd := m.Update(tea.KeyPressMsg{Code: k})
		if cmd != nil {
			t.Errorf("key %q should not produce a cmd, got %T", string(k), cmd())
		}
		if updated.CurrentView() != before {
			t.Errorf("key %q should not change CurrentView (was %v, now %v)", string(k), before, updated.CurrentView())
		}
	}
}
