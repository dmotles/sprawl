package calllog

import "time"

// TestOptions configures deterministic seams for tests.
type TestOptions struct {
	Now   func() time.Time
	NewID func() string
	// SyncCounter, if non-nil, is incremented by the Logger on every fsync
	// of the JSONL file. Used by TestLogger_FsyncEveryLine to verify
	// per-line durability.
	SyncCounter *int
}

// SetMaxLogBytesForTest overrides the rotation size threshold and returns a
// restore func. Used by rotation tests so they don't have to write 64 MiB.
func SetMaxLogBytesForTest(n int64) func() {
	prev := maxLogBytes
	maxLogBytes = n
	return func() { maxLogBytes = prev }
}

// OpenForTest returns the logger plus a triggerTick function that drives a
// single heartbeat iteration synchronously and blocks until the in-flight
// registry has been flushed to disk.
func OpenForTest(sprawlRoot string, opts TestOptions) (*Logger, func(), error) {
	l, err := openInternal(sprawlRoot, internalOptions(opts))
	if err != nil {
		return nil, nil, err
	}
	l.tickReq = make(chan chan struct{})
	go l.runHeartbeat()

	triggerTick := func() {
		done := make(chan struct{})
		select {
		case l.tickReq <- done:
			<-done
		case <-l.stop:
		}
	}
	return l, triggerTick, nil
}
