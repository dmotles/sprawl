package tui

const (
	minTreeWidth = 20
	maxTreeWidth = 50
	// defaultInputHeight is the input box height when collapsed (1 line + 2
	// border cells).
	defaultInputHeight = 3
	// maxInputHeight caps input growth so it doesn't eat the viewport.
	maxInputHeight  = 12
	statusBarHeight = 1
	// shortHelpHeight reserves one row above the status bar for the
	// context-sensitive short-help strip (QUM-420). The strip is always
	// drawn so the main panel area is shrunk by this amount.
	shortHelpHeight = 1

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
	InputWidth, InputHeight       int
	StatusWidth, StatusHeight     int
	// ShortHelpWidth / ShortHelpHeight describe the single-line short-help
	// row sandwiched between the input bar and the status bar (QUM-420).
	ShortHelpWidth, ShortHelpHeight int
	TermWidth, TermHeight           int
}

// ComputeLayout calculates panel dimensions from terminal size.
// Tree panel is ~25% width (clamped to min/max). Input height is dynamic
// (driven by the textarea's current line count) and clamped to
// [defaultInputHeight, maxInputHeight]. Status bar is 1 line at bottom.
// Viewport fills the rest.
func ComputeLayout(width, height, inputHeight int) Layout {
	// Clamp input height.
	if inputHeight < defaultInputHeight {
		inputHeight = defaultInputHeight
	}
	if inputHeight > maxInputHeight {
		inputHeight = maxInputHeight
	}

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

	// Vertical: status bar (1) + short-help (1) + input (dynamic) + main
	// panels (rest).
	l.StatusHeight = statusBarHeight
	l.InputHeight = inputHeight
	l.ShortHelpHeight = shortHelpHeight
	l.StatusWidth = width
	l.InputWidth = width
	l.ShortHelpWidth = width

	mainHeight := height - l.StatusHeight - l.ShortHelpHeight - l.InputHeight
	if mainHeight < 0 {
		mainHeight = 0
	}

	l.TreeHeight = mainHeight
	l.ViewportHeight = mainHeight

	return l
}
