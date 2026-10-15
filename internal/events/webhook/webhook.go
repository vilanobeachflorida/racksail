// Package webhook sends events to outgoing webhooks: the core.webhook kind,
// its reconciler, and the dispatcher that delivers events.
//
// A webhook names a URL and the event types it wants, as glob patterns.
// Every event published after the webhook was created that it wants becomes
// a delivery: a POST of the event as JSON, signed the way the Standard
// Webhooks specification describes (webhook-id, webhook-timestamp and
// webhook-signature headers, HMAC-SHA256 with the webhook's secret), so
// receivers can use any library that checks those. Deliveries that don't
// get a 2xx answer are tried again on a fixed schedule for most of a day,
// and every attempt is kept in the delivery log, which the API shows.
//
// The dispatcher follows the event timeline by its sequence numbers and
// keeps its place in the panel database, together with the deliveries, so
// nothing is lost or sent twice when the panel restarts or falls behind.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// WebhookSpec is what a webhook is set to do.
type WebhookSpec struct {
	URL string `json:"url" format:"uri" minLength:"1" maxLength:"2048" doc:"Where events are sent, with POST. Use https, so the events can't be read on the way" example:"https://hooks.example.com/racksail"`
	// Events are the event types to send, as glob patterns.
	Events []string `json:"events" minItems:"1" maxItems:"64" doc:"Event types to send, as patterns such as web.site.* or *.failed; * sends every event" example:"[\"web.site.*\",\"*.failed\"]"`
	// MinSeverity leaves out events less urgent than it.
	MinSeverity events.Severity `json:"minSeverity,omitempty" enum:"info,notice,warning,error,critical" doc:"Only send events at least this urgent"`
	// Owner limits the webhook to events about one reseller's or account's
	// resources.
	Owner *store.Scope `json:"owner,omitempty" doc:"Only send events about the resources of this reseller or account"`
	// Secret is a reference to the signing secret; its value is set with
	// the change's secrets and never shown again.
	Secret string `json:"secret,omitempty" doc:"The signing secret. Send its value in the change's secrets, as secret; receivers use the same value to check signatures"`
	// Disabled stops deliveries without forgetting the webhook.
	Disabled    bool   `json:"disabled,omitempty" doc:"Stops sending events until the webhook is enabled again"`
	Description string `json:"description,omitempty" maxLength:"200" doc:"What the webhook is for"`
}

// WebhookStatus is what the dispatcher last saw of a webhook.
type WebhookStatus struct {
	store.Status
	LastDelivery        *time.Time `json:"lastDelivery,omitempty" doc:"When an event was last sent"`
	LastResponse        int        `json:"lastResponse,omitempty" doc:"The HTTP status of the last answer"`
	ConsecutiveFailures int        `json:"consecutiveFailures,omitempty" doc:"Attempts in a row that didn't get a 2xx answer"`
}

// RedeliverInput names the delivery to send again.
type RedeliverInput struct {
	Delivery store.ID `json:"delivery" doc:"The delivery to send again"`
}

// Validate implements the check every action input gets.
func (in RedeliverInput) Validate() error {
	if !in.Delivery.Valid() || in.Delivery.Prefix() != DeliveryPrefix {
		return i18n.Invalid(i18n.Field("input.delivery", i18n.ErrBadValue, "reason", "not a delivery ID"))
	}
	return nil
}

// DeliveryPrefix is the TypeID prefix of deliveries.
const DeliveryPrefix = "dlv"

// Error codes.
var (
	ErrDeliveryNotFound = i18n.Define("webhook.delivery_not_found", http.StatusNotFound, "The webhook has no delivery {delivery}.")
	ErrNoSecret         = i18n.Define("webhook.no_secret", http.StatusUnprocessableEntity, "Give the webhook a signing secret, so receivers can check that events come from this server.")
)

// failingAfter is how many attempts in a row have to fail before a webhook
// counts as degraded.
const failingAfter = 5

// Kind is core.webhook.
var Kind = store.Define[WebhookSpec, WebhookStatus](store.KindInfo{
	Name: "core.webhook", Prefix: "whk", Scope: store.ScopeServer,
	Title: "Webhook", Plural: "webhooks",
	Description: "Sends events to a URL of your choice as they happen, signed so the receiver can check they came from this server. " +
		"Deliveries that fail are tried again for most of a day, and every attempt is kept in the webhook's delivery log.",
	Actions: []store.Action{
		{
			Name: "test", Title: "Send a test", Summary: "Send a test event to the webhook",
			Description: "Sends a core.webhook.ping event to the webhook right away and reports how the receiver answered. Test deliveries aren't tried again.",
			Risk:        store.RiskLow, Output: Delivery{},
		},
		{
			Name: "redeliver", Title: "Send again", Summary: "Send an earlier delivery again",
			Description: "Sends the event of an earlier delivery once more, as a new delivery that is tried again on the usual schedule if it fails.",
			Risk:        store.RiskLow, Input: RedeliverInput{}, Output: Delivery{},
		},
	},
	Indexes: []store.Index{{Name: "url", Path: "url"}},
	UI: store.UIHints{
		Icon: "webhook", TitleField: "url", SubtitleField: "description",
		Columns: []store.Column{{Field: "spec.url", Format: "link"}, {Field: "spec.events", Format: "badge"}, {Field: "status.phase", Format: "status"}, {Field: "status.lastDelivery", Format: "date"}},
		Fields:  map[string]store.FieldHint{"secret": {Sensitive: true, Widget: "password"}, "events": {Order: 1}, "url": {Order: 0}, "owner": {Advanced: true}},
	},
}).WithValidator(validate)

