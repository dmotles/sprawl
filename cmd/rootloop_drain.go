// rootloop_drain.go wires a queue-drain goroutine into the tmux-mode weave
// root-loop. It polls weave's harness queue (`.sprawl/agents/weave/queue/
// pending/`) at a fixed cadence and, on pending entries, renders the
// async/interrupt flush prompt (via internal/agentloop) and injects it into
// the weave tmux pane via `tmux send-keys -l`. After a successful inject the
// entries are marked delivered.
//
// This provides tmux-mode parity with the child agentloop's flushQueue step,
// so child agents can reach weave asynchronously without the human acting as
// a message pump. See QUM-323 and docs/designs/messaging-overhaul.md §4.5.
//
// Coexists with (does NOT replace) buildLegacyRootNotifier's `[inbox] New
// message from <from>` send-keys poke — that path is gated on
// SPRAWL_MESSAGING=legacy and remains the rollback surface.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
)

// rootDrainInterval is how often the drain poller wakes to check weave's
// harness queue. 2s matches the TUI's tickAgentsCmd cadence.
const rootDrainInterval = 2 * time.Second

// rootDrainer polls weave's harness queue and injects pending entries into
// the weave tmux pane. Exposed as a struct (rather than a free function)
// purely so tests can inject deps; production callers use startRootDrainLoop.
type rootDrainer struct {
	sprawlRoot string
	rootName   string
	// sendPrompt is invoked with the rendered flush-prompt text. In production
	// this runs `tmux send-keys -l -t <session>:<window> <prompt>` followed by
	// a separate Enter keypress. Tests stub this to capture prompts.
	sendPrompt func(prompt string) error
	// peek returns the pending queue contents without mutating disk.
	peek func(sprawlRoot, agentName string) ([]agentloop.Entry, error)
	// markDelivered moves a single entry from pending/ to delivered/.
	markDelivered func(sprawlRoot, agentName, entryID string) error
	// interval overrides rootDrainInterval in tests.
	interval time.Duration
	logW     io.Writer
}

// defaultPeek and defaultMarkDelivered forward to the real agentloop
// functions. Factored out so tests can stub.
func defaultPeek(sprawlRoot, agentName string) ([]agentloop.Entry, error) {
	return agentloop.ListPending(sprawlRoot, agentName)
}

func defaultMarkDelivered(sprawlRoot, agentName, entryID string) error {
	return agentloop.MarkDelivered(sprawlRoot, agentName, entryID)
}

// tmuxSendLiteral runs `tmux send-keys -l -t <target> <text>` followed by
// `tmux send-keys -t <target> Enter`. The -l (literal) flag prevents tmux from
// interpreting keyname substrings (e.g. "Enter", "Tab") inside the prompt
// body as key events, which matters because flush prompts are free-form text.
func tmuxSendLiteral(tmuxPath, session, window, text string) error {
	target := session + ":" + window
	// Literal text — may contain newlines. tmux treats each character as a
	// keystroke; newlines within the body become Enter in the receiver's
	// input, which is what we want (Claude's input box preserves them).
	if err := exec.Command(tmuxPath, "send-keys", "-l", "-t", target, text).Run(); err != nil { //nolint:gosec // arguments are not user-controlled
		return fmt.Errorf("send-keys -l: %w", err)
	}
	// Final commit keystroke.
	if err := exec.Command(tmuxPath, "send-keys", "-t", target, "Enter").Run(); err != nil { //nolint:gosec // arguments are not user-controlled
		return fmt.Errorf("send-keys Enter: %w", err)
	}
	return nil
}

// runOnce performs one drain pass: peek pending, render the (interrupt |
// async) prompt, inject via sendPrompt, and on success mark entries delivered.
// Returns the number of entries drained (interrupts + asyncs) plus any error.
// Non-fatal: errors are logged and the caller continues polling.
func (d *rootDrainer) runOnce() (int, error) {
	pending, err := d.peek(d.sprawlRoot, d.rootName)
	if err != nil {
		return 0, fmt.Errorf("peek pending: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}
	interrupts, asyncs := agentloop.SplitByClass(pending)
	drained := 0

	if len(interrupts) > 0 {
		prompt := agentloop.BuildInterruptFlushPrompt(interrupts)
		if err := d.sendPrompt(prompt); err != nil {
			return drained, fmt.Errorf("send interrupt prompt: %w", err)
		}
		for _, e := range interrupts {
			if mErr := d.markDelivered(d.sprawlRoot, d.rootName, e.ID); mErr != nil {
				fmt.Fprintf(d.logW, "[root-drain] warn: mark %s delivered: %v\n", e.ID, mErr)
			}
		}
		drained += len(interrupts)
	}

	if len(asyncs) > 0 {
		prompt := agentloop.BuildQueueFlushPrompt(asyncs)
		if err := d.sendPrompt(prompt); err != nil {
			return drained, fmt.Errorf("send async prompt: %w", err)
		}
		for _, e := range asyncs {
			if mErr := d.markDelivered(d.sprawlRoot, d.rootName, e.ID); mErr != nil {
				fmt.Fprintf(d.logW, "[root-drain] warn: mark %s delivered: %v\n", e.ID, mErr)
			}
		}
		drained += len(asyncs)
	}

	return drained, nil
}

// run blocks in a poll loop until ctx is cancelled. Each iteration calls
// runOnce, logs any error, and waits interval before the next iteration.
func (d *rootDrainer) run(ctx context.Context) {
	iv := d.interval
	if iv <= 0 {
		iv = rootDrainInterval
	}
	ticker := time.NewTicker(iv)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := d.runOnce(); err != nil {
				fmt.Fprintf(d.logW, "[root-drain] %v\n", err)
			} else if n > 0 {
				fmt.Fprintf(d.logW, "[root-drain] drained %d entries to weave pane\n", n)
			}
		}
	}
}

// startRootDrainLoop spawns a goroutine that drains weave's harness queue into
// the weave tmux pane for the lifetime of ctx. Returns a WaitGroup-backed
// cancel that lets the caller block until the drainer exits (so logs don't
// race with shutdown output).
func startRootDrainLoop(
	ctx context.Context,
	sprawlRoot, rootName, tmuxPath, session, window string,
	logW io.Writer,
) (wait func()) {
	d := &rootDrainer{
		sprawlRoot:    sprawlRoot,
		rootName:      rootName,
		sendPrompt:    func(prompt string) error { return tmuxSendLiteral(tmuxPath, session, window, prompt) },
		peek:          defaultPeek,
		markDelivered: defaultMarkDelivered,
		interval:      rootDrainInterval,
		logW:          logW,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.run(ctx)
	}()
	return wg.Wait
}
