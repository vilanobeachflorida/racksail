package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// QuietHours hold back notifications that can wait. What arrives during
// them is sent as one summary when they end, unless it is urgent enough to
// be sent at once.
type QuietHours struct {
	Start string `json:"start" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" doc:"When quiet hours start, as HH:MM" example:"22:00"`
	End   string `json:"end" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" doc:"When they end, as HH:MM; earlier than the start means the next day" example:"07:00"`
	// TimeZone is an IANA name; the server's own when empty.
	TimeZone string `json:"timeZone,omitempty" maxLength:"64" doc:"The time zone the times are in, such as Europe/Madrid; the server's when empty"`
	// Days are the days quiet hours start on; every day when empty.
	Days []string `json:"days,omitempty" maxItems:"7" doc:"The days quiet hours start on, as mon to sun; every day when empty"`
	// Urgent is the least urgent severity sent at once anyway.
	Urgent events.Severity `json:"urgent,omitempty" enum:"info,notice,warning,error,critical" doc:"Notifications at least this urgent are sent at once anyway; critical when empty"`
	// Drop leaves out what arrives during quiet hours instead of sending a
	// summary when they end.
	Drop bool `json:"drop,omitempty" doc:"Leave out what arrives during quiet hours instead of sending a summary when they end"`
}

var clockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func minutes(hhmm string) int {
	m := clockTime.FindStringSubmatch(hhmm)
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	return h*60 + mm
}

// check returns what is wrong with quiet hours, with paths under prefix.
func (q *QuietHours) check(prefix string) []i18n.FieldError {
	var fields []i18n.FieldError
	start, end := minutes(q.Start), minutes(q.End)
	if start < 0 {
		fields = append(fields, i18n.Field(prefix+".start", i18n.ErrBadValue, "reason", "use a time such as 22:00"))
	}
	if end < 0 {
		fields = append(fields, i18n.Field(prefix+".end", i18n.ErrBadValue, "reason", "use a time such as 07:00"))
	}
	if start >= 0 && start == end {
		fields = append(fields, i18n.Field(prefix+".end", i18n.ErrBadValue, "reason", "quiet hours have to end at another time than they start"))
	}
	if q.TimeZone != "" {
		if _, err := time.LoadLocation(q.TimeZone); err != nil || q.TimeZone == "Local" {
			fields = append(fields, i18n.Field(prefix+".timeZone", i18n.ErrBadValue, "reason", "use a time zone name such as Europe/Madrid"))
		}
	}
	for i, d := range q.Days {
		if !slices.Contains(weekdays, d) {
			fields = append(fields, i18n.Field(fmt.Sprintf("%s.days[%d]", prefix, i), i18n.ErrBadValue, "reason", "use mon, tue, wed, thu, fri, sat or sun"))
		}
	}
	if q.Urgent != "" && !q.Urgent.Valid() {
		fields = append(fields, i18n.Field(prefix+".urgent", i18n.ErrBadValue, "reason", "use info, notice, warning, error or critical"))
	}
	return fields
}

func (q *QuietHours) location() *time.Location {
	if q.TimeZone != "" {
		if loc, err := time.LoadLocation(q.TimeZone); err == nil {
			return loc
		}
	}
	return time.Local
}

func (q *QuietHours) startsOn(day time.Weekday) bool {
	return len(q.Days) == 0 || slices.Contains(q.Days, weekdays[day])
}

// Holds reports whether a notification of severity sev is held back at
// now, and until when.
func (q *QuietHours) Holds(sev events.Severity, now time.Time) (bool, time.Time) {
	if q == nil {
		return false, time.Time{}
	}
	urgent := q.Urgent
	if urgent == "" {
		urgent = events.SeverityCritical
	}
	if sev.AtLeast(urgent) {
		return false, time.Time{}
	}
	return q.Quiet(now)
}

