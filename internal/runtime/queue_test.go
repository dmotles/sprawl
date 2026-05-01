package runtime

import (
	"sync"
	"testing"
	"time"
)

func TestMessageQueue_EmptyDrainReturnsEmpty(t *testing.T) {
	q := NewMessageQueue()
	if got := q.DrainAll(); len(got) != 0 {
		t.Fatalf("DrainAll on empty queue: got %d items, want 0", len(got))
	}
	if got := q.Len(); got != 0 {
		t.Fatalf("Len on empty queue: got %d, want 0", got)
	}
}

func TestMessageQueue_FIFOWithinSameClass(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "async", Prompt: "a"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "b"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "c"})

	if got := q.Len(); got != 3 {
		t.Fatalf("Len: got %d, want 3", got)
	}

	items := q.DrainAll()
	if len(items) != 3 {
		t.Fatalf("DrainAll: got %d items, want 3", len(items))
	}
	want := []string{"a", "b", "c"}
	for i, it := range items {
		if it.Prompt != want[i] {
			t.Errorf("item[%d].Prompt = %q, want %q", i, it.Prompt, want[i])
		}
	}
}

func TestMessageQueue_PriorityOrdering(t *testing.T) {
	q := NewMessageQueue()
	// Enqueue in reverse priority order to verify sort actually runs.
	q.Enqueue(QueueItem{Class: "inbox", Prompt: "inbox1"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "async1"})
	q.Enqueue(QueueItem{Class: "user", Prompt: "user1"})
	q.Enqueue(QueueItem{Class: "task", Prompt: "task1"})
	q.Enqueue(QueueItem{Class: "interrupt", Prompt: "int1"})

	items := q.DrainAll()
	if len(items) != 5 {
		t.Fatalf("DrainAll: got %d items, want 5", len(items))
	}
	wantClasses := []string{"interrupt", "task", "user", "async", "inbox"}
	for i, it := range items {
		if it.Class != wantClasses[i] {
			t.Errorf("item[%d].Class = %q, want %q", i, it.Class, wantClasses[i])
		}
	}
}

func TestMessageQueue_PriorityStableWithinClass(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "async", Prompt: "a1"})
	q.Enqueue(QueueItem{Class: "interrupt", Prompt: "i1"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "a2"})
	q.Enqueue(QueueItem{Class: "interrupt", Prompt: "i2"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "a3"})

	items := q.DrainAll()
	wantPrompts := []string{"i1", "i2", "a1", "a2", "a3"}
	if len(items) != len(wantPrompts) {
		t.Fatalf("DrainAll: got %d items, want %d", len(items), len(wantPrompts))
	}
	for i, it := range items {
		if it.Prompt != wantPrompts[i] {
			t.Errorf("item[%d].Prompt = %q, want %q", i, it.Prompt, wantPrompts[i])
		}
	}
}

func TestMessageQueue_DrainClearsQueue(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "async", Prompt: "a"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "b"})

	first := q.DrainAll()
	if len(first) != 2 {
		t.Fatalf("first DrainAll: got %d, want 2", len(first))
	}
	if got := q.Len(); got != 0 {
		t.Errorf("Len after drain: got %d, want 0", got)
	}
	second := q.DrainAll()
	if len(second) != 0 {
		t.Errorf("second DrainAll: got %d, want 0", len(second))
	}
}

func TestMessageQueue_PreservesEntryIDs(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "inbox", Prompt: "p", EntryIDs: []string{"e1", "e2"}})
	items := q.DrainAll()
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	got := items[0].EntryIDs
	if len(got) != 2 || got[0] != "e1" || got[1] != "e2" {
		t.Errorf("EntryIDs = %v, want [e1 e2]", got)
	}
}

func TestMessageQueue_SignalFiresOnEnqueue(t *testing.T) {
	q := NewMessageQueue()

	select {
	case <-q.Signal():
		t.Fatal("signal fired on empty queue")
	default:
	}

	q.Enqueue(QueueItem{Class: "user", Prompt: "hi"})

	select {
	case <-q.Signal():
		// good
	case <-time.After(time.Second):
		t.Fatal("signal did not fire after Enqueue")
	}
}

