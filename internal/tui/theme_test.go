package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image/color"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestNewTheme_WithAccentColor(t *testing.T) {
	theme := NewTheme("colour212")
	if theme.AccentColor != "212" {
		t.Errorf("AccentColor = %q, want %q", theme.AccentColor, "212")
	}
}

func TestNewTheme_StripsCoulourPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"colour141", "141"},
		{"colour39", "39"},
		{"212", "212"},         // already a plain number
		{"#ff00ff", "#ff00ff"}, // hex color unchanged
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			theme := NewTheme(tt.input)
			if theme.AccentColor != tt.want {
				t.Errorf("NewTheme(%q).AccentColor = %q, want %q", tt.input, theme.AccentColor, tt.want)
			}
		})
	}
}

func TestNewTheme_DefaultAccentNormalized(t *testing.T) {
	theme := NewTheme("")
	// Default should be normalized (no "colour" prefix)
	if theme.AccentColor == "" {
		t.Error("AccentColor should not be empty when constructed with empty string")
	}
	if len(theme.AccentColor) > 3 {
		// "colour" prefix would make it > 3 chars for a number like "39"
		t.Errorf("AccentColor = %q, expected a short numeric string (no 'colour' prefix)", theme.AccentColor)
	}
}

func TestNewTheme_EmptyAccent(t *testing.T) {
	theme := NewTheme("")
	if theme.AccentColor == "" {
		t.Error("AccentColor should not be empty when constructed with empty string; expected a default")
	}
}

func TestNewTheme_RenderStyles(t *testing.T) {
	theme := NewTheme("212")

	// Each style should be able to Render without panicking.
	_ = theme.ActiveBorder.Render("active")
	_ = theme.InactiveBorder.Render("inactive")
	_ = theme.AccentText.Render("accent")
	_ = theme.NormalText.Render("normal")
	_ = theme.StatusBar.Render("status")
	_ = theme.SelectedItem.Render("selected")
}

// QUM-661: chassis styles (panel chrome + foreground-only text) must render
// without applying any background, so the host terminal's bg shows through.
// StatusBar is the deliberate exception — it keeps Palette.BgLessVisible.
func TestNewTheme_ChassisStylesHaveNoBackground(t *testing.T) {
	theme := NewTheme("39")
	cases := []struct {
		name  string
		style lipgloss.Style
	}{
		{"ActiveBorder", theme.ActiveBorder},
		{"InactiveBorder", theme.InactiveBorder},
		{"AccentText", theme.AccentText},
		{"NormalText", theme.NormalText},
		{"ErrorText", theme.ErrorText},
		{"SystemText", theme.SystemText},
		{"NotificationText", theme.NotificationText},
		{"InterruptText", theme.InterruptText},
		{"StatusChangeText", theme.StatusChangeText},
		{"SelectedItem", theme.SelectedItem},
		{"PlaceholderStyle", theme.PlaceholderStyle},
		{"ReportDotWorking", theme.ReportDotWorking},
		{"ReportDotBlocked", theme.ReportDotBlocked},
		{"ReportDotFailure", theme.ReportDotFailure},
		{"ReportDotComplete", theme.ReportDotComplete},
		{"ReportDotIdle", theme.ReportDotIdle},
	}
	noColor := lipgloss.NoColor{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.style.GetBackground(); got != noColor {
				t.Errorf("%s.GetBackground() = %v; want NoColor (QUM-661: chassis styles must not paint a bg)", tc.name, got)
			}
			// Belt-and-suspenders: render of an "x" should not include the
			// SGR 48; (background) parameter — terminal-native bg shows
			// through.
			if got := tc.style.Render("x"); strings.Contains(got, "48;") {
				t.Errorf("%s.Render(\"x\") = %q; contains a background SGR (48;) — chassis must render bg-free", tc.name, got)
			}
		})
	}
}

