package i18n

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Plural forms are chosen by the cardinal plural rules of the Unicode CLDR,
// as the browser's Intl.PluralRules chooses them for the panel's screens, so
// a translation picks the same form of a sentence in an email as on a
// screen. Each language's rules are CLDR's own, written in its syntax and
// read as the package starts: a category, then the condition under which a
// number takes it, tried in order; a number that matches none is other.
// Languages that share rules share a line.
var pluralData = []struct {
	locales string
	rules   []string
}{
	{"bm bo dz hnj id ig ii in ja jbo jv jw kde kea km ko lkt lo ms my nqo osa sah ses sg su th to tpi vi wo yo yue zh", nil},
	{"am as bn doi fa gu hi kn pcm zu", []string{"one: i = 0 or n = 1"}},
	{"ff hy kab", []string{"one: i = 0,1"}},
	{"ast de en et fi fy gl ia ie io ji lij nl sc sv sw ur yi", []string{"one: i = 1 and v = 0"}},
	{"si", []string{"one: n = 0,1 or i = 0 and f = 1"}},
	{"ak bho csw guw ln mg nso pa ti wa", []string{"one: n = 0..1"}},
	{"tzm", []string{"one: n = 0..1 or n = 11..99"}},
	{"af an asa az bal bem bez bg brx ce cgg chr ckb dv ee el eo eu fo fur gsw ha haw hu jgo jmc ka kaj kcg kk kkj kl ks ksb ku ky lb lg mas mgo ml mn mr nah nb nd ne nn nnh no nr ny nyn om or os ps rm rof rwk saq sd sdh seh sn so sq ss ssy st syr ta te teo tig tk tn tr ts ug uz ve vo vun wae xh xog",
		[]string{"one: n = 1"}},
	{"da", []string{"one: n = 1 or t != 0 and i = 0,1"}},
	{"is", []string{"one: t = 0 and i % 10 = 1 and i % 100 != 11 or t % 10 = 1 and t % 100 != 11"}},
	{"mk", []string{"one: v = 0 and i % 10 = 1 and i % 100 != 11 or f % 10 = 1 and f % 100 != 11"}},
	{"ceb fil tl", []string{"one: v = 0 and i = 1,2,3 or v = 0 and i % 10 != 4,6,9 or v != 0 and f % 10 != 4,6,9"}},
	{"lv prg", []string{
		"zero: n % 10 = 0 or n % 100 = 11..19 or v = 2 and f % 100 = 11..19",
		"one: n % 10 = 1 and n % 100 != 11 or v = 2 and f % 10 = 1 and f % 100 != 11 or v != 2 and f % 10 = 1",
	}},
	{"lag", []string{"zero: n = 0", "one: i = 0,1 and n != 0"}},
	{"blo ksh", []string{"zero: n = 0", "one: n = 1"}},
	{"he iw", []string{"one: i = 1 and v = 0 or i = 0 and v != 0", "two: i = 2 and v = 0"}},
	{"iu naq sat se sma smi smj smn sms", []string{"one: n = 1", "two: n = 2"}},
	{"shi", []string{"one: i = 0 or n = 1", "few: n = 2..10"}},
	{"mo ro", []string{"one: i = 1 and v = 0", "few: v != 0 or n = 0 or n != 1 and n % 100 = 1..19"}},
	{"bs hr sh sr", []string{
		"one: v = 0 and i % 10 = 1 and i % 100 != 11 or f % 10 = 1 and f % 100 != 11",
		"few: v = 0 and i % 10 = 2..4 and i % 100 != 12..14 or f % 10 = 2..4 and f % 100 != 12..14",
	}},
	{"fr", []string{"one: i = 0,1", "many: e = 0 and i != 0 and i % 1000000 = 0 and v = 0 or e != 0..5"}},
	{"pt", []string{"one: i = 0..1", "many: e = 0 and i != 0 and i % 1000000 = 0 and v = 0 or e != 0..5"}},
	{"ca it lld pt-pt scn vec", []string{"one: i = 1 and v = 0", "many: e = 0 and i != 0 and i % 1000000 = 0 and v = 0 or e != 0..5"}},
	{"es", []string{"one: n = 1", "many: e = 0 and i != 0 and i % 1000000 = 0 and v = 0 or e != 0..5"}},
	{"gd", []string{"one: n = 1,11", "two: n = 2,12", "few: n = 3..10,13..19"}},
	{"sl", []string{"one: v = 0 and i % 100 = 1", "two: v = 0 and i % 100 = 2", "few: v = 0 and i % 100 = 3..4 or v != 0"}},
	{"dsb hsb", []string{"one: v = 0 and i % 100 = 1 or f % 100 = 1", "two: v = 0 and i % 100 = 2 or f % 100 = 2", "few: v = 0 and i % 100 = 3..4 or f % 100 = 3..4"}},
	{"cs sk", []string{"one: i = 1 and v = 0", "few: i = 2..4 and v = 0", "many: v != 0"}},
	{"pl", []string{
		"one: i = 1 and v = 0",
		"few: v = 0 and i % 10 = 2..4 and i % 100 != 12..14",
		"many: v = 0 and i != 1 and i % 10 = 0..1 or v = 0 and i % 10 = 5..9 or v = 0 and i % 100 = 12..14",
	}},
	{"be", []string{"one: n % 10 = 1 and n % 100 != 11", "few: n % 10 = 2..4 and n % 100 != 12..14", "many: n % 10 = 0 or n % 10 = 5..9 or n % 100 = 11..14"}},
	{"lt", []string{"one: n % 10 = 1 and n % 100 != 11..19", "few: n % 10 = 2..9 and n % 100 != 11..19", "many: f != 0"}},
	{"ru uk", []string{
		"one: v = 0 and i % 10 = 1 and i % 100 != 11",
		"few: v = 0 and i % 10 = 2..4 and i % 100 != 12..14",
		"many: v = 0 and i % 10 = 0 or v = 0 and i % 10 = 5..9 or v = 0 and i % 100 = 11..14",
	}},
	{"br", []string{
		"one: n % 10 = 1 and n % 100 != 11,71,91",
		"two: n % 10 = 2 and n % 100 != 12,72,92",
		"few: n % 10 = 3..4,9 and n % 100 != 10..19,70..79,90..99",
		"many: n != 0 and n % 1000000 = 0",
	}},
	{"mt", []string{"one: n = 1", "two: n = 2", "few: n = 0 or n % 100 = 3..10", "many: n % 100 = 11..19"}},
	{"ga", []string{"one: n = 1", "two: n = 2", "few: n = 3..6", "many: n = 7..10"}},
	{"gv", []string{"one: v = 0 and i % 10 = 1", "two: v = 0 and i % 10 = 2", "few: v = 0 and i % 100 = 0,20,40,60,80", "many: v != 0"}},
	{"kw", []string{
		"zero: n = 0",
		"one: n = 1",
		"two: n % 100 = 2,22,42,62,82 or n % 1000 = 0 and n % 100000 = 1000..20000,40000,60000,80000 or n != 0 and n % 1000000 = 100000",
		"few: n % 100 = 3,23,43,63,83",
		"many: n != 1 and n % 100 = 1,21,41,61,81",
	}},
	{"ar ars", []string{"zero: n = 0", "one: n = 1", "two: n = 2", "few: n % 100 = 3..10", "many: n % 100 = 11..99"}},
	{"cy", []string{"zero: n = 0", "one: n = 1", "two: n = 2", "few: n = 3", "many: n = 6"}},
}

