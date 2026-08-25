package card

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

// Embedded seed cards.
//
// Compiled into the binary because spawn must keep working with the event log
// unreachable: an agent that cannot be given a prompt cannot be started at all,
// so an outage in the definitions store would take the whole product down.
// The DB copy is the publishable one; this is the floor.
//
// The bodies here were EXTRACTED mechanically from the Go prompt constants
// (see internal/agent's card-generation helper), not retyped. Fidelity is
// proven by rendering each seed against the existing prompt goldens.
//
//go:embed seeds/*.md
var seedsFS embed.FS

// Seeds returns the embedded cards, sorted by name then version. The parse and
// validation cost is paid once.
var Seeds = sync.OnceValues(loadSeeds)

func loadSeeds() ([]*Card, error) {
	entries, err := fs.Glob(seedsFS, "seeds/*.md")
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	out := make([]*Card, 0, len(entries))
	seen := make(map[string]string, len(entries))
	for _, name := range entries {
		src, err := seedsFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		c, err := Parse(src)
		if err != nil {
			return nil, fmt.Errorf("seed %s: %w", name, err)
		}
		key := fmt.Sprintf("%s@%d", c.Name, c.Version)
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("seed %s: %s is already defined by %s", name, key, prev)
		}
		seen[key] = name
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("card: no seed cards were embedded")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// SeedForType returns the highest-version embedded card for an agent type.
func SeedForType(agentType string) (*Card, error) {
	cards, err := Seeds()
	if err != nil {
		return nil, err
	}
	best := pickHighest(cards, agentType)
	if best == nil {
		return nil, fmt.Errorf("card: no embedded card for agent type %q", agentType)
	}
	return best, nil
}

// pickHighest returns the highest-version card for an agent type, or nil.
//
// Highest rather than first: when the slim v2 cards land alongside the legacy
// v1 ones, a first-match pick would keep serving legacy forever and look right
// while doing it.
func pickHighest(cards []*Card, agentType string) *Card {
	var best *Card
	for _, c := range cards {
		if c.AgentType == agentType && (best == nil || c.Version > best.Version) {
			best = c
		}
	}
	return best
}
