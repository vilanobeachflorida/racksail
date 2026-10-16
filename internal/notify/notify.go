package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/theme"
)

// Errors.
var (
	ErrUnknownType = i18n.Define("notify.unknown_type", http.StatusUnprocessableEntity, "There is no notification channel type called {type}.")
	ErrSendFailed  = i18n.Define("notify.send_failed", http.StatusBadGateway, "The {channel} notification couldn't be sent: {reason}.")
	ErrSettings    = i18n.Define("notify.settings", http.StatusUnprocessableEntity, "The {channel} channel's settings aren't valid: {reason}.")
	ErrNoRelay     = i18n.Define("notify.no_relay", http.StatusConflict, "Email can't be sent until an admin sets up the mail relay in the notification settings.")
)

// Message is one notification.
type Message struct {
	// ID is the same on every attempt to deliver the message, so receivers
	// can drop repeats.
	ID       string          `json:"id,omitempty" doc:"The same on every attempt to deliver the message"`
	Title    string          `json:"title" doc:"What happened, in a few words"`
	Body     string          `json:"body" doc:"What happened and what to do, in plain text"`
	Severity events.Severity `json:"severity" enum:"info,notice,warning,error,critical" doc:"How urgent it is"`
	// URL links back to the panel, such as the failed job.
	URL string `json:"url,omitempty" doc:"Where in the panel to look"`
	// ActionLabel names the link, such as "Look at the backup".
	ActionLabel string `json:"actionLabel,omitempty" doc:"What the link to the panel says, such as Look at the backup"`
	// Event is the event the message is about, when there is one.
	Event *events.Event `json:"event,omitempty" doc:"The event it is about, when there is one"`
	// Digest holds the messages a summary of quiet hours stands for.
	Digest []Message `json:"digest,omitempty" doc:"For a summary of quiet hours, the messages it stands for"`
}

// Channel delivers messages to one destination.
type Channel interface {
	// Type is the channel type, such as "ntfy".
	Type() string
	Send(ctx context.Context, msg Message) error
}

// Env is what channels are built with.
type Env struct {
	// Secrets holds the values of secret settings, such as a bot token,
	// which the settings refer to.
	Secrets secrets.Reader
	// HTTP sends what channels post; it only connects where the panel
	// allows outgoing notifications to go.
	HTTP *http.Client
	// Relay returns the mail relay email goes out through.
	Relay func(ctx context.Context) (*Relay, error)
	// Theme returns the theme emails are dressed in.
	Theme func(ctx context.Context) (*theme.Package, error)
	// BrandName signs messages.
	BrandName string
	// TelegramAPI is where Telegram's Bot API is; Telegram's own when
	// empty.
	TelegramAPI string
}

// Factory builds a channel from its saved settings.
type Factory func(settings json.RawMessage, env Env) (Channel, error)

// Type describes a channel type.
type Type struct {
	// Name is lowercase, such as email, webhook, matrix, ntfy, telegram,
	// discord or slack.
	Name  string `json:"name"`
	Title string `json:"title"`
	// Description says what the channel needs, for people choosing one.
	Description string `json:"description,omitempty"`
	// Settings is a zero value of the settings type, for the form's schema.
	Settings any `json:"-"`
	// Secrets are the settings that hold secrets, by field name; their
	// values are kept in the secrets store and the settings hold
	// references.
	Secrets []string `json:"secrets,omitempty"`
	New     Factory  `json:"-"`
}

var typeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

var types = struct {
	sync.RWMutex
	m    map[string]Type
	errs []error
}{m: map[string]Type{}}

// RegisterType adds a channel type. It is meant for init functions; problems
// are reported by Err.
func RegisterType(t Type) {
	types.Lock()
	defer types.Unlock()
	switch {
	case !typeNamePattern.MatchString(t.Name) || t.New == nil:
		types.errs = append(types.errs, fmt.Errorf("notify: channel type %q needs a lowercase name and a factory", t.Name))
	case types.m[t.Name].New != nil:
		types.errs = append(types.errs, fmt.Errorf("notify: channel type %s is registered twice", t.Name))
	default:
		types.m[t.Name] = t
	}
}

// Types lists the registered channel types by name.
func Types() []Type {
	types.RLock()
	defer types.RUnlock()
	out := make([]Type, 0, len(types.m))
	for _, t := range types.m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LookupType returns a registered channel type.
func LookupType(name string) (Type, bool) {
	types.RLock()
	defer types.RUnlock()
	t, ok := types.m[name]
	return t, ok
}

// Err reports problems found while channel types were registered.
func Err() error {
	types.RLock()
	defer types.RUnlock()
	return errors.Join(types.errs...)
}

// New builds a channel of a registered type.
func New(typ string, settings json.RawMessage, env Env) (Channel, error) {
	t, ok := LookupType(typ)
	if !ok {
		return nil, ErrUnknownType.New("type", typ)
	}
	return t.New(settings, env)
}

// Notifier sends a message to several channels and reports every failure.
type Notifier struct {
	Channels []Channel
}

// Send delivers msg to every channel. A failing channel doesn't stop the
// others.
func (n *Notifier) Send(ctx context.Context, msg Message) error {
	var errs []error
	for _, c := range n.Channels {
		if err := c.Send(ctx, msg); err != nil {
			errs = append(errs, ErrSendFailed.Wrap(err, "channel", c.Type(), "reason", reasonOf(err)))
		}
	}
	return errors.Join(errs...)
}

// reasonOf is an error's message for people: its own when it has a code,
// its text otherwise.
func reasonOf(err error) string {
	e := i18n.From(err)
	if e.Code == i18n.ErrInternal.Code {
		return err.Error()
	}
	return e.Message
}
