package tui

const (
	minTreeWidth    = 20
	maxTreeWidth    = 50
	inputHeight     = 3
	statusBarHeight = 1
)

// Layout holds computed panel dimensions for the TUI.
type Layout struct {
	TreeWidth, TreeHeight         int
	ViewportWidth, ViewportHeight int
	InputWidth, InputHeight       int
	StatusWidth, StatusHeight     int
	TermWidth, TermHeight         int
}

// ComputeLayout calculates panel dimensions from terminal size.
// Tree panel is ~25% width (clamped to min/max). Input is 3 lines at bottom.
// Status bar is 1 line at bottom. Viewport fills the rest.
func ComputeLayout(width, height int) Layout {
	l := Layout{
		TermWidth:  width,
		TermHeight: height,
	}

	// Tree width: 25% clamped to [min, max].
	l.TreeWidth = width / 4
	if l.TreeWidth < minTreeWidth {
		l.TreeWidth = minTreeWidth
	}
	if l.TreeWidth > maxTreeWidth {
		l.TreeWidth = maxTreeWidth
	}
	if l.TreeWidth > width {
		l.TreeWidth = width
	}

	// Viewport takes remaining horizontal space.
	l.ViewportWidth = width - l.TreeWidth
	if l.ViewportWidth < 0 {
		l.ViewportWidth = 0
	}

	// Vertical: status bar (1) + input (3) + main panels (rest).
	l.StatusHeight = statusBarHeight
	l.InputHeight = inputHeight
	l.StatusWidth = width
	l.InputWidth = width

	mainHeight := height - l.StatusHeight - l.InputHeight
	if mainHeight < 0 {
		mainHeight = 0
	}

	l.TreeHeight = mainHeight
	l.ViewportHeight = mainHeight

	return l
}
