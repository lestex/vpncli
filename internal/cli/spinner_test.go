package cli

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// A pipe or a file gets the plain output it always had: a log full of redrawn
// frames is worse than no progress at all.
func TestSpinnerIsSilentWhenNotOnATerminal(t *testing.T) {
	var out bytes.Buffer

	s := startSpinner(&out, "waiting")
	time.Sleep(3 * spinnerInterval)
	s.stop()

	if out.Len() != 0 {
		t.Errorf("wrote %q, want nothing off a terminal", out.String())
	}
}

// stop is deferred and called on the success path both, so it has to be safe
// to call twice.
func TestSpinnerStopIsRepeatable(t *testing.T) {
	var out bytes.Buffer

	s := startSpinner(&out, "waiting")
	s.stop()
	s.stop()
}

func TestSpinnerDrawsAndClears(t *testing.T) {
	var out safeBuffer
	s := &spinner{
		out:      &out,
		message:  "waiting for the server to be ready",
		interval: time.Millisecond,
		started:  time.Now(),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.run()

	time.Sleep(20 * time.Millisecond)
	s.stop()

	drawn := out.String()
	if !strings.Contains(drawn, "waiting for the server to be ready") {
		t.Errorf("the spinner does not say what it is waiting for:\n%q", drawn)
	}
	// More than one frame, or it is not a spinner.
	var seen int
	for _, frame := range spinnerFrames {
		if strings.Contains(drawn, frame) {
			seen++
		}
	}
	if seen < 2 {
		t.Errorf("only %d frames drawn, want the animation to advance:\n%q", seen, drawn)
	}
	// Whatever is printed next must not land on a half-drawn frame.
	if !strings.HasSuffix(drawn, clearLine) {
		t.Errorf("the spinner left its line behind:\n%q", drawn)
	}
}

func TestTook(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{d: 47 * time.Second, want: "47s"},
		// Sub-second precision is noise on a wait measured in minutes.
		{d: 47*time.Second + 400*time.Millisecond, want: "47s"},
		{d: 72 * time.Second, want: "1m12s"},
	}

	for _, tt := range tests {
		if got := took(tt.d); got != tt.want {
			t.Errorf("took(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// A terminal that has said it cannot do better is not drawn on.
func TestAnimatesRefusesADumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")

	if animates(os.Stdout) {
		t.Error("animates() on TERM=dumb, want plain output")
	}
}

func TestAnimatesRefusesAnythingThatIsNotAFile(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")

	if animates(&bytes.Buffer{}) {
		t.Error("animates() on a buffer, want plain output")
	}
}

// safeBuffer is a bytes.Buffer the spinner goroutine and the test can both
// touch. The spinner writes until it is stopped, and the race detector is
// right to care.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
