// Package tui — UsageModalModel renders the /usage slash-command modal
// (QUM-721). It displays per-agent token and cost totals aggregated from
// .sprawl/logs/usage/ NDJSON files. The modal is owned by AppModel; AppModel
// routes key events to it while showUsage is true and listens for
// DismissUsageMsg to drive the modal close path.
package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/usage"
)

// usageView enumerates the three tabular views the modal can render.
type usageView int

const (
	usageViewTokens usageView = iota
	usageViewCost
	usageViewAll
)

const usageCostDisclaimer = `API-reported; doesn't reflect subscription credits (Claude Max etc.)`

// UsageModalModel renders a centered modal showing usage totals.
type UsageModalModel struct {
	theme         *Theme
	width, height int
	visible       bool
	view          usageView
	totals        map[string]usage.TokenTotals
	installed     bool
}

// NewUsageModalModel constructs a hidden, empty modal.
func NewUsageModalModel(theme *Theme) UsageModalModel {
	return UsageModalModel{theme: theme}
}

// SetSize updates the centering dimensions.
func (m UsageModalModel) SetSize(w, h int) UsageModalModel {
	m.width = w
	m.height = h
	return m
}

// Install replaces the modal's stored totals and resets the view to tokens.
func (m UsageModalModel) Install(totals map[string]usage.TokenTotals) UsageModalModel {
	m.totals = totals
	m.installed = true
	m.view = usageViewTokens
	return m
}

// Show makes the modal visible.
func (m UsageModalModel) Show() UsageModalModel {
	m.visible = true
	return m
}

// Hide hides the modal.
func (m UsageModalModel) Hide() UsageModalModel {
	m.visible = false
	return m
}

// Visible reports whether the modal is currently visible.
func (m UsageModalModel) Visible() bool { return m.visible }

// CurrentView returns the active view selector.
//
//nolint:revive // usageView is intentionally unexported (TUI-internal enum).
func (m UsageModalModel) CurrentView() usageView { return m.view }

// Update handles key events while the modal is visible. It emits a
// DismissUsageMsg on Esc or 'q'; switches view on 't'/'c'/'a'; ignores
// everything else.
func (m UsageModalModel) Update(msg tea.Msg) (UsageModalModel, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.Code {
	case tea.KeyEscape:
		return m, func() tea.Msg { return DismissUsageMsg{} }
	case 'q':
		return m, func() tea.Msg { return DismissUsageMsg{} }
	case 't':
		m.view = usageViewTokens
		return m, nil
	case 'c':
		m.view = usageViewCost
		return m, nil
	case 'a':
		m.view = usageViewAll
		return m, nil
	}
	return m, nil
}

// View renders the modal centered in the available area. Returns empty when
// the modal is not visible.
func (m UsageModalModel) View() string {
	if !m.visible {
		return ""
	}

	body := m.renderBody()

	accent := "212"
	if m.theme != nil && m.theme.AccentColor != "" {
		accent = m.theme.AccentColor
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(accent)).
		Padding(1, 2).
		Render(body)

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

func (m UsageModalModel) renderBody() string {
	if !m.installed || len(m.totals) == 0 {
		return "no usage records yet — see QUM-368 for the usage recorder.\n\n[esc/q] close"
	}

	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)

	// Sort keys by the relevant view's primary key.
	keys := make([]string, 0, len(m.totals))
	for k := range m.totals {
		keys = append(keys, k)
	}
	switch m.view {
	case usageViewCost:
		sort.SliceStable(keys, func(i, j int) bool {
			if m.totals[keys[i]].TotalCostUsd != m.totals[keys[j]].TotalCostUsd {
				return m.totals[keys[i]].TotalCostUsd > m.totals[keys[j]].TotalCostUsd
			}
			return keys[i] < keys[j]
		})
	default:
		sort.SliceStable(keys, func(i, j int) bool {
			if m.totals[keys[i]].InputTokens != m.totals[keys[j]].InputTokens {
				return m.totals[keys[i]].InputTokens > m.totals[keys[j]].InputTokens
			}
			return keys[i] < keys[j]
		})
	}

	var sum usage.TokenTotals
	switch m.view {
	case usageViewTokens:
		fmt.Fprintln(tw, "AGENT\tINPUT\tOUTPUT\tCACHE_READ\tCACHE_CREATE")
		for _, k := range keys {
			t := m.totals[k]
			sum.InputTokens += t.InputTokens
			sum.OutputTokens += t.OutputTokens
			sum.CacheReadInputTokens += t.CacheReadInputTokens
			sum.CacheCreationInputTokens += t.CacheCreationInputTokens
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k,
				formatUsageTokens(t.InputTokens), formatUsageTokens(t.OutputTokens),
				formatUsageTokens(t.CacheReadInputTokens), formatUsageTokens(t.CacheCreationInputTokens))
		}
		fmt.Fprintf(tw, "TOTAL\t%s\t%s\t%s\t%s\n",
			formatUsageTokens(sum.InputTokens), formatUsageTokens(sum.OutputTokens),
			formatUsageTokens(sum.CacheReadInputTokens), formatUsageTokens(sum.CacheCreationInputTokens))
	case usageViewCost:
		fmt.Fprintln(tw, "AGENT\tCOST")
		for _, k := range keys {
			t := m.totals[k]
			sum.TotalCostUsd += t.TotalCostUsd
			fmt.Fprintf(tw, "%s\t%s\n", k, formatUsageCost(t.TotalCostUsd))
		}
		fmt.Fprintf(tw, "TOTAL\t%s\n", formatUsageCost(sum.TotalCostUsd))
	case usageViewAll:
		fmt.Fprintln(tw, "AGENT\tINPUT\tOUTPUT\tCACHE_READ\tCACHE_CREATE\tCOST")
		for _, k := range keys {
			t := m.totals[k]
			sum.InputTokens += t.InputTokens
			sum.OutputTokens += t.OutputTokens
			sum.CacheReadInputTokens += t.CacheReadInputTokens
			sum.CacheCreationInputTokens += t.CacheCreationInputTokens
			sum.TotalCostUsd += t.TotalCostUsd
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k,
				formatUsageTokens(t.InputTokens), formatUsageTokens(t.OutputTokens),
				formatUsageTokens(t.CacheReadInputTokens), formatUsageTokens(t.CacheCreationInputTokens),
				formatUsageCost(t.TotalCostUsd))
		}
		fmt.Fprintf(tw, "TOTAL\t%s\t%s\t%s\t%s\t%s\n",
			formatUsageTokens(sum.InputTokens), formatUsageTokens(sum.OutputTokens),
			formatUsageTokens(sum.CacheReadInputTokens), formatUsageTokens(sum.CacheCreationInputTokens),
			formatUsageCost(sum.TotalCostUsd))
	}
	_ = tw.Flush()

	if m.view == usageViewCost || m.view == usageViewAll {
		b.WriteString("\nnote: ")
		b.WriteString(usageCostDisclaimer)
		b.WriteString("\n")
	}
	b.WriteString("\n[t]okens  [c]ost  [a]ll  [esc/q] close")
	return b.String()
}

// formatUsageTokens renders an integer with comma thousands separators.
func formatUsageTokens(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
		if len(s) > first {
			b.WriteByte(',')
		}
	}
	for i := first; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// formatUsageCost renders a USD float as "$0.0000".
func formatUsageCost(c float64) string {
	return fmt.Sprintf("$%.4f", c)
}
