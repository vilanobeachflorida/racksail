package theme

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxCustomCSSBytes bounds an admin's custom CSS.
const MaxCustomCSSBytes = 64 << 10

var customURL = regexp.MustCompile(`(?i)url\(\s*['"]?\s*([^'")\s]*)`)

// CheckCustomCSS checks custom CSS an admin wrote for their own server. It
// may restyle the panel, but it can't load anything from the network,
// reach the protected palette or the elements that use it, or escape the
// cascade layer it is placed in, which sits below personal preferences and
// the protected palette.
func CheckCustomCSS(css string) error {
	if len(css) > MaxCustomCSSBytes {
		return fmt.Errorf("custom CSS is limited to %d KiB", MaxCustomCSSBytes>>10)
	}
	code, err := stripCSS(css)
	if err != nil {
		return err
	}
	lower := strings.ToLower(code)
	for _, bad := range []struct{ text, why string }{
		{"@import", "custom CSS can't import style sheets"},
		{"@layer", "custom CSS can't declare cascade layers; the panel places it in its own"},
		{"@namespace", "custom CSS can't declare namespaces"},
		{"--rs-critical", "custom CSS can't change the protected colors of safety-critical screens"},
		{"data-critical", "custom CSS can't restyle safety-critical screens"},
		{"expression(", "custom CSS can't contain expressions"},
		{"javascript:", "custom CSS can't contain javascript: URLs"},
		{"behavior:", "custom CSS can't attach behaviors"},
		{"-moz-binding", "custom CSS can't attach bindings"},
		{"<", "custom CSS can't contain HTML"},
	} {
		if strings.Contains(lower, bad.text) {
			return errors.New(bad.why)
		}
	}
	for _, m := range customURL.FindAllStringSubmatch(code, -1) {
		if !strings.HasPrefix(strings.ToLower(m[1]), "data:image/") {
			return fmt.Errorf("custom CSS can't load %q; images must be data URLs", m[1])
		}
	}
	return nil
}

// stripCSS blanks out comments and checks that strings are closed and
// braces balance, so custom CSS can't close the block it is wrapped in.
// String contents are kept, since a URL can hide in one.
func stripCSS(css string) (string, error) {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(css); i++ {
		c := css[i]
		switch {
		case c == '/' && i+1 < len(css) && css[i+1] == '*':
			end := strings.Index(css[i+2:], "*/")
			if end < 0 {
				return "", errors.New("custom CSS has a comment that isn't closed")
			}
			i += end + 3
			b.WriteByte(' ')
		case c == '"' || c == '\'':
			j := i + 1
			for ; j < len(css) && css[j] != c; j++ {
				if css[j] == '\\' {
					j++
				} else if css[j] == '\n' {
					return "", errors.New("custom CSS has a string that isn't closed")
				}
			}
			if j >= len(css) {
				return "", errors.New("custom CSS has a string that isn't closed")
			}
			b.WriteString(css[i : j+1])
			i = j
		case c == '\\':
			return "", errors.New("custom CSS can't use escapes outside strings")
		default:
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth < 0 {
					return "", errors.New("custom CSS closes a block it didn't open")
				}
			}
			b.WriteByte(c)
		}
	}
	if depth != 0 {
		return "", errors.New("custom CSS leaves a block open")
	}
	return b.String(), nil
}

// WrapCustomCSS checks custom CSS and places it in its cascade layer, ready
// to serve after the token stylesheet.
func WrapCustomCSS(css string) (string, error) {
	if err := CheckCustomCSS(css); err != nil {
		return "", err
	}
	return "@layer " + LayerCustom + " {\n" + css + "\n}\n", nil
}
