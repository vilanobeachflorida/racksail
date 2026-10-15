package i18n

import (
	"fmt"
	"strings"
)

// Format fills {name} placeholders in msg from params. Placeholders without a
// value are left as they are, so a missing parameter is visible rather than
// silently dropped. Braces that don't enclose a simple name are copied
// unchanged.
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
		end := strings.IndexByte(msg[i:], '}')
		if end < 0 {
			b.WriteString(msg[i:])
			break
		}
		name := msg[i+1 : i+end]
		v, ok := params[name]
		if !isName(name) || !ok {
			b.WriteString(msg[i : i+end+1])
		} else {
			b.WriteString(fmt.Sprint(v))
		}
		i += end + 1
	}
	return b.String()
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
