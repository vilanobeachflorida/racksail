package i18n

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Each language takes the plural forms CLDR gives it, the same the panel's
// screens take in a browser, for whole numbers and for decimals, which
// count by the digits they show.
func TestPluralCategories(t *testing.T) {
	for locale, want := range map[Locale]map[any]string{
		"en":    {0: "other", 1: "one", 2: "other", 1.5: "other", "1.0": "other", "1": "one", -1: "one"},
		"de":    {1: "one", 0: "other", "1.0": "other", 21: "other"},
		"fr":    {0: "one", 1: "one", 1.5: "one", 2: "other", 1000000: "many", 2000000: "many", 1000001: "other", "1000000.0": "other"},
		"pt":    {0: "one", 1: "one", 1.5: "one", 2: "other", 1000000: "many"},
		"pt-PT": {0: "other", 1: "one", 2: "other", 1000000: "many"},
		"pt-BR": {0: "one", 1: "one"},
		"es":    {0: "other", 1: "one", 2: "other", 1000000: "many", 1.5: "other"},
		"it":    {0: "other", 1: "one", 1000000: "many"},
		"ca":    {1: "one", 2: "other", 3000000: "many"},
		"ru":    {1: "one", 21: "one", 101: "one", 2: "few", 22: "few", 4: "few", 5: "many", 11: "many", 12: "many", 14: "many", 0: "many", 1.5: "other"},
		"uk":    {1: "one", 3: "few", 11: "many", 2.5: "other"},
		"pl":    {1: "one", 2: "few", 22: "few", 12: "many", 21: "many", 5: "many", 0: "many", 1.5: "other"},
		"cs":    {1: "one", 2: "few", 4: "few", 5: "other", 0: "other", 1.5: "many"},
		"ar":    {0: "zero", 1: "one", 2: "two", 3: "few", 10: "few", 103: "few", 11: "many", 99: "many", 100: "other", 102: "other", 0.5: "other"},
		"he":    {1: "one", 2: "two", 0.5: "one", 3: "other", 20: "other", "2.0": "other"},
		"lt":    {1: "one", 21: "one", 11: "other", 2: "few", 9: "few", 10: "other", 19: "other", 1.5: "many"},
		"lv":    {0: "zero", 10: "zero", 11: "zero", 19: "zero", 1: "one", 21: "one", 2: "other", 0.1: "one", 0.2: "other", "10.0": "zero", "1.0": "one"},
		"hr":    {1: "one", 21: "one", 11: "other", 2: "few", 24: "few", 12: "other", 5: "other", 0.1: "one", 0.2: "few"},
		"sr":    {1: "one", 3: "few", 5: "other"},
		"sl":    {1: "one", 101: "one", 2: "two", 102: "two", 3: "few", 4: "few", 5: "other", 0.5: "few"},
		"ro":    {1: "one", 0: "few", 2: "few", 19: "few", 20: "other", 101: "few", 120: "other", 1.5: "few"},
		"da":    {1: "one", 0: "other", 0.1: "one", 1.5: "one", 2: "other", 2.5: "other"},
		"is":    {1: "one", 21: "one", 11: "other", 0.1: "one", 2: "other"},
		"mk":    {1: "one", 11: "other", 21: "one", 0.1: "one"},
		"fil":   {1: "one", 2: "one", 3: "one", 4: "other", 5: "one", 6: "other", 14: "other", 0.5: "one", 0.4: "other"},
		"ga":    {1: "one", 2: "two", 3: "few", 6: "few", 7: "many", 10: "many", 11: "other"},
		"cy":    {0: "zero", 1: "one", 2: "two", 3: "few", 6: "many", 4: "other", 5: "other"},
		"mt":    {0: "few", 1: "one", 2: "two", 3: "few", 10: "few", 11: "many", 19: "many", 20: "other", 102: "other"},
		"br":    {1: "one", 21: "one", 11: "other", 71: "other", 2: "two", 3: "few", 9: "few", 19: "other", 1000000: "many", 5: "other"},
		"hi":    {0: "one", 1: "one", 0.5: "one", 2: "other"},
		"tr":    {1: "one", 0: "other", 2: "other"},
		"ja":    {0: "other", 1: "other", 2: "other"},
		"zh-TW": {1: "other"},
		"xx":    {1: "other", 0: "other"},
	} {
		for count, category := range want {
			if got := pluralCategory(pluralRulesOf(locale), count); got != category {
				t.Errorf("%s: %v is %q, want %q", locale, count, got, category)
			}
		}
	}
	for _, count := range []any{"many", "", "1e3", "1.2.3", nil, true} {
		if got := pluralCategory(pluralRulesOf("en"), count); got != "" {
			t.Errorf("%v, which isn't a number, is %q", count, got)
		}
	}
	if got := pluralCategory(pluralRulesOf("ru"), json.Number("21")); got != "one" {
		t.Errorf("a JSON number is %q", got)
	}
}

