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
)

// Errors.
var (
	ErrUnknownType = i18n.Define("notify.unknown_type", http.StatusUnprocessableEntity, "There is no notification channel type called {type}.")
	ErrSendFailed  = i18n.Define("notify.send_failed", http.StatusBadGateway, "The {channel} notification couldn't be sent: {reason}.")
)

// Message is one notification.
type Message struct {
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Severity events.Severity `json:"severity"`
	// URL links back to the panel, such as the failed job.
	URL string `json:"url,omitempty"`
	// Event is the event the message is about, when there is one.
	Event *events.Event `json:"event,omitempty"`
}

// Channel delivers messages to one destination.
type Channel interface {
	// Type is the channel type, such as "ntfy".
	Type() string
	Send(ctx context.Context, msg Message) error
}

// Secrets resolves secret references in channel settings, such as a bot
// token.
type Secrets interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// Factory builds a channel from its saved settings.
type Factory func(settings json.RawMessage, secrets Secrets) (Channel, error)

// Type describes a channel type.
type Type struct {
	// Name is lowercase, such as email, webhook, matrix, ntfy, telegram,
	// discord or slack.
	Name  string
	Title string
	// Settings is a zero value of the settings type, for the form's schema.
	Settings any
	New      Factory
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

// Err reports problems found while channel types were registered.
func Err() error {
	types.RLock()
	defer types.RUnlock()
	return errors.Join(types.errs...)
}

// New builds a channel of a registered type.
func New(typ string, settings json.RawMessage, secrets Secrets) (Channel, error) {
	types.RLock()
	t, ok := types.m[typ]
	types.RUnlock()
	if !ok {
		return nil, ErrUnknownType.New("type", typ)
	}
	return t.New(settings, secrets)
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
			errs = append(errs, ErrSendFailed.Wrap(err, "channel", c.Type(), "reason", err.Error()))
		}
	}
	return errors.Join(errs...)
}
