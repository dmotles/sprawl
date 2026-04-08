package cmd

import (
	"os"
	"os/exec"
	"strings"
)

// runBashScript executes an inline bash script with bash -e.
// The script runs in the given working directory with the provided env vars
// merged into the current environment.
func runBashScript(script, workDir string, env map[string]string) ([]byte, error) {
	cmd := exec.Command("bash", "-e")
	cmd.Stdin = strings.NewReader(script)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd.CombinedOutput()
}