// Quiet reports whether now falls in quiet hours, and when they end.
func (q *QuietHours) Quiet(now time.Time) (bool, time.Time) {
	start, end := minutes(q.Start), minutes(q.End)
	if start < 0 || end < 0 || start == end {
		return false, time.Time{}
	}
	t := now.In(q.location())
	m := t.Hour()*60 + t.Minute()
	at := func(day time.Time, min int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), min/60, min%60, 0, 0, day.Location())
	}
	if start < end {
		if m >= start && m < end && q.startsOn(t.Weekday()) {
			return true, at(t, end)
		}
		return false, time.Time{}
	}
	// Quiet hours that run past midnight started either today or yesterday.
	if m >= start && q.startsOn(t.Weekday()) {
		return true, at(t.AddDate(0, 0, 1), end)
	}
	if yesterday := t.AddDate(0, 0, -1); m < end && q.startsOn(yesterday.Weekday()) {
		return true, at(t, end)
	}
	return false, time.Time{}
}

// ChannelSpec is a notification channel: where notifications go, and which.
type ChannelSpec struct {
	Name string `json:"name" minLength:"1" maxLength:"80" doc:"What the channel is called in the panel" example:"Ops on Matrix"`
	// Type is a channel type built into the panel, or a notifier a module
	// or add-on provides.
	Type     string          `json:"type" minLength:"1" maxLength:"64" doc:"email, webhook, matrix, ntfy, telegram, discord, slack, or a notifier a module or add-on provides" example:"ntfy"`
	Settings json.RawMessage `json:"settings,omitempty" doc:"The channel type's settings; secret settings hold references, and their values are sent in the change's secrets as settings.<name>"`
	// Events are the event types to send, as glob patterns.
	Events []string `json:"events" minItems:"1" maxItems:"64" doc:"Event types to send, as patterns such as *.failed or backups.*; * sends every event" example:"[\"*.failed\",\"core.safe_mode.*\"]"`
	// MinSeverity leaves out events less urgent than it.
	MinSeverity events.Severity `json:"minSeverity,omitempty" enum:"info,notice,warning,error,critical" doc:"Only send events at least this urgent"`
	// Owner limits the channel to events about one reseller's or account's
	// resources.
	Owner      *store.Scope `json:"owner,omitempty" doc:"Only send events about the resources of this reseller or account"`
	QuietHours *QuietHours  `json:"quietHours,omitempty" doc:"Hold back what can wait, and send it as one summary when quiet hours end"`
	// Disabled stops notifications without forgetting the channel.
	Disabled bool `json:"disabled,omitempty" doc:"Stops sending until the channel is enabled again"`
	// Locale is the language of the words the panel adds to messages.
	Locale string `json:"locale,omitempty" maxLength:"35" pattern:"^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$" doc:"The language of the panel's own words in the channel's messages, such as de or pt-BR; English when empty"`
}

// ChannelStatus is what the panel last saw of a channel.
type ChannelStatus struct {
	store.Status
	LastSent            *time.Time `json:"lastSent,omitempty" doc:"When a notification last got through"`
	LastError           string     `json:"lastError,omitempty" doc:"Why the last attempt failed"`
	ConsecutiveFailures int        `json:"consecutiveFailures,omitempty" doc:"Attempts in a row that failed"`
}

// ChannelTestOutput is what a test notification found.
type ChannelTestOutput struct {
	Notification *Notification `json:"notification"`
}

