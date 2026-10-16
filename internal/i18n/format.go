package i18n

import (
	"fmt"
	"strings"
)

// Format fills {name} placeholders in msg from params, as FormatIn does
// for English.
func Format(msg string, params map[string]any) string {
	return FormatIn(English, msg, params)
}

// FormatIn fills {name} placeholders in msg from params, writing dates,
// times and numbers as the language of locale does. Placeholders without a
// value are left as they are, so a missing parameter is visible rather than
// silently dropped. Braces that don't enclose a simple name are copied
// unchanged, except the arguments the UI's ICU catalogs use: plurals such
// as {count, plural, one {# day} other {# days}}, which pick the form =N
// for exactly N, or else the form the language's CLDR plural rules give
// the count, zero, one, two, few, many or other, and put the count where #
// is; dates and times of a time.Time, as {when, date, medium} and {when,
// time, short}, in the styles full, long, medium and short; and numbers,
// as {size, number}, {size, number, integer} and {share, number, percent}.
func FormatIn(locale Locale, msg string, params map[string]any) string {
	return formatIn(writing{c: conventionsOf(locale), plurals: pluralRulesOf(locale)}, msg, params)
}

// writing is how a language writes: its dates and numbers, and its plural
// forms.
type writing struct {
	c       *conventions
	plurals []pluralRule
}

func formatIn(w writing, msg string, params map[string]any) string {
	c := w.c
	if !strings.Contains(msg, "{") {
		return msg
	}
	var b strings.Builder
	b.Grow(len(msg))
	for i := 0; i < len(msg); {
		if msg[i] != '{' {
			b.WriteByte(msg[i])
			i++
			continue
		}
		end := closing(msg, i)
		if end < 0 {
			b.WriteString(msg[i:])
			break
		}
		inner := msg[i+1 : end]
		if v, ok := params[inner]; ok && isName(inner) {
			b.WriteString(fmt.Sprint(v))
		} else if text, ok := plural(inner, params, w); ok {
			b.WriteString(text)
		} else if text, ok := typed(inner, params, c); ok {
			b.WriteString(text)
		} else {
			b.WriteString(msg[i : end+1])
		}
		i = end + 1
	}
	return b.String()
}

// closing returns where the brace that opens at start closes, counting the
// braces inside, or -1 when it never does.
func closing(msg string, start int) int {
	depth := 0
	for i := start; i < len(msg); i++ {
		switch msg[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// plural formats the inside of a plural placeholder, "count, plural, one
// {...} other {...}", for the count params gives, choosing =N first, then
// the category the language's rules give the count, and other when the
// message has no form for it or the count isn't a number, and writing the
// count as the language does. ok is false when it isn't a plural or the
// count is missing.
func plural(inner string, params map[string]any, w writing) (string, bool) {
	name, rest, ok := strings.Cut(inner, ",")
	name = strings.TrimSpace(name)
	if !ok || !isName(name) {
		return "", false
	}
	kind, rest, ok := strings.Cut(rest, ",")
	if !ok || strings.TrimSpace(kind) != "plural" {
		return "", false
	}
	v, ok := params[name]
	if !ok {
		return "", false
	}
	options := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; rest = strings.TrimSpace(rest) {
		open := strings.IndexByte(rest, '{')
		if open <= 0 {
			return "", false
		}
		end := closing(rest, open)
		if end < 0 {
			return "", false
		}
		options[strings.TrimSpace(rest[:open])] = rest[open+1 : end]
		rest = rest[end+1:]
	}
	text, ok := options["="+fmt.Sprint(v)]
	if !ok {
		if text, ok = options[pluralCategory(w.plurals, v)]; !ok {
			if text, ok = options["other"]; !ok {
				return "", false
			}
		}
	}
	return formatIn(w, strings.ReplaceAll(text, "#", countText(v, w.c)), params), true
}

func isName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
