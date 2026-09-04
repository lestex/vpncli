package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// spinnerFrames is one revolution. Braille is used because every frame is the
// same width, so the message after it never shifts.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval is how often a frame is drawn. Fast enough to read as
// movement, slow enough that a serial console is not flooded.
const spinnerInterval = 100 * time.Millisecond

// clearLine returns the cursor to the start of the line and wipes what was
// there, so the spinner leaves nothing behind for the next line to collide
// with.
const clearLine = "\r\x1b[K"

// spinner animates one line while something slow happens. Waiting for a server
// to boot takes a minute or so, and a command that prints nothing for a minute
// is indistinguishable from one that has hung.
//
// It draws only to a terminal. Redirected into a file or a pipe the whole
// thing is silent, so logs and tests keep the plain output they had.
type spinner struct {
	out      io.Writer
	interval time.Duration
	started  time.Time

	// mu guards message, which the caller changes as the work moves on while
	// the drawing goroutine is reading it.
	mu      sync.Mutex
	message string

	silent bool
	once   sync.Once
	quit   chan struct{}
	done   chan struct{}
}

// say changes what the spinner is waiting for. A bootstrap runs to several
// minutes and goes through a dozen steps, and which one it is on is the
// difference between a wait and a stall.
func (s *spinner) say(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = message
}

// saying is the current message.
func (s *spinner) saying() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.message
}

// startSpinner begins animating message until the returned spinner is stopped.
func startSpinner(out io.Writer, message string) *spinner {
	s := &spinner{
		out:      out,
		message:  message,
		interval: spinnerInterval,
		started:  time.Now(),
		silent:   !animates(out),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	if s.silent {
		return s
	}

	go s.run()
	return s
}

// stop ends the animation and clears the line. It waits for the drawing to
// finish, so whatever the caller prints next cannot land on a half-drawn
// frame. Calling it twice is harmless, which is what lets it be deferred and
// called on the success path both.
func (s *spinner) stop() {
	s.once.Do(func() {
		if s.silent {
			return
		}
		close(s.quit)
		<-s.done
	})
}

// elapsed is how long the spinner has been running, for the caller to report
// once it is over.
func (s *spinner) elapsed() time.Duration {
	return time.Since(s.started)
}

func (s *spinner) run() {
	defer close(s.done)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for i := 0; ; i++ {
		fmt.Fprintf(s.out, "\r%s %s (%s)\x1b[K", spinnerFrames[i%len(spinnerFrames)], s.saying(), took(s.elapsed()))

		select {
		case <-s.quit:
			fmt.Fprint(s.out, clearLine)
			return
		case <-ticker.C:
		}
	}
}

// took renders a duration the way a wait is talked about: whole seconds, and
// minutes once there are any.
func took(d time.Duration) string {
	return d.Round(time.Second).String()
}

// animates reports whether out is a terminal worth drawing on. A pipe, a file
// and a terminal that has said it cannot do better all get plain output.
func animates(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return false
	}

	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
