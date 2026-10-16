package notify

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Person is someone notifications reach on their own: an admin, a
// reseller or a customer with a sign-in.
type Person struct {
	ID    store.ID
	Name  string
	Email string
	// Actor is the person as the authorizer knows them, so they only hear
	// about what they may see.
	Actor *auth.Actor
	// Locale is the language they chose for the panel, which their
	// messages use too; English when empty.
	Locale string
}

// Directory finds people by ID. The panel's is Users, over the users the
// identity service keeps.
type Directory interface {
	// Person returns the person with the given ID, or nil when there is
	// none.
	Person(ctx context.Context, id store.ID) (*Person, error)
}

// PersonOf returns the ID a signed-in actor's preferences are kept under:
// their user, whatever signed them in.
func PersonOf(a *auth.Actor) (store.ID, bool) {
	switch {
	case a == nil:
		return "", false
	case a.User != "":
		return a.User, true
	case a.Type == auth.ActorUser && a.ID != "":
		return a.ID, true
	}
	return "", false
}

// ErrNotAPerson refuses preferences to callers that aren't a person, such
// as an API token without a user.
var ErrNotAPerson = i18n.Define("notify.not_a_person", http.StatusForbidden, "Notification preferences belong to people. Sign in with your own user to set yours.")

// NotificationPreferences are what one person wants to hear about, and when.
type NotificationPreferences struct {
	// Email sends what the person wants to their email address.
	Email bool `json:"email" doc:"Send notifications to my email address"`
	// Events are the event types wanted, as patterns; DefaultEvents when
	// empty.
	Events []string `json:"events,omitempty" maxItems:"64" doc:"Event types to send, as patterns such as *.failed; failures, security events, safe mode, hooks switched off, Racksail's updates and reboots the server schedules when empty"`
	// MinSeverity leaves out events less urgent than it; warning when
	// empty.
	MinSeverity events.Severity `json:"minSeverity,omitempty" enum:"info,notice,warning,error,critical" doc:"Only send events at least this urgent; warning when empty"`
	// Muted are event types never sent, whatever Events says.
	Muted      []string    `json:"muted,omitempty" maxItems:"64" doc:"Event types never to send, as patterns"`
	QuietHours *QuietHours `json:"quietHours,omitempty" doc:"Hold back what can wait, and send it as one summary when quiet hours end"`
}

// MyPreferences are a person's preferences as they see them, with when
// they last changed them.
type MyPreferences struct {
	NotificationPreferences
	UpdatedAt *time.Time `json:"updatedAt,omitempty" readOnly:"true" doc:"When they were last changed"`
}

// DefaultEvents are what people hear about until they choose: failures,
// security events, safe mode, hooks switched off, Racksail's updates and
// reboots the server schedules for itself.
var DefaultEvents = []string{"*.failed", "security.*", "core.safe_mode.*", "core.hook.disabled", "core.update.*", "core.reboot.scheduled"}

// DirectoryFunc lets a function find people, such as one that reads the
// panel's users.
type DirectoryFunc func(ctx context.Context, id store.ID) (*Person, error)

// Person implements Directory.
func (f DirectoryFunc) Person(ctx context.Context, id store.ID) (*Person, error) { return f(ctx, id) }

// Users finds people among the panel's users, which users returns once the
// panel has them: their email address, and their grants, so they only hear
// about what they may see. Users who are gone or turned off hear nothing.
func Users(users func() auth.Directory) Directory {
	return DirectoryFunc(func(ctx context.Context, id store.ID) (*Person, error) {
		dir := users()
		if dir == nil {
			return nil, nil
		}
		u, err := dir.User(ctx, id)
		switch {
		case store.ErrNotFound.Is(err):
			return nil, nil
		case err != nil:
			return nil, err
		case u.Disabled:
			return nil, nil
		}
		name := u.Name
		if name == "" {
			name = u.Username
		}
		actor := &auth.Actor{Type: auth.ActorUser, ID: u.ID, Name: u.Username, User: u.ID, Grants: u.Grants}
		return &Person{ID: u.ID, Name: name, Email: u.Email, Actor: actor, Locale: u.Locale}, nil
	})
}

// Check returns what is wrong with preferences.
func (p *NotificationPreferences) Check() error {
	if fields := p.check(""); len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// check lists what is wrong with preferences, at paths under prefix.
func (p *NotificationPreferences) check(prefix string) []i18n.FieldError {
	var fields []i18n.FieldError
	for i, pat := range p.Events {
		if !events.ValidPattern(pat) {
			fields = append(fields, i18n.Field(fmt.Sprintf("%sevents[%d]", prefix, i), i18n.ErrBadValue, "reason", "use lowercase event type names, dots and the wildcards * and ?"))
		}
	}
	for i, pat := range p.Muted {
		if !events.ValidPattern(pat) {
			fields = append(fields, i18n.Field(fmt.Sprintf("%smuted[%d]", prefix, i), i18n.ErrBadValue, "reason", "use lowercase event type names, dots and the wildcards * and ?"))
		}
	}
	if p.MinSeverity != "" && !p.MinSeverity.Valid() {
		fields = append(fields, i18n.Field(prefix+"minSeverity", i18n.ErrBadValue, "reason", "use info, notice, warning, error or critical"))
	}
	if p.QuietHours != nil {
		fields = append(fields, p.QuietHours.check(prefix+"quietHours")...)
	}
	return fields
}

// Wants reports whether the person wants to hear about an event.
func (p *NotificationPreferences) Wants(e events.Event) bool {
	if !p.Email {
		return false
	}
	min := p.MinSeverity
	if min == "" {
		min = events.SeverityWarning
	}
	patterns := p.Events
	if len(patterns) == 0 {
		patterns = DefaultEvents
	}
	switch {
	case !e.Severity.AtLeast(min):
		return false
	case len(p.Muted) > 0 && events.MatchTypes(p.Muted, e.Type):
		return false
	}
	return events.MatchTypes(patterns, e.Type)
}

// mayRead reports whether a person may see an event, as the timeline would
// show it to them.
func mayRead(ctx context.Context, authz auth.Authorizer, p *Person, e events.Event) bool {
	if authz == nil || p.Actor == nil {
		return false
	}
	target := auth.Target{Scope: e.Scope, Object: e.Subject}
	if target.Scope.Level == "" {
		target.Scope = store.ServerScope()
	}
	return authz.Authorize(ctx, p.Actor, eventRead, target) == nil
}

// eventRead is the permission to see events in the timeline.
const eventRead auth.Permission = "core.event.read"

// wantsChannel reports whether a channel wants an event.
func wantsChannel(c *store.Object[ChannelSpec, ChannelStatus], e events.Event) bool {
	switch {
	case c.Deleted() || c.Spec.Disabled:
		return false
	case e.Time.Before(c.CreatedAt):
		return false
	case c.Spec.MinSeverity != "" && !e.Severity.AtLeast(c.Spec.MinSeverity):
		return false
	case c.Spec.Owner != nil && e.Scope != *c.Spec.Owner:
		return false
	}
	return events.MatchTypes(c.Spec.Events, e.Type) && !slices.Contains(quietTypes, e.Type)
}

// quietTypes are events about notifications themselves, which would echo.
var quietTypes = []string{"core.notification_channel.tested", "core.smtp_relay.tested"}
