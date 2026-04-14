package tui

import "regexp"

// stripANSI removes ANSI escape sequences from a string for test assertions.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}
