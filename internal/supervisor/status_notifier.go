package supervisor

import "sync"

// statusNotifier is the in-process per-recipient ring used by
// Real.ReportStatus to emit ephemeral <system-notification> lines
// (QUM-559). Lines are pushed once via Enqueue and consumed by the
// parent's drain path (peekAndDrainCmd / unifiedHandle.drainPendingToQueue)
// via Drain. Not persisted to maildir; not retrievable via messages_read;
// not counted in unread.
type statusNotifier struct {
	mu    sync.Mutex
	rings map[string][]string
}

func newStatusNotifier() *statusNotifier {
	return &statusNotifier{rings: make(map[string][]string)}
}

// Enqueue appends line onto the per-recipient FIFO ring. Empty recipient
// or empty line is a no-op.
func (n *statusNotifier) Enqueue(recipient, line string) {
	if recipient == "" || line == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rings[recipient] = append(n.rings[recipient], line)
}

// Drain returns and clears recipient's ring in FIFO order. Returns nil
// (empty slice) when the ring is empty or absent.
func (n *statusNotifier) Drain(recipient string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	lines := n.rings[recipient]
	delete(n.rings, recipient)
	return lines
}
