package tui

import (
	"testing"
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
	_ = theme.Background.Render("bg")
	_ = theme.ActiveBorder.Render("active")
	_ = theme.InactiveBorder.Render("inactive")
	_ = theme.AccentText.Render("accent")
	_ = theme.NormalText.Render("normal")
	_ = theme.StatusBar.Render("status")
	_ = theme.SelectedItem.Render("selected")
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
