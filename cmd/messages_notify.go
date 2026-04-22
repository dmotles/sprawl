package cmd

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// buildLegacyRootNotifier returns the process-level messages.NotifyFunc that
// pokes the root weave tmux pane with an `[inbox] New message from <from>`
// line when delivering in legacy mode.
//
// The returned closure gates on:
//   - getenv("SPRAWL_MESSAGING") == "legacy"   (rollback knob; async queue
//     path is the default delivery mechanism post-QUM-292/293/295)
//   - the recipient matches the root agent name (resolved from on-disk state
//     at call time, falling back to tmux.DefaultRootName)
//
// Both conditions must hold or the closure no-ops. Tmux send-keys errors are
// silently swallowed — notification is best-effort; delivery already
// succeeded before Send invokes the notifier. See QUM-310.
//
// The builder captures tmuxRunner and sprawlRoot at construction time, but
// re-reads env + state on each call so late configuration (e.g.
// SPRAWL_MESSAGING flipped after process start) is observed.
func buildLegacyRootNotifier(getenv func(string) string, tmuxRunner tmux.Runner, sprawlRoot string) messages.NotifyFunc {
	if tmuxRunner == nil || getenv == nil || sprawlRoot == "" {
		return nil
	}
	return func(to, from, _ /*subject*/, msgID string) {
		if getenv("SPRAWL_MESSAGING") != "legacy" {
			return
		}
		rootName := state.ReadRootName(sprawlRoot)
		if rootName == "" {
			rootName = tmux.DefaultRootName
		}
		if to != rootName {
			return
		}
		namespace := getenv("SPRAWL_NAMESPACE")
		if namespace == "" {
			namespace = state.ReadNamespace(sprawlRoot)
		}
		if namespace == "" {
			namespace = tmux.DefaultNamespace
		}
		rootSession := tmux.RootSessionName(namespace)
		notification := fmt.Sprintf("[inbox] New message from %s. Run: `sprawl messages read %s`", from, msgID)
		_ = tmuxRunner.SendKeys(rootSession, tmux.RootWindowName, notification)
	}
}