// pluralRule is one category of a language and the numbers that take it.
type pluralRule struct {
	category string
	when     condition
}

// pluralRules are each language's rules, by its lowercase tag.
var pluralRules = func() map[Locale][]pluralRule {
	out := map[Locale][]pluralRule{}
	for _, d := range pluralData {
		rules := make([]pluralRule, 0, len(d.rules))
		for _, text := range d.rules {
			category, cond, ok := strings.Cut(text, ":")
			when, err := parseCondition(cond)
			if !ok || err != nil {
				panic(fmt.Sprintf("i18n: the plural rule %q of %s: %v", text, d.locales, err))
			}
			rules = append(rules, pluralRule{category: strings.TrimSpace(category), when: when})
		}
		for _, l := range strings.Fields(d.locales) {
			out[Locale(l)] = rules
		}
	}
	return out
}()

// pluralRulesOf returns a language's rules: its region's, such as pt-PT's,
// its language's, or CLDR's root, where every number is other.
func pluralRulesOf(l Locale) []pluralRule {
	if rules, ok := pluralRules[Locale(strings.ToLower(string(l)))]; ok {
		return rules
	}
	return pluralRules[l.Base()]
}

// pluralCategory returns the category a count takes in a language, or ""
// when the count isn't a number.
func pluralCategory(rules []pluralRule, count any) string {
	ops, ok := operandsOf(count)
	if !ok {
		return ""
	}
	for _, r := range rules {
		if r.when.holds(ops) {
			return r.category
		}
	}
	return "other"
}

// operands are a number as plural rules see it: n its absolute value, i its
// integer digits, v and f how many fraction digits it shows and what they
// say, and w and t the same without trailing zeros. Numbers here are never
// written in compact notation, so the exponent, e, is always 0.
type operands struct {
	n          float64
	i, f, t, e int64
	v, w       int
}

