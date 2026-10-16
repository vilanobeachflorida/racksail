package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ColorDepth is how many colors a terminal can show.
type ColorDepth int

// Color depths, from none to 24-bit color.
const (
	NoColor ColorDepth = iota
	// Color16 is the Linux console and other basic terminals: eight colors
	// and their bright versions.
	Color16
	Color256
	TrueColor
)

// DetectColorDepth works out a terminal's color depth from its environment,
// as the installer, the console status screen and the CLI do. NO_COLOR
// turns color off whatever the terminal supports.
func DetectColorDepth(getenv func(string) string) ColorDepth {
	if getenv("NO_COLOR") != "" {
		return NoColor
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return TrueColor
	}
	term := strings.ToLower(getenv("TERM"))
	switch {
	case term == "" || term == "dumb":
		return NoColor
	case strings.Contains(term, "direct"):
		return TrueColor
	case strings.Contains(term, "256color"):
		return Color256
	default:
		return Color16
	}
}

// ConsoleRole is a color of the console palette.
type ConsoleRole int

// Console palette roles.
const (
	ConsoleForeground ConsoleRole = iota
	ConsoleAccent
	ConsoleSuccess
	ConsoleWarning
	ConsoleDanger
)

func (c Console) color(role ConsoleRole) Color {
	switch role {
	case ConsoleAccent:
		return c.Accent
	case ConsoleSuccess:
		return c.Success
	case ConsoleWarning:
		return c.Warning
	case ConsoleDanger:
		return c.Danger
	}
	return c.Foreground
}

// basic16 are the colors a 16-color terminal falls back to for each role:
// bright versions, which stay readable on the dark backgrounds consoles use.
var basic16 = map[ConsoleRole]int{
	ConsoleForeground: 97, ConsoleAccent: 94, ConsoleSuccess: 92, ConsoleWarning: 93, ConsoleDanger: 91,
}

// SGR returns the escape sequence that sets the foreground to a role's
// color at the given depth, or "" without color.
func (c Console) SGR(role ConsoleRole, depth ColorDepth) string {
	return sgr(c.color(role), basic16[role], depth, false)
}

// Paint wraps text in a role's color and a reset, or returns it as it is
// without color.
func (c Console) Paint(role ConsoleRole, depth ColorDepth, text string) string {
	code := c.SGR(role, depth)
	if code == "" {
		return text
	}
	return code + text + "\x1b[0m"
}

func sgr(col Color, basic int, depth ColorDepth, background bool) string {
	r, g, b, err := rgb(col)
	if err != nil || depth == NoColor {
		return ""
	}
	switch depth {
	case TrueColor:
		layer := 38
		if background {
			layer = 48
		}
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, r, g, b)
	case Color256:
		layer := 38
		if background {
			layer = 48
		}
		return fmt.Sprintf("\x1b[%d;5;%dm", layer, xterm256(r, g, b))
	default:
		if background {
			basic += 10
		}
		return "\x1b[" + strconv.Itoa(basic) + "m"
	}
}

func rgb(c Color) (r, g, b int, err error) {
	s := strings.TrimPrefix(string(c), "#")
	if len(s) != 6 && len(s) != 8 {
		return 0, 0, 0, fmt.Errorf("color %q must be #RRGGBB", c)
	}
	v, err := strconv.ParseUint(s[:6], 16, 32)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("color %q: %w", c, err)
	}
	return int(v >> 16), int(v >> 8 & 0xFF), int(v & 0xFF), nil
}

// xterm256 picks the closest color of the xterm 256-color palette: the
// 6×6×6 cube or the gray ramp, leaving the first 16, which terminals
// redefine, alone.
func xterm256(r, g, b int) int {
	levels := []int{0, 95, 135, 175, 215, 255}
	nearest := func(v int) int {
		best := 0
		for i, l := range levels {
			if abs(l-v) < abs(levels[best]-v) {
				best = i
			}
		}
		return best
	}
	ri, gi, bi := nearest(r), nearest(g), nearest(b)
	cube := 16 + 36*ri + 6*gi + bi
	cubeDist := dist2(r, g, b, levels[ri], levels[gi], levels[bi])
	gray := (r + g + b) / 3
	gi2 := min(23, max(0, (gray-8+5)/10))
	gv := 8 + 10*gi2
	if dist2(r, g, b, gv, gv, gv) < cubeDist {
		return 232 + gi2
	}
	return cube
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func dist2(r1, g1, b1, r2, g2, b2 int) int {
	return (r1-r2)*(r1-r2) + (g1-g2)*(g1-g2) + (b1-b2)*(b1-b2)
}

// MarkSize picks one of the drawings of the mark for terminals.
type MarkSize int

// Terminal mark sizes.
const (
	// MarkSmall is 16 columns by 8 lines, for the console status screen and
	// the CLI.
	MarkSmall MarkSize = iota
	// MarkLarge is 32 columns by 13 lines, for the installer's welcome.
	MarkLarge
)

// Mark draws the Racksail mark with Unicode half blocks, two pixels to a
// character cell: the sails and hull in the foreground color and the jib
// and wave in the accent color. Without color, the shapes are drawn in the
// terminal's own color.
func (c Console) Mark(size MarkSize, depth ColorDepth) string {
	return c.DrawPixels(markGrids[size], depth)
}

// DrawPixels draws rows of pixels with half blocks: "i" in the foreground
// color, "s" in the accent color and "." left empty. Mark uses it with the
// mark's pixels, and the brand tool with freshly sampled ones.
func (c Console) DrawPixels(grid []string, depth ColorDepth) string {
	colors := map[byte]struct {
		col   Color
		basic int
	}{
		'i': {c.Foreground, basic16[ConsoleForeground]},
		's': {c.Accent, basic16[ConsoleAccent]},
	}
	var b strings.Builder
	for y := 0; y < len(grid); y += 2 {
		top := grid[y]
		bottom := ""
		if y+1 < len(grid) {
			bottom = grid[y+1]
		}
		var line strings.Builder
		// The colors in effect, so escapes are only written when they change.
		var curFG, curBG byte
		for x := range max(len(top), len(bottom)) {
			t, u := pixel(top, x), pixel(bottom, x)
			var glyph string
			var fg, bg byte
			switch {
			case t == '.' && u == '.':
				glyph = " "
			case t == u:
				glyph, fg = "█", t
			case u == '.':
				glyph, fg = "▀", t
			case t == '.':
				glyph, fg = "▄", u
			default:
				glyph, fg, bg = "▀", t, u
			}
			if depth != NoColor && (fg != curFG || bg != curBG) {
				line.WriteString("\x1b[0m")
				if fg != 0 {
					line.WriteString(sgr(colors[fg].col, colors[fg].basic, depth, false))
				}
				if bg != 0 {
					line.WriteString(sgr(colors[bg].col, colors[bg].basic, depth, true))
				}
				curFG, curBG = fg, bg
			}
			line.WriteString(glyph)
		}
		if curFG != 0 || curBG != 0 {
			line.WriteString("\x1b[0m")
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// pixel reads one pixel of a row; anything but ink and sea is empty, and so
// is the space past the end of a short row.
func pixel(row string, x int) byte {
	if x < len(row) && (row[x] == 'i' || row[x] == 's') {
		return row[x]
	}
	return '.'
}

// MarkWidth is how many columns a terminal mark takes.
func MarkWidth(size MarkSize) int {
	if len(markGrids[size]) == 0 {
		return 0
	}
	return len(markGrids[size][0])
}

// MarkLines is how many lines a terminal mark takes.
func MarkLines(size MarkSize) int {
	return int(math.Ceil(float64(len(markGrids[size])) / 2))
}
