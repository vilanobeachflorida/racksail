package identity

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
)

// alertTimeout bounds how long sending one alert may take; alerts go out in
// the background, so signing in never waits for them.
const alertTimeout = 30 * time.Second

// providerPrefix marks alert channels that add-ons provide.
const providerPrefix = "provider:"

// AlertChannelType is a kind of channel security alerts can go to.
type AlertChannelType struct {
	// ID is the channel type, or provider:<name> for one an add-on adds.
	ID    string `json:"id"`
	Title string `json:"title"`
	// Settings is a zero value of the channel's settings, for its form.
	Settings any `json:"-"`
}

// AlertChannelTypes lists where security alerts can go: the notification
// channel types built in, and those add-ons provide.
func (s *Service) AlertChannelTypes() []AlertChannelType {
	var out []AlertChannelType
	for _, t := range notify.Types() {
		out = append(out, AlertChannelType{ID: t.Name, Title: t.Title, Settings: t.Settings})
	}
	if s.opts.Hooks != nil && s.opts.Hooks.Registry != nil {
		for _, reg := range s.opts.Hooks.Registry.Providers(hooks.ProvideNotifier) {
			p := reg.Provider
			out = append(out, AlertChannelType{ID: providerPrefix + p.Name, Title: p.Title, Settings: p.Settings})
		}
	}
	return out
}

// AlertChannelInput says where the actor's security alerts go.
type AlertChannelInput struct {
	// Type is a channel type from AlertChannelTypes; empty turns alerts off.
	Type string `json:"type,omitempty" doc:"The channel type, or provider:<name>; leave it out to turn alerts off"`
	// Settings are the channel's settings, kept encrypted and never shown
	// again.
	Settings json.RawMessage `json:"settings,omitempty" doc:"The channel's settings, as its form asks for them. They're kept encrypted and never shown again"`
}

// SetAlertChannel says where the actor's security alerts go: sign-ins from
// new devices, lockouts and changes to how they sign in. The channel is
// opened once to check the settings.
func (s *Service) SetAlertChannel(ctx context.Context, actor *auth.Actor, in AlertChannelInput) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "change where their alerts go")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	ref := secretRef(u.ID, alertSettings)
	if in.Type == "" {
		s.opts.Secrets.Delete(ctx, ref)
	} else {
		if len(in.Settings) == 0 {
			in.Settings = json.RawMessage("{}")
		}
		if len(in.Settings) > 16<<10 {
			return i18n.ErrTooLarge.New("limit", "16 KiB")
		}
		if _, err := s.openChannel(ctx, in.Type, in.Settings); err != nil {
			return i18n.Invalid(i18n.Field("settings", i18n.ErrBadValue, "reason", i18n.From(err).Message))
		}
		if err := s.opts.Secrets.Put(ctx, ref, in.Settings); err != nil {
			return err
		}
	}
	u.AlertChannel, u.UpdatedAt = in.Type, s.now()
	if err := s.opts.Store.UpdateUser(ctx, u, 0); err != nil {
		return err
	}
	summary := "Turn security alerts off"
	if in.Type != "" {
		summary = "Send security alerts to " + in.Type
	}
	s.audit(ctx, actor, "core.user.set_alerts", u.ID, u.Home, events.OutcomeSuccess, "", summary)
	return nil
}

// TestAlert sends a test alert to the actor's channel and reports how it
// went.
func (s *Service) TestAlert(ctx context.Context, actor *auth.Actor) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	if u.AlertChannel == "" {
		return notFound("A channel for security alerts")
	}
	ch, err := s.userChannel(ctx, u)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, alertTimeout)
	defer cancel()
	return ch.Send(ctx, notify.Message{
		Title: "Security alerts work", Severity: events.SeverityInfo, URL: s.panelURL("/settings/account/security"),
		Body: "Security alerts for " + u.Username + " come here: sign-ins from new devices, lockouts and changes to how you sign in.",
	})
}

func (s *Service) openChannel(ctx context.Context, typ string, settings json.RawMessage) (notify.Channel, error) {
	if name, ok := strings.CutPrefix(typ, providerPrefix); ok {
		if s.opts.Hooks == nil || s.opts.Hooks.Registry == nil {
			return nil, hooks.ErrUnknownProvider.New("capability", string(hooks.ProvideNotifier), "provider", name)
		}
		return hooks.Open[notify.Channel](ctx, s.opts.Hooks.Registry, hooks.ProvideNotifier, name, settings)
	}
	return notify.New(typ, settings, secretResolver{s.opts.Secrets})
}

func (s *Service) userChannel(ctx context.Context, u *User) (notify.Channel, error) {
	settings, err := s.opts.Secrets.Get(ctx, secretRef(u.ID, alertSettings))
	if err != nil {
		return nil, err
	}
	return s.openChannel(ctx, u.AlertChannel, settings)
}

// secretResolver lets channel settings refer to secrets the panel keeps.
type secretResolver struct{ store secrets.Store }

func (r secretResolver) Resolve(ctx context.Context, ref string) (string, error) {
	v, err := r.store.Get(ctx, secrets.Ref(ref))
	return string(v), err
}

func (s *Service) panelURL(path string) string {
	if s.opts.Hostname == "" || s.opts.Hostname == "localhost" {
		return ""
	}
	return "https://" + s.opts.Hostname + path
}

// alert sends a security alert to the user's channel, if they chose one,
// in the background.
func (s *Service) alert(ctx context.Context, u *User, title, body string, severity events.Severity) {
	if u.AlertChannel == "" || s.opts.Secrets == nil {
		return
	}
	msg := notify.Message{Title: title, Body: body, Severity: severity, URL: s.panelURL("/settings/account/security")}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertTimeout)
		defer cancel()
		ch, err := s.userChannel(ctx, u)
		if err == nil {
			err = ch.Send(ctx, msg)
		}
		if err != nil {
			s.opts.Log.Warn("sending a security alert", "user", u.ID, "channel", u.AlertChannel, "error", err)
		}
	}()
}
