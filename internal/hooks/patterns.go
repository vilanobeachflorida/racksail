package hooks

import (
	"path"
	"regexp"
	"strings"
)

// Patterns name several hook points at once, segment by segment: * stands
// for any part of one segment and ? for one character, so
// web.site.*.validate names the validate points of every operation on
// sites, and *.*.create.plan the plan points of every create. A pattern
// never spans segments, so it can't reach points of another shape.

// IsPattern reports whether p names points with wildcards.
func IsPattern(p Point) bool { return strings.ContainsAny(string(p), "*?") }

var patternSegment = regexp.MustCompile(`^[a-z0-9*?][a-z0-9_*?-]*$`)

// validPattern checks a pattern's shape: at least two segments of the
// characters points use, and wildcards.
func validPattern(p Point) bool {
	segments := strings.Split(string(p), ".")
	if len(segments) < 2 {
		return false
	}
	for _, s := range segments {
		if !patternSegment.MatchString(s) {
			return false
		}
		if _, err := path.Match(s, ""); err != nil {
			return false
		}
	}
	return true
}

// MatchPoint reports whether a pattern names a point. A point that isn't
// a pattern only names itself.
func MatchPoint(pattern, p Point) bool {
	if !IsPattern(pattern) {
		return pattern == p
	}
	ps, xs := strings.Split(string(pattern), "."), strings.Split(string(p), ".")
	if len(ps) != len(xs) {
		return false
	}
	for i, seg := range ps {
		if ok, err := path.Match(seg, xs[i]); err != nil || !ok {
			return false
		}
	}
	return true
}

// KindPattern returns the pattern of the points of one stage of some
// operations on some kinds. An empty kind or operation stands for every
// one, so KindPattern("", "create", "plan") is *.*.create.plan.
func KindPattern(kind, operation string, stage string) Point {
	if kind == "" {
		kind = "*.*"
	}
	if operation == "" {
		operation = "*"
	}
	return Point(kind + "." + operation + "." + stage)
}

// ParseKindPoint splits a point of the change pipeline into its kind,
// operation and stage, such as web.site, deploy and validate. It reports
// false for points that aren't of that form.
func ParseKindPoint(p Point) (kind, operation, stage string, ok bool) {
	parts := strings.Split(string(p), ".")
	if len(parts) != 4 {
		return "", "", "", false
	}
	switch parts[3] {
	case "validate", "plan", "applied":
	default:
		return "", "", "", false
	}
	return parts[0] + "." + parts[1], parts[2], parts[3], true
}
