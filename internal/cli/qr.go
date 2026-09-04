package cli

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// quietZone is the light border a QR code needs on every side. It is part of
// the spec, not decoration: without it a scanner cannot find the code against
// whatever is around it, which for a terminal is a wall of text.
const quietZone = 4

// Half a character cell. Drawing two rows of modules per line of text is what
// keeps a code that is sixty modules wide from being sixty lines tall.
const upperHalf = "▀"

// Colors are written out for every cell rather than left to the terminal.
// A QR code inverted by a dark theme is one most scanners refuse, and the
// theme is not ours to know.
const (
	darkAbove  = "\x1b[30m"
	lightAbove = "\x1b[97m"
	darkBelow  = "\x1b[40m"
	lightBelow = "\x1b[107m"
	resetColor = "\x1b[0m"
)

// printQR draws text as a QR code.
//
// Medium error correction: a code on a screen is not going to be creased or
// printed badly, and every level above M makes it wider for nothing.
func printQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return fmt.Errorf("encoding a QR code: %w", err)
	}

	for _, line := range qrLines(code) {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// qrLines renders a code as lines of half blocks, two module rows per line.
func qrLines(code *qr.Code) []string {
	dark := func(x, y int) bool {
		// Anything outside the code is the quiet zone, which is light.
		if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
			return false
		}
		return code.Black(x, y)
	}

	var lines []string
	for y := -quietZone; y < code.Size+quietZone; y += 2 {
		var line strings.Builder
		// Runs of the same two colors share one escape sequence. A code is
		// mostly runs, and repeating the colors per cell makes the output ten
		// times the size for the same picture.
		previous := ""
		for x := -quietZone; x < code.Size+quietZone; x++ {
			if colors := cell(dark(x, y), dark(x, y+1)); colors != previous {
				line.WriteString(colors)
				previous = colors
			}
			line.WriteString(upperHalf)
		}
		line.WriteString(resetColor)
		lines = append(lines, line.String())
	}
	return lines
}

// cell is the colors for one character: the top module becomes the foreground
// of an upper half block, the bottom one its background.
func cell(top, bottom bool) string {
	foreground, background := lightAbove, lightBelow
	if top {
		foreground = darkAbove
	}
	if bottom {
		background = darkBelow
	}
	return foreground + background
}
