package notify

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// The words notifications add to what they are about, such as how urgent a
// message is, the lines that say what it is about and the link back to the
// panel, are ICU messages like the UI's: messages/en.json holds them in
// English, and a translation is messages/<locale>.json with the same keys.
//
//go:embed messages/*.json
var messageFiles embed.FS

var (
	messagesOnce sync.Once
	messages     *i18n.Catalog
	messagesErr  error
)

var localeFile = regexp.MustCompile(`^([a-z]{2,3}(-[A-Za-z0-9]{2,8})*)\.json$`)

// Messages returns the catalog of the words notifications add, in every
// language there is a translation for.
func Messages() *i18n.Catalog {
	messagesOnce.Do(func() { messages, messagesErr = loadMessages(messageFiles) })
	return messages
}

// loadMessages reads the message catalogs. A translation may only hold
// keys the English catalog has.
func loadMessages(files fs.FS) (*i18n.Catalog, error) {
	c := i18n.NewCatalog()
	read := func(name string) (map[string]string, error) {
		data, err := fs.ReadFile(files, path.Join("messages", name))
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("notify: messages/%s: %w", name, err)
		}
		return m, nil
	}
	english, err := read("en.json")
	if err != nil {
		return c, err
	}
	c.Add(i18n.English, english)
	entries, err := fs.ReadDir(files, "messages")
	if err != nil {
		return c, err
	}
	var errs []string
	for _, e := range entries {
		m := localeFile.FindStringSubmatch(e.Name())
		if m == nil {
			errs = append(errs, "messages/"+e.Name()+" isn't named after a locale")
			continue
		}
		if m[1] == string(i18n.English) {
			continue
		}
		translated, err := read(e.Name())
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for key := range translated {
			if _, ok := english[key]; !ok {
				errs = append(errs, fmt.Sprintf("messages/%s has %s, which English doesn't", e.Name(), key))
			}
		}
		c.Add(i18n.Locale(m[1]), translated)
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("notify: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// words is a message of the catalog in a locale, English when it has no
// translation.
func words(locale, key string, params map[string]any) string {
	return Messages().Message(i18n.Locale(locale), key, params)
}

// rtlLanguages are written right to left.
var rtlLanguages = []string{"ar", "fa", "he", "ur"}

// direction is how text of a locale runs, for emails.
func direction(locale string) string {
	base := string(i18n.Locale(locale).Base())
	for _, l := range rtlLanguages {
		if base == l {
			return "rtl"
		}
	}
	return "ltr"
}

// language is the lang of an email in a locale.
func language(locale string) string {
	if locale == "" {
		return string(i18n.English)
	}
	return locale
}
