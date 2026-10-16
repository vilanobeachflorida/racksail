package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// german is a translation of the words notifications add, for the tests.
var german = map[string]string{
	"notify.action.open":       "Im Panel öffnen",
	"notify.details.about":     "Betrifft: {subject}",
	"notify.details.by":        "Von: {actor}",
	"notify.details.event":     "Ereignis: {type}",
	"notify.details.when":      "Wann: {time}",
	"notify.digest.body":       "Diese kamen während der Ruhezeit an, ab {time}.",
	"notify.digest.title":      "{count, plural, one {# Benachrichtigung} other {# Benachrichtigungen}} aus der Ruhezeit",
	"notify.footer":            "Sie erhalten dies wegen Ihrer Benachrichtigungseinstellungen in {brand}.",
	"notify.link":              "{label}: {url}",
	"notify.severity.critical": "Kritisch",
	"notify.severity.error":    "Fehler",
	"notify.severity.info":     "Info",
	"notify.severity.notice":   "Hinweis",
	"notify.severity.warning":  "Warnung",
	"notify.subject":           "{severity}: {title}",
	"notify.test.body":         "Dies ist ein Test von {brand}. Wenn er angekommen ist, funktioniert der Kanal {channel}.",
	"notify.test.title":        "Testbenachrichtigung",
	"notify.test_email.body":   "Dies ist ein Test von {brand}. Wenn er angekommen ist, funktioniert das Mail-Relay.",
	"notify.test_email.title":  "Test-E-Mail",
}

