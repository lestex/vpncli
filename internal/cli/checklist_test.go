package cli

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// cursorMove matches the return-to-top sequence a redraw starts with.
var cursorMove = regexp.MustCompile(`\r?\x1b\[\d+A`)

func testSteps() []string {
	return []string{"installing packages", "turning on BBR", "starting Xray"}
}

// drawing returns a checklist wired to a buffer and forced to draw, since a
// test is never on a terminal.
func drawing(steps []string) (*checklist, *safeBuffer) {
	var out safeBuffer
	c := &checklist{
		out:      &out,
		steps:    steps,
		interval: time.Millisecond,
		started:  time.Now(),
		state:    make([]rune, len(steps)),
		active:   -1,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for i := range c.state {
		c.state[i] = []rune(markPending)[0]
	}
	c.draw()
	return c, &out
}

// Everything is on screen from the start, so the list says what is still to
// come and not only what is happening.
func TestChecklistShowsEveryStepUpFront(t *testing.T) {
	_, out := drawing(testSteps())

	for _, step := range testSteps() {
		if !strings.Contains(out.String(), step) {
			t.Errorf("%q is not on screen:\n%s", step, out.String())
		}
	}
}

func TestChecklistMarksStepsDoneAsItGoes(t *testing.T) {
	c, out := drawing(testSteps())

	c.start("installing packages")
	c.start("turning on BBR")
	c.draw()

	// The block is rewritten in place, so what is on screen is the last copy.
	last := lastBlock(t, out.String(), len(testSteps()))
	if !strings.HasPrefix(strings.TrimSpace(last[0]), markDone) {
		t.Errorf("the finished step is not ticked off: %q", last[0])
	}
	if !strings.Contains(last[1], "turning on BBR") || strings.Contains(last[1], markDone) {
		t.Errorf("the running step is marked done already: %q", last[1])
	}
	if strings.Contains(last[2], markDone) {
		t.Errorf("a step that has not run is marked done: %q", last[2])
	}
}

func TestChecklistMarksEverythingDoneWhenItFinishes(t *testing.T) {
	c, out := drawing(testSteps())
	close(c.done) // nothing is animating, so stop has nothing to wait for

	c.start("installing packages")
	c.stop(nil)

	for _, line := range lastBlock(t, out.String(), len(testSteps())) {
		if !strings.Contains(line, markDone) {
			t.Errorf("a step was left unmarked after a clean finish: %q", line)
		}
	}
}

// Which step failed is the most useful thing on screen after a failure, and it
// stays there rather than being erased.
func TestChecklistMarksTheStepThatFailed(t *testing.T) {
	c, out := drawing(testSteps())
	close(c.done)

	c.start("installing packages")
	c.start("turning on BBR")
	c.stop(errors.New("dpkg was interrupted"))

	last := lastBlock(t, out.String(), len(testSteps()))
	if !strings.Contains(last[0], markDone) {
		t.Errorf("the step that succeeded is not ticked off: %q", last[0])
	}
	if !strings.Contains(last[1], markFailed) {
		t.Errorf("the step that failed is not marked: %q", last[1])
	}
	if strings.Contains(last[2], markDone) || strings.Contains(last[2], markFailed) {
		t.Errorf("a step that never ran is marked: %q", last[2])
	}
}

// Redrawing means moving back over the block. One line out and it rewrites
// something else on screen.
func TestChecklistRedrawsOverItself(t *testing.T) {
	c, out := drawing(testSteps())
	c.draw()

	if want := "\x1b[" + strconv.Itoa(len(testSteps())) + "A"; !strings.Contains(out.String(), want) {
		t.Errorf("the redraw does not move back over the block (%q):\n%q", want, out.String())
	}
	// The first draw must not, or it would eat whatever was printed before it.
	if strings.HasPrefix(out.String(), "\x1b[") {
		t.Errorf("the first draw moves the cursor: %q", out.String())
	}
}

// Off a terminal there is no cursor to move, and a log wants a plain list of
// what happened rather than eight copies of the same block.
func TestChecklistOffATerminalPrintsEachStepOnce(t *testing.T) {
	var out safeBuffer
	c := startChecklist(&out, testSteps())

	c.start("installing packages")
	c.start("turning on BBR")
	c.stop(nil)

	written := out.String()
	if strings.Contains(written, "\x1b[") {
		t.Errorf("cursor escapes went into a log:\n%q", written)
	}
	for _, step := range []string{"installing packages", "turning on BBR"} {
		if n := strings.Count(written, step); n != 1 {
			t.Errorf("%q appears %d times, want once:\n%s", step, n, written)
		}
	}
	if strings.Contains(written, "starting Xray") {
		t.Errorf("a step that never started was printed:\n%s", written)
	}
}

func TestChecklistStopIsRepeatable(t *testing.T) {
	var out safeBuffer
	c := startChecklist(&out, testSteps())

	c.stop(nil)
	c.stop(errors.New("something else"))
}

// lastBlock returns the final copy of the redrawn block, as the lines a
// terminal would be showing: the control sequences that position and clear
// them are stripped, since what is being asserted is what a person sees.
func lastBlock(t *testing.T, written string, steps int) []string {
	t.Helper()

	var lines []string
	for _, line := range strings.Split(written, "\n") {
		trimmed := cursorMove.ReplaceAllString(strings.TrimSuffix(line, "\x1b[K"), "")
		if strings.TrimSpace(trimmed) != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) < steps {
		t.Fatalf("only %d lines drawn, want at least %d:\n%q", len(lines), steps, written)
	}
	return lines[len(lines)-steps:]
}
