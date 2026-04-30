package tui

import "fmt"

// sprawlBanner is the stylized ASCII art displayed on session start.
// Kept under 80 columns wide and roughly 6-10 lines tall.
const sprawlBanner = `
 ╔═╗╔═╗╦═╗╔═╗╦ ╦╦
 ╚═╗╠═╝╠╦╝╠═╣║║║║
 ╚═╝╩  ╩╚═╩ ╩╚╩╝╩═╝
 ─────────────────────
 agent orchestration
`

// SessionBanner returns the full banner block with an optional tagline
// showing the session ID and/or version. Suitable for rendering into
// the TUI viewport on session start or restart.
func SessionBanner(sessionID, version string) string {
	tagline := buildTagline(sessionID, version)
	if tagline == "" {
		return sprawlBanner
	}
	return sprawlBanner + " " + tagline + "\n"
}

// buildTagline assembles a compact "version | session: ID" line.
func buildTagline(sessionID, version string) string {
	var parts []string
	if version != "" {
		parts = append(parts, version)
	}
	if sessionID != "" {
		parts = append(parts, fmt.Sprintf("session: %s", sessionID))
	}
	if len(parts) == 0 {
		return ""
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += " · "
		}
		result += p
	}
	return result
}
