package main

import (
	"embed"

	"github.com/dmotles/sprawl/internal/hooks"
)

// hookScriptsFS embeds the canonical guard scripts so `sprawl hooks install` is
// self-contained on repos that have no scripts/ directory. go:embed cannot
// traverse "..", so this lives in package main (the only package whose
// directory contains scripts/ as a subdirectory). The same scripts/ files are
// the single source of truth shared with the QUM-837 repo-local hook install.
//
//go:embed scripts/guard-main-commit scripts/guard-main-ref scripts/guard-employer-leak
var hookScriptsFS embed.FS

// embeddedHookAssets reads the embedded guard bodies. The files are guaranteed
// present at compile time by the embed directive above.
func embeddedHookAssets() hooks.Assets {
	commit, err := hookScriptsFS.ReadFile("scripts/guard-main-commit")
	if err != nil {
		panic("embed: scripts/guard-main-commit: " + err.Error())
	}
	ref, err := hookScriptsFS.ReadFile("scripts/guard-main-ref")
	if err != nil {
		panic("embed: scripts/guard-main-ref: " + err.Error())
	}
	leak, err := hookScriptsFS.ReadFile("scripts/guard-employer-leak")
	if err != nil {
		panic("embed: scripts/guard-employer-leak: " + err.Error())
	}
	return hooks.Assets{CommitGuard: commit, RefGuard: ref, LeakGuard: leak}
}
