package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func newVault() *secrets.Memory {
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	return secrets.NewMemory(box)
}

// request is what a fake receiver got.
type request struct {
	method, path string
	header       http.Header
	body         []byte
}

// receiver answers every request with status and keeps what it got.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []request
	status int
	answer string
}

func newReceiver(t *testing.T, status int, answer string) *receiver {
	r := &receiver{status: status, answer: answer}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, request{req.Method, req.URL.EscapedPath(), req.Header.Clone(), body})
		status, answer := r.status, r.answer
		r.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) requests() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]request(nil), r.got...)
}

func (r *receiver) set(status int, answer string) {
	r.mu.Lock()
	r.status, r.answer = status, answer
	r.mu.Unlock()
}

// owner is the channel the test secrets belong to.
var owner = store.NewID("nch")

func secret(t *testing.T, vault *secrets.Memory, name, value string) string {
	t.Helper()
	ref := secrets.NewRef(owner, name)
	if err := vault.Put(context.Background(), ref, []byte(value)); err != nil {
		t.Fatal(err)
	}
	return string(ref)
}

func settings(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

var alert = Message{
	ID: "ntf_01jtestmessage0000000000000", Title: "Backup failed", Body: "The nightly backup of shop.example.com failed.",
	Severity: events.SeverityError, URL: "https://panel.example.com/backups", ActionLabel: "Look at the backup",
}

func TestWebhookChannel(t *testing.T) {
	ctx := context.Background()
	vault := newVault()
	r := newReceiver(t, http.StatusNoContent, "")
	ev := events.Event{ID: store.NewID("evt"), Type: "backups.run.failed", Severity: events.SeverityError, Time: time.Now(), Message: "Backup failed"}
	msg := alert
	msg.Event = &ev
	ch, err := New("webhook", settings(WebhookSettings{URL: r.srv.URL + "/alerts", Secret: secret(t, vault, "settings.secret", "s3cret")}), Env{Secrets: vault, HTTP: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	got := r.requests()
	if len(got) != 1 || got[0].method != http.MethodPost || got[0].path != "/alerts" {
		t.Fatalf("requests = %+v", got)
	}
	h := got[0].header
	if h.Get("webhook-id") != alert.ID {
		t.Errorf("the webhook-id is the message's, the same on every attempt: %q", h.Get("webhook-id"))
	}
	if !webhook.Verify([]byte("s3cret"), store.ID(h.Get("webhook-id")), h.Get("webhook-timestamp"), h.Get("webhook-signature"), got[0].body, time.Now(), time.Minute) {
		t.Error("the post is signed the way Standard Webhooks describes")
	}
	var body WebhookBody
	if err := json.Unmarshal(got[0].body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Type != "notification" || body.Title != "Backup failed" || body.Event == nil || body.Event.Type != "backups.run.failed" {
		t.Errorf("body = %+v", body)
	}
	r.set(http.StatusInternalServerError, "database down")
	if err := ch.Send(ctx, msg); err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "database down") {
		t.Errorf("a refusal says how the receiver answered: %v", err)
	}
}

func TestMatrixChannel(t *testing.T) {
	ctx := context.Background()
	vault := newVault()
	var mu sync.Mutex
	var sent []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if req.Header.Get("Authorization") != "Bearer syt_bot_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		sent = append(sent, request{req.Method, req.URL.EscapedPath(), req.Header.Clone(), body})
		mu.Unlock()
		if strings.HasPrefix(req.URL.Path, "/_matrix/client/v3/directory/room/") {
			io.WriteString(w, `{"room_id":"!abc:example.org"}`)
			return
		}
		io.WriteString(w, `{"event_id":"$1"}`)
	}))
	defer srv.Close()
	ch, err := New("matrix", settings(MatrixSettings{Homeserver: srv.URL + "/", Room: "#ops:example.org", AccessToken: secret(t, vault, "settings.access-token", "syt_bot_token")}),
		Env{Secrets: vault, HTTP: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, alert); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0].method != http.MethodGet || sent[1].method != http.MethodPut {
		t.Fatalf("an alias is resolved, then the message is put: %+v", sent)
	}
	if want := "/_matrix/client/v3/rooms/%21abc:example.org/send/m.room.message/" + alert.ID; sent[1].path != want {
		t.Errorf("path = %s, want %s: the transaction ID is the message's, so a retry lands once", sent[1].path, want)
	}
	var body map[string]string
	json.Unmarshal(sent[1].body, &body)
	if body["msgtype"] != "m.notice" || !strings.Contains(body["formatted_body"], "<strong>Error: Backup failed</strong>") {
		t.Errorf("body = %v", body)
	}
	if _, err := New("matrix", settings(MatrixSettings{Homeserver: srv.URL, Room: "ops"}), Env{}); err == nil {
		t.Error("a room that is neither an ID nor an alias, and no token, is refused")
	}
}

