package tui

import (
	"testing"
)

func TestNewTheme_WithAccentColor(t *testing.T) {
	theme := NewTheme("colour212")
	if theme.AccentColor != "colour212" {
		t.Errorf("AccentColor = %q, want %q", theme.AccentColor, "colour212")
	}
}

func TestNewTheme_EmptyAccent(t *testing.T) {
	theme := NewTheme("")
	if theme.AccentColor == "" {
		t.Error("AccentColor should not be empty when constructed with empty string; expected a default")
	}
}

func TestNewTheme_RenderStyles(t *testing.T) {
	theme := NewTheme("colour212")

	// Each style should be able to Render without panicking.
	_ = theme.Background.Render("bg")
	_ = theme.ActiveBorder.Render("active")
	_ = theme.InactiveBorder.Render("inactive")
	_ = theme.AccentText.Render("accent")
	_ = theme.NormalText.Render("normal")
	_ = theme.StatusBar.Render("status")
	_ = theme.SelectedItem.Render("selected")
}
