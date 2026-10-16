package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The channel types built into the core.
func init() {
	RegisterType(Type{
		Name: "email", Title: "Email",
		Description: "Sends email through the mail relay set up in the notification settings.",
		Settings:    EmailSettings{}, New: newEmail,
	})
	RegisterType(Type{
		Name: "webhook", Title: "Webhook",
		Description: "Posts each notification as JSON to a URL, signed the way Standard Webhooks describes.",
		Settings:    WebhookSettings{}, Secrets: []string{"secret"}, New: newWebhook,
	})
	RegisterType(Type{
		Name: "matrix", Title: "Matrix",
		Description: "Posts to a Matrix room as a bot account. Invite the account to the room first.",
		Settings:    MatrixSettings{}, Secrets: []string{"accessToken"}, New: newMatrix,
	})
	RegisterType(Type{
		Name: "ntfy", Title: "ntfy",
		Description: "Pushes to an ntfy topic, on ntfy.sh or your own server, for the ntfy app on phones and desktops.",
		Settings:    NtfySettings{}, Secrets: []string{"token", "password"}, New: newNtfy,
	})
	RegisterType(Type{
		Name: "telegram", Title: "Telegram",
		Description: "Sends messages from a Telegram bot to a chat, group or channel the bot was added to.",
		Settings:    TelegramSettings{}, Secrets: []string{"botToken"}, New: newTelegram,
	})
	RegisterType(Type{
		Name: "discord", Title: "Discord",
		Description: "Posts to a Discord channel through one of its webhooks.",
		Settings:    DiscordSettings{}, Secrets: []string{"webhookUrl"}, New: newDiscord,
	})
	RegisterType(Type{
		Name: "slack", Title: "Slack",
		Description: "Posts to a Slack channel through an incoming webhook.",
		Settings:    SlackSettings{}, Secrets: []string{"webhookUrl"}, New: newSlack,
	})
}

// SecretFields are the settings fields that hold secrets, across every
// channel type; channels keep references to secrets in them.
var SecretFields = []string{"accessToken", "botToken", "password", "secret", "token", "webhookUrl"}

// decode reads a channel's settings strictly, so a misspelt field is an
// error rather than a setting silently left out.
func decode[T interface{ check() []i18n.FieldError }](channel string, raw json.RawMessage) (T, error) {
	var s T
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, ErrSettings.New("channel", channel, "reason", strings.TrimPrefix(err.Error(), "json: "))
	}
	if fields := s.check(); len(fields) > 0 {
		for i := range fields {
			fields[i].Path = "spec.settings." + fields[i].Path
		}
		return s, i18n.Invalid(fields...)
	}
	return s, nil
}

// resolve returns the value of a secret setting.
func (env Env) resolve(ctx context.Context, field, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if env.Secrets == nil {
		return "", fmt.Errorf("the %s setting can't be read here", field)
	}
	v, err := env.Secrets.Get(ctx, secrets.Ref(ref))
	if err != nil {
		return "", fmt.Errorf("reading the %s setting: %w", field, err)
	}
	return string(v), nil
}

func (env Env) client() *http.Client {
	if env.HTTP != nil {
		return env.HTTP
	}
	return Guard{}.Client()
}

func (env Env) brand() string {
	if env.BrandName != "" {
		return env.BrandName
	}
	return "Racksail"
}

// maxAnswer is how much of a refusal is kept to explain it.
const maxAnswer = 512

