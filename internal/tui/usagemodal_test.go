package tui

// QUM-721 — failing tests for the /usage slash command modal.
//
// These tests are red on current code (the implementation does not yet exist)
// and define the public contract that the implementer must satisfy.

import (
	"strings"
	"testing"
	"time"

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

// --- QUM-798: time-range window state ---

func TestUsageModal_DefaultWindowIsAll(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture())
	if m.Window() != usageWindowAll {
		t.Errorf("default window = %v, want usageWindowAll", m.Window())
	}
}

func TestUsageModal_WindowKeysSetWindowAndEmitMsg(t *testing.T) {
	cases := []struct {
		key  rune
		want usageWindow
	}{
		{'1', usageWindow24h},
		{'2', usageWindowWeek},
		{'3', usageWindowMonth},
		{'4', usageWindowYear},
		{'5', usageWindowAll},
	}
	for _, tc := range cases {
		m := newTestUsageModalModel(t)
		m = m.Install(usageFixture()).Show()
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tc.key})
		if updated.Window() != tc.want {
			t.Errorf("key %q: window = %v, want %v", string(tc.key), updated.Window(), tc.want)
		}
		if cmd == nil {
			t.Fatalf("key %q: expected a cmd, got nil", string(tc.key))
		}
		if _, ok := cmd().(SetUsageWindowMsg); !ok {
			t.Errorf("key %q: cmd returned %T, want SetUsageWindowMsg", string(tc.key), cmd())
		}
	}
}

func TestUsageModal_WindowKeyPreservesView(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'c'}) // cost view
	m, _ = m.Update(tea.KeyPressMsg{Code: '1'}) // switch window
	if m.CurrentView() != usageViewCost {
		t.Errorf("window change clobbered view: got %v, want usageViewCost", m.CurrentView())
	}
	if m.Window() != usageWindow24h {
		t.Errorf("window = %v, want usageWindow24h", m.Window())
	}
}

func TestUsageModal_InstallResetsWindowToAll(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: '1'})
	m = m.Install(usageFixture())
	if m.Window() != usageWindowAll {
		t.Errorf("Install should reset window to all, got %v", m.Window())
	}
}

func TestUsageModal_SetTotalsPreservesWindowAndView(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'c'}) // cost view
	m, _ = m.Update(tea.KeyPressMsg{Code: '2'}) // week window
	m = m.SetTotals(usageFixture())
	if m.CurrentView() != usageViewCost {
		t.Errorf("SetTotals clobbered view: got %v, want usageViewCost", m.CurrentView())
	}
	if m.Window() != usageWindowWeek {
		t.Errorf("SetTotals clobbered window: got %v, want usageWindowWeek", m.Window())
	}
}

func TestUsageModal_FooterShowsWindowLabel(t *testing.T) {
	cases := []struct {
		key rune
		w   usageWindow
	}{
		{'5', usageWindowAll},
		{'1', usageWindow24h},
		{'2', usageWindowWeek},
		{'3', usageWindowMonth},
		{'4', usageWindowYear},
	}
	for _, tc := range cases {
		m := newTestUsageModalModel(t)
		m = m.Install(usageFixture()).Show()
		m, _ = m.Update(tea.KeyPressMsg{Code: tc.key})
		if !strings.Contains(m.View(), tc.w.label()) {
			t.Errorf("key %q: footer should show window label %q; view:\n%s", string(tc.key), tc.w.label(), m.View())
		}
	}
}

func TestUsageModal_WindowSince(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		w    usageWindow
		want time.Time
	}{
		{usageWindowAll, time.Time{}},
		{usageWindow24h, now.Add(-24 * time.Hour)},
		{usageWindowWeek, now.Add(-7 * 24 * time.Hour)},
		{usageWindowMonth, now.Add(-30 * 24 * time.Hour)},
		{usageWindowYear, now.Add(-365 * 24 * time.Hour)},
	}
	for _, tc := range cases {
		if got := tc.w.since(now); !got.Equal(tc.want) {
			t.Errorf("window %v since = %v, want %v", tc.w, got, tc.want)
		}
	}
}

