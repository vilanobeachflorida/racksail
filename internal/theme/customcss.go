package theme

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxCustomCSSBytes bounds an admin's custom CSS.
const MaxCustomCSSBytes = 64 << 10

// CustomCSSScope is where custom CSS applies: the whole page except
// safety-critical areas (elements marked data-critical) and everything
// inside them.
const CustomCSSScope = "@scope (:root) to ([data-critical])"

// CheckCustomCSS checks CSS an admin wrote for their own server. It may
// restyle the panel, but it can't load anything from the network, use
// !important, mention the protected palette or the markers of
// safety-critical areas, or escape the cascade layer and scope
// WrapCustomCSS puts it in, which rank below personal preferences and the
// protected palette and leave safety-critical areas alone. The CSS is read
// as browsers read it (CSS Syntax Level 3), so what's checked is what they
// see.
func CheckCustomCSS(css string) error {
	_, err := customCSSRules.split(css)
	return err
}

func (r cssRules) split(css string) ([]cssRule, error) {
	if len(css) > MaxCustomCSSBytes {
		return nil, fmt.Errorf("custom CSS is limited to %d KiB", MaxCustomCSSBytes>>10)
	}
	if strings.Contains(css, "<") {
		return nil, errors.New("custom CSS can't contain the < character, which could start HTML")
	}
	return checkCSS(css, r, cssSheet)
}

// WrapCustomCSS checks custom CSS and wraps it, ready to serve after the
// token stylesheet: its rules go in the custom cascade layer, inside a
// scope that stops at safety-critical areas. Rules that define something
// for the whole document, such as @font-face and @keyframes, sit in the
// layer outside the scope, which they can't be nested in.
func WrapCustomCSS(css string) (string, error) {
	rules, err := customCSSRules.split(css)
	if err != nil {
		return "", err
	}
	var global, scoped []string
	for _, rule := range rules {
		if slices.Contains(cssGlobalAtRules, rule.atRule) {
			global = append(global, rule.text)
		} else {
			scoped = append(scoped, rule.text)
		}
	}
	var b strings.Builder
	b.WriteString("@layer " + LayerCustom + " {\n")
	for _, rule := range global {
		b.WriteString(rule + "\n")
	}
	if len(scoped) > 0 {
		b.WriteString(CustomCSSScope + " {\n")
		for _, rule := range scoped {
			b.WriteString(rule + "\n")
		}
		b.WriteString("}\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}
