package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Point is a position in artwork units, y pointing down as in SVG.
type Point struct{ X, Y float64 }

func (p Point) dist(q Point) float64 { return math.Hypot(p.X-q.X, p.Y-q.Y) }
func (p Point) eq(q Point) bool      { return math.Abs(p.X-q.X) < 1e-9 && math.Abs(p.Y-q.Y) < 1e-9 }

// lerp and apply convert products explicitly so no platform fuses them
// into a multiply-add, which would round differently and make the
// generated files depend on the machine that wrote them.
func lerp(p, q Point, t float64) Point {
	return Point{p.X + float64((q.X-p.X)*t), p.Y + float64((q.Y-p.Y)*t)}
}

func (p Point) apply(t Transform) Point {
	return Point{float64(p.X*t.Scale) + t.DX, float64(p.Y*t.Scale) + t.DY}
}

// Op is a path command.
type Op byte

// Path commands, all absolute.
const (
	MoveTo  Op = 'M'
	LineTo  Op = 'L'
	QuadTo  Op = 'Q'
	CubicTo Op = 'C'
	Close   Op = 'Z'
)

// Seg is one path command and its points: the control points of a curve
// come first and the end point last.
type Seg struct {
	Op  Op
	Pts []Point
}

// Path is a list of closed contours.
type Path []Seg

// Transform scales a path, then moves it.
type Transform struct{ Scale, DX, DY float64 }

// Identity leaves a path where it is.
var Identity = Transform{Scale: 1}

// ParsePath reads SVG path data with the commands M, L, H, V, Q, C and Z in
// either case. Zero-length lines are dropped, since font tools leave them
// behind and they only add bytes.
func ParsePath(d string) (Path, error) {
	toks, err := tokenize(d)
	if err != nil {
		return nil, err
	}
	var p Path
	var cur, start Point
	var cmd byte
	i := 0
	num := func() (float64, error) {
		if i >= len(toks) || toks[i].cmd != 0 {
			return 0, fmt.Errorf("path data %q: command %c needs more numbers", short(d), cmd)
		}
		i++
		return toks[i-1].num, nil
	}
	pt := func(rel bool) (Point, error) {
		x, err := num()
		if err != nil {
			return Point{}, err
		}
		y, err := num()
		if err != nil {
			return Point{}, err
		}
		if rel {
			return Point{cur.X + x, cur.Y + y}, nil
		}
		return Point{x, y}, nil
	}
	for i < len(toks) {
		if toks[i].cmd != 0 {
			cmd = toks[i].cmd
			i++
		} else if cmd == 0 {
			return nil, fmt.Errorf("path data %q starts with a number", short(d))
		}
		rel := cmd >= 'a'
		switch cmd {
		case 'M', 'm':
			q, err := pt(rel)
			if err != nil {
				return nil, err
			}
			p = append(p, Seg{MoveTo, []Point{q}})
			cur, start = q, q
			// Numbers after a move are lines.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L', 'l', 'H', 'h', 'V', 'v':
			var q Point
			switch cmd {
			case 'L', 'l':
				q, err = pt(rel)
			case 'H', 'h':
				var x float64
				x, err = num()
				q = Point{x, cur.Y}
				if rel {
					q.X += cur.X
				}
			default:
				var y float64
				y, err = num()
				q = Point{cur.X, y}
				if rel {
					q.Y += cur.Y
				}
			}
			if err != nil {
				return nil, err
			}
			if !q.eq(cur) {
				p = append(p, Seg{LineTo, []Point{q}})
			}
			cur = q
		case 'Q', 'q':
			c, err := pt(rel)
			if err != nil {
				return nil, err
			}
			q, err := pt(rel)
			if err != nil {
				return nil, err
			}
			p = append(p, Seg{QuadTo, []Point{c, q}})
			cur = q
		case 'C', 'c':
			c1, err := pt(rel)
			if err != nil {
				return nil, err
			}
			c2, err := pt(rel)
			if err != nil {
				return nil, err
			}
			q, err := pt(rel)
			if err != nil {
				return nil, err
			}
			p = append(p, Seg{CubicTo, []Point{c1, c2, q}})
			cur = q
		case 'Z', 'z':
			p = append(p, Seg{Op: Close})
			cur = start
		default:
			return nil, fmt.Errorf("path data %q: command %c isn't supported", short(d), cmd)
		}
	}
	return p, nil
}

type token struct {
	cmd byte
	num float64
}

