package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// EngineerNames contains tree-themed names for engineer agents.
var EngineerNames = []string{
	"ash", "elm", "fig", "oak", "yew", "palm", "pine", "teak",
	"alder", "aspen", "beech", "birch", "cedar", "hazel",
	"holly", "larch", "maple", "rowan", "spruce", "walnut",
}

// ResearcherNames contains river/water-themed names for researcher agents.
var ResearcherNames = []string{
	"brook", "creek", "delta", "fjord", "lake", "marsh", "rapids", "reed",
	"rill", "shoal", "spring", "strait", "tide", "cove", "bay",
}

// ManagerNames contains mountain/ridge-themed names for manager agents.
var ManagerNames = []string{
	"summit", "ridge", "peak", "bluff", "cliff", "crest", "mesa", "butte",
	"gorge", "ledge", "vale", "glen", "knoll", "cairn", "sierra",
}

// NamePools maps agent type to its name pool.
var NamePools = map[string][]string{
	"engineer":    EngineerNames,
	"researcher":  ResearcherNames,
	"manager":     ManagerNames,
	"tester":      EngineerNames,
	"code-merger": EngineerNames,
}

// FallbackPrefix maps agent type to its overflow name prefix.
var FallbackPrefix = map[string]string{
	"engineer":    "tree",
	"researcher":  "river",
	"manager":     "peak",
	"tester":      "tree",
	"code-merger": "tree",
}

// NamePool is the union of all partitioned pools, preserved for backward compatibility.
var NamePool = func() []string {
	all := make([]string, 0, len(EngineerNames)+len(ResearcherNames)+len(ManagerNames))
	all = append(all, EngineerNames...)
	all = append(all, ResearcherNames...)
	all = append(all, ManagerNames...)
	return all
}()

// AllocateName returns the first unused name from the pool for the given agent type.
// A name is considered "used" if a file named <name>.json exists in stateDir.
// If the typed pool is exhausted, it falls back to numeric suffix names (e.g. "tree-1").
func AllocateName(stateDir string, agentType string) (string, error) {
	pool, ok := NamePools[agentType]
	if !ok {
		return "", fmt.Errorf("unknown agent type %q", agentType)
	}

	for _, name := range pool {
		path := filepath.Join(stateDir, name+".json")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return name, nil
		}
	}

	// Pool exhausted — fall back to numeric suffix names
	prefix := FallbackPrefix[agentType]
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s-%d", prefix, i)
		path := filepath.Join(stateDir, name+".json")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if i > 2*len(pool) {
				log.Printf("WARNING: fallback name counter for %q reached %d (pool size: %d) — possible name leak", agentType, i, len(pool))
			}
			return name, nil
		}
	}
}