// send makes one request and accepts only a 2xx answer. Errors leave the
// URL out, since some carry a token in their path.
func (env Env) send(ctx context.Context, method, target string, header http.Header, body any) error {
	var data []byte
	switch b := body.(type) {
	case []byte:
		data = b
	default:
		var err error
		if data, err = json.Marshal(body); err != nil {
			return err
		}
		if header.Get("Content-Type") == "" {
			if header == nil {
				header = http.Header{}
			}
			header.Set("Content-Type", "application/json")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("the address isn't valid: %w", err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", env.brand()+" notifications")
	resp, err := env.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if resp.StatusCode/100 != 2 {
		text := strings.TrimSpace(string(answer))
		if !utf8.ValidString(text) {
			text = ""
		}
		if text != "" {
			return fmt.Errorf("the server answered %s: %s", resp.Status, text)
		}
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	return nil
}

func checkURL(field, raw string, required bool) []i18n.FieldError {
	if raw == "" {
		if required {
			return []i18n.FieldError{i18n.Field(field, i18n.ErrRequired)}
		}
		return nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return []i18n.FieldError{i18n.Field(field, i18n.ErrBadValue, "reason", "use an http or https URL with a host name")}
	case u.User != nil:
		return []i18n.FieldError{i18n.Field(field, i18n.ErrBadValue, "reason", "a URL can't carry a user name or password")}
	}
	return nil
}

// truncate shortens s to at most n characters, marking the cut.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// EmailSettings are an email channel's.
type EmailSettings struct {
	To []string `json:"to" minItems:"1" maxItems:"20" doc:"The addresses to send to" example:"[\"ops@example.com\"]"`
}

func (s EmailSettings) check() []i18n.FieldError {
	if len(s.To) == 0 {
		return []i18n.FieldError{i18n.Field("to", i18n.ErrRequired)}
	}
	var fields []i18n.FieldError
	for i, a := range s.To {
		if _, err := mail.ParseAddress(a); err != nil {
			fields = append(fields, i18n.Field(fmt.Sprintf("to[%d]", i), i18n.ErrBadValue, "reason", "not an email address"))
		}
	}
	return fields
}

type emailChannel struct {
	settings EmailSettings
	env      Env
}

func newEmail(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[EmailSettings]("email", raw)
	if err != nil {
		return nil, err
	}
	return &emailChannel{settings: s, env: env}, nil
}

func (c *emailChannel) Type() string { return "email" }

func (c *emailChannel) Send(ctx context.Context, msg Message) error {
	return SendEmail(ctx, c.env, c.settings.To, msg)
}

// SendEmail sends a message by email to the given addresses, through the
// relay and dressed by the theme.
func SendEmail(ctx context.Context, env Env, to []string, msg Message) error {
	if env.Relay == nil {
		return ErrNoRelay.New()
	}
	relay, err := env.Relay(ctx)
	if err != nil {
		return err
	}
	if relay == nil {
		return ErrNoRelay.New()
	}
	body, err := HTML(ctx, msg, env)
	if err != nil {
		return fmt.Errorf("rendering the email: %w", err)
	}
	headers := map[string]string{}
	if msg.Event != nil {
		headers["X-Racksail-Event"] = msg.Event.Type
	}
	return relay.Send(ctx, Email{To: to, Subject: Subject(msg), Text: Text(msg), HTML: body, Headers: headers})
}

// WebhookSettings are a webhook channel's.
type WebhookSettings struct {
	URL string `json:"url" format:"uri" maxLength:"2048" doc:"Where notifications are posted" example:"https://hooks.example.com/alerts"`
	// Secret signs every post; receivers check it with any Standard
	// Webhooks library.
	Secret string `json:"secret,omitempty" doc:"The signing secret; send its value in the change's secrets, as settings.secret"`
}

func (s WebhookSettings) check() []i18n.FieldError { return checkURL("url", s.URL, true) }

type webhookChannel struct {
	settings WebhookSettings
	env      Env
}

func newWebhook(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[WebhookSettings]("webhook", raw)
	if err != nil {
		return nil, err
	}
	return &webhookChannel{settings: s, env: env}, nil
}

func (c *webhookChannel) Type() string { return "webhook" }

// WebhookBody is what a webhook channel posts.
type WebhookBody struct {
	Type        string          `json:"type" doc:"Always notification"`
	ID          string          `json:"id,omitempty" doc:"Also sent as the webhook-id header, the same on every attempt"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Severity    events.Severity `json:"severity"`
	URL         string          `json:"url,omitempty"`
	ActionLabel string          `json:"actionLabel,omitempty"`
	// Event is the event as webhooks send it, when the message is about one.
	Event  *webhook.WebhookPayload `json:"event,omitempty"`
	Digest []Message               `json:"digest,omitempty"`
}

func (c *webhookChannel) Send(ctx context.Context, msg Message) error {
	body := WebhookBody{
		Type: "notification", ID: msg.ID, Title: msg.Title, Body: msg.Body, Severity: msg.Severity,
		URL: msg.URL, ActionLabel: msg.ActionLabel, Digest: stripEvents(msg.Digest),
	}
	if msg.Event != nil {
		p := webhook.PayloadOf(*msg.Event)
		body.Event = &p
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	header := http.Header{"Content-Type": {"application/json"}}
	if c.settings.Secret != "" {
		secret, err := c.env.resolve(ctx, "secret", c.settings.Secret)
		if err != nil {
			return err
		}
		id := store.ID(msg.ID)
		if id == "" {
			id = store.NewID(DeliveryPrefix)
		}
		now := time.Now()
		header.Set("webhook-id", string(id))
		header.Set("webhook-timestamp", fmt.Sprint(now.Unix()))
		header.Set("webhook-signature", webhook.Sign([]byte(secret), id, now, data))
	}
	return c.env.send(ctx, http.MethodPost, c.settings.URL, header, data)
}

// stripEvents leaves the events out of a digest's messages, which say
// enough without them.
func stripEvents(msgs []Message) []Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		m.Event = nil
		out[i] = m
	}
	return out
}

// MatrixSettings are a Matrix channel's.
type MatrixSettings struct {
	Homeserver string `json:"homeserver" format:"uri" doc:"The bot account's homeserver" example:"https://matrix.example.org"`
	// Room is a room ID or an alias.
	Room string `json:"room" doc:"The room's ID or alias" example:"#ops:example.org"`
	// AccessToken is the bot account's.
	AccessToken string `json:"accessToken" doc:"The bot account's access token; send its value in the change's secrets, as settings.accessToken"`
}

var matrixRoom = regexp.MustCompile(`^[!#][^:\s]+:[A-Za-z0-9.\-]+(:[0-9]+)?$`)

func (s MatrixSettings) check() []i18n.FieldError {
	fields := checkURL("homeserver", s.Homeserver, true)
	if !matrixRoom.MatchString(s.Room) {
		fields = append(fields, i18n.Field("room", i18n.ErrBadValue, "reason", "use a room ID such as !abc:example.org or an alias such as #ops:example.org"))
	}
	if s.AccessToken == "" {
		fields = append(fields, i18n.Field("accessToken", i18n.ErrRequired))
	}
	return fields
}

type matrixChannel struct {
	settings MatrixSettings
	env      Env
}

func newMatrix(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[MatrixSettings]("matrix", raw)
	if err != nil {
		return nil, err
	}
	s.Homeserver = strings.TrimRight(s.Homeserver, "/")
	return &matrixChannel{settings: s, env: env}, nil
}

func (c *matrixChannel) Type() string { return "matrix" }

func (c *matrixChannel) Send(ctx context.Context, msg Message) error {
	token, err := c.env.resolve(ctx, "accessToken", c.settings.AccessToken)
	if err != nil {
		return err
	}
	header := http.Header{"Authorization": {"Bearer " + token}}
	room := c.settings.Room
	if strings.HasPrefix(room, "#") {
		if room, err = c.resolveAlias(ctx, token); err != nil {
			return err
		}
	}
	text := Subject(msg) + "\n\n" + Text(msg)
	formatted := "<strong>" + html.EscapeString(Subject(msg)) + "</strong><br><br>" + strings.ReplaceAll(html.EscapeString(Text(msg)), "\n", "<br>")
	txn := msg.ID
	if txn == "" {
		txn = string(store.NewID(DeliveryPrefix))
	}
	// The transaction ID makes a retried send land once.
	target := c.settings.Homeserver + "/_matrix/client/v3/rooms/" + url.PathEscape(room) + "/send/m.room.message/" + url.PathEscape(txn)
	return c.env.send(ctx, http.MethodPut, target, header, map[string]string{
		"msgtype": "m.notice", "body": text, "format": "org.matrix.custom.html", "formatted_body": formatted,
	})
}

func (c *matrixChannel) resolveAlias(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.settings.Homeserver+"/_matrix/client/v3/directory/room/"+url.PathEscape(c.settings.Room), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.env.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		RoomID string `json:"room_id"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out) != nil || out.RoomID == "" {
		return "", fmt.Errorf("the homeserver doesn't know the room %s (it answered %s)", c.settings.Room, resp.Status)
	}
	return out.RoomID, nil
}

// NtfySettings are an ntfy channel's.
type NtfySettings struct {
	Server string `json:"server,omitempty" format:"uri" doc:"The ntfy server; ntfy.sh unless you run your own" example:"https://ntfy.sh"`
	Topic  string `json:"topic" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"The topic to publish to; on ntfy.sh, anyone who knows it can read it, so make it hard to guess"`
	// Token or Username and Password sign in to servers that need it.
	Token    string `json:"token,omitempty" doc:"An access token; send its value in the change's secrets, as settings.token"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty" doc:"Send its value in the change's secrets, as settings.password"`
}

var ntfyTopic = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s NtfySettings) check() []i18n.FieldError {
	fields := checkURL("server", s.Server, false)
	if !ntfyTopic.MatchString(s.Topic) {
		fields = append(fields, i18n.Field("topic", i18n.ErrBadValue, "reason", "use up to 64 letters, digits, hyphens and underscores"))
	}
	if s.Token != "" && s.Username != "" {
		fields = append(fields, i18n.Field("username", i18n.ErrBadValue, "reason", "use a token or a user name and password, not both"))
	}
	if (s.Username == "") != (s.Password == "") {
		fields = append(fields, i18n.Field("password", i18n.ErrBadValue, "reason", "a user name needs a password, and a password a user name"))
	}
	return fields
}

type ntfyChannel struct {
	settings NtfySettings
	env      Env
}

func newNtfy(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[NtfySettings]("ntfy", raw)
	if err != nil {
		return nil, err
	}
	if s.Server == "" {
		s.Server = "https://ntfy.sh"
	}
	s.Server = strings.TrimRight(s.Server, "/")
	return &ntfyChannel{settings: s, env: env}, nil
}

func (c *ntfyChannel) Type() string { return "ntfy" }

func (c *ntfyChannel) Send(ctx context.Context, msg Message) error {
	header := http.Header{}
	switch {
	case c.settings.Token != "":
		token, err := c.env.resolve(ctx, "token", c.settings.Token)
		if err != nil {
			return err
		}
		header.Set("Authorization", "Bearer "+token)
	case c.settings.Username != "":
		password, err := c.env.resolve(ctx, "password", c.settings.Password)
		if err != nil {
			return err
		}
		req := &http.Request{Header: http.Header{}}
		req.SetBasicAuth(c.settings.Username, password)
		header.Set("Authorization", req.Header.Get("Authorization"))
	}
	// Publishing as JSON keeps titles in any language intact, which
	// headers can't carry.
	body := map[string]any{
		"topic": c.settings.Topic, "title": Subject(msg), "message": truncate(Text(msg), 4000),
		"priority": priority(msg.Severity), "tags": []string{string(msg.Severity)},
	}
	if msg.URL != "" {
		body["click"] = msg.URL
	}
	return c.env.send(ctx, http.MethodPost, c.settings.Server, header, body)
}

// TelegramSettings are a Telegram channel's.
type TelegramSettings struct {
	// BotToken is what @BotFather gave the bot.
	BotToken string `json:"botToken" doc:"The bot's token from @BotFather; send its value in the change's secrets, as settings.botToken"`
	ChatID   string `json:"chatId" doc:"The chat's ID, or @name for a public channel" example:"-1001234567890"`
}

var telegramChat = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

func (s TelegramSettings) check() []i18n.FieldError {
	var fields []i18n.FieldError
	if s.BotToken == "" {
		fields = append(fields, i18n.Field("botToken", i18n.ErrRequired))
	}
	if !telegramChat.MatchString(s.ChatID) {
		fields = append(fields, i18n.Field("chatId", i18n.ErrBadValue, "reason", "use the chat's number, or @name for a public channel"))
	}
	return fields
}

// TelegramAPI is Telegram's Bot API.
const TelegramAPI = "https://api.telegram.org"

func (env Env) telegramAPI() string {
	if env.TelegramAPI != "" {
		return strings.TrimSuffix(env.TelegramAPI, "/")
	}
	return TelegramAPI
}

var telegramToken = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,}$`)

type telegramChannel struct {
	settings TelegramSettings
	env      Env
}

func newTelegram(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[TelegramSettings]("telegram", raw)
	if err != nil {
		return nil, err
	}
	return &telegramChannel{settings: s, env: env}, nil
}

func (c *telegramChannel) Type() string { return "telegram" }

func (c *telegramChannel) Send(ctx context.Context, msg Message) error {
	token, err := c.env.resolve(ctx, "botToken", c.settings.BotToken)
	if err != nil {
		return err
	}
	// The token goes in the path, so a malformed one could change where
	// the request goes.
	if !telegramToken.MatchString(token) {
		return errors.New("the bot token doesn't look like one @BotFather gives")
	}
	text := "<b>" + html.EscapeString(Subject(msg)) + "</b>\n\n" + html.EscapeString(truncate(Text(msg), 3800))
	return c.env.send(ctx, http.MethodPost, c.env.telegramAPI()+"/bot"+token+"/sendMessage", nil, map[string]any{
		"chat_id": c.settings.ChatID, "text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
}

// DiscordSettings are a Discord channel's.
type DiscordSettings struct {
	// WebhookURL is a secret: whoever has it can post to the channel.
	WebhookURL string `json:"webhookUrl" doc:"The channel's webhook URL; send its value in the change's secrets, as settings.webhookUrl"`
}

func (s DiscordSettings) check() []i18n.FieldError {
	if s.WebhookURL == "" {
		return []i18n.FieldError{i18n.Field("webhookUrl", i18n.ErrRequired)}
	}
	return nil
}

type discordChannel struct {
	settings DiscordSettings
	env      Env
}

func newDiscord(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[DiscordSettings]("discord", raw)
	if err != nil {
		return nil, err
	}
	return &discordChannel{settings: s, env: env}, nil
}

func (c *discordChannel) Type() string { return "discord" }

func (c *discordChannel) Send(ctx context.Context, msg Message) error {
	target, err := c.env.resolve(ctx, "webhookUrl", c.settings.WebhookURL)
	if err != nil {
		return err
	}
	if fields := checkURL("webhookUrl", target, true); len(fields) > 0 {
		return errors.New("the webhook URL isn't an http or https URL")
	}
	embed := map[string]any{
		"title": truncate(Subject(msg), 256), "description": truncate(Text(msg), 4000),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	// The embed's bar takes the theme's color for how urgent the message
	// is; a theme that can't be read only costs the color.
	if rgb, err := statusColor(ctx, c.env, msg.Severity); err == nil {
		embed["color"] = rgb
	}
	if msg.URL != "" {
		embed["url"] = msg.URL
	}
	return c.env.send(ctx, http.MethodPost, target, nil, map[string]any{
		"username": c.env.brand(), "embeds": []any{embed},
		// Nothing in a notification pings anyone.
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
}

// SlackSettings are a Slack channel's.
type SlackSettings struct {
	// WebhookURL is a secret: whoever has it can post to the channel.
	WebhookURL string `json:"webhookUrl" doc:"The incoming webhook's URL; send its value in the change's secrets, as settings.webhookUrl"`
}

func (s SlackSettings) check() []i18n.FieldError {
	if s.WebhookURL == "" {
		return []i18n.FieldError{i18n.Field("webhookUrl", i18n.ErrRequired)}
	}
	return nil
}

type slackChannel struct {
	settings SlackSettings
	env      Env
}

func newSlack(raw json.RawMessage, env Env) (Channel, error) {
	s, err := decode[SlackSettings]("slack", raw)
	if err != nil {
		return nil, err
	}
	return &slackChannel{settings: s, env: env}, nil
}

func (c *slackChannel) Type() string { return "slack" }

// slackEscape escapes what Slack's mrkdwn treats as markup.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (c *slackChannel) Send(ctx context.Context, msg Message) error {
	target, err := c.env.resolve(ctx, "webhookUrl", c.settings.WebhookURL)
	if err != nil {
		return err
	}
	if fields := checkURL("webhookUrl", target, true); len(fields) > 0 {
		return errors.New("the webhook URL isn't an http or https URL")
	}
	text := "*" + slackEscape.Replace(Subject(msg)) + "*\n" + slackEscape.Replace(truncate(Text(msg), 2900))
	return c.env.send(ctx, http.MethodPost, target, nil, map[string]any{
		"text": Subject(msg),
		"blocks": []any{map[string]any{
			"type": "section", "text": map[string]string{"type": "mrkdwn", "text": text},
		}},
	})
}