func tokenize(d string) ([]token, error) {
	var toks []token
	for i := 0; i < len(d); {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\n' || c == '\t' || c == '\r':
			i++
		case strings.IndexByte("MmLlHhVvQqCcZz", c) >= 0:
			toks = append(toks, token{cmd: c})
			i++
		default:
			j := i
			if d[j] == '-' || d[j] == '+' {
				j++
			}
			dot := false
			for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !dot) {
				if d[j] == '.' {
					dot = true
				}
				j++
			}
			if j < len(d) && (d[j] == 'e' || d[j] == 'E') {
				j++
				if j < len(d) && (d[j] == '-' || d[j] == '+') {
					j++
				}
				for j < len(d) && d[j] >= '0' && d[j] <= '9' {
					j++
				}
			}
			v, err := strconv.ParseFloat(d[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("path data %q: %q isn't a number", short(d), d[i:j])
			}
			toks = append(toks, token{num: v})
			i = j
		}
	}
	return toks, nil
}

func short(d string) string {
	if len(d) > 24 {
		return d[:24] + "..."
	}
	return d
}

// MustPath is ParsePath for the artwork constants, which are known to parse.
func MustPath(d string) Path {
	p, err := ParsePath(d)
	if err != nil {
		panic(err)
	}
	return p
}

// Apply returns the path transformed.
func (p Path) Apply(t Transform) Path {
	out := make(Path, len(p))
	for i, s := range p {
		pts := make([]Point, len(s.Pts))
		for j, q := range s.Pts {
			pts[j] = q.apply(t)
		}
		out[i] = Seg{s.Op, pts}
	}
	return out
}

// Bounds returns the path's bounding box, including how far curves bulge
// between their end points.
func (p Path) Bounds() (minPt, maxPt Point) {
	minPt = Point{math.Inf(1), math.Inf(1)}
	maxPt = Point{math.Inf(-1), math.Inf(-1)}
	p.flatten(0.01, func(a, b Point) {
		for _, q := range []Point{a, b} {
			minPt = Point{math.Min(minPt.X, q.X), math.Min(minPt.Y, q.Y)}
			maxPt = Point{math.Max(maxPt.X, q.X), math.Max(maxPt.Y, q.Y)}
		}
	})
	return minPt, maxPt
}

// flatten turns the path into line segments no further than tolerance from
// the curves, closing every contour.
func (p Path) flatten(tolerance float64, line func(a, b Point)) {
	var cur, start Point
	open := false
	closeContour := func() {
		if open && !cur.eq(start) {
			line(cur, start)
		}
		cur = start
		open = false
	}
	for _, s := range p {
		switch s.Op {
		case MoveTo:
			closeContour()
			cur, start = s.Pts[0], s.Pts[0]
			open = true
		case LineTo:
			line(cur, s.Pts[0])
			cur = s.Pts[0]
		case QuadTo:
			c, e := s.Pts[0], s.Pts[1]
			n := steps(cur.dist(c)+c.dist(e), tolerance)
			prev := cur
			for i := 1; i <= n; i++ {
				t := float64(i) / float64(n)
				q := lerp(lerp(cur, c, t), lerp(c, e, t), t)
				line(prev, q)
				prev = q
			}
			cur = e
		case CubicTo:
			c1, c2, e := s.Pts[0], s.Pts[1], s.Pts[2]
			n := steps(cur.dist(c1)+c1.dist(c2)+c2.dist(e), tolerance)
			prev := cur
			for i := 1; i <= n; i++ {
				t := float64(i) / float64(n)
				a, b, c := lerp(cur, c1, t), lerp(c1, c2, t), lerp(c2, e, t)
				q := lerp(lerp(a, b, t), lerp(b, c, t), t)
				line(prev, q)
				prev = q
			}
			cur = e
		case Close:
			closeContour()
		}
	}
	closeContour()
}

// steps picks how many lines approximate a curve of the given control
// polygon length; the error of a chord shrinks with the square of its length.
func steps(length, tolerance float64) int {
	n := int(math.Ceil(math.Sqrt(length / tolerance)))
	return max(1, min(n, 4096))
}

// D writes the path as SVG path data with at most two decimals.
func (p Path) D() string {
	var b strings.Builder
	for _, s := range p {
		b.WriteByte(byte(s.Op))
		for i, q := range s.Pts {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(num(q.X))
			b.WriteByte(' ')
			b.WriteString(num(q.Y))
		}
	}
	return b.String()
}

// num formats a coordinate with at most two decimals and no trailing zeros.
func num(v float64) string {
	s := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	if s == "-0" {
		return "0"
	}
	return s
}