// QUM-661: ActiveBorder/InactiveBorder must have zero frame size — the
// chassis port strips the rounded border so panel content sits flush against
// the terminal-native bg.
func TestNewTheme_BorderStylesHaveNoFrame(t *testing.T) {
	theme := NewTheme("39")
	for _, tc := range []struct {
		name  string
		style lipgloss.Style
	}{
		{"ActiveBorder", theme.ActiveBorder},
		{"InactiveBorder", theme.InactiveBorder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if fh, fv := tc.style.GetHorizontalFrameSize(), tc.style.GetVerticalFrameSize(); fh != 0 || fv != 0 {
				t.Errorf("%s frame = %dx%d; want 0x0 (QUM-661: borders stripped)", tc.name, fh, fv)
			}
		})
	}
}

// QUM-661: StatusBar keeps its BgLessVisible tint — it is the one slot in
// the chassis where a subtle bg fill is allowed (the redesign anchor that
// distinguishes the status row from the terminal-native body).
func TestNewTheme_StatusBarKeepsBgLessVisibleTint(t *testing.T) {
	theme := NewTheme("39")
	noColor := lipgloss.NoColor{}
	if got := theme.StatusBar.GetBackground(); got == noColor {
		t.Fatalf("StatusBar.GetBackground() = NoColor; want Palette.BgLessVisible")
	}
}

// QUM-338: SystemText must render distinctly from AccentText so inbox-drained
// system messages are visually distinguishable from accent-styled labels.
func TestNewTheme_SystemTextDistinctFromAccent(t *testing.T) {
	theme := NewTheme("")
	if theme.SystemText.Render("x") == theme.AccentText.Render("x") {
		t.Errorf("SystemText.Render(x) should differ from AccentText.Render(x); both produced %q",
			theme.SystemText.Render("x"))
	}
}

// QUM-417: Theme.Palette must expose semantic color roles instead of hardcoded
// ANSI 256 indices scattered through theme.go.
func TestNewTheme_PaletteRolesPopulated(t *testing.T) {
	theme := NewTheme("39")
	roles := []struct {
		name string
		get  func() color.Color
	}{
		{"Primary", func() color.Color { return theme.Palette.Primary }},
		{"Accent", func() color.Color { return theme.Palette.Accent }},
		{"Success", func() color.Color { return theme.Palette.Success }},
		{"Warning", func() color.Color { return theme.Palette.Warning }},
		{"Error", func() color.Color { return theme.Palette.Error }},
		{"Info", func() color.Color { return theme.Palette.Info }},
		{"Busy", func() color.Color { return theme.Palette.Busy }},
		{"FgBase", func() color.Color { return theme.Palette.FgBase }},
		{"FgSubtle", func() color.Color { return theme.Palette.FgSubtle }},
		{"FgMostSubtle", func() color.Color { return theme.Palette.FgMostSubtle }},
		{"BgBase", func() color.Color { return theme.Palette.BgBase }},
		{"BgLessVisible", func() color.Color { return theme.Palette.BgLessVisible }},
		{"System", func() color.Color { return theme.Palette.System }},
	}
	for _, r := range roles {
		t.Run(r.name, func(t *testing.T) {
			c := r.get()
			if c == nil {
				t.Errorf("Palette.%s should be non-nil", r.name)
				return
			}
			if fmt.Sprintf("%v", c) == "" {
				t.Errorf("Palette.%s should be non-empty", r.name)
			}
		})
	}
}

// renderColor renders a small swatch using the given color as foreground so we
// can compare color.Color values for visual equality regardless of the concrete
// type backing the Palette field (lipgloss.Color, lipgloss.ANSIColor, etc.).
func renderColor(c color.Color) string {
	return lipgloss.NewStyle().Foreground(c).Render("█")
}

