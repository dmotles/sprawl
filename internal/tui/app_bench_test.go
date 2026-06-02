package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// BenchmarkAppModel_View_PasteBurst measures View() wall time per
// pasted-rune in a stripped-paste burst. The Update is excluded from
// the timer (it dominates wall time due to textarea re-wrap, but is
// outside QUM-451's scope) so the headline ns/op reflects just View()
// cost — the metric the QUM-451 acceptance criterion gates on:
//
//	"Benchmark shows <200 µs/View() when only the input panel is dirty."
//
// Each iteration: fresh KeyPressMsg → Update (stop-timer) → View()
// (start-timer). The input panel mutates every iteration; tree, viewport,
// status do not — so the cache should serve three of the four panels'
// bordered renders + the JoinHorizontal mainRow on every call.
func BenchmarkAppModel_View_PasteBurst(b *testing.B) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app := updated.(AppModel)
	app.activePanel = PanelInput
	app.updateFocus()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ch := rune('a' + (i % 26))
		msg := tea.KeyPressMsg{Code: ch, Text: string(ch)}
		next, _ := app.Update(msg)
		app = next.(AppModel)
		b.StartTimer()
		_ = app.View()
	}
}

// BenchmarkAppModel_UpdateAndView_PasteBurst measures combined Update +
// View cost — the wall-time the user actually waits between pasted runes.
// Useful as a sanity check, but not the gating metric: textarea Update
// dominates and is out of scope for QUM-451.
func BenchmarkAppModel_UpdateAndView_PasteBurst(b *testing.B) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app := updated.(AppModel)
	app.activePanel = PanelInput
	app.updateFocus()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch := rune('a' + (i % 26))
		msg := tea.KeyPressMsg{Code: ch, Text: string(ch)}
		next, _ := app.Update(msg)
		app = next.(AppModel)
		_ = app.View()
	}
}

// BenchmarkAppModel_View_SteadyState measures pure View() cost when
// nothing about the model has changed since the previous render. With
// the QUM-451 cache this should be near-zero (just the cache lookups
// and the lipgloss vertical/horizontal join). Without the cache it's
// the full re-render every call.
func BenchmarkAppModel_View_SteadyState(b *testing.B) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app := updated.(AppModel)
	app.activePanel = PanelInput
	app.updateFocus()
	// Type a single key so the input has some content and the model
	// is in a "post-update" state.
	next, _ := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = next.(AppModel)
	// Prime any first-call cache work.
	_ = app.View()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = app.View()
	}
}

// BenchmarkAppModel_View_PasteBurst_BoundedInput resets the input every
// 500 keystrokes so we measure View() cost at a realistic paste length
// instead of letting the textarea accumulate b.N runes (which artificially
// inflates per-iteration cost as the textarea's wrap+render scales with
// content length).
func BenchmarkAppModel_View_PasteBurst_BoundedInput(b *testing.B) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app := updated.(AppModel)
	app.activePanel = PanelInput
	app.updateFocus()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if i%500 == 0 {
			app.input.SetValue("")
		}
		ch := rune('a' + (i % 26))
		msg := tea.KeyPressMsg{Code: ch, Text: string(ch)}
		next, _ := app.Update(msg)
		app = next.(AppModel)
		b.StartTimer()
		_ = app.View()
	}
}
