package prompt

import (
	"errors"
	"strings"
	"testing"
)

var regions = []Option{
	{Key: "ams3", Label: "Amsterdam 3"},
	{Key: "fra1", Label: "Frankfurt 1"},
	{Key: "nyc3", Label: "New York 3"},
}

// selectFrom runs one Select against scripted input and returns what it chose
// along with everything the user would have seen.
func selectFrom(t *testing.T, input, defaultKey string) (int, string, error) {
	t.Helper()

	var out strings.Builder
	i, err := New(strings.NewReader(input), &out).Select("Region", regions, defaultKey)
	return i, out.String(), err
}

func TestSelectByNumber(t *testing.T) {
	i, _, err := selectFrom(t, "2\n", "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if i != 1 {
		t.Errorf("chose %d (%s), want 1 (fra1)", i, regions[i].Key)
	}
}

// The list can scroll off a short terminal, so the key has to work too.
func TestSelectByKey(t *testing.T) {
	for _, answer := range []string{"nyc3", "NYC3", "  nyc3  "} {
		i, _, err := selectFrom(t, answer+"\n", "")
		if err != nil {
			t.Fatalf("Select(%q): %v", answer, err)
		}
		if i != 2 {
			t.Errorf("Select(%q) chose %d, want 2 (nyc3)", answer, i)
		}
	}
}

func TestSelectEmptyAnswerTakesTheDefault(t *testing.T) {
	i, out, err := selectFrom(t, "\n", "fra1")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if i != 1 {
		t.Errorf("chose %d, want the default (fra1)", i)
	}
	if !strings.Contains(out, "[fra1]") {
		t.Errorf("prompt does not show the default:\n%s", out)
	}
}

// A default from a config written against another provider matches nothing.
// That is not an error; the question just has to be answered.
func TestSelectUnknownDefaultIsNotOffered(t *testing.T) {
	i, out, err := selectFrom(t, "\n1\n", "hel1")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if i != 0 {
		t.Errorf("chose %d, want the answered 1 (ams3)", i)
	}
	if strings.Contains(out, "[hel1]") {
		t.Errorf("prompt offers a default that is not on the list:\n%s", out)
	}
}

func TestSelectReAsksOnBadAnswer(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"out of range", "9\n2\n"},
		{"zero", "0\n2\n"},
		{"negative", "-1\n2\n"},
		{"not a choice", "london\n2\n"},
		{"empty with no default", "\n2\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, out, err := selectFrom(t, tt.input, "")
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if i != 1 {
				t.Errorf("chose %d, want the second answer (fra1)", i)
			}
			if strings.Count(out, "Region") != 2 {
				t.Errorf("question was not asked again:\n%s", out)
			}
		})
	}
}

// Ctrl-D, or a pipe that ran dry, must end the wizard rather than loop.
func TestSelectEndOfInput(t *testing.T) {
	_, _, err := selectFrom(t, "", "fra1")
	if !errors.Is(err, ErrNoInput) {
		t.Fatalf("got %v, want ErrNoInput", err)
	}
}

func TestSelectEndOfInputAfterBadAnswers(t *testing.T) {
	_, _, err := selectFrom(t, "nope\nalso nope\n", "")
	if !errors.Is(err, ErrNoInput) {
		t.Fatalf("got %v, want ErrNoInput", err)
	}
}

// A last line with no trailing newline is still an answer.
func TestSelectUnterminatedAnswer(t *testing.T) {
	i, _, err := selectFrom(t, "3", "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if i != 2 {
		t.Errorf("chose %d, want 2 (nyc3)", i)
	}
}

func TestSelectPrintsEveryOption(t *testing.T) {
	_, out, err := selectFrom(t, "1\n", "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	for _, opt := range regions {
		for _, want := range []string{opt.Key, opt.Label} {
			if !strings.Contains(out, want) {
				t.Errorf("listing is missing %q:\n%s", want, out)
			}
		}
	}
	// Numbered from 1, since nobody counts menus from zero.
	if !strings.Contains(out, "1)") || !strings.Contains(out, "3)") {
		t.Errorf("options are not numbered 1..n:\n%s", out)
	}
	if strings.Contains(out, "0)") {
		t.Errorf("options are numbered from zero:\n%s", out)
	}
}

func TestSelectWithNoOptions(t *testing.T) {
	var out strings.Builder
	if _, err := New(strings.NewReader(""), &out).Select("Region", nil, ""); err == nil {
		t.Fatal("expected an error when there is nothing to choose from")
	}
}
