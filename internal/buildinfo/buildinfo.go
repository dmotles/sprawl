// Package buildinfo holds this build's linker stamp and answers one question
// the rest of the tree could not previously ask: is the image this process is
// RUNNING still the image installed on disk?
//
// Every merge-safety gate sprawl has measures an artifact — a tree, a file, a
// version string, a grep result. None measures the process. On 2026-08-07 a
// `make install` replaced the binary while a `sprawl enter` session kept
// running the deleted inode, so the MCP server served `merge` from the old
// engine while every available check correctly reported the new one (QUM-1154).
package buildinfo

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"strings"
	"sync"
)

// deletedSuffix is what Linux appends to /proc/<pid>/exe once the running
// image's directory entry is gone.
const deletedSuffix = " (deleted)"

// notVerified is the phrase every degraded verdict carries. An absent field
// reads as "fine", so a check that could not run has to say so in words.
const notVerified = "was NOT verified"

var (
	mu      sync.RWMutex
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Set records the linker-stamped identity. Called from main().
func Set(v, c, d string) {
	mu.Lock()
	defer mu.Unlock()
	version, commit, date = v, c, d
}

// Version returns the stamped version, or "dev".
func Version() string {
	mu.RLock()
	defer mu.RUnlock()
	return version
}

// Commit returns the stamped commit, or "none".
func Commit() string {
	mu.RLock()
	defer mu.RUnlock()
	return commit
}

// Date returns the stamped build date, or "unknown".
func Date() string {
	mu.RLock()
	defer mu.RUnlock()
	return date
}

// ImageStatus is the running-vs-on-disk verdict, emitted verbatim as the
// `runtime` block of the MCP status tool.
//
// Only Detail is omitempty: its absence is how a clean image stays quiet. The
// verdict fields are always emitted, because a missing field reads as "fine"
// and that misreading is the whole subject of QUM-1154.
type ImageStatus struct {
	ExePath       string `json:"exe_path"`
	ExeCheck      string `json:"exe_check"` // ok | deleted | unavailable
	RunningCommit string `json:"running_commit"`
	OnDiskCommit  string `json:"on_disk_commit"`
	CommitCheck   string `json:"commit_check"` // match | differ | unknown
	Stale         bool   `json:"stale"`
	Detail        string `json:"detail,omitempty"`
}

// Image inspects the image THIS process is running, right now.
//
// Deliberately os.Readlink and not os.Executable: os.Executable strips the
// " (deleted)" marker (go/src/os/executable_procfs.go), so a check built on it
// stats the freshly-installed replacement, finds it present, and reports a
// confident false clean in exactly the case this exists to catch.
//
// Recomputed per call, never cached: the divergence is created DURING a
// session by `make install`, so a verdict snapshotted at startup is
// structurally blind to it.
//
// AC3's live exercise (QUM-1154): with sprawl installed and a `sprawl enter`
// session running from the installed binary, call the MCP `status` tool and
// record the runtime block; run `make install` from a different commit in
// another terminal; confirm `readlink /proc/<pid>/exe` now ends in
// " (deleted)"; call `status` again and expect exe_check "deleted", stale
// true, and both commits named; then restart and expect the block to go quiet
// again. All three readings are the assertion — an always-loud field is as
// useless as an always-quiet one.
func Image() ImageStatus {
	link, err := os.Readlink("/proc/self/exe")
	return classifyImage(link, err, Commit(), os.Stat, onDiskCommit)
}

// classifyImage is the pure verdict, over an already-read link.
func classifyImage(
	link string,
	linkErr error,
	runningCommit string,
	statFn func(string) (os.FileInfo, error),
	onDiskCommitFn func(string) (string, error),
) ImageStatus {
	st := ImageStatus{RunningCommit: runningCommit}

	if linkErr != nil || link == "" {
		st.ExeCheck = "unavailable"
		st.CommitCheck = "unknown"
		reason := "the link was empty"
		if linkErr != nil {
			reason = linkErr.Error()
		}
		st.Detail = fmt.Sprintf(
			"cannot read /proc/self/exe (%s): this process's image %s (the check is Linux-only)",
			reason, notVerified)
		return st
	}

	st.ExePath = link
	st.ExeCheck = "ok"
	if strings.HasSuffix(link, deletedSuffix) {
		// The marker is only meaningful if nothing answers to that literal
		// name. A file genuinely called "sprawl (deleted)" is not stale.
		if _, err := statFn(link); err != nil {
			st.ExeCheck = "deleted"
			st.Stale = true
			st.ExePath = strings.TrimSuffix(link, deletedSuffix)
		}
	}

	if c, err := onDiskCommitFn(st.ExePath); err == nil {
		st.OnDiskCommit = c
	}

	switch {
	case isUnknownCommit(runningCommit) || isUnknownCommit(st.OnDiskCommit):
		st.CommitCheck = "unknown"
	case runningCommit == st.OnDiskCommit:
		st.CommitCheck = "match"
	default:
		st.CommitCheck = "differ"
		st.Stale = true
	}

	st.Detail = describe(st, link)
	return st
}

// isUnknownCommit reports whether a commit string carries no usable identity.
// Applied symmetrically to both sides so an unstamped dev build degrades to
// "unknown" rather than manufacturing a permanent false divergence.
func isUnknownCommit(c string) bool {
	return c == "" || c == "none" || c == "dev"
}

func describe(st ImageStatus, link string) string {
	var parts []string
	if st.ExeCheck == "deleted" {
		parts = append(parts, fmt.Sprintf(
			"the running image was replaced on disk: /proc/self/exe -> %q", link))
	}
	switch st.CommitCheck {
	case "differ":
		parts = append(parts, fmt.Sprintf(
			"this process is running commit %s; the binary at %s is commit %s",
			st.RunningCommit, st.ExePath, st.OnDiskCommit))
	case "unknown":
		parts = append(parts, fmt.Sprintf(
			"running commit %q vs on-disk commit %q: at least one carries no linker stamp, so running-vs-on-disk %s",
			st.RunningCommit, st.OnDiskCommit, notVerified))
	}
	if len(parts) == 0 {
		return ""
	}
	if st.Stale {
		parts = append(parts, "restart sprawl to run the installed build")
	}
	return strings.Join(parts, "; ") + "."
}

// onDiskCommit recovers a binary's stamped commit WITHOUT executing it, by
// reading the -ldflags build setting the Go toolchain records in the image.
//
// Only the -X main.commit stamp is honoured — no vcs.revision fallback. The
// running side has nothing but the stamp, and comparing a vcs.revision on disk
// against a stamp in memory compares two different provenances, reporting
// `differ` for identical source.
func onDiskCommit(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, s := range info.Settings {
		if s.Key != "-ldflags" {
			continue
		}
		for _, f := range strings.Fields(s.Value) {
			if v, ok := strings.CutPrefix(f, "-X"); ok && strings.HasPrefix(v, "main.commit=") {
				return strings.TrimPrefix(v, "main.commit="), nil
			}
			if v, ok := strings.CutPrefix(f, "main.commit="); ok {
				return v, nil
			}
		}
	}
	return "", nil
}
