package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// QUM-451: per-panel View() render cache.
//
// AppModel.View() is invoked once per Bubble Tea Update. When bracketed-paste
// markers are stripped (QUM-432) and a paste arrives as N KeyPressMsgs, View()
// is called N times even though only the input panel mutated between calls.
// Each call previously re-ran lipgloss bordered-render of the tree, viewport,
// and status panels — measured at ~4 ms / call on a 200x60 layout.
//
// The cache stores the bordered output strings keyed on a cheap fingerprint
// of each panel's inner state. On cache hit (the common paste-burst case),
// View() skips the lipgloss border render entirely; only the per-panel
// inner View() and the final lipgloss.JoinHorizontal/JoinVertical compose
// remain.
//
// The cache is held behind a pointer so a value-receiver View() (the tea.Model
// interface contract used throughout this package) can mutate it. AppModel
// values returned from Update share the same *viewCache; this is safe because
// Bubble Tea immediately discards the prior value after Update returns.

// viewCache stores the most recent bordered render of each panel together
// with the fingerprint of the panel state that produced it. View() reuses
// the stored output when the fingerprint matches; otherwise it re-renders
// and updates both.
//
// Field names tree/viewport/input/status mirror the assertion
// surface used by app_view_cache_test.go — they hold the *bordered* output
// strings. The *Key fields hold the corresponding fingerprint for cheap
// equality comparison.
type viewCache struct {
	tree, treeKey         string
	viewport, viewportKey string
	input, inputKey       string
	status, statusKey     string

	// mainRow caches lipgloss.JoinHorizontal(tree, viewport).
	// Reused while neither of those two panel keys change — i.e. across an
	// entire paste burst, where only the input panel is invalidating.
	mainRow, mainRowKey string

	// mainRowPadded caches mainRow padded to the full terminal width via
	// lipgloss.PlaceHorizontal so the final compose can be a plain
	// strings.Join(parts, "\n") instead of another lipgloss.JoinVertical
	// (QUM-451). Keyed on mainRowKey + targetWidth.
	mainRowPadded, mainRowPaddedKey string

	// inputPadded caches input panel padded to full terminal width.
	// Keyed on inputKey + targetWidth.
	inputPadded, inputPaddedKey string

	// composed caches the full vertically-joined content (mainRow + optional
	// overlay + input + status, or mainRow + status when input is hidden).
	// Reused when none of the constituent keys change since the last render.
	composed, composedKey string
}

func newViewCache() *viewCache { return &viewCache{} }

// panelSlot identifies which cached panel slot to read/write.
type panelSlot int

const (
	panelSlotTree panelSlot = iota
	panelSlotViewport
	panelSlotInput
)

// panelKey builds a fingerprint string that uniquely identifies the rendered
// output for a panel: the inner content + dimensions + active flag (which
// selects the active vs inactive border style). A simple concatenation is
// sufficient — equality is what matters, not opacity, and the fields are
// already separated by characters that cannot appear in width/height ints.
func panelKey(content string, w, h int, active bool) string {
	var ab byte = '0'
	if active {
		ab = '1'
	}
	// Pre-size: rough upper bound on the formatted ints + separators + flag.
	buf := make([]byte, 0, len(content)+24)
	buf = append(buf, content...)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(w), 10)
	buf = append(buf, 'x')
	buf = strconv.AppendInt(buf, int64(h), 10)
	buf = append(buf, '|', ab)
	return string(buf)
}

