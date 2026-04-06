package agent

import (
	"fmt"
	"regexp"
)

// validNameRe matches names that start with an alphanumeric character and contain
// only alphanumeric characters, hyphens, or underscores.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateName checks that an agent name is safe for use in filesystem paths
// and tmux session names. It rejects empty names, names exceeding 64 characters,
// and names containing path separators, dots, spaces, or other special characters.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("invalid agent name: must not be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("invalid agent name %q: must be 64 characters or fewer", name)
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: must start with alphanumeric and contain only alphanumeric, hyphens, or underscores", name)
	}
	return nil
}