func TestNtfyChannel(t *testing.T) {
	ctx := context.Background()
	vault := newVault()
	r := newReceiver(t, http.StatusOK, `{"id":"x"}`)
	ch, err := New("ntfy", settings(NtfySettings{Server: r.srv.URL, Topic: "racksail-alerts_7f3", Username: "panel", Password: secret(t, vault, "settings.password", "pw")}),
		Env{Secrets: vault, HTTP: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(ctx, alert); err != nil {
		t.Fatal(err)
	}
	got := r.requests()
	if len(got) != 1 || got[0].path != "/" {
		t.Fatalf("ntfy is published to as JSON at the server's root: %+v", got)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("panel:pw")); got[0].header.Get("Authorization") != want {
		t.Errorf("Authorization = %q", got[0].header.Get("Authorization"))
	}
	var body map[string]any
	json.Unmarshal(got[0].body, &body)
	if body["topic"] != "racksail-alerts_7f3" || body["priority"] != float64(4) || body["click"] != alert.URL || body["title"] != "Error: Backup failed" {
		t.Errorf("body = %v", body)
	}
	for name, s := range map[string]NtfySettings{
		"bad topic":            {Topic: "no spaces allowed"},
		"token and user":       {Topic: "t", Token: "ref", Username: "u", Password: "ref"},
		"user without a token": {Topic: "t", Username: "u"},
	} {
		if _, err := New("ntfy", settings(s), Env{}); err == nil {
			t.Errorf("%s: settings accepted", name)
		}
	}
}

func TestTelegramChannel(t *testing.T) {
	ctx := context.Background()
	vault := newVault()
	r := newReceiver(t, http.StatusOK, `{"ok":true}`)
	const token = "123456:AAH-abcdefghijklmnopqrstuvwxyz"
	env := Env{Secrets: vault, HTTP: http.DefaultClient, TelegramAPI: r.srv.URL}
	ch, err := New("telegram", settings(TelegramSettings{BotToken: secret(t, vault, "settings.bot-token", token), ChatID: "-1001234567890"}), env)
	if err != nil {
		t.Fatal(err)
	}
	msg := alert
	msg.Body = "Disk <sdb> & friends"
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	got := r.requests()
	if len(got) != 1 || got[0].path != "/bot"+token+"/sendMessage" {
		t.Fatalf("requests = %+v", got)
	}
	var body map[string]any
	json.Unmarshal(got[0].body, &body)
	if body["chat_id"] != "-1001234567890" || body["parse_mode"] != "HTML" || !strings.Contains(body["text"].(string), "Disk &lt;sdb&gt; &amp; friends") {
		t.Errorf("text is escaped for Telegram's HTML: %v", body)
	}
	bad, _ := New("telegram", settings(TelegramSettings{BotToken: secret(t, vault, "settings.bot-token.2", "../../evil"), ChatID: "42"}), env)
	if err := bad.Send(ctx, alert); err == nil || len(r.requests()) != 1 {
		t.Error("a token that could change where the request goes is refused before sending")
	}
	r.set(http.StatusBadRequest, `{"ok":false,"description":"Bad Request: chat not found"}`)
	err = ch.Send(ctx, alert)
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("errors say what Telegram answered and never carry the token: %v", err)
	}
}

func TestDiscordAndSlackChannels(t *testing.T) {
	ctx := context.Background()
	vault := newVault()
	r := newReceiver(t, http.StatusNoContent, "")
	discord, err := New("discord", settings(DiscordSettings{WebhookURL: secret(t, vault, "settings.webhook-url", r.srv.URL+"/api/webhooks/1/token")}), Env{Secrets: vault, HTTP: http.DefaultClient, BrandName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := discord.Send(ctx, alert); err != nil {
		t.Fatal(err)
	}
	slack, err := New("slack", settings(SlackSettings{WebhookURL: secret(t, vault, "settings.webhook-url.slack", r.srv.URL+"/services/T/B/X")}), Env{Secrets: vault, HTTP: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	msg := alert
	msg.Title = "<!channel> look"
	if err := slack.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	got := r.requests()
	if len(got) != 2 || got[0].path != "/api/webhooks/1/token" || got[1].path != "/services/T/B/X" {
		t.Fatalf("requests = %+v", got)
	}
	var d struct {
		Username string `json:"username"`
		Embeds   []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Color int    `json:"color"`
		} `json:"embeds"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	json.Unmarshal(got[0].body, &d)
	if d.Username != "Acme" || len(d.Embeds) != 1 || d.Embeds[0].URL != alert.URL || d.Embeds[0].Color != 0xD1242F || d.AllowedMentions.Parse == nil {
		t.Errorf("discord body = %s", got[0].body)
	}
	var sl struct {
		Blocks []struct {
			Text struct{ Text string } `json:"text"`
		} `json:"blocks"`
	}
	json.Unmarshal(got[1].body, &sl)
	if len(sl.Blocks) != 1 || !strings.Contains(sl.Blocks[0].Text.Text, "&lt;!channel&gt;") {
		t.Errorf("slack markup in a title is escaped: %s", got[1].body)
	}
}

func TestEmailChannelWithoutRelay(t *testing.T) {
	ch, err := New("email", settings(EmailSettings{To: []string{"ops@example.com"}}), Env{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), alert); !errors.Is(err, ErrNoRelay) {
		t.Errorf("email waits for a relay: %v", err)
	}
	_, err = New("email", settings(EmailSettings{To: []string{"not an address"}}), Env{})
	if e := i18n.From(err); len(e.Fields) != 1 || e.Fields[0].Path != "spec.settings.to[0]" {
		t.Errorf("a bad address is pointed at: %v", err)
	}
	if _, err := New("email", json.RawMessage(`{"to":["a@example.com"],"cc":["b@example.com"]}`), Env{}); !errors.Is(err, ErrSettings) {
		t.Errorf("a misspelt or unknown setting is refused: %v", err)
	}
}

func TestGuard(t *testing.T) {
	g := Guard{Allow: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.0.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "10.20.1.1": true,
	} {
		if got := g.Allowed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", addr, got, want)
		}
	}
	r := newReceiver(t, http.StatusOK, "")
	ch, _ := New("webhook", settings(WebhookSettings{URL: r.srv.URL}), Env{HTTP: Guard{}.Client()})
	err := ch.Send(context.Background(), alert)
	if err == nil || !strings.Contains(err.Error(), "isn't a public address") || len(r.requests()) != 0 {
		t.Errorf("notifications don't reach the server's own addresses: %v", err)
	}
}