// QUM-417: Primary should follow whatever accent the user passed to NewTheme,
// so user-configurable accent still threads through the semantic palette.
func TestNewTheme_PrimaryTracksAccentArg(t *testing.T) {
	got := NewTheme("212").Palette.Primary
	want := lipgloss.Color("212")
	if renderColor(got) != renderColor(want) {
		t.Errorf("Palette.Primary render = %q, want %q", renderColor(got), renderColor(want))
	}
}

// QUM-417: NewTheme("212").AccentColor must still equal "212" — the string
// field is the back-compat surface that downstream code (status bar, etc.)
// still reads. Adding the Palette must not break this.
func TestNewTheme_AccentColorStringPreserved(t *testing.T) {
	if got := NewTheme("212").AccentColor; got != "212" {
		t.Errorf("NewTheme(\"212\").AccentColor = %q, want %q", got, "212")
	}
}

// QUM-417: Semantic roles must be pairwise distinct or they collapse into the
// same visual signal (e.g. Error reading as Success).
func TestNewTheme_RolesAreDistinct(t *testing.T) {
	theme := NewTheme("39")
	roles := []struct {
		name string
		c    color.Color
	}{
		{"Error", theme.Palette.Error},
		{"Success", theme.Palette.Success},
		{"Warning", theme.Palette.Warning},
		{"Busy", theme.Palette.Busy},
		{"Info", theme.Palette.Info},
		{"FgBase", theme.Palette.FgBase},
		{"FgMostSubtle", theme.Palette.FgMostSubtle},
		{"BgBase", theme.Palette.BgBase},
	}
	for i := 0; i < len(roles); i++ {
		for j := i + 1; j < len(roles); j++ {
			if renderColor(roles[i].c) == renderColor(roles[j].c) {
				t.Errorf("Palette.%s and Palette.%s render identically (%q); semantic roles must be distinct",
					roles[i].name, roles[j].name, renderColor(roles[i].c))
			}
		}
	}
}

// QUM-417 / QUM-661: ReportDot styles must derive their foreground colors
// from Palette roles so the chip palette stays in lockstep with the semantic
// palette. As of QUM-661 they no longer paint a background — terminal-native
// bg shows through. The control style pins foreground-only.
func TestReportDots_UsePaletteRoles(t *testing.T) {
	theme := NewTheme("39")

	cases := []struct {
		name string
		dot  lipgloss.Style
		role color.Color
	}{
		{"Failure_uses_Error", theme.ReportDotFailure, theme.Palette.Error},
		{"Working_uses_Success", theme.ReportDotWorking, theme.Palette.Success},
		{"Complete_uses_Info", theme.ReportDotComplete, theme.Palette.Info},
		{"Blocked_uses_Busy", theme.ReportDotBlocked, theme.Palette.Busy},
		{"Idle_uses_FgMostSubtle", theme.ReportDotIdle, theme.Palette.FgMostSubtle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := lipgloss.NewStyle().Foreground(tc.role)
			if got, want := tc.dot.Render("●"), control.Render("●"); got != want {
				t.Errorf("ReportDot render mismatch:\n got:  %q\n want: %q (foreground role %s)",
					got, want, tc.name)
			}
		})
	}
}

// QUM-417: Once the semantic palette lands, no TUI source file outside
// theme.go and colors.go should reach for raw `lipgloss.Color("<digits>")`
// literals — those should be migrated to palette references. This AST sweep
// is the regression guard.
func TestTUI_NoStrayAnsiColorLiterals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("os.ReadDir: %v", err)
	}
	digits := regexp.MustCompile(`^[0-9]+$`)
	fset := token.NewFileSet()
	var offenders []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "colors.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s): %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Color" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "lipgloss" {
				return true
			}
			if len(call.Args) != 1 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if digits.MatchString(val) {
				pos := fset.Position(call.Pos())
				offenders = append(offenders, pos.String()+`: lipgloss.Color("`+val+`")`)
			}
			return true
		})
	}
	for _, o := range offenders {
		t.Errorf("stray ANSI color literal: %s — migrate to Theme.Palette", o)
	}
}
