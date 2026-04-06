package cmd

import "os"

// FindSprawlBin returns the sprawl binary path for spawning child processes.
// If SPRAWL_BIN is set, it returns that value directly; otherwise it falls
// back to os.Executable().
func FindSprawlBin() (string, error) {
	if v := os.Getenv("SPRAWL_BIN"); v != "" {
		return v, nil
	}
	return os.Executable()
}
