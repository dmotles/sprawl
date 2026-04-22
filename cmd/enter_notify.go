package cmd

import (
	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/tui"
)

// buildTUIRootNotifier returns a messages.NotifyFunc that delivers
// inbox-arrival signals into the running bubbletea program.
//
// The returned closure fires only when the delivered message is addressed to
// the root agent (rootName); messages to child agents are ignored because the
// TUI's tree panel already polls each child's maildir via tickAgentsCmd. On a
// match, send is invoked with tui.InboxArrivalMsg, which the AppModel's
// Update handler turns into a status banner + an immediate tree refresh so
// the weave row's unread badge updates within ~1s.
//
// Returns nil if send or rootName is empty; callers should treat a nil return
// as "no TUI notifier to install" and leave the process-level notifier at its
// existing (legacy / noop) value. See QUM-311.
func buildTUIRootNotifier(rootName string, send func(tea.Msg)) messages.NotifyFunc {
	if send == nil || rootName == "" {
		return nil
	}
	return func(to, from, subject, _ /*msgID*/ string) {
		if to != rootName {
			return
		}
		send(tui.InboxArrivalMsg{From: from, Subject: subject})
	}
}