func TestUsageModal_OtherKeysReturnNoCmd(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	for _, k := range []rune{'x', 'z', '9'} {
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

// --- cache hit rate (QUM-1258) ---

// TestUsageModal_TokensViewShowsCacheHitRate: usageFixture's cache-read totals
// (980k and 2.1M) are summed re-reads, not context sizes. Rendered raw beside
// INPUT they read as implausible contexts, which is the defect.
func TestUsageModal_TokensViewShowsCacheHitRate(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	got := m.View()

	if strings.Contains(got, "2,100,000") {
		t.Errorf("tokens view renders the raw summed cache-read total 2,100,000:\n%s", got)
	}
	if strings.Contains(got, "980,000") {
		t.Errorf("tokens view renders the raw summed cache-read total 980,000:\n%s", got)
	}
	if !strings.Contains(got, "CACHE_HIT") {
		t.Errorf("tokens view is missing a CACHE_HIT column:\n%s", got)
	}
	// finn: 2100000 / (298000 + 2100000 + 24000) = 86.7%.
	if !strings.Contains(got, "86.7%") {
		t.Errorf("tokens view missing finn's 86.7%% cache hit rate:\n%s", got)
	}
	// CACHE_CREATE stays a raw count — it is billed at a premium and does not
	// grow without bound, so it was never the misleading column.
	if !strings.Contains(got, "34,000") {
		t.Errorf("tokens view should still render CACHE_CREATE as a raw token count:\n%s", got)
	}
}

func TestUsageModal_AllViewShowsCacheHitRate(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(usageFixture()).Show()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a'})
	got := m.View()

	if strings.Contains(got, "2,100,000") || strings.Contains(got, "980,000") {
		t.Errorf("all view renders raw summed cache-read totals:\n%s", got)
	}
	if !strings.Contains(got, "CACHE_HIT") || !strings.Contains(got, "86.7%") {
		t.Errorf("all view missing the CACHE_HIT column or finn's 86.7%% rate:\n%s", got)
	}
	// The all view still carries cost, so this is not a tokens-view copy.
	if !strings.Contains(got, "$0.43") {
		t.Errorf("all view lost its cost column:\n%s", got)
	}
}

// TestUsageModal_CacheHitTotalIsPooled mirrors the CLI pin: the TOTAL row must
// pool counts rather than average the per-agent rates.
func TestUsageModal_CacheHitTotalIsPooled(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(map[string]usage.TokenTotals{
		// Huge and cache-hot.
		"weave": {InputTokens: 10_000, CacheReadInputTokens: 990_000},
		// Tiny and entirely cache-cold.
		"finn": {InputTokens: 1000},
	}).Show()
	got := m.View()

	// Pooled: 990000 / (11000 + 990000) = 98.9%.
	// Mean of rows: (99.0 + 0.0) / 2 = 49.5% — the wrong answer.
	if cell := modalTableCell(t, got, "CACHE_HIT", "TOTAL"); cell != "98.9%" {
		t.Errorf("TOTAL CACHE_HIT = %q, want the pooled 98.9%%:\n%s\n"+
			"49.5%% would be the arithmetic mean of the per-agent rates", cell, got)
	}
}

// TestUsageModal_CacheHitIsBlankWithNoInputTokens: an agent with no
// input-side tokens has no rate to report, and 0.0% would be a claim about a
// cache that was never consulted. Scoped to the cell, and paired with the
// absence of 0.0% — a renderer that em-dashes every cell must not pass.
func TestUsageModal_CacheHitIsBlankWithNoInputTokens(t *testing.T) {
	m := newTestUsageModalModel(t)
	m = m.Install(map[string]usage.TokenTotals{
		"weave": {OutputTokens: 500, TotalCostUsd: 0.01},
	}).Show()
	got := m.View()
	if cell := modalTableCell(t, got, "CACHE_HIT", "weave"); cell != "—" {
		t.Errorf("weave CACHE_HIT = %q, want the em-dash placeholder:\n%s", cell, got)
	}
	if strings.Contains(got, "0.0%") {
		t.Errorf("rendered 0.0%%, which claims the cache was consulted and missed:\n%s", got)
	}
}

// modalTableCell returns the named column's cell on the row keyed by rowKey,
// from inside the modal's lipgloss border. Border runes are stripped before
// splitting; group keys are assumed free of whitespace.
func modalTableCell(t *testing.T, view, col, rowKey string) string {
	t.Helper()
	strip := func(s string) []string {
		return strings.Fields(strings.Trim(stripANSI(s), "│|─ "))
	}
	idx := -1
	for _, ln := range strings.Split(view, "\n") {
		fields := strip(ln)
		if idx < 0 {
			for i, h := range fields {
				if h == col {
					idx = i
					break
				}
			}
			continue
		}
		if len(fields) > idx && fields[0] == rowKey {
			return fields[idx]
		}
	}
	if idx < 0 {
		t.Fatalf("column %q not found in view:\n%s", col, view)
	}
	t.Fatalf("row %q not found in view:\n%s", rowKey, view)
	return ""
}
