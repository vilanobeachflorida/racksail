package i18n

import (
	"fmt"
	"strconv"
	"strings"
)

// Format fills {name} placeholders in msg from params. Placeholders without a
// value are left as they are, so a missing parameter is visible rather than
// silently dropped. Braces that don't enclose a simple name are copied
// unchanged, except plurals in the form the UI's ICU catalogs use, such as
// {count, plural, one {# day} other {# days}}, which pick the form English
// takes for the count and put the count where # is.
func Format(msg string, params map[string]any) string {
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
		} else if text, ok := plural(inner, params); ok {
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
// one for exactly 1 and other for everything else. ok is false when it
// isn't a plural or the count is missing.
func plural(inner string, params map[string]any) (string, bool) {
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
	count := fmt.Sprint(v)
	text, ok := options["="+count]
	if !ok {
		key := "other"
		if n, err := strconv.ParseFloat(count, 64); err == nil && n == 1 {
			key = "one"
		}
		if text, ok = options[key]; !ok {
			if text, ok = options["other"]; !ok {
				return "", false
			}
		}
	}
	return Format(strings.ReplaceAll(text, "#", count), params), true
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
