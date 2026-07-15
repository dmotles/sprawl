package stream

import "testing"

// buildLog appends n frames (seq 1..n) to a fresh Log and returns it.
func buildLog(n int) *Log {
	l := &Log{}
	for i := 0; i < n; i++ {
		l.Append(KindHeartbeat, "", int64(i))
	}
	return l
}

func seqs(frames []Frame) []uint64 {
	out := make([]uint64, len(frames))
	for i, f := range frames {
		out[i] = f.Seq
	}
	return out
}

func TestAppendAssignsMonotonicSeqFromOne(t *testing.T) {
	l := &Log{}
	for i := uint64(1); i <= 5; i++ {
		f := l.Append(KindData, "x", 0)
		if f.Seq != i {
			t.Fatalf("append %d: got seq %d, want %d", i, f.Seq, i)
		}
	}
}

func TestFramesSinceFromZeroReturnsAll(t *testing.T) {
	l := buildLog(4)
	got := seqs(l.Since(0))
	want := []uint64{1, 2, 3, 4}
	if !equal(got, want) {
		t.Fatalf("from 0: got %v, want %v", got, want)
	}
}

func TestFramesSinceFromMidReturnsTail(t *testing.T) {
	l := buildLog(5)
	got := seqs(l.Since(2))
	want := []uint64{3, 4, 5}
	if !equal(got, want) {
		t.Fatalf("from 2: got %v, want %v", got, want)
	}
}

func TestFramesSinceAtOrBeyondLastReturnsEmpty(t *testing.T) {
	l := buildLog(3)
	if got := l.Since(3); len(got) != 0 {
		t.Fatalf("from last: got %v, want empty", seqs(got))
	}
	if got := l.Since(99); len(got) != 0 {
		t.Fatalf("from beyond: got %v, want empty", seqs(got))
	}
}

// The reconnect "one rule": resuming from the last-seen seq yields zero dupes
// (first returned seq == fromSeq+1) and zero gaps (strictly contiguous +1).
func TestReconnectNoGapsNoDupes(t *testing.T) {
	l := buildLog(10)
	const lastSeen = 6
	got := l.Since(lastSeen)
	if len(got) == 0 {
		t.Fatal("expected a resume tail, got none")
	}
	if got[0].Seq != lastSeen+1 {
		t.Fatalf("dupe/gap at head: first seq %d, want %d", got[0].Seq, lastSeen+1)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("gap: seq %d follows %d", got[i].Seq, got[i-1].Seq)
		}
	}
	if last := got[len(got)-1].Seq; last != 10 {
		t.Fatalf("tail should reach head: last seq %d, want 10", last)
	}
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
