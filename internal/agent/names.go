package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// EngineerNames contains cyberpunk hacker/runner-themed names for engineer agents.
var EngineerNames = []string{
	"finn", "ratz", "zone", "chip", "byte", "flux", "grid",
	"hex", "link", "node", "ping", "riot", "sync", "volt", "wire",
	"ajax", "blur", "dash", "edge",
}

// ResearcherNames contains cyberpunk decker/netrunner-themed names for researcher agents.
var ResearcherNames = []string{
	"ghost", "trace", "query", "probe", "recon", "scout", "cipher",
	"prism", "pulse", "signal", "vector", "index", "logic", "orbit",
}

// ManagerNames contains cyberpunk fixer/operator-themed names for manager agents.
var ManagerNames = []string{
	"tower", "forge", "bastion", "citadel", "command",
	"axis", "vault", "bridge", "cortex", "matrix", "prime", "zenith", "apex",
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
	"engineer":    "runner",
	"researcher":  "decker",
	"manager":     "fixer",
	"tester":      "runner",
	"code-merger": "runner",
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
// If the typed pool is exhausted, it falls back to numeric suffix names (e.g. "runner-1").
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
