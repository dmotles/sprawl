package supervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/blurb"
	"github.com/dmotles/sprawl/internal/state"
)

// blurbTaskFirstLineMax caps how much of a task prompt's first line is fed into
// the blurb signal, so a long delegated prompt doesn't dominate the context.
const blurbTaskFirstLineMax = 120

// asyncGenerateBlurb is the default dispatchBlurb seam: it fires the generation
// in a background goroutine (context.Background so it outlives the request) and
// logs failures at debug — a missing blurb is never fatal. Mirrors the
// memory-consolidation background-dispatch shape (QUM-899).
func (r *Real) asyncGenerateBlurb(name string, kind blurb.TriggerKind) {
	go func() {
		if err := r.generateAndPersistBlurb(context.Background(), name, kind); err != nil {
			slog.Default().Debug("supervisor: blurb generation failed",
				slog.String("agent", name),
				slog.String("kind", kind.String()),
				slog.Any("err", err))
		}
	}()
}

// generateAndPersistBlurb assembles the rolling-summary context for the named
// agent, invokes the model, and persists the result. Root (weave) agents are
// skipped — they have the handoff/memory system. An empty model result is a
// no-op (the previous blurb is kept, never clobbered). Synchronous and
// dependency-injected so it is unit-testable without a real subprocess.
func (r *Real) generateAndPersistBlurb(ctx context.Context, name string, kind blurb.TriggerKind) error {
	st, err := state.LoadAgent(r.sprawlRoot, name)
	if err != nil {
		return err
	}
	if st.Parent == "" {
		return nil // skip weave/root (QUM-899)
	}
	if r.blurbInvoker == nil {
		return nil
	}
	sig := r.assembleBlurbSignals(st)
	text, err := blurb.Generate(ctx, r.blurbInvoker, r.blurbModel, 0, kind, sig)
	if err != nil {
		return err
	}
	if text == "" {
		return nil // empty-result guard: keep the previous blurb
	}
	return r.persistBlurb(name, text)
}

// maybeRefreshBlurb is the heartbeat RefreshBlurb seam. It applies the pure
// DecideTrigger to the agent's persisted watermark + the runtime-derived last
// activity time and dispatches a generation only when the dirty-check + floor
// permit (or the agent has no blurb yet).
func (r *Real) maybeRefreshBlurb(name string, lastActivityAt time.Time) {
	if name == "" || r.dispatchBlurb == nil {
		return
	}
	st, err := state.LoadAgent(r.sprawlRoot, name)
	if err != nil {
		return
	}
	kind := blurb.DecideTrigger(blurb.TriggerInput{
		IsRoot:         st.Parent == "",
		HasBlurb:       st.Blurb != "",
		BlurbAt:        st.BlurbAt,
		LastActivityAt: lastActivityAt,
		Now:            r.now(),
	})
	if kind == blurb.TriggerNone {
		return
	}
	r.dispatchBlurb(name, kind)
}

// persistBlurb reloads the agent's state under reportMu (serializing with
// ReportStatus's read-modify-write on the same file), stamps the new blurb +
// watermark, and saves.
func (r *Real) persistBlurb(name, text string) error {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	st, err := state.LoadAgent(r.sprawlRoot, name)
	if err != nil {
		return err
	}
	st.Blurb = text
	st.BlurbAt = r.now()
	return state.SaveAgent(r.sprawlRoot, st)
}

// now resolves the blurb clock (defaults to time.Now).
func (r *Real) now() time.Time {
	if r.blurbNow != nil {
		return r.blurbNow()
	}
	return time.Now()
}

// assembleBlurbSignals gathers the rolling-summary context from disk: the full
// activity delta since the last blurb, the git diff --stat, task history, and
// referenced Linear issue keys. See QUM-899 §2.
func (r *Real) assembleBlurbSignals(st *state.AgentState) blurb.Signals {
	entries, _ := agentloop.ReadActivityFile(agentloop.ActivityPath(r.sprawlRoot, st.Name), 0)
	delta, omitted := blurb.ActivityDelta(entries, st.BlurbAt)

	var gitDiff string
	if r.gitDiffStat != nil && st.Worktree != "" {
		gitDiff, _ = r.gitDiffStat(st.Worktree)
	}

	tasks := r.blurbTaskSummaries(st.Name)

	srcs := make([]string, 0, len(delta)+len(tasks)+2)
	srcs = append(srcs, st.Prompt, st.Blurb)
	for _, e := range delta {
		srcs = append(srcs, e.Summary)
	}
	srcs = append(srcs, tasks...)

	return blurb.Signals{
		AgentName:    st.Name,
		Role:         strings.TrimSpace(st.Type + " / " + st.Family),
		Prompt:       st.Prompt,
		PrevBlurb:    st.Blurb,
		Delta:        delta,
		OmittedDelta: omitted,
		GitDiffStat:  gitDiff,
		Tasks:        tasks,
		LinearKeys:   blurb.ExtractLinearKeys(srcs...),
	}
}

// blurbTaskSummaries renders each queued/completed task as a compact
// "first-line [status]" signal line.
func (r *Real) blurbTaskSummaries(name string) []string {
	tasks, err := state.ListTasks(r.sprawlRoot, name)
	if err != nil || len(tasks) == 0 {
		return nil
	}
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		first := t.Prompt
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		first = strings.TrimSpace(first)
		if len(first) > blurbTaskFirstLineMax {
			first = first[:blurbTaskFirstLineMax]
		}
		out = append(out, fmt.Sprintf("%s [%s]", first, t.Status))
	}
	return out
}

// realGitDiffStat resolves the `git diff --stat main...HEAD` signal for a
// worktree (branch changes since the merge-base with main). Best-effort: any
// error (missing base branch, not-a-repo) yields an empty signal. stdio is
// redirected to io.Discard so errors never inherit the parent's FD 2 in TUI
// mode (mirrors realGitRevParseHEAD).
func realGitDiffStat(worktree string) (string, error) {
	if worktree == "" {
		return "", nil
	}
	cmd := exec.Command("git", "-C", worktree, "diff", "--stat", "main...HEAD") //nolint:gosec // args are not user-controlled
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
