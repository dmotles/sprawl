package hooks

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitOutput runs git with the given args and returns trimmed stdout.
func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RealHooksDir resolves the target hooks directory: core.hooksPath when set
// (absolute, or relative to the repo top-level), else <git-common-dir>/hooks.
// The git-common-dir form is correct from a linked worktree as well as the main
// checkout, since worktrees share one common hooks dir.
func RealHooksDir() (string, error) {
	if hp, err := gitOutput("config", "--get", "core.hooksPath"); err == nil && hp != "" {
		if filepath.IsAbs(hp) {
			return hp, nil
		}
		top, err := gitOutput("rev-parse", "--show-toplevel")
		if err != nil {
			return "", fmt.Errorf("resolving repo top-level for core.hooksPath: %w", err)
		}
		return filepath.Join(top, hp), nil
	}

	common, err := gitOutput("rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("not a git repository (or git is unavailable): %w", err)
	}
	if !filepath.IsAbs(common) {
		if abs, aerr := filepath.Abs(common); aerr == nil {
			common = abs
		}
	}
	return filepath.Join(common, "hooks"), nil
}

// RealDetectBranch resolves the repo's default branch: origin/HEAD →
// init.defaultBranch → "main".
func RealDetectBranch() (string, error) {
	if ref, err := gitOutput("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimPrefix(ref, "origin/"); b != "" {
			return b, nil
		}
	}
	if b, err := gitOutput("config", "--get", "init.defaultBranch"); err == nil && b != "" {
		return b, nil
	}
	return "main", nil
}

// RealWriteFileAtomic writes data to path atomically (temp file in the same
// directory + rename) and sets mode explicitly so umask cannot strip the
// executable bit a hook needs.
func RealWriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sprawl-hooks-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