// ChannelKind is core.notification_channel.
var ChannelKind = store.Define[ChannelSpec, ChannelStatus](store.KindInfo{
	Name: "core.notification_channel", Prefix: "nch", Scope: store.ScopeServer,
	Title: "Notification channel", Plural: "notification-channels",
	Description: "Sends alerts to email, a webhook, Matrix, ntfy, Telegram, Discord or Slack as events happen. " +
		"Each channel picks the events it wants and how urgent they have to be, and can hold back what can wait during quiet hours.",
	Actions: []store.Action{{
		Name: "test", Title: "Send a test", Summary: "Send a test notification",
		Description: "Sends a test notification through the channel right away and reports what happened. Test notifications ignore quiet hours and aren't tried again.",
		Risk:        store.RiskLow, Output: ChannelTestOutput{},
	}},
	Indexes: []store.Index{{Name: "type", Path: "type"}},
	// A channel sends the panel's events out of the server.
	Risks: store.Risks{Create: store.RiskMedium, Update: store.RiskMedium, Delete: store.RiskLow},
	UI: store.UIHints{
		Icon: "bell", TitleField: "name", SubtitleField: "type",
		Columns: []store.Column{{Field: "spec.type", Format: "badge"}, {Field: "spec.events", Format: "badge"}, {Field: "status.phase", Format: "status"}, {Field: "status.lastSent", Format: "date"}},
		Fields: map[string]store.FieldHint{
			"name": {Order: 0}, "type": {Order: 1}, "settings": {Order: 2, Widget: "code"}, "events": {Order: 3},
			"owner": {Advanced: true}, "quietHours": {Advanced: true},
			"settings.accessToken": {Sensitive: true, Widget: "password"}, "settings.botToken": {Sensitive: true, Widget: "password"},
			"settings.password": {Sensitive: true, Widget: "password"}, "settings.secret": {Sensitive: true, Widget: "password"},
			"settings.token": {Sensitive: true, Widget: "password"}, "settings.webhookUrl": {Sensitive: true, Widget: "password"},
		},
	},
}).WithValidator(validateChannel)

// RelaySpec is the mail relay notifications are emailed through.
type RelaySpec struct {
	Host     string `json:"host" minLength:"1" maxLength:"253" doc:"The mail server email goes out through" example:"smtp.example.com"`
	Port     int    `json:"port,omitempty" minimum:"1" maximum:"65535" doc:"587 for STARTTLS, 465 for TLS and 25 without encryption, unless set"`
	Security string `json:"security,omitempty" enum:"starttls,tls,none" doc:"How the connection is encrypted; starttls when empty"`
	Username string `json:"username,omitempty" maxLength:"254" doc:"The user name to sign in with, if the relay wants one"`
	Password string `json:"password,omitempty" doc:"Send its value in the change's secrets, as password"`
	From     string `json:"from" minLength:"3" maxLength:"254" doc:"The address email comes from" example:"panel@example.com"`
	FromName string `json:"fromName,omitempty" maxLength:"80" doc:"The name shown with it; the panel's brand name when empty"`
	// HELO is the name the panel greets the relay with.
	HELO string `json:"helo,omitempty" maxLength:"253" doc:"The name the panel greets the relay with; localhost when empty"`
}

// RelayStatus is what the panel last saw of the relay.
type RelayStatus struct {
	store.Status
	LastSent  *time.Time `json:"lastSent,omitempty" doc:"When an email last got through"`
	LastError string     `json:"lastError,omitempty" doc:"Why the last email failed"`
}

// RelayTestInput names who gets the test email.
type RelayTestInput struct {
	To     string `json:"to" minLength:"3" maxLength:"254" doc:"Who gets the test email" example:"you@example.com"`
	Locale string `json:"locale,omitempty" maxLength:"35" pattern:"^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$" doc:"The language of the panel's own words in the test email, such as de; English when empty"`
}

// Validate implements the check every action input gets.
func (in RelayTestInput) Validate() error {
	if _, err := mail.ParseAddress(in.To); err != nil {
		return i18n.Invalid(i18n.Field("input.to", i18n.ErrBadValue, "reason", "not an email address"))
	}
	return nil
}

// RelayTestOutput is how the test went.
type RelayTestOutput struct {
	Sent  bool   `json:"sent"`
	Error string `json:"error,omitempty" doc:"Why the relay didn't take the email"`
}

