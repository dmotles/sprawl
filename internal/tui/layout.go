package tui

const (
	minTreeWidth    = 20
	maxTreeWidth    = 50
	inputHeight     = 3
	statusBarHeight = 1

	// Activity panel sizing (QUM-296). The panel is a third column to the
	// right of the viewport. It is only shown when the terminal is wide
	// enough that shrinking the viewport to make room does not compromise
	// readability.
	minActivityWidth       = 30
	maxActivityWidth       = 60
	activityPanelThreshold = 140 // total term width below which the panel is hidden

	// MinTermWidth is the minimum terminal width for rendering panels.
	MinTermWidth = 40
	// MinTermHeight is the minimum terminal height for rendering panels.
	MinTermHeight = 10
)

// IsTooSmall returns true if the terminal dimensions are below the minimum
// required to render the TUI panels.
func IsTooSmall(width, height int) bool {
	return width < MinTermWidth || height < MinTermHeight
}

// Layout holds computed panel dimensions for the TUI.
type Layout struct {
	TreeWidth, TreeHeight         int
	ViewportWidth, ViewportHeight int
	ActivityWidth, ActivityHeight int
	InputWidth, InputHeight       int
	StatusWidth, StatusHeight     int
	TermWidth, TermHeight         int
}

// ComputeLayout calculates panel dimensions from terminal size.
// Tree panel is ~25% width (clamped to min/max). Input is 3 lines at bottom.
// Status bar is 1 line at bottom. Viewport fills the rest. When the terminal
// is at least activityPanelThreshold wide, a third column (activity panel)
// is reserved on the right; otherwise ActivityWidth is 0 and the panel is
// hidden.
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

	// Activity panel: only on wide terminals; ~25% clamped.
	if width >= activityPanelThreshold {
		l.ActivityWidth = width / 4
		if l.ActivityWidth < minActivityWidth {
			l.ActivityWidth = minActivityWidth
		}
		if l.ActivityWidth > maxActivityWidth {
			l.ActivityWidth = maxActivityWidth
		}
		// Guarantee the viewport still has room (≥30 chars) before reserving.
		if width-l.TreeWidth-l.ActivityWidth < 30 {
			l.ActivityWidth = 0
		}
	}

	// Viewport takes remaining horizontal space.
	l.ViewportWidth = width - l.TreeWidth - l.ActivityWidth
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
	l.ActivityHeight = mainHeight

	return l
}
