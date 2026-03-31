package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// NamePool is the pre-set pool of agent names. Names are allocated in order.
var NamePool = []string{
	"ash", "elm", "fig", "oak", "yew", "palm", "pine", "teak",
	"alder", "aspen", "beech", "birch", "cedar", "ebony", "elder", "hazel",
	"holly", "larch", "maple", "olive", "rowan", "sumac", "willow", "acacia",
	"bamboo", "banyan", "baobab", "buckeye", "cherry", "cypress", "hemlock", "hickory",
	"juniper", "laurel", "linden", "myrtle", "pecan", "poplar", "spruce", "walnut",
	"magnolia", "mangrove", "redwood", "sequoia", "tamarack", "chestnut", "cottonwood", "dogwood",
	"sassafras", "sycamore",
}

// AllocateName returns the first unused name from the pool.
// A name is considered "used" if a file named <name>.json exists in stateDir.
func AllocateName(stateDir string) (string, error) {
	for _, name := range NamePool {
		path := filepath.Join(stateDir, name+".json")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no more agents can be spawned: all %d names are allocated", len(NamePool))
}
