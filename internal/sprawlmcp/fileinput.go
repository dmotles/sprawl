// fileinput.go — file-path input for the goal tools (QUM-1347).
//
// `report_result` and `create_goal` take EITHER inline text OR a path to a file
// the agent wrote first. The reason is upstream: emitting a very large tool-call
// argument in one turn is occasionally refused by the API, and a long research
// result or goal brief had no other way in. Writing the content to a file
// piecewise and passing the path removes that failure mode.
//
// THE CONTENT IS WHAT GETS PERSISTED, NEVER THE PATH. The file is read here, at
// call time, and only its bytes reach the ledger — a path is a claim about one
// host's disk at one moment, and the log has to answer for a result long after
// the worktree is gone.
//
// The path is resolved against the CALLER'S OWN WORKTREE and refused if it
// escapes. That containment is the entire safety story, which is why a caller
// whose worktree cannot be resolved is refused rather than defaulted: without a
// root, "reject escapes" has nothing to reject against and the tool becomes
// "read any file on this host, as the host process".
package sprawlmcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// maxInputFileBytes bounds a slurped file.
//
// The boundary here is an arbitrary file on disk, so the bound is enforced
// before the read rather than discovered as a database error after the agent
// believes it has reported. 1MiB is far more prose than any brief or result
// needs and far less than would trouble an artifact row.
const maxInputFileBytes = 1 << 20

// exactlyOneTextInput enforces the mutual exclusion between the inline and
// file-backed forms of one field.
//
// Both-given is an error rather than a precedence rule: a tool that preferred
// one would silently discard the other, and the discarded one may have been the
// real result.
func exactlyOneTextInput(inlineArg, fileArg, inline, path string) error {
	switch {
	case inline == "" && path == "":
		return fmt.Errorf("pass exactly one of %s (the text inline) or %s (a path to a file holding it, read at call time)", inlineArg, fileArg)
	case inline != "" && path != "":
		return fmt.Errorf("%s and %s are alternatives, not both: pass the text inline OR the path to a file holding it, never both — otherwise one of them would be silently discarded", inlineArg, fileArg)
	}
	return nil
}

// callerWorktree resolves the directory a caller's file paths are relative to.
func (s *Server) callerWorktree(ctx context.Context) (string, error) {
	if s.cfg == nil {
		return "", fmt.Errorf("this host has no sprawl root configured, so a file path cannot be resolved or checked; pass the text inline instead")
	}
	caller := backendpkg.CallerIdentity(ctx)
	if caller == "" {
		return "", fmt.Errorf("file input needs to know which agent is calling, so it can resolve the path inside that agent's own worktree; pass the text inline instead")
	}
	st, err := state.LoadAgent(s.cfg.SprawlRoot(), caller)
	if err != nil {
		return "", fmt.Errorf("no worktree is recorded for %q, so a file path has nothing to be resolved against; pass the text inline instead: %w", caller, err)
	}
	if st.Worktree == "" {
		return "", fmt.Errorf("agent %q has no worktree recorded, so a file path has nothing to be resolved against; pass the text inline instead", caller)
	}
	return st.Worktree, nil
}

// readInputFile reads the file at path, which must be inside the caller's
// worktree, and returns its contents.
//
// Every failure names `arg` — the consumer is an agent that has to correct the
// call without a human in the loop, and "no such file or directory" alone does
// not say which argument it belongs to.
func (s *Server) readInputFile(ctx context.Context, arg, path string) (string, error) {
	worktree, err := s.callerWorktree(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: %w", arg, err)
	}
	// Symlinks are evaluated on the ROOT, so a worktree reached through one
	// (/tmp on macOS) does not fail its own containment check. The candidate is
	// deliberately NOT symlink-resolved: EvalSymlinks on a missing path returns
	// an error that would report "outside the worktree" for a file that is
	// simply absent, and the honest message for that is "it does not exist".
	root, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", fmt.Errorf("%s: the worktree %s cannot be read, so a path inside it cannot be checked: %w", arg, worktree, err)
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: %s resolves to %s, which is outside your worktree (%s) — pass a path inside your own worktree, or the text inline", arg, path, candidate, root)
	}

	info, err := os.Stat(candidate)
	if err != nil {
		// Reported as non-existent for every stat failure, deliberately: the
		// remedy an agent needs is the same (write the file, then retry), and
		// the underlying error is kept in the chain for an operator.
		return "", fmt.Errorf("%s: %s does not exist or cannot be read — write the file first, then call again with the same path: %w", arg, candidate, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s: %s is not a file, it is a directory — pass the path of the file holding the text", arg, candidate)
	}
	if info.Size() > maxInputFileBytes {
		return "", fmt.Errorf("%s: %s is %d bytes, which is too large (the limit is %d) — shorten it, or pass a summary and keep the detail in the file", arg, candidate, info.Size(), maxInputFileBytes)
	}

	body, err := os.ReadFile(candidate) //nolint:gosec // G304: the path is contained to the caller's own worktree above
	if err != nil {
		return "", fmt.Errorf("%s: reading %s: %w", arg, candidate, err)
	}
	// An empty file is refused rather than passed through: the field it feeds
	// is required, and "" would be refused a layer later with a message about
	// a missing argument the caller did in fact pass.
	if len(body) == 0 {
		return "", fmt.Errorf("%s: %s is empty, and this text is required — write the content, then call again with the same path", arg, candidate)
	}
	return string(body), nil
}
