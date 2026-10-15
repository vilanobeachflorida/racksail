package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"math"
	"strings"
	"testing"
)

func TestGeneratedFilesAreCurrent(t *testing.T) {
	stale, err := generate("../..", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range stale {
		t.Errorf("%s is out of date; run go run ./brand/gen", path)
	}
}

// pngChunks lists the chunk types of a PNG file.
func pngChunks(t *testing.T, data []byte) []string {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("not a PNG file")
	}
	var chunks []string
	for i := 8; i+8 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		chunks = append(chunks, string(data[i+4:i+8]))
		i += 12 + n
	}
	return chunks
}

func TestImagesCarryNoMetadata(t *testing.T) {
	allowed := map[string]bool{"IHDR": true, "IDAT": true, "IEND": true}
	check := func(name string, data []byte) {
		for _, c := range pngChunks(t, data) {
			if !allowed[c] {
				t.Errorf("%s has a %s chunk; brand images hold pixels and nothing else", name, c)
			}
		}
	}
	for _, f := range outputs() {
		switch f.Kind {
		case Raster:
			check(f.Path, f.Data)
		case Icon:
			entries, err := icoEntries(f.Data)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				check(f.Path, e)
			}
		}
	}
}

func TestParsePath(t *testing.T) {
	p, err := ParsePath("M1 2L1 2l3 0h1v-2H0V5Q1 1 2 2c1 0 1 1 2 2zm10,10 1e1-1z")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.D(), "M1 2L4 2L5 2L5 0L0 0L0 5Q1 1 2 2C3 2 3 3 4 4ZM11 12L21 11Z"; got != want {
		t.Errorf("D() = %s, want %s", got, want)
	}
	for _, bad := range []string{"1 2", "M1", "M1 2X3", "M1 2L3 4..5"} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func sum(cov []float64) float64 {
	s := 0.0
	for _, c := range cov {
		s += c
	}
	return s
}

func TestCoverage(t *testing.T) {
	square := MustPath("M1 1L3 1L3 3L1 3Z")
	cov := coverage(square, 4, 4)
	for y := range 4 {
		for x := range 4 {
			want := 0.0
			if x >= 1 && x < 3 && y >= 1 && y < 3 {
				want = 1
			}
			if math.Abs(cov[y*4+x]-want) > 1e-9 {
				t.Errorf("pixel %d,%d = %v, want %v", x, y, cov[y*4+x], want)
			}
		}
	}
	// A triangle's coverage adds up to its area, and a diagonal edge splits
	// the pixels it crosses in half.
	tri := coverage(MustPath("M0 0L4 0L0 4Z"), 4, 4)
	if math.Abs(sum(tri)-8) > 1e-9 || math.Abs(tri[3]-0.5) > 1e-9 {
		t.Errorf("triangle coverage %v, sum %v", tri, sum(tri))
	}
	// Overlapping shapes don't count twice, and an opposite contour cuts a hole.
	twice := coverage(append(MustPath("M0 0L4 0L4 4L0 4Z"), MustPath("M1 1L3 1L3 3L1 3Z")...), 4, 4)
	if sum(twice) != 16 {
		t.Errorf("overlap sums to %v", sum(twice))
	}
	hole := coverage(append(MustPath("M0 0L4 0L4 4L0 4Z"), MustPath("M1 1L1 3L3 3L3 1Z")...), 4, 4)
	if sum(hole) != 12 || hole[5] != 0 {
		t.Errorf("hole coverage %v", hole)
	}
	// Curves are flattened finely: a circle's area comes out right.
	k := 0.5522847498 * 10
	circle := MustPath("M20 10C20 " + num(10+k) + " " + num(10+k) + " 20 10 20C" + num(10-k) + " 20 0 " + num(10+k) +
		" 0 10C0 " + num(10-k) + " " + num(10-k) + " 0 10 0C" + num(10+k) + " 0 20 " + num(10-k) + " 20 10Z")
	if area := sum(coverage(circle, 20, 20)); math.Abs(area-math.Pi*100) > 0.2 {
		t.Errorf("circle area %v, want %v", area, math.Pi*100)
	}
}

func TestICO(t *testing.T) {
	a, b := NewCanvas(16, 16), NewCanvas(32, 32)
	data := ICO(a, b)
	entries, err := icoEntries(data)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %d, %v", len(entries), err)
	}
	if data[6] != 16 || data[6+16] != 32 {
		t.Error("the directory doesn't give the sizes")
	}
	for i, e := range entries {
		img, err := png.Decode(bytes.NewReader(e))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != []int{16, 32}[i] {
			t.Errorf("entry %d is %v", i, img.Bounds())
		}
	}
	if _, err := icoEntries([]byte("nope")); err == nil {
		t.Error("garbage isn't an icon")
	}
}

