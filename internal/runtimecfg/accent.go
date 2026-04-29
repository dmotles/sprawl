package runtimecfg

import (
	"math/rand/v2"
	"strings"
)

// AccentColor represents a persisted accent color with human-readable aliases.
type AccentColor struct {
	Name    string
	Aliases []string
}

var AccentColors = []AccentColor{
	{Name: "colour39", Aliases: []string{"cyan", "DeepSkyBlue1"}},
	{Name: "colour198", Aliases: []string{"magenta", "DeepPink1"}},
	{Name: "colour82", Aliases: []string{"green", "Chartreuse2"}},
	{Name: "colour208", Aliases: []string{"orange", "DarkOrange"}},
	{Name: "colour141", Aliases: []string{"purple", "MediumPurple1"}},
	{Name: "colour196", Aliases: []string{"red", "Red1"}},
	{Name: "colour220", Aliases: []string{"yellow", "Gold1"}},
	{Name: "colour43", Aliases: []string{"teal", "DarkCyan"}},
	{Name: "colour205", Aliases: []string{"pink", "HotPink"}},
	{Name: "colour69", Aliases: []string{"blue", "CornflowerBlue"}},
}

var AccentColorPool = []string{
	"colour39",
	"colour198",
	"colour82",
	"colour208",
	"colour141",
	"colour196",
	"colour220",
	"colour43",
	"colour205",
	"colour69",
}

func FindAccentColor(nameOrAlias string) (AccentColor, bool) {
	lower := strings.ToLower(nameOrAlias)
	for _, c := range AccentColors {
		if strings.ToLower(c.Name) == lower {
			return c, true
		}
		for _, alias := range c.Aliases {
			if strings.ToLower(alias) == lower {
				return c, true
			}
		}
	}
	return AccentColor{}, false
}

func PickAccentColor() string {
	return AccentColorPool[rand.IntN(len(AccentColorPool))] //nolint:gosec // cosmetic randomness only
}

func PickAccentColorExcluding(current string) string {
	candidates := make([]string, 0, len(AccentColorPool))
	for _, color := range AccentColorPool {
		if color != current {
			candidates = append(candidates, color)
		}
	}
	if len(candidates) == 0 {
		return PickAccentColor()
	}
	return candidates[rand.IntN(len(candidates))] //nolint:gosec // cosmetic randomness only
}
