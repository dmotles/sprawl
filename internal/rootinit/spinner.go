package rootinit

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinner displays an animated progress indicator on a single terminal line.
type spinner struct {
	w      io.Writer
	prefix string
	label  string
	done   chan struct{}
	wg     sync.WaitGroup
}

// startSpinner starts a background goroutine that animates the spinner.
// prefix is prepended to each frame (typically "[root-loop]" or "[enter]").
// If empty, frames are rendered without a bracketed prefix.
func startSpinner(w io.Writer, prefix, label string) *spinner {
	s := &spinner{
		w:      w,
		prefix: prefix,
		label:  label,
		done:   make(chan struct{}),
	}
	s.wg.Add(1)
	go s.run()
	return s
}

// stop halts the spinner and clears the line. Blocks until cleanup is done.
func (s *spinner) stop() {
	close(s.done)
	s.wg.Wait()
}

func (s *spinner) run() {
	defer s.wg.Done()
	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	start := time.Now()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	i := 0
	for {
		select {
		case <-s.done:
			fmt.Fprintf(s.w, "\033[2K\r")
			return
		case <-tick.C:
			elapsed := time.Since(start).Truncate(time.Second)
			if s.prefix != "" {
				fmt.Fprintf(s.w, "\033[2K\r%s   %c %s (%s)", s.prefix, frames[i%len(frames)], s.label, elapsed)
			} else {
				fmt.Fprintf(s.w, "\033[2K\r  %c %s (%s)", frames[i%len(frames)], s.label, elapsed)
			}
			i++
		}
	}
}
