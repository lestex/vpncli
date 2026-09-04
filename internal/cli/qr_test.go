package cli

import (
	"bytes"
	"strings"
	"testing"

	"rsc.io/qr"
)

// rendered reads the drawn code back into a grid, dark being true, so a test
// can compare what was drawn against what was encoded. Two modules share every
// character - the top one is its foreground, the bottom one its background -
// which is exactly the kind of arrangement that ends up one index away from an
// inverted or shifted code that no scanner will read.
func rendered(t *testing.T, lines []string) [][]bool {
	t.Helper()

	var grid [][]bool
	for _, line := range lines {
		top, bottom := decodeLine(t, line)
		grid = append(grid, top, bottom)
	}
	return grid
}

// decodeLine turns one line of half blocks back into the two module rows it
// was drawn from.
func decodeLine(t *testing.T, line string) (top, bottom []bool) {
	t.Helper()

	dark := [2]bool{}
	for len(line) > 0 {
		switch {
		case strings.HasPrefix(line, darkAbove):
			dark[0], line = true, strings.TrimPrefix(line, darkAbove)
		case strings.HasPrefix(line, lightAbove):
			dark[0], line = false, strings.TrimPrefix(line, lightAbove)
		case strings.HasPrefix(line, darkBelow):
			dark[1], line = true, strings.TrimPrefix(line, darkBelow)
		case strings.HasPrefix(line, lightBelow):
			dark[1], line = false, strings.TrimPrefix(line, lightBelow)
		case strings.HasPrefix(line, resetColor):
			line = strings.TrimPrefix(line, resetColor)
		case strings.HasPrefix(line, upperHalf):
			top = append(top, dark[0])
			bottom = append(bottom, dark[1])
			line = strings.TrimPrefix(line, upperHalf)
		default:
			t.Fatalf("unexpected output in a rendered line: %q", line)
		}
	}
	return top, bottom
}

// What is drawn has to be the code that was encoded. Inverted or shifted by
// one, it is still a picture of a QR code and still unreadable.
func TestQRRendersTheCode(t *testing.T) {
	code, err := qr.Encode("vless://example", qr.M)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	grid := rendered(t, qrLines(code))

	width := code.Size + 2*quietZone
	for _, row := range grid {
		if len(row) != width {
			t.Fatalf("a row is %d cells wide, want %d", len(row), width)
		}
	}
	if len(grid) < width {
		t.Fatalf("the drawing is %d rows for %d columns, want at least square", len(grid), width)
	}

	for y := range code.Size {
		for x := range code.Size {
			if got := grid[y+quietZone][x+quietZone]; got != code.Black(x, y) {
				t.Fatalf("module (%d,%d) drawn dark=%v, want dark=%v", x, y, got, code.Black(x, y))
			}
		}
	}
}

// A scanner finds a code by the light border around it. Without one there is
// nothing separating the code from whatever else is in the terminal.
func TestQRHasAQuietZone(t *testing.T) {
	code, err := qr.Encode("vless://example", qr.M)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	grid := rendered(t, qrLines(code))
	width := code.Size + 2*quietZone

	for y := range quietZone {
		for x := range width {
			if grid[y][x] {
				t.Fatalf("the top border has a dark cell at (%d,%d)", x, y)
			}
		}
	}
	for y := range len(grid) {
		for x := range quietZone {
			if grid[y][x] {
				t.Fatalf("the left border has a dark cell at (%d,%d)", x, y)
			}
		}
	}
}

// Two rows of modules per line, or a code sixty modules wide is sixty lines
// tall and scrolls off the screen it has to be scanned from.
func TestQRIsTwoModuleRowsPerLine(t *testing.T) {
	code, err := qr.Encode("vless://example", qr.M)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	lines := qrLines(code)
	want := (code.Size + 2*quietZone + 1) / 2
	if len(lines) != want {
		t.Errorf("drew %d lines for %d module rows, want %d", len(lines), code.Size+2*quietZone, want)
	}
}

// Colors are written out rather than left to the terminal: a code inverted by
// a dark theme is one most scanners refuse.
func TestQRSetsItsOwnColors(t *testing.T) {
	var out bytes.Buffer
	if err := printQR(&out, "vless://example"); err != nil {
		t.Fatalf("printQR: %v", err)
	}

	drawn := out.String()
	for _, want := range []string{darkAbove, lightAbove, darkBelow, lightBelow, resetColor} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the drawing never sets %q", strings.TrimPrefix(want, "\x1b"))
		}
	}
	// Every line ends by putting the terminal back how it was found.
	for _, line := range strings.Split(strings.TrimRight(drawn, "\n"), "\n") {
		if !strings.HasSuffix(line, resetColor) {
			t.Fatalf("a line does not reset the terminal colors: %q", line)
		}
	}
}

// Repeating the colors for every cell makes the output ten times the size for
// the same picture, and a terminal has to draw all of it.
func TestQRSharesColorsAcrossRuns(t *testing.T) {
	code, err := qr.Encode("vless://example", qr.M)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	lines := qrLines(code)
	cells := (code.Size + 2*quietZone) * len(lines)

	var escapes int
	for _, line := range lines {
		escapes += strings.Count(line, "\x1b[")
	}
	if escapes >= cells {
		t.Errorf("%d escape sequences for %d cells, want runs to share one", escapes, cells)
	}
}
