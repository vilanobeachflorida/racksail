package theme

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDetectColorDepth(t *testing.T) {
	tests := []struct {
		vars map[string]string
		want ColorDepth
	}{
		{map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, TrueColor},
		{map[string]string{"TERM": "xterm-direct"}, TrueColor},
		{map[string]string{"TERM": "xterm-256color"}, Color256},
		{map[string]string{"TERM": "linux"}, Color16},
		{map[string]string{"TERM": "dumb", "COLORTERM": ""}, NoColor},
		{map[string]string{}, NoColor},
		{map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": "1"}, NoColor},
	}
	for _, tt := range tests {
		if got := DetectColorDepth(env(tt.vars)); got != tt.want {
			t.Errorf("%v: depth %d, want %d", tt.vars, got, tt.want)
		}
	}
}

func TestConsoleMark(t *testing.T) {
	c := Default().Console
	for _, size := range []MarkSize{MarkSmall, MarkLarge} {
		plain := c.Mark(size, NoColor)
		lines := strings.Split(strings.TrimSuffix(plain, "\n"), "\n")
		if len(lines) != MarkLines(size) {
			t.Errorf("size %d: %d lines, want %d", size, len(lines), MarkLines(size))
		}
		for _, l := range lines {
			if n := utf8.RuneCountInString(l); n > MarkWidth(size) {
				t.Errorf("size %d: line %q is %d columns", size, l, n)
			}
		}
		if strings.Contains(plain, "\x1b") || !strings.ContainsAny(plain, "█▀▄") {
			t.Errorf("size %d without color: %q", size, plain)
		}
	}
	if MarkWidth(MarkSmall) != 16 || MarkWidth(MarkLarge) != 32 {
		t.Errorf("widths %d, %d", MarkWidth(MarkSmall), MarkWidth(MarkLarge))
	}
	truecolor := c.Mark(MarkLarge, TrueColor)
	if !strings.Contains(truecolor, "\x1b[38;2;230;237;245m") || !strings.Contains(truecolor, "\x1b[38;2;61;155;255m") {
		t.Error("the mark uses the console's foreground and accent")
	}
	if !strings.HasSuffix(strings.Split(truecolor, "\n")[0], "\x1b[0m") {
		t.Error("lines end with a reset")
	}
	if c256 := c.Mark(MarkSmall, Color256); !strings.Contains(c256, "\x1b[38;5;") {
		t.Error("256-color marks use the palette")
	}
	if c16 := c.Mark(MarkSmall, Color16); !strings.Contains(c16, "\x1b[97m") || !strings.Contains(c16, "\x1b[94m") {
		t.Error("16-color marks use bright white and bright blue")
	}
}

func TestDrawPixels(t *testing.T) {
	c := Console{Foreground: "#FFFFFF", Accent: "#0000FF"}
	got := c.DrawPixels([]string{"i.s", "isx", "s"}, NoColor)
	if got != "█▄▀\n▀\n" {
		t.Errorf("drawn %q", got)
	}
	colored := c.DrawPixels([]string{"is", "si"}, TrueColor)
	if !strings.Contains(colored, "\x1b[38;2;255;255;255m\x1b[48;2;0;0;255m▀") {
		t.Errorf("two shapes in one cell use foreground and background: %q", colored)
	}
}

func TestSGR(t *testing.T) {
	c := Default().Console
	if got := c.SGR(ConsoleDanger, TrueColor); got != "\x1b[38;2;248;81;73m" {
		t.Errorf("danger = %q", got)
	}
	if got := c.SGR(ConsoleSuccess, Color16); got != "\x1b[92m" {
		t.Errorf("success = %q", got)
	}
	if c.Paint(ConsoleWarning, NoColor, "careful") != "careful" {
		t.Error("no color leaves text alone")
	}
	if got := c.Paint(ConsoleAccent, Color256, "go"); got != "\x1b[38;5;69mgo\x1b[0m" {
		t.Errorf("accent = %q", got)
	}
	for _, tt := range []struct {
		r, g, b, want int
	}{{0, 0, 0, 16}, {255, 255, 255, 231}, {128, 128, 128, 244}, {1, 33, 79, 17}} {
		if got := xterm256(tt.r, tt.g, tt.b); got != tt.want {
			t.Errorf("xterm256(%d,%d,%d) = %d, want %d", tt.r, tt.g, tt.b, got, tt.want)
		}
	}
}

func TestConsolePaletteIsReadable(t *testing.T) {
	c := Default().Console
	for _, role := range []Color{c.Foreground, c.Accent, c.Success, c.Warning, c.Danger} {
		if ratio, err := Contrast(role, c.Background); err != nil || ratio < TextContrast {
			t.Errorf("%s on %s is %.2f:1", role, c.Background, ratio)
		}
	}
}