// RelayKind is core.smtp_relay.
var RelayKind = store.Define[RelaySpec, RelayStatus](store.KindInfo{
	Name: "core.smtp_relay", Prefix: "smtp", Scope: store.ScopeServer, Singleton: true,
	Title: "Mail relay", Plural: "mail-relays",
	Description: "The mail server the panel sends email through: email notifications, sign-in alerts and messages to customers. " +
		"Use your provider's submission server, or this server's own when the mail module is on.",
	Actions: []store.Action{{
		Name: "test", Title: "Send a test email", Summary: "Send a test email through the relay",
		Description: "Sends a short email through the relay to the address given and reports what the relay answered.",
		Risk:        store.RiskLow, Input: RelayTestInput{}, Output: RelayTestOutput{},
	}},
	Risks: store.Risks{Create: store.RiskMedium, Update: store.RiskMedium, Delete: store.RiskMedium},
	UI: store.UIHints{
		Icon: "mail", TitleField: "host", SubtitleField: "from", Hidden: true,
		Fields: map[string]store.FieldHint{
			"host": {Order: 0}, "port": {Order: 1}, "security": {Order: 2}, "username": {Order: 3},
			"password": {Order: 4, Sensitive: true, Widget: "password"}, "from": {Order: 5}, "fromName": {Order: 6},
			"helo": {Advanced: true},
		},
	},
}).WithValidator(validateRelay)

func init() {
	store.RegisterKind(ChannelKind)
	store.RegisterKind(RelayKind)
	store.ReservePrefix(DeliveryPrefix, "notification deliveries")
}

// Errors of channels.
var (
	ErrUnknownChannelType = i18n.Define("notify.unknown_channel_type", http.StatusUnprocessableEntity, "There is no channel type or notifier called {type}.")
)

func validateChannel(ctx context.Context, obj, old *store.Object[ChannelSpec, ChannelStatus]) error {
	var fields []i18n.FieldError
	for i, p := range obj.Spec.Events {
		if !events.ValidPattern(p) {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.events[%d]", i), i18n.ErrBadValue, "reason", "use lowercase event type names, dots and the wildcards * and ?"))
		}
	}
	if obj.Spec.MinSeverity != "" && !obj.Spec.MinSeverity.Valid() {
		fields = append(fields, i18n.Field("spec.minSeverity", i18n.ErrBadValue, "reason", "use info, notice, warning, error or critical"))
	}
	if o := obj.Spec.Owner; o != nil {
		if err := o.Validate(); err != nil || o.Level == store.ScopeServer {
			fields = append(fields, i18n.Field("spec.owner", i18n.ErrBadValue, "reason", "name a reseller or an account"))
		}
	}
	if q := obj.Spec.QuietHours; q != nil {
		fields = append(fields, q.check("spec.quietHours")...)
	}
	// Built-in types check their settings here; notifiers modules and
	// add-ons provide are checked when the change is planned, where the
	// registry is.
	if _, ok := LookupType(obj.Spec.Type); ok {
		if _, err := New(obj.Spec.Type, obj.Spec.Settings, Env{}); err != nil {
			if e := i18n.From(err); len(e.Fields) > 0 {
				fields = append(fields, e.Fields...)
			} else {
				fields = append(fields, i18n.Field("spec.settings", i18n.ErrBadValue, "reason", reasonOf(err)))
			}
		}
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

func validateRelay(ctx context.Context, obj, old *store.Object[RelaySpec, RelayStatus]) error {
	var fields []i18n.FieldError
	host := obj.Spec.Host
	if strings.ContainsAny(host, " /:@") || host == "" {
		fields = append(fields, i18n.Field("spec.host", i18n.ErrBadValue, "reason", "use a host name or an IP address, without a port"))
	}
	switch obj.Spec.Security {
	case "", SecuritySTARTTLS, SecurityTLS, SecurityNone:
	default:
		fields = append(fields, i18n.Field("spec.security", i18n.ErrBadValue, "reason", "use starttls, tls or none"))
	}
	if a, err := mail.ParseAddress(obj.Spec.From); err != nil || a.Name != "" {
		fields = append(fields, i18n.Field("spec.from", i18n.ErrBadValue, "reason", "use a plain email address; the name goes in fromName"))
	}
	if obj.Spec.Username != "" && obj.Spec.Password == "" {
		fields = append(fields, i18n.Field("secrets.password", i18n.ErrRequired))
	}
	if strings.ContainsAny(obj.Spec.FromName+obj.Spec.HELO, "\r\n") {
		fields = append(fields, i18n.Field("spec.fromName", i18n.ErrBadValue, "reason", "keep it on one line"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}