// Every rule of every language is one the panel reads, of a category CLDR
// has, and no language has two lists of rules.
func TestPluralRulesParse(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range pluralData {
		for _, l := range strings.Fields(d.locales) {
			if seen[l] {
				t.Errorf("%s has two lists of rules", l)
			}
			seen[l] = true
		}
		for _, text := range d.rules {
			category, cond, _ := strings.Cut(text, ":")
			if !slices.Contains([]string{"zero", "one", "two", "few", "many"}, category) {
				t.Errorf("%s: %q isn't a category", text, category)
			}
			if _, err := parseCondition(cond); err != nil {
				t.Errorf("%s: %v", text, err)
			}
		}
	}
	for _, bad := range []string{"n", "n = x", "q = 1", "n % 0 = 1", "n = 3..1", "i % = 2"} {
		if _, err := parseCondition(bad); err == nil {
			t.Errorf("%q is read as a condition", bad)
		}
	}
}

// Messages choose the form the language takes, falling back to other where
// a message has no such form, and =N before anything.
func TestPluralMessages(t *testing.T) {
	days := "{n, plural, one {# jour} other {# jours}}"
	files := "{n, plural, one {# plik} few {# pliki} many {# plików} other {# pliku}}"
	for _, tt := range []struct {
		locale Locale
		msg    string
		n      any
		want   string
	}{
		{"fr", days, 0, "0 jour"},
		{"fr", days, 1.5, "1,5 jour"},
		{"fr", days, 2, "2 jours"},
		{"fr", "{n, plural, one {# jour} many {# de jours} other {# jours}}", 1000000, "1\u202f000\u202f000 de jours"},
		{"pl", files, 1, "1 plik"},
		{"pl", files, 3, "3 pliki"},
		{"pl", files, 25, "25 plików"},
		{"pl", files, 1.5, "1.5 pliku"},
		{"en", days, 0, "0 jours"},
		{"ar", "{n, plural, zero {none} one {one} two {two} few {a few} many {many} other {#}}", 100, "100"},
		{"ar", "{n, plural, =0 {nothing} zero {none} other {#}}", 0, "nothing"},
		{"ru", "{n, plural, one {# файл} other {# файла}}", 5, "5 файла"},
	} {
		if got := FormatIn(tt.locale, tt.msg, map[string]any{"n": tt.n}); got != tt.want {
			t.Errorf("FormatIn(%s, %q, %v) = %q, want %q", tt.locale, tt.msg, tt.n, got, tt.want)
		}
	}
	// What nobody translated is English, and takes English forms.
	c := NewCatalog()
	c.Add(English, map[string]string{"count": "{n, plural, one {# notification} other {# notifications}}"})
	if got := c.Message("fr", "count", map[string]any{"n": 0}); got != "0 notifications" {
		t.Errorf("English words in French = %q", got)
	}
}
