package i18n

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRegisteredCodesAreValid(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	for _, d := range Defs() {
		if !d.Code.Valid() {
			t.Errorf("invalid code %q", d.Code)
		}
	}
}

func TestCodeValid(t *testing.T) {
	tests := []struct {
		code Code
		want bool
	}{
		{"web.domain_taken", true},
		{"core.not_found", true},
		{"storage.pool.scrub_failed", true},
		{"web", false},
		{"Web.domain", false},
		{"web.Domain", false},
		{"web.domain-taken", false},
		{".domain", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.code.Valid(); got != tt.want {
			t.Errorf("Code(%q).Valid() = %v, want %v", tt.code, got, tt.want)
		}
	}
	if got := Code("web.domain_taken").Module(); got != "web" {
		t.Errorf("Module() = %q", got)
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		msg    string
		params map[string]any
		want   string
	}{
		{"plain", nil, "plain"},
		{"The domain {domain} is taken.", map[string]any{"domain": "example.com"}, "The domain example.com is taken."},
		{"{a} and {b}", map[string]any{"a": 1}, "1 and {b}"},
		{"{not a name} stays", map[string]any{"not a name": "x"}, "{not a name} stays"},
		{"unclosed {brace", map[string]any{"brace": "x"}, "unclosed {brace"},
		{"{n}{n}", map[string]any{"n": 2}, "22"},
		{"Wait {s, plural, one {# second} other {# seconds}}.", map[string]any{"s": "1"}, "Wait 1 second."},
		{"Wait {s, plural, one {# second} other {# seconds}}.", map[string]any{"s": 6}, "Wait 6 seconds."},
		{"{n, plural, =0 {nothing} one {# file} other {# files}} for {who}", map[string]any{"n": 0, "who": "ada"}, "nothing for ada"},
		{"{n, plural, one {# file of {who}} other {# files of {who}}}", map[string]any{"n": "3", "who": "ada"}, "3 files of ada"},
		{"{n, plural, one {# file} other {# files}}", map[string]any{"n": "many"}, "many files"},
		{"{n, plural, one {# file} other {# files}} stays", nil, "{n, plural, one {# file} other {# files}} stays"},
		{"{n, select, a {x} other {y}}", map[string]any{"n": "a"}, "{n, select, a {x} other {y}}"},
		{`JSON such as {"a": {"b": 1}} stays`, map[string]any{"a": 1}, `JSON such as {"a": {"b": 1}} stays`},
	}
	for _, tt := range tests {
		if got := Format(tt.msg, tt.params); got != tt.want {
			t.Errorf("Format(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestErrorMatchingAndWrapping(t *testing.T) {
	cause := errors.New("disk full")
	err := fmt.Errorf("saving site: %w", ErrInternal.Wrap(cause))

	if !errors.Is(err, ErrInternal) {
		t.Error("errors.Is should match the Def")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("errors.Is should not match a different code")
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is should reach the cause")
	}
	if got := CodeOf(err); got != "core.internal" {
		t.Errorf("CodeOf = %q", got)
	}
	e := From(err)
	if e.Message == "" || e.Status != http.StatusInternalServerError {
		t.Errorf("From = %+v", e)
	}
	if e.Message == e.Error() {
		t.Error("Error() should include the cause for logs, Message should not")
	}
}

func TestFromPlainError(t *testing.T) {
	e := From(errors.New("boom"))
	if e.Code != ErrInternal.Code {
		t.Fatalf("code = %q", e.Code)
	}
	if e.Message == "boom" {
		t.Error("internal causes must not become the user-facing message")
	}
	if From(nil) != nil {
		t.Error("From(nil) should be nil")
	}
}

func TestNewParams(t *testing.T) {
	e := ErrNotFound.New("what", "Site site_123")
	if e.Message != "Site site_123 doesn't exist or was deleted." {
		t.Errorf("Message = %q", e.Message)
	}
	if e.Params["what"] != "Site site_123" {
		t.Errorf("Params = %v", e.Params)
	}
	odd := ErrNotFound.New("what")
	if _, ok := odd.Params["what"]; !ok {
		t.Error("a dangling key should still be recorded")
	}
}

func TestInvalidFields(t *testing.T) {
	e := Invalid(Field("spec.domains[0]", ErrTooLong, "max", 253))
	if e.Code != ErrInvalid.Code || len(e.Fields) != 1 {
		t.Fatalf("unexpected error %+v", e)
	}
	if e.Fields[0].Message != "Use at most 253 characters." {
		t.Errorf("field message = %q", e.Fields[0].Message)
	}
}

func TestCatalogFallback(t *testing.T) {
	c := NewCatalog()
	c.Add(English, map[string]string{"greet": "Hello {name}", "only.en": "English"})
	c.Add("pt", map[string]string{"greet": "Olá {name}"})
	if got := c.Message("pt-BR", "greet", map[string]any{"name": "Ana"}); got != "Olá Ana" {
		t.Errorf("base language fallback = %q", got)
	}
	if got := c.Message("pt", "only.en", nil); got != "English" {
		t.Errorf("English fallback = %q", got)
	}
	if got := c.Message("de", "missing", nil); got != "missing" {
		t.Errorf("key fallback = %q", got)
	}
	if got := len(c.Locales()); got != 2 {
		t.Errorf("Locales = %d", got)
	}
}

func TestNegotiate(t *testing.T) {
	available := []Locale{"en", "de", "pt-BR"}
	tests := []struct {
		header string
		want   Locale
	}{
		{"", "en"},
		{"de-AT,de;q=0.9,en;q=0.8", "de"},
		{"pt-BR", "pt-BR"},
		{"pt;q=0.9, fr", "pt-BR"},
		{"fr, ja;q=0.5", "en"},
		{"en;q=0.1, de;q=0.9", "de"},
	}
	for _, tt := range tests {
		if got := Negotiate(tt.header, available); got != tt.want {
			t.Errorf("Negotiate(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

// Dates, times and numbers in messages are written as each language does,
// from CLDR: its region's way, its language's, or ISO 8601 for languages
// the panel has no conventions of.
func TestFormatInLocales(t *testing.T) {
	when := time.Date(2026, 3, 2, 15, 4, 5, 0, time.UTC)
	morning := time.Date(2026, 3, 2, 0, 4, 5, 0, time.UTC)
	tests := []struct {
		locale Locale
		msg    string
		params map[string]any
		want   string
	}{
		{"en", "{t, date, full}", map[string]any{"t": when}, "Monday, March 2, 2026"},
		{"en", "{t, date, long}", map[string]any{"t": when}, "March 2, 2026"},
		{"en", "{t, date, medium}", map[string]any{"t": when}, "Mar 2, 2026"},
		{"en", "{t, date}", map[string]any{"t": when}, "Mar 2, 2026"},
		{"en", "{t, date, short}", map[string]any{"t": when}, "3/2/26"},
		{"en", "{t, time, short}", map[string]any{"t": when}, "3:04 PM"},
		{"en", "{t, time, long}", map[string]any{"t": when}, "3:04:05 PM UTC"},
		{"en", "{t, time, medium}", map[string]any{"t": morning}, "12:04:05 AM"},
		{"en-US", "{t, date, medium}, {t, time, short}", map[string]any{"t": &when}, "Mar 2, 2026, 3:04 PM"},
		{"en-GB", "{t, date, medium}, {t, time, short}", map[string]any{"t": when}, "2 Mar 2026, 15:04"},
		{"en-GB", "{t, date, short}", map[string]any{"t": when}, "02/03/2026"},
		{"de", "{t, date, full}", map[string]any{"t": when}, "Montag, 2. März 2026"},
		{"de", "{t, date, medium}, {t, time, long}", map[string]any{"t": when}, "02.03.2026, 15:04:05 UTC"},
		{"de-AT", "{t, date, short}", map[string]any{"t": when}, "02.03.26"},
		{"fr", "{t, date, full} à {t, time, short}", map[string]any{"t": when}, "lundi 2 mars 2026 à 15:04"},
		{"es", "{t, date, long}", map[string]any{"t": when}, "2 de marzo de 2026"},
		{"es", "{t, time, full}", map[string]any{"t": when}, "15:04:05 (UTC)"},
		{"it", "{t, date, medium}", map[string]any{"t": when}, "2 mar 2026"},
		{"pt-BR", "{t, date, medium}", map[string]any{"t": when}, "2 de mar. de 2026"},
		{"ja", "{t, date, long} {t, time, short}", map[string]any{"t": when}, "2026-03-02 15:04"},
		{"de", "{t, time, short}", map[string]any{"t": when.In(time.FixedZone("CET", 3600))}, "16:04"},
		{"en", "{t, time, long}", map[string]any{"t": when.In(time.FixedZone("CET", 3600))}, "4:04:05 PM CET"},
		{"en", "{t, time, long}", map[string]any{"t": when.In(time.FixedZone("", 5400))}, "4:34:05 PM GMT+01:30"},
		{"en", "{n, number}", map[string]any{"n": 1234567.891}, "1,234,567.891"},
		{"en", "{n, number}", map[string]any{"n": 2.5}, "2.5"},
		{"en", "{n, number}", map[string]any{"n": -1234}, "-1,234"},
		{"de", "{n, number}", map[string]any{"n": 1234567.891}, "1.234.567,891"},
		{"fr", "{n, number}", map[string]any{"n": 1234567}, "1 234 567"},
		{"es", "{n, number}", map[string]any{"n": 1234}, "1234"},
		{"es", "{n, number}", map[string]any{"n": 12345}, "12.345"},
		{"en", "{n, number, integer}", map[string]any{"n": 2.5}, "2"},
		{"en", "{n, number, integer}", map[string]any{"n": 3.5}, "4"},
		{"en", "{n, number, percent}", map[string]any{"n": 0.25}, "25%"},
		{"de", "{n, number, percent}", map[string]any{"n": 0.25}, "25 %"},
		{"de", "{n, plural, one {# Datei} other {# Dateien}}", map[string]any{"n": 1500}, "1.500 Dateien"},
		{"en", "{n, plural, one {# file} other {# files}}", map[string]any{"n": "1500"}, "1500 files"},
		{"en", "{t, date, medium}", map[string]any{"t": "yesterday"}, "{t, date, medium}"},
		{"en", "{t, date, sometime}", map[string]any{"t": when}, "{t, date, sometime}"},
		{"en", "{n, number, currency}", map[string]any{"n": 3}, "{n, number, currency}"},
		{"en", "{n, number}", map[string]any{"n": "3"}, "{n, number}"},
		{"en", "it's {t, date, 'medium'}", map[string]any{"t": when}, "it's {t, date, 'medium'}"},
	}
	for _, tt := range tests {
		if got := FormatIn(tt.locale, tt.msg, tt.params); got != tt.want {
			t.Errorf("FormatIn(%s, %q) = %q, want %q", tt.locale, tt.msg, got, tt.want)
		}
	}
	if got := Format("{t, date, medium}", map[string]any{"t": when}); got != "Mar 2, 2026" {
		t.Errorf("Format writes English: %q", got)
	}
	// Quoted text in a pattern stays as it is.
	if got := formatTime("d 'de' MMMM 'o''clock'", when, conventionsOf("es")); got != "2 de marzo o'clock" {
		t.Errorf("quoted text = %q", got)
	}
}

// A catalog writes dates as the language of the words it found: a
// translation's own way, and English for what nobody translated.
func TestCatalogFormatsInItsLanguage(t *testing.T) {
	c := NewCatalog()
	c.Add(English, map[string]string{"when": "When: {t, date, medium}", "only.en": "Since {t, date, medium}"})
	c.Add("de", map[string]string{"when": "Wann: {t, date, medium}"})
	when := map[string]any{"t": time.Date(2026, 3, 2, 15, 4, 5, 0, time.UTC)}
	if got := c.Message("de-CH", "when", when); got != "Wann: 02.03.2026" {
		t.Errorf("a translation = %q", got)
	}
	if got := c.Message("de", "only.en", when); got != "Since Mar 2, 2026" {
		t.Errorf("English for what isn't translated = %q", got)
	}
	if got := c.Message("en-GB", "when", when); got != "When: 2 Mar 2026" {
		t.Errorf("English of a region = %q", got)
	}
}