func init() {
	store.RegisterKind(Kind)
	store.ReservePrefix(DeliveryPrefix, "webhook deliveries")
}

func validate(ctx context.Context, obj, old *store.Object[WebhookSpec, WebhookStatus]) error {
	var fields []i18n.FieldError
	u, err := url.Parse(obj.Spec.URL)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		fields = append(fields, i18n.Field("spec.url", i18n.ErrBadValue, "reason", "use an http or https URL with a host name"))
	case u.User != nil:
		fields = append(fields, i18n.Field("spec.url", i18n.ErrBadValue, "reason", "a URL can't carry a user name or password; receivers check the signature instead"))
	case u.Fragment != "":
		fields = append(fields, i18n.Field("spec.url", i18n.ErrBadValue, "reason", "a URL sent to a server can't have a #fragment"))
	}
	for i, p := range obj.Spec.Events {
		if !events.ValidPattern(p) {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.events[%d]", i), i18n.ErrBadValue, "reason", "use lowercase event type names, dots and the wildcards * and ?"))
		}
	}
	if o := obj.Spec.Owner; o != nil {
		if err := o.Validate(); err != nil || o.Level == store.ScopeServer {
			fields = append(fields, i18n.Field("spec.owner", i18n.ErrBadValue, "reason", "name a reseller or an account"))
		}
	}
	if obj.Spec.Secret == "" {
		fields = append(fields, i18n.Field("secrets.secret", ErrNoSecret))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// Wants reports whether a webhook wants an event.
func Wants(w *store.Object[WebhookSpec, WebhookStatus], e events.Event) bool {
	switch {
	case w.Deleted() || w.Spec.Disabled:
		return false
	case e.Time.Before(w.CreatedAt):
		return false
	case w.Spec.MinSeverity != "" && !e.Severity.AtLeast(w.Spec.MinSeverity):
		return false
	case w.Spec.Owner != nil && e.Scope != *w.Spec.Owner:
		return false
	}
	return events.MatchTypes(w.Spec.Events, e.Type)
}

// PingType is the type of the events test deliveries send.
const PingType = "core.webhook.ping"

// WebhookActor is who caused an event, as deliveries tell it. Addresses are left
// out, so they don't leave the server.
type WebhookActor struct {
	Type   string   `json:"type"`
	ID     store.ID `json:"id,omitempty"`
	Name   string   `json:"name,omitempty"`
	User   store.ID `json:"user,omitempty"`
	Source string   `json:"source,omitempty"`
}

// WebhookPayload is the body of a delivery: the event.
type WebhookPayload struct {
	ID        store.ID        `json:"id"`
	Type      string          `json:"type"`
	Time      time.Time       `json:"time"`
	Severity  events.Severity `json:"severity"`
	Subject   store.ID        `json:"subject,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Scope     store.Scope     `json:"scope"`
	Actor     *WebhookActor   `json:"actor,omitempty"`
	ChangeSet store.ID        `json:"changeSet,omitempty"`
	Job       store.ID        `json:"job,omitempty"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// PayloadOf turns an event into the body of a delivery.
func PayloadOf(e events.Event) WebhookPayload {
	p := WebhookPayload{
		ID: e.ID, Type: e.Type, Time: e.Time, Severity: e.Severity, Subject: e.Subject, Kind: e.Kind, Scope: e.Scope,
		ChangeSet: e.ChangeSet, Job: e.Job, Message: e.Message, Data: e.Data,
	}
	if a := e.Actor; a != nil {
		p.Actor = &WebhookActor{Type: string(a.Type), ID: a.ID, Name: a.Name, User: a.User, Source: string(a.Source)}
	}
	return p
}

// signingKey turns a webhook's secret into the HMAC key. Secrets written the
// way Standard Webhooks libraries make them, whsec_ and base64, are decoded;
// any other secret is used as it is.
func signingKey(secret []byte) []byte {
	s := string(secret)
	if rest, ok := strings.CutPrefix(s, "whsec_"); ok {
		if key, err := base64.StdEncoding.DecodeString(rest); err == nil && len(key) > 0 {
			return key
		}
	}
	return secret
}

// Sign returns the webhook-signature header of a delivery: "v1," and the
// base64 HMAC-SHA256 of "<id>.<timestamp>.<body>".
func Sign(secret []byte, id store.ID, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, signingKey(secret))
	mac.Write([]byte(string(id) + "." + strconv.FormatInt(timestamp.Unix(), 10) + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a webhook-signature header the way receivers do, against
// every signature it lists, and that the timestamp is within tolerance of
// now. Racksail itself only signs; Verify documents the scheme and lets
// tests and add-ons check deliveries.
func Verify(secret []byte, id store.ID, timestamp string, signature string, body []byte, now time.Time, tolerance time.Duration) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	when := time.Unix(ts, 0)
	if now.Sub(when) > tolerance || when.Sub(now) > tolerance {
		return false
	}
	want := Sign(secret, id, when, body)
	for _, sig := range strings.Fields(signature) {
		if hmac.Equal([]byte(sig), []byte(want)) {
			return true
		}
	}
	return false
}