func TestMessageQueue_SignalCoalescesMultipleEnqueues(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "async", Prompt: "1"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "2"})
	q.Enqueue(QueueItem{Class: "async", Prompt: "3"})

	// First receive succeeds.
	select {
	case <-q.Signal():
	case <-time.After(time.Second):
		t.Fatal("signal did not fire")
	}

	// Buffer is size 1, so additional enqueues without drain should not stack.
	select {
	case <-q.Signal():
		t.Fatal("signal fired more than once before drain")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMessageQueue_SignalResetsAfterDrain(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "async", Prompt: "1"})
	// Don't read the signal — let DrainAll reset it.
	_ = q.DrainAll()

	// After drain, signal channel should be empty (turn loop will block).
	select {
	case <-q.Signal():
		t.Fatal("signal channel still buffered after drain")
	case <-time.After(50 * time.Millisecond):
	}

	// New enqueue should fire signal again.
	q.Enqueue(QueueItem{Class: "async", Prompt: "2"})
	select {
	case <-q.Signal():
	case <-time.After(time.Second):
		t.Fatal("signal did not fire after post-drain enqueue")
	}
}

func TestMessageQueue_ConcurrentEnqueueSafety(t *testing.T) {
	q := NewMessageQueue()
	const goroutines = 20
	const perGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				q.Enqueue(QueueItem{Class: "async", Prompt: "x"})
			}
		}()
	}
	wg.Wait()

	if got, want := q.Len(), goroutines*perGoroutine; got != want {
		t.Fatalf("Len after concurrent enqueues: got %d, want %d", got, want)
	}
	items := q.DrainAll()
	if len(items) != goroutines*perGoroutine {
		t.Fatalf("DrainAll: got %d, want %d", len(items), goroutines*perGoroutine)
	}
}

func TestMessageQueue_ConcurrentEnqueueAndDrain(t *testing.T) {
	q := NewMessageQueue()
	const producers = 8
	const perProducer = 100
	total := producers * perProducer

	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				q.Enqueue(QueueItem{Class: "async", Prompt: "x"})
			}
		}()
	}

	collected := 0
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	for {
		items := q.DrainAll()
		collected += len(items)
		if collected >= total {
			break
		}
		select {
		case <-done:
			// One last drain to capture stragglers.
			collected += len(q.DrainAll())
			if collected != total {
				t.Errorf("collected %d, want %d", collected, total)
			}
			return
		case <-q.Signal():
		case <-time.After(time.Second):
			t.Fatalf("timeout: collected %d of %d", collected, total)
		}
	}
}

func TestMessageQueue_Wake_UnblocksSignalReceiver(t *testing.T) {
	q := NewMessageQueue()

	// Wake on empty queue must produce a signal that a receiver can read.
	q.Wake()

	select {
	case <-q.Signal():
		// good
	case <-time.After(time.Second):
		t.Fatal("Signal did not fire after Wake on empty queue")
	}

	// Queue must still be empty — Wake doesn't enqueue anything.
	if got := q.Len(); got != 0 {
		t.Errorf("Len after Wake on empty queue: got %d, want 0", got)
	}
}

func TestMessageQueue_Wake_CoalescesWithEnqueue(t *testing.T) {
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "hi"})
	// Signal slot is now full from Enqueue. A subsequent Wake should be a
	// no-op (coalesced) rather than blocking or stacking signals.
	q.Wake()

	// Exactly one signal is readable.
	select {
	case <-q.Signal():
	case <-time.After(time.Second):
		t.Fatal("Signal did not fire after Enqueue+Wake")
	}

	// No second signal should be available.
	select {
	case <-q.Signal():
		t.Fatal("Wake produced a second buffered signal after Enqueue already filled the slot")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMessageQueue_Wake_NoBlockOnRepeatedCalls(t *testing.T) {
	q := NewMessageQueue()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			q.Wake()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated Wake calls blocked")
	}
}

func TestMessageQueue_UnknownClassSortsLast(t *testing.T) {
	// Defensive: an unrecognized class should not panic and should not jump
	// ahead of known classes.
	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: "weird", Prompt: "w"})
	q.Enqueue(QueueItem{Class: "interrupt", Prompt: "i"})
	q.Enqueue(QueueItem{Class: "inbox", Prompt: "ib"})

	items := q.DrainAll()
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Class != "interrupt" {
		t.Errorf("items[0].Class = %q, want interrupt", items[0].Class)
	}
	// "weird" should not come before "inbox" (lowest known).
	weirdIdx, inboxIdx := -1, -1
	for i, it := range items {
		switch it.Class {
		case "weird":
			weirdIdx = i
		case "inbox":
			inboxIdx = i
		}
	}
	if weirdIdx < inboxIdx {
		t.Errorf("unknown class %q sorted before inbox (idx %d < %d)", "weird", weirdIdx, inboxIdx)
	}
}
