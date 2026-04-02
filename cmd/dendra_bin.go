package cmd

import "os"

// FindDendraBin returns the dendra binary path for spawning child processes.
// If DENDRA_BIN is set, it returns that value directly; otherwise it falls
// back to os.Executable().
func FindDendraBin() (string, error) {
	if v := os.Getenv("DENDRA_BIN"); v != "" {
		return v, nil
	}
	return os.Executable()
}
