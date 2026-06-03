package tui

const (
	// defaultInputHeight is the input box height when collapsed. QUM-661
	// dropped this from 3 to 1: the prior value reserved two extra rows
	// for the rounded border frame, but the chassis port stripped the
	// border so the input bar is now a single text row flush with the
	// terminal-native bg.
	defaultInputHeight = 1
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
//
// QUM-656: the left-column agent tree was removed; the tree now lives in the
// top header alongside the SPRAWL wordmark. HeaderWidth / HeaderHeight describe
// the header strip (3 wide / 1 narrow / 0 at zero width); HeaderTreeWidth is
// the cell budget within the header reserved for the orbital tree.
type Layout struct {
	ViewportWidth, ViewportHeight int
	InputWidth, InputHeight       int
	StatusWidth, StatusHeight     int
	// ShortHelpWidth / ShortHelpHeight describe the single-line short-help
	// row sandwiched between the input bar and the status bar (QUM-420).
	ShortHelpWidth, ShortHelpHeight int
	// HeaderWidth / HeaderHeight describe the top-of-TUI header strip
	// composing the SPRAWL wordmark + orbital agent tree (QUM-656).
	HeaderWidth, HeaderHeight int
	// HeaderTreeWidth is the cell budget within the header reserved for the
	// orbital agent tree (QUM-656).
	HeaderTreeWidth       int
	TermWidth, TermHeight int
}

// ComputeLayout calculates panel dimensions from terminal size.
// The viewport now claims the full terminal width (QUM-656 removed the
// left-column tree). Input height is dynamic (driven by the textarea's
// current line count) and clamped to [defaultInputHeight, maxInputHeight].
// Status bar is 1 line at bottom; header strip lives at the top.
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

	// QUM-656: viewport claims the full terminal width — no left-column tree.
	l.ViewportWidth = width
	if l.ViewportWidth < 0 {
		l.ViewportWidth = 0
	}

	// Vertical: status bar (1) + short-help (1) + input (dynamic) + header
	// strip + main panels (rest).
	l.StatusHeight = statusBarHeight
	l.InputHeight = inputHeight
	l.ShortHelpHeight = shortHelpHeight
	l.StatusWidth = width
	l.InputWidth = width
	l.ShortHelpWidth = width
	l.HeaderWidth = width
	l.HeaderHeight = HeaderHeight(width)
	l.HeaderTreeWidth = HeaderTreeWidth(width)

	mainHeight := height - l.StatusHeight - l.ShortHelpHeight - l.InputHeight - l.HeaderHeight
	if mainHeight < 0 {
		mainHeight = 0
	}

	l.ViewportHeight = mainHeight

	return l
}
