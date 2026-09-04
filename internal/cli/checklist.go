package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Marks down the left of a checklist. They are one column wide so the text
// after them lines up whatever state a step is in.
const (
	markPending = " "
	markDone    = "✓"
	markFailed  = "✗"
)

// cursorUp moves back to the top of the block so it can be drawn again. A
// checklist is a handful of lines that are rewritten in place; anything longer
// than the window would scroll and this would rewrite the wrong lines.
func cursorUp(n int) string { return fmt.Sprintf("\x1b[%dA", n) }

// checklist shows the steps of a long job and ticks them off.
//
// It is the same idea as the spinner and for the same reason - a command that
// prints nothing for three minutes looks hung - but a bootstrap is eight
// distinct steps, and which one is running says much more than that something
// is. Seeing the whole list also says what is still to come.
//
// Off a terminal it prints each step once as it starts, so a log reads as a
// list of what happened, in order, with no cursor games in it.
type checklist struct {
	out      io.Writer
	steps    []string
	interval time.Duration
	started  time.Time

	mu     sync.Mutex
	state  []rune // the mark against each step
	active int    // the step being worked on, or -1
	// activeSince is when that step started. A stall shows up as one step's
	// clock running away, which the total elapsed would hide.
	activeSince time.Time
	frame       int
	drawn       bool
	stopped     bool

	silent bool
	once   sync.Once
	quit   chan struct{}
	done   chan struct{}
}

// startChecklist draws the steps and begins animating whichever is active.
func startChecklist(out io.Writer, steps []string) *checklist {
	c := &checklist{
		out:      out,
		steps:    steps,
		interval: spinnerInterval,
		started:  time.Now(),
		state:    make([]rune, len(steps)),
		active:   -1,
		silent:   !animates(out),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for i := range c.state {
		c.state[i] = []rune(markPending)[0]
	}

	if c.silent {
		return c
	}

	c.draw()
	go c.run()
	return c
}

// start marks the named step as the one being worked on, and everything before
// it as done. It is the Progress callback the bootstrap calls.
func (c *checklist) start(what string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, step := range c.steps {
		if step != what {
			continue
		}
		// Anything before the new step finished, or the bootstrap would not
		// have reached this one.
		for before := range i {
			c.state[before] = []rune(markDone)[0]
		}
		c.active, c.activeSince = i, time.Now()
		break
	}

	if c.silent {
		fmt.Fprintf(c.out, "  %s\n", what)
	}
}

// stop finishes the list: the active step is marked done, or failed if the
// job ended badly, and the block is left on screen.
func (c *checklist) stop(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		if c.active >= 0 {
			mark := markDone
			if err != nil {
				mark = markFailed
			}
			c.state[c.active] = []rune(mark)[0]
		}
		if err == nil {
			for i := range c.state {
				c.state[i] = []rune(markDone)[0]
			}
		}
		c.active = -1
		c.stopped = true
		c.mu.Unlock()

		if c.silent {
			return
		}
		close(c.quit)
		<-c.done
		c.draw() // once more, so the marks left on screen are the final ones
	})
}

// elapsed is how long the whole list has been running.
func (c *checklist) elapsed() time.Duration { return time.Since(c.started) }

func (c *checklist) run() {
	defer close(c.done)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.quit:
			return
		case <-ticker.C:
			c.mu.Lock()
			c.frame++
			c.mu.Unlock()
			c.draw()
		}
	}
}

// draw rewrites the whole block in place.
func (c *checklist) draw() {
	c.mu.Lock()
	defer c.mu.Unlock()

	var b strings.Builder
	if c.drawn {
		b.WriteString("\r" + cursorUp(len(c.steps)))
	}
	c.drawn = true

	for i, step := range c.steps {
		mark := string(c.state[i])
		line := fmt.Sprintf("  %s %s", mark, step)

		if i == c.active {
			// The step being worked on gets the spinner and its own clock:
			// a stall shows up as one step's time running away.
			line = fmt.Sprintf("  %s %s (%s)",
				spinnerFrames[c.frame%len(spinnerFrames)], step, took(time.Since(c.activeSince)))
		}
		b.WriteString(line + "\x1b[K\n")
	}

	fmt.Fprint(c.out, b.String())
}
