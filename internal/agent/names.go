package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// NamePool is the pre-set pool of agent names. Names are allocated in order.
var NamePool = []string{
	"alice", "bob", "carol", "dave", "eve", "frank", "grace", "hank",
	"iris", "june", "karl", "luna", "mike", "nora", "omar", "petra",
	"quinn", "rosa", "sam", "tara", "uma", "vera", "walt", "xena",
	"yuri", "zara", "amber", "blake", "corey", "diana", "ellis", "felix",
	"gina", "hugo", "ivy", "jake", "kira", "leo", "mona", "neil",
	"olive", "pat", "reed", "sky", "tess", "uri", "val", "wade",
	"yara", "zeke",
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