// operandsOf reads a count's operands from its decimal digits: a string,
// such as "1.50", as it is written, and a float as its shortest form.
func operandsOf(count any) (operands, bool) {
	var text string
	switch c := count.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		text = fmt.Sprint(c)
	case float32:
		text = strconv.FormatFloat(float64(c), 'f', -1, 32)
	case float64:
		text = strconv.FormatFloat(c, 'f', -1, 64)
	case json.Number:
		text = string(c)
	case string:
		text = strings.TrimSpace(c)
	default:
		return operands{}, false
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "-"), "+")
	integer, fraction, _ := strings.Cut(text, ".")
	if integer == "" || !digits(integer) || !digits(fraction) {
		return operands{}, false
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(n, 0) {
		return operands{}, false
	}
	var ops operands
	ops.n = n
	ops.i = trailingInt(integer)
	ops.v = len(fraction)
	trimmed := strings.TrimRight(fraction, "0")
	ops.w = len(trimmed)
	ops.f, ops.t = trailingInt(fraction), trailingInt(trimmed)
	return ops, true
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// trailingInt reads digits as a number, keeping the last 18 of them when
// there are more, which the rules' moduli never reach beyond.
func trailingInt(s string) int64 {
	if len(s) > 18 {
		s = s[len(s)-18:]
	}
	v, _ := strconv.ParseInt("0"+s, 10, 64)
	return v
}

// A condition is the relations of a rule, joined by and within each of its
// alternatives, which are joined by or.
type condition [][]relation

// relation is "operand [% modulus] (= | !=) ranges".
type relation struct {
	operand byte
	modulus int64
	negated bool
	ranges  [][2]int64
}

func (c condition) holds(ops operands) bool {
	for _, all := range c {
		ok := true
		for _, r := range all {
			if !r.holds(ops) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (r relation) holds(ops operands) bool {
	var x float64
	switch r.operand {
	case 'n':
		x = ops.n
	case 'i':
		x = float64(ops.i)
	case 'v':
		x = float64(ops.v)
	case 'w':
		x = float64(ops.w)
	case 'f':
		x = float64(ops.f)
	case 't':
		x = float64(ops.t)
	case 'c', 'e':
		x = float64(ops.e)
	}
	if r.modulus > 0 {
		x = math.Mod(x, float64(r.modulus))
	}
	in := false
	// A number is in a range only when it is a whole number within it.
	if x == math.Trunc(x) {
		for _, rg := range r.ranges {
			if x >= float64(rg[0]) && x <= float64(rg[1]) {
				in = true
				break
			}
		}
	}
	return in != r.negated
}

// parseCondition reads a condition in CLDR's syntax, such as
// "v = 0 and i % 10 = 2..4 and i % 100 != 12..14".
func parseCondition(text string) (condition, error) {
	var c condition
	for _, alternative := range strings.Split(text, " or ") {
		var all []relation
		for _, part := range strings.Split(alternative, " and ") {
			r, err := parseRelation(strings.TrimSpace(part))
			if err != nil {
				return nil, err
			}
			all = append(all, r)
		}
		c = append(c, all)
	}
	return c, nil
}

func parseRelation(text string) (relation, error) {
	var r relation
	left, right, ok := strings.Cut(text, "!=")
	if ok {
		r.negated = true
	} else if left, right, ok = strings.Cut(text, "="); !ok {
		return r, fmt.Errorf("%q has no = or !=", text)
	}
	left = strings.TrimSpace(left)
	if operand, mod, ok := strings.Cut(left, "%"); ok {
		m, err := strconv.ParseInt(strings.TrimSpace(mod), 10, 64)
		if err != nil || m <= 0 {
			return r, fmt.Errorf("%q has a modulus that isn't a number", text)
		}
		r.modulus, left = m, strings.TrimSpace(operand)
	}
	if len(left) != 1 || !strings.ContainsRune("nivwftce", rune(left[0])) {
		return r, fmt.Errorf("%q names no operand", text)
	}
	r.operand = left[0]
	for _, item := range strings.Split(right, ",") {
		from, to, isRange := strings.Cut(strings.TrimSpace(item), "..")
		a, err := strconv.ParseInt(from, 10, 64)
		if err != nil {
			return r, fmt.Errorf("%q has a value that isn't a number", text)
		}
		b := a
		if isRange {
			if b, err = strconv.ParseInt(to, 10, 64); err != nil || b < a {
				return r, fmt.Errorf("%q has a range that isn't one", text)
			}
		}
		r.ranges = append(r.ranges, [2]int64{a, b})
	}
	return r, nil
}
