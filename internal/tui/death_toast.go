// QUM-725: death observability — AgentDiedMsg + BuildDeathToast.
//
// A Died agent fires AgentDiedMsg into the TUI; the app reducer spawns a
// persistent (user-only-dismiss) error toast informing the user that the
// agent died and that its parent has been notified (the route-up path in
// Real.SendMessage / Real.ReportStatus delivers the parent notification).
package tui

import (
	"fmt"
	"time"
)

// AgentDiedMsg is dispatched into the TUI when a child runtime transitions
// to liveness.Died. Name/Type/Parent identify the dead agent; LastSeen is
// the runtime's last activity timestamp. A zero LastSeen renders as
// "just now" (sentinel: covers the case where the runtime never recorded
// any activity).
type AgentDiedMsg struct {
	Name     string
	Type     string
	Parent   string
	LastSeen time.Time
}

// BuildDeathToast renders the persistent error toast surfaced when an agent
// dies. Template: `<name> (<type>) died — last seen <humanize> ago. Parent
// <parent> notified.` Style is ToastError because death is an error-class
// event; DismissOn is UserOnlyDismiss so the user must acknowledge it
// explicitly (no auto-dismiss timer — death is not a transient signal).
func BuildDeathToast(msg AgentDiedMsg, now time.Time) Toast {
	var ago string
	if msg.LastSeen.IsZero() {
		ago = "just now"
	} else {
		ago = humanizeSince(now.Sub(msg.LastSeen)) + " ago"
	}
	text := fmt.Sprintf("%s (%s) died — last seen %s. Parent %s notified.",
		msg.Name, msg.Type, ago, msg.Parent)
	return Toast{
		Text:      text,
		Style:     ToastError,
		DismissOn: UserOnlyDismiss(),
	}
}

// humanizeSince renders a non-negative duration as a short, agent-readable
// "Xs"/"Xm"/"Xh"/"Xd" string. Negative durations are clamped to 0s (clock
// skew should not produce nonsense).
func humanizeSince(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		s := int(d.Round(time.Second).Seconds())
		return fmt.Sprintf("%ds", s)
	case d < time.Hour:
		m := int(d.Round(time.Minute).Minutes())
		return fmt.Sprintf("%dm", m)
	case d < 24*time.Hour:
		h := int(d.Round(time.Hour).Hours())
		return fmt.Sprintf("%dh", h)
	default:
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%dd", days)
	}
}
