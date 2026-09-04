// Package prompt asks the questions the `vpncli init` wizard is made of.
//
// It is deliberately a numbered list read off stdin rather than a full-screen
// cursor menu. The wizard is a handful of questions asked once, and a plain
// line-oriented prompt works over SSH, inside a pipe and in a test, none of
// which a raw-mode TUI does without a good deal of machinery.
package prompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// ErrNoInput is returned when the input ends before a question was answered -
// Ctrl-D, or a pipe with nothing left in it. It means the wizard should stop,
// not ask again.
var ErrNoInput = errors.New("no input: nothing was chosen")

// Option is one choice. Key is the value that ends up in the config file, and
// is accepted as an answer in its own right, so a list that has scrolled off
// the screen can still be answered.
//
// A tab in Label starts another column, aligned down the whole list along with
// the numbers and the keys.
type Option struct {
	Key   string
	Label string
}

// Prompter asks questions over one input/output pair.
type Prompter struct {
	out   io.Writer
	lines <-chan answer
	// done is the error that ended the input, remembered so a later question
	// is refused rather than waiting on a channel nothing will send to again.
	done error
}

// answer is one line as it came off the input.
type answer struct {
	text string
	err  error
}

// New returns a Prompter reading from in and writing to out.
//
// Input is read on a goroutine of its own. A read from a terminal blocks until
// a line arrives, which would mean Ctrl-C going unanswered until the user
// pressed Enter - so the question waits on the reader and on the context at
// the same time, and gives up on whichever comes first.
func New(in io.Reader, out io.Writer) *Prompter {
	lines := make(chan answer)
	go readLines(bufio.NewReader(in), lines)
	return &Prompter{out: out, lines: lines}
}

// readLines feeds the Prompter until the input ends. The error that ends it is
// sent along with the last line, so nothing is lost, and then the goroutine
// stops: on a terminal it parks on the read until the process exits, which is
// the point at which nobody is asking questions any more.
func readLines(r *bufio.Reader, lines chan<- answer) {
	for {
		text, err := r.ReadString('\n')
		lines <- answer{text: text, err: err}
		if err != nil {
			return
		}
	}
}

// readLine waits for one line, the context, or the end of the input.
func (p *Prompter) readLine(ctx context.Context) (string, error) {
	if p.done != nil {
		return "", p.done
	}

	select {
	case <-ctx.Done():
		// The cursor is sitting on the prompt line, and whatever is printed
		// next should not land on it.
		p.Printf("\n")
		return "", ctx.Err()
	case line := <-p.lines:
		if line.err != nil {
			p.done = line.err
		}
		return line.text, line.err
	}
}

// ReadLine asks nothing and reads one trimmed answer, for a question that is
// not a menu. The end of the input is ErrNoInput, as it is everywhere here.
func (p *Prompter) ReadLine(ctx context.Context) (string, error) {
	line, err := p.readLine(ctx)
	text := strings.TrimSpace(line)
	switch {
	case errors.Is(err, io.EOF) && text == "":
		p.Printf("\n")
		return "", ErrNoInput
	case err != nil && !errors.Is(err, io.EOF):
		return "", err
	}
	return text, nil
}

// Select prints the options and returns the index of the one chosen. An answer
// is either a number from the list or an option's key, matched case
// insensitively.
//
// defaultKey, when it names one of the options, is what an empty answer picks;
// otherwise an empty answer re-asks. A key that matches nothing is not an
// error: that is what a config carrying a region from a different provider
// looks like, and the question simply has no default.
func (p *Prompter) Select(ctx context.Context, question string, options []Option, defaultKey string) (int, error) {
	if len(options) == 0 {
		return 0, fmt.Errorf("%s: nothing to choose from", question)
	}

	if err := p.printOptions(options); err != nil {
		return 0, err
	}

	def := indexOf(options, defaultKey)
	for {
		answer, err := p.ask(ctx, question, def, options)
		if err != nil {
			return 0, err
		}

		if answer == "" {
			if def >= 0 {
				return def, nil
			}
			p.Printf("Pick a number from 1 to %d, or a name from the list.\n", len(options))
			continue
		}

		if i, ok := match(options, answer); ok {
			return i, nil
		}
		p.Printf("%q is not one of the choices. Pick a number from 1 to %d, or a name from the list.\n", answer, len(options))
	}
}

// Input asks for a value typed out rather than picked off a list. An empty
// answer takes def; with no def it re-asks, since a question worth asking has
// no blank answer.
//
// Validation is the caller's: what makes an answer good differs per question,
// and only the caller can say so in words worth printing.
func (p *Prompter) Input(ctx context.Context, question, def string) (string, error) {
	for {
		if def != "" {
			p.Printf("%s [%s]: ", question, def)
		} else {
			p.Printf("%s: ", question)
		}

		line, err := p.readLine(ctx)
		answer := strings.TrimSpace(line)
		switch {
		case errors.Is(err, io.EOF) && answer == "":
			p.Printf("\n")
			return "", ErrNoInput
		case err != nil && !errors.Is(err, io.EOF):
			return "", err
		}

		if answer != "" {
			return answer, nil
		}
		if def != "" {
			return def, nil
		}
		p.Printf("An answer is needed here.\n")
	}
}

// ask writes the prompt line and reads one answer, trimmed.
func (p *Prompter) ask(ctx context.Context, question string, def int, options []Option) (string, error) {
	if def >= 0 {
		p.Printf("%s [%s]: ", question, options[def].Key)
	} else {
		p.Printf("%s: ", question)
	}

	line, err := p.readLine(ctx)
	answer := strings.TrimSpace(line)
	switch {
	case errors.Is(err, io.EOF) && answer == "":
		// Echo a newline: the input ended where the cursor is, and the error
		// the caller prints should not land on the prompt line.
		p.Printf("\n")
		return "", ErrNoInput
	case err != nil && !errors.Is(err, io.EOF):
		return "", err
	}
	return answer, nil
}

// printOptions writes the numbered list, keys aligned so the labels line up.
func (p *Prompter) printOptions(options []Option) error {
	tw := tabwriter.NewWriter(p.out, 0, 0, 2, ' ', 0)
	for i, opt := range options {
		fmt.Fprintf(tw, "  %d)\t%s\t%s\n", i+1, opt.Key, opt.Label)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	p.Printf("\n")
	return nil
}

// Printf writes the wizard's own narration to the same place the questions go.
// Nothing here checks the error: the answers are read from the same terminal,
// so a write failure surfaces as the next read failing.
func (p *Prompter) Printf(format string, args ...any) {
	fmt.Fprintf(p.out, format, args...)
}

// match resolves an answer to an option index. Numbers are positions in the
// list; anything else is compared against the keys.
func match(options []Option, answer string) (int, bool) {
	if n, err := strconv.Atoi(answer); err == nil {
		if n >= 1 && n <= len(options) {
			return n - 1, true
		}
		return 0, false
	}
	i := indexOf(options, answer)
	return i, i >= 0
}

// indexOf returns the index of the option with this key, or -1.
func indexOf(options []Option, key string) int {
	if key == "" {
		return -1
	}
	for i, opt := range options {
		if strings.EqualFold(opt.Key, key) {
			return i
		}
	}
	return -1
}
