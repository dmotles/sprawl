package hooks

import (
	"errors"
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

// RealHooksPathOrigins reports every config scope that sets core.hooksPath, in
// git's own precedence order (git obeys the last). An unset key is not an
// error — git exits 1 with no output, which is reported as an empty list.
func RealHooksPathOrigins() ([]ConfigOrigin, error) {
	out, err := exec.Command("git", "config", "--show-origin", "--show-scope", "--get-all", "core.hooksPath").Output()
	if err != nil {
		var ee *exec.ExitError
		// Exit 1 with empty stderr is git's "key not set".
		if errors.As(err, &ee) && ee.ExitCode() == 1 && len(ee.Stderr) == 0 {
			return nil, nil
		}
		return nil, err
	}
	var origins []ConfigOrigin
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		// Format: <scope>\t<origin>\t<value>
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unparseable `git config --show-scope --show-origin` line: %q", line)
		}
		origins = append(origins, ConfigOrigin{Scope: parts[0], Origin: parts[1], Value: parts[2]})
	}
	return origins, nil
}

// RealResolvedHooksDir asks git itself where hooks live. `rev-parse --git-path
// hooks` honours core.hooksPath (including a relative value, resolved against
// the working-tree top-level) and returns the shared common dir from a linked
// worktree, so it is the authoritative answer rather than a reimplementation of
// git's precedence rules.
func RealResolvedHooksDir() (string, error) {
	p, err := gitOutput("rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

// RealCommonDir returns the absolute shared git dir (the same path from every
// linked worktree).
func RealCommonDir() (string, error) {
	p, err := gitOutput("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

// RealGitDir returns this worktree's own git dir. It differs from the common
// dir exactly when the caller is in a linked worktree.
func RealGitDir() (string, error) {
	return gitOutput("rev-parse", "--absolute-git-dir")
}

// RealTopLevel returns the working-tree top-level.
func RealTopLevel() (string, error) {
	return gitOutput("rev-parse", "--show-toplevel")
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