// cachedPanel returns the bordered render of a panel, reusing the prior
// render if the fingerprint matches. When useCache is false (test oracle),
// it always re-renders without consulting or updating the cache.
func (m AppModel) cachedPanel(useCache bool, slot panelSlot, content string, w, h int, active bool) string {
	if !useCache || m.cache == nil {
		return m.renderPanel(content, w, h, active)
	}
	key := panelKey(content, w, h, active)
	switch slot {
	case panelSlotTree:
		if key == m.cache.treeKey && m.cache.tree != "" {
			return m.cache.tree
		}
		out := m.renderPanel(content, w, h, active)
		m.cache.tree = out
		m.cache.treeKey = key
		return out
	case panelSlotViewport:
		if key == m.cache.viewportKey && m.cache.viewport != "" {
			return m.cache.viewport
		}
		out := m.renderPanel(content, w, h, active)
		m.cache.viewport = out
		m.cache.viewportKey = key
		return out
	case panelSlotInput:
		if key == m.cache.inputKey && m.cache.input != "" {
			return m.cache.input
		}
		out := m.renderPanel(content, w, h, active)
		m.cache.input = out
		m.cache.inputKey = key
		return out
	}
	return m.renderPanel(content, w, h, active)
}

// renderPanel sizes a panel slot to its declared outer dimensions.
//
// QUM-501: w and h are the *outer* panel dimensions. MaxWidth/MaxHeight
// clamp overflowing content (QUM-483).
//
// QUM-661: the chassis port stripped the rounded border + bg fill from the
// underlying ActiveBorder/InactiveBorder styles, so this call now applies a
// no-decoration size+clamp. The `active` parameter is retained for caller
// symmetry (and as part of the cache fingerprint) but is currently a visual
// no-op pending the QUM-655 cleanup sweep. The cachedPanel memoization
// upstream is intentionally kept — the per-keystroke paste-burst hot path
// (QUM-451) keys composed-row caches off the per-panel render output, which
// is still expensive enough to memoize even without the border decoration.
func (m AppModel) renderPanel(content string, w, h int, active bool) string {
	_ = active
	return lipgloss.NewStyle().
		Width(w).Height(h).
		MaxWidth(w).
		MaxHeight(h).
		Render(content)
}

// cachedMainRow memoizes lipgloss.JoinHorizontal of the tree+viewport
// columns. The fingerprint is each constituent's cache key so the join is
// reused across paste-burst Updates that only mutate the input panel.
func (m AppModel) cachedMainRow(useCache bool, tree, viewport string) string {
	join := func() string {
		return lipgloss.JoinHorizontal(lipgloss.Top, tree, viewport)
	}
	if !useCache || m.cache == nil {
		return join()
	}
	// Build a cheap composite key from per-panel cache keys. They were just
	// computed by cachedPanel() and are stable while content is unchanged.
	key := m.cache.treeKey + "\x00" + m.cache.viewportKey
	if key == m.cache.mainRowKey && m.cache.mainRow != "" {
		return m.cache.mainRow
	}
	out := join()
	m.cache.mainRow = out
	m.cache.mainRowKey = key
	return out
}