func TestPalettes(t *testing.T) {
	for _, tc := range []struct {
		d     Drawing
		fills []string
	}{
		{markDrawing(Color), []string{Navy, Blue}},
		{markDrawing(Dark), []string{White, Blue}},
		{markDrawing(Mono), []string{Navy}},
		{horizontalDrawing(Reversed), []string{White, White}},
		{stackedDrawing(Color), []string{Navy, Blue, Navy}},
	} {
		var fills []string
		for _, l := range tc.d.Layers {
			fills = append(fills, l.Fill)
		}
		if strings.Join(fills, " ") != strings.Join(tc.fills, " ") {
			t.Errorf("fills = %v, want %v", fills, tc.fills)
		}
	}
}

func TestLockupsFitTheirViewBoxes(t *testing.T) {
	for name, d := range map[string]Drawing{
		"mark": markDrawing(Color), "wordmark": wordmarkDrawing(Color),
		"horizontal": horizontalDrawing(Color), "stacked": stackedDrawing(Color),
		"favicon": squareMark(Color), "app icon": appIcon,
	} {
		for _, l := range d.Layers {
			lo, hi := l.Path.Bounds()
			if lo.X < d.View.X-0.01 || lo.Y < d.View.Y-0.01 || hi.X > d.View.X+d.View.W+0.01 || hi.Y > d.View.Y+d.View.H+0.01 {
				t.Errorf("%s: a layer reaches %v to %v, outside %+v", name, lo, hi, d.View)
			}
		}
	}
	h := horizontalDrawing(Color)
	if h.View.W < 3*h.View.H {
		t.Errorf("the horizontal lockup is %v by %v", h.View.W, h.View.H)
	}
	s := stackedDrawing(Color)
	if s.View.W != stackedWordmarkWidth || s.View.H < markHeight {
		t.Errorf("the stacked lockup is %v by %v", s.View.W, s.View.H)
	}
}

func TestSVGIsAccessible(t *testing.T) {
	svg := horizontalDrawing(Color).SVG()
	for _, want := range []string{`role="img"`, `aria-label="Racksail"`, `fill="#01214F"`, `fill="#007CFD"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG lacks %s", want)
		}
	}
	fav := adaptiveFavicon().SVG()
	if !strings.Contains(fav, "prefers-color-scheme:dark") || !strings.Contains(fav, `class="ink"`) {
		t.Errorf("the favicon doesn't follow dark mode: %s", fav)
	}
}

func TestTerminalGrids(t *testing.T) {
	small, large := terminalGrids()
	for name, g := range map[string][]string{"small": small, "large": large} {
		if len(g) == 0 {
			t.Fatalf("%s grid is empty", name)
		}
		counts := map[rune]int{}
		for _, row := range g {
			if len(row) != len(g[0]) {
				t.Errorf("%s grid has ragged rows", name)
			}
			for _, c := range row {
				counts[c]++
			}
		}
		if counts['i'] == 0 || counts['s'] == 0 || len(counts) != 3 {
			t.Errorf("%s grid uses %v", name, counts)
		}
	}
	if len(small[0]) != 16 || len(large[0]) != 32 {
		t.Errorf("widths %d and %d", len(small[0]), len(large[0]))
	}
}
