package i18n

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Locale is a BCP 47 language tag such as "en", "de" or "pt-BR".
type Locale string

// English is the source language of every message.
const English Locale = "en"

// Base returns the language part of the tag ("pt" for "pt-BR").
func (l Locale) Base() Locale {
	base, _, _ := strings.Cut(string(l), "-")
	return Locale(strings.ToLower(base))
}

// Catalog holds translated messages for server-side text that isn't an error:
// CLI output, notification emails, installer and console screens.
type Catalog struct {
	mu       sync.RWMutex
	messages map[Locale]map[string]string
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{messages: map[Locale]map[string]string{}}
}

// Add merges messages for a locale into the catalog.
func (c *Catalog) Add(locale Locale, messages map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.messages[locale]
	if m == nil {
		m = make(map[string]string, len(messages))
		c.messages[locale] = m
	}
	for k, v := range messages {
		m[k] = v
	}
}

// Message returns the message for key in locale with params filled in. It
// falls back to the base language, then to English, then to the key itself.
// Dates and numbers are written as the language of the words found does:
// locale's way for its own words and its base language's, and English for
// words nobody translated, so a sentence never mixes the two.
func (c *Catalog) Message(locale Locale, key string, params map[string]any) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, l := range []Locale{locale, locale.Base(), English} {
		if msg, ok := c.messages[l][key]; ok {
			if l == English && locale.Base() != English {
				return FormatIn(English, msg, params)
			}
			return FormatIn(locale, msg, params)
		}
	}
	return key
}

// Locales returns the locales that have at least one message, sorted.
func (c *Catalog) Locales() []Locale {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Locale, 0, len(c.messages))
	for l := range c.messages {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Negotiate picks the best of the available locales for an Accept-Language
// header value. It matches exact tags first, then base languages, and falls
// back to English.
func Negotiate(acceptLanguage string, available []Locale) Locale {
	type pref struct {
		tag Locale
		q   float64
	}
	var prefs []pref
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if q > 0 {
			prefs = append(prefs, pref{Locale(tag), q})
		}
	}
	sort.SliceStable(prefs, func(i, j int) bool { return prefs[i].q > prefs[j].q })
	for _, p := range prefs {
		for _, a := range available {
			if strings.EqualFold(string(a), string(p.tag)) {
				return a
			}
		}
		for _, a := range available {
			if a.Base() == p.tag.Base() {
				return a
			}
		}
	}
	return English
}