// The words the code uses are in the English catalog, and the catalog
// holds none the code doesn't use.
func TestMessageCatalog(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("messages", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	var english map[string]string
	if err := json.Unmarshal(raw, &english); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("*.go")
	used := map[string]bool{}
	key := regexp.MustCompile(`words\([^,()]+, "(notify\.[a-z_.]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range key.FindAllStringSubmatch(string(src), -1) {
			used[m[1]] = true
		}
	}
	for _, sev := range []events.Severity{events.SeverityInfo, events.SeverityNotice, events.SeverityWarning, events.SeverityError, events.SeverityCritical} {
		if !used["notify.severity."] {
			t.Fatal("the severities' words aren't looked up")
		}
		used["notify.severity."+string(sev)] = true
	}
	delete(used, "notify.severity.")
	for k := range used {
		if _, ok := english[k]; !ok {
			t.Errorf("the code uses %s, which messages/en.json doesn't have", k)
		}
	}
	for k := range english {
		if !used[k] {
			t.Errorf("messages/en.json has %s, which nothing uses", k)
		}
	}
	for k := range german {
		if _, ok := english[k]; !ok {
			t.Errorf("the test's translation has %s, which English doesn't", k)
		}
	}

	// A translation follows the English catalog's keys, and its file is
	// named after its locale.
	en, _ := json.Marshal(english)
	_, err = loadMessages(fstest.MapFS{
		"messages/en.json":      {Data: en},
		"messages/de.json":      {Data: []byte(`{"notify.subject":"{severity}: {title}","notify.nothing":"x"}`)},
		"messages/english.json": {Data: []byte(`{}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "notify.nothing") || !strings.Contains(err.Error(), "english.json isn't named after a locale") {
		t.Errorf("a stray key and a file not named after a locale: %v", err)
	}
	c, err := loadMessages(fstest.MapFS{"messages/en.json": {Data: en}, "messages/pt-BR.json": {Data: []byte(`{"notify.test.title":"Notificação de teste"}`)}})
	if err != nil || c.Message("pt-BR", "notify.test.title", nil) != "Notificação de teste" || c.Message("pt-BR", "notify.footer", map[string]any{"brand": "Acme"}) != "You get this because of your notification settings in Acme." {
		t.Errorf("a translation, and English for what it leaves out: %v", err)
	}
}

// Messages carry the panel's words in the language of whoever gets them:
// a channel's, or the person's own.
func TestMessagesInTheirLanguage(t *testing.T) {
	Messages().Add("de", german)
	warning := Message{Title: "Speicher fast voll", Body: "Noch 2 GB frei.", Severity: events.SeverityWarning, URL: "https://panel.example.com/storage", Locale: "de"}
	if got := Subject(warning); got != "Warnung: Speicher fast voll" {
		t.Errorf("subject = %q", got)
	}
	if got := Text(warning); !strings.HasSuffix(got, "Im Panel öffnen: https://panel.example.com/storage") {
		t.Errorf("text = %q", got)
	}
	html, err := HTML(context.Background(), warning, Env{BrandName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `lang="de"`) || !strings.Contains(html, "Benachrichtigungseinstellungen in Acme") || !strings.Contains(html, "Im Panel öffnen") {
		t.Errorf("the email is in German: %s", html)
	}
	if html, _ := HTML(context.Background(), Message{Title: "x", Locale: "ar"}, Env{}); !strings.Contains(html, `dir="rtl"`) {
		t.Error("Arabic runs right to left")
	}
	if got := Subject(Message{Title: "Disk almost full", Severity: events.SeverityError}); got != "Error: Disk almost full" {
		t.Errorf("without a language, the words are English: %q", got)
	}

	s := &Service{opts: Options{Link: func(events.Event) string { return "https://panel.example.com/x" }}}
	e := events.Event{Type: "backups.run.failed", Severity: events.SeverityError, Subject: "bpol_1", Message: "The backup failed.", Time: time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC), Actor: &auth.Actor{Name: "nightly"}}
	msg := s.messageOf(e, "de")
	for _, want := range []string{"Ereignis: backups.run.failed", "Wann: 2026-03-02 03:00:00 UTC", "Betrifft: bpol_1", "Von: nightly"} {
		if !strings.Contains(msg.Body, want) {
			t.Errorf("the body lacks %q: %s", want, msg.Body)
		}
	}
	if msg.Locale != "de" || msg.ActionLabel != "Im Panel öffnen" {
		t.Errorf("message = %+v", msg)
	}
	group := []*Notification{{Message: &msg, Severity: events.SeverityError}, {Message: &msg, Severity: events.SeverityWarning}}
	if d := s.summary(group, time.Now()); d.Title != "2 Benachrichtigungen aus der Ruhezeit" || d.Message.Locale != "de" || !strings.HasPrefix(d.Message.Body, "Diese kamen") {
		t.Errorf("summary = %+v %+v", d, d.Message)
	}
}

// A channel's messages, and a person's, come in their language.
func TestDeliveriesInTheirLanguage(t *testing.T) {
	Messages().Add("de", german)
	alice := &Person{ID: store.NewID("user"), Name: "Alice", Email: "alice@example.com", Locale: "de", Actor: &auth.Actor{
		Type: auth.ActorUser, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}},
	}}
	alice.Actor.ID = alice.ID
	p := newPanel(t, Options{Directory: directory{alice.ID: alice}, Authorizer: auth.NewRoleAuthorizer(nil)})
	r := newReceiver(t, http.StatusOK, "")
	p.run(t)
	p.createChannel(t, ChannelSpec{Name: "Ops", Type: "webhook", Settings: settings(WebhookSettings{URL: r.srv.URL}), Events: []string{"*.failed"}, Locale: "de"}, nil)
	ctx := context.Background()
	req, err := p.svc.PreferencesRequest(ctx, alice.ID, NotificationPreferences{Email: true})
	if err != nil {
		t.Fatal(err)
	}
	req.Actor = PreferencesActor(alice.Actor)
	cs, err := p.engine.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := p.jobs.Wait(wctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	p.publish(t, "web.site.failed", events.SeverityError, store.ServerScope())
	waitFor(t, "routed to both", func() bool {
		return len(p.deliveries(t, DeliveryFilter{Person: alice.ID})) == 1 && len(r.requests()) == 1
	})
	if body := string(r.requests()[0].body); !strings.Contains(body, "Ereignis: web.site.failed") {
		t.Errorf("the channel's message is in German: %s", body)
	}
	d, err := p.svc.Notification(ctx, p.deliveries(t, DeliveryFilter{Person: alice.ID})[0].ID)
	if err != nil || d.Message == nil || d.Message.Locale != "de" || !strings.Contains(d.Message.Body, "Ereignis: web.site.failed") {
		t.Errorf("alice's message is in German: %+v, %v", d, err)
	}
	// So is one sent to her alone.
	sent, err := p.svc.Send(ctx, Recipient{Person: alice.ID}, Message{Title: "Neue Anmeldung", Severity: events.SeverityNotice})
	if err != nil || sent.Message.Locale != "de" {
		t.Errorf("a message for alice alone = %+v, %v", sent, err)
	}
}