// cachedComposed memoizes the final lipgloss.JoinVertical pass over
// (mainRow, optional overlay, optional input, status). On a paste burst this
// hits when nothing but the input panel changed AND the input panel cache key
// is unchanged from a prior identical state — which doesn't happen during a
// burst since each rune mutates input — but it does hit on no-op re-renders
// (e.g. spinner ticks where view content is the same).
func (m AppModel) cachedComposed(useCache bool, termWidth int, mainRow, overlay, inputView, shortHelpView, statusView string, inputVisible bool) string {
	// QUM-664: shortHelpView is no longer composed into the chassis — the
	// row was removed. The parameter is retained to keep the call-site
	// signature stable during the spike port; it is intentionally unused.
	_ = shortHelpView
	if !useCache || m.cache == nil {
		// Test oracle path: defer to lipgloss for ground-truth composition.
		if inputVisible {
			if overlay != "" {
				return lipgloss.JoinVertical(lipgloss.Left, mainRow, overlay, inputView, statusView)
			}
			return lipgloss.JoinVertical(lipgloss.Left, mainRow, inputView, statusView)
		}
		return lipgloss.JoinVertical(lipgloss.Left, mainRow, statusView)
	}
	var iv byte = '0'
	if inputVisible {
		iv = '1'
	}
	composedKey := m.cache.mainRowKey + "\x00" + overlay + "\x00" + m.cache.inputKey + "\x00" + shortHelpView + "\x00" + m.cache.statusKey + "\x00" + strconv.Itoa(termWidth) + "\x00" + string(iv)
	if composedKey == m.cache.composedKey && m.cache.composed != "" {
		return m.cache.composed
	}

	// Fast compose path (QUM-451): pre-pad each part to termWidth via
	// PlaceHorizontal (cached per-part on its own panel key), then join
	// with plain strings.Join. PlaceHorizontal output for parts already at
	// termWidth is a no-op return — i.e. statusView passes through. The
	// resulting string is byte-identical to lipgloss.JoinVertical(Left, ...);
	// the TestViewCache_OutputEqualsUncached_AcrossKeystrokes test gates this
	// invariant against the real-lipgloss path used by viewUncached().
	mainRowPadded := m.cachedMainRowPadded(termWidth, mainRow)
	statusPadded := lipgloss.PlaceHorizontal(termWidth, lipgloss.Left, statusView)

	var out string
	if inputVisible {
		inputPadded := m.cachedInputPadded(termWidth, inputView)
		if overlay != "" {
			overlayPadded := lipgloss.PlaceHorizontal(termWidth, lipgloss.Left, overlay)
			out = strings.Join([]string{mainRowPadded, overlayPadded, inputPadded, statusPadded}, "\n")
		} else {
			out = strings.Join([]string{mainRowPadded, inputPadded, statusPadded}, "\n")
		}
	} else {
		out = strings.Join([]string{mainRowPadded, statusPadded}, "\n")
	}

	m.cache.composed = out
	m.cache.composedKey = composedKey
	return out
}

// cachedMainRowPadded memoizes lipgloss.PlaceHorizontal(termWidth, ...) of
// mainRow. mainRow is large (full terminal width × ~50 rows) and pre-padding
// is the per-call hot path during a paste burst — but mainRow itself does
// not change while only the input panel is dirty, so this cache hits across
// the entire burst (QUM-451).
func (m AppModel) cachedMainRowPadded(termWidth int, mainRow string) string {
	key := m.cache.mainRowKey + "\x00" + strconv.Itoa(termWidth)
	if key == m.cache.mainRowPaddedKey && m.cache.mainRowPadded != "" {
		return m.cache.mainRowPadded
	}
	out := lipgloss.PlaceHorizontal(termWidth, lipgloss.Left, mainRow)
	m.cache.mainRowPadded = out
	m.cache.mainRowPaddedKey = key
	return out
}

// cachedInputPadded memoizes the padded input render. Input changes on
// every keystroke during a paste burst, so this cache rarely hits during a
// burst — but the padding op for the small (3-row) input panel is cheap
// (~10µs) and consistent with the rest of the caching shape.
func (m AppModel) cachedInputPadded(termWidth int, inputView string) string {
	key := m.cache.inputKey + "\x00" + strconv.Itoa(termWidth)
	if key == m.cache.inputPaddedKey && m.cache.inputPadded != "" {
		return m.cache.inputPadded
	}
	out := lipgloss.PlaceHorizontal(termWidth, lipgloss.Left, inputView)
	m.cache.inputPadded = out
	m.cache.inputPaddedKey = key
	return out
}

// cachedStatus memoizes the status bar render. Status has no border; the
// fingerprint is the inner content + width.
func (m AppModel) cachedStatus(useCache bool, content string, w int) string {
	if !useCache || m.cache == nil {
		return content
	}
	// Width is an upper bound; the StatusBar renders to its own SetWidth().
	// Including width in the key guards against a same-content-different-
	// terminal-width scenario (e.g. a no-op WindowSizeMsg with new dims).
	key := panelKey(content, w, 0, false)
	if key == m.cache.statusKey && m.cache.status != "" {
		return m.cache.status
	}
	m.cache.status = content
	m.cache.statusKey = key
	return content
}
