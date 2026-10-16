// Package notifyapi adds notifications' operations to the API: the channel
// types and the settings form of each, the log of what was sent, and every
// person's own preferences. Channels and the mail relay are kinds, whose
// operations the API generates.
package notifyapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func init() {
	api.RegisterExtension(api.Extension{Name: "notifications", Operations: register, Reserved: []string{"notifications"}})
}

var tags = []string{"Notifications"}

func service(svc *modules.Services) (*notify.Service, error) {
	if svc == nil || svc.Notify == nil {
		return nil, i18n.ErrUnavailable.New("what", "Notifications")
	}
	return svc.Notify, nil
}

// ChannelType is a channel type with the schema of its settings.
type ChannelType struct {
	notify.ChannelType
	// Schema is the JSON Schema of the settings, for the form.
	Schema json.RawMessage `json:"schema,omitempty" doc:"The JSON Schema of the type's settings"`
}

type typeList struct {
	Body struct {
		Types []ChannelType `json:"types" doc:"Every channel type"`
	}
}

type deliveriesInput struct {
	Channel store.ID `query:"channel" doc:"Only what went through this channel"`
	State   string   `query:"state" enum:"pending,held,sent,failed,dropped,merged" doc:"Only notifications in this state"`
	Cursor  string   `query:"cursor" doc:"Continues a list where the page before it ended"`
	Limit   int      `query:"limit" minimum:"0" maximum:"1000" doc:"How many to return; 50 when left out"`
}

type deliveryList struct {
	Body struct {
		Notifications []*notify.Notification `json:"notifications" doc:"The notifications on this page, newest first"`
		Next          string                 `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type deliveryInput struct {
	ID store.ID `path:"id" doc:"The notification's ID, which starts with ntf_"`
}

type deliveryOutput struct {
	Body *notify.Notification
}

type preferencesOutput struct {
	Body notify.MyPreferences
}

type preferencesInput struct {
	api.ChangeParams
	Body notify.NotificationPreferences
}

type sendTestOutput struct {
	Body *notify.Notification
}

// PermChannelRead is reading channels and what went through them.
var PermChannelRead = api.ReadPermission(notify.ChannelKind.Name())

func register(a huma.API, svc *modules.Services) {
	op := huma.Operation{
		OperationID: "listChannelTypes", Method: http.MethodGet, Path: api.Prefix + "/notifications/types", Tags: tags, Security: api.Protected,
		Summary:     "List the channel types",
		Description: "The channel types built into the panel and the notifiers modules and add-ons provide, each with the JSON Schema of its settings.",
	}
	api.Describe(&op, api.Info{Permission: string(PermChannelRead), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*typeList, error) {
		s, err := service(svc)
		if err != nil {
			return nil, err
		}
		if err := api.Authorize(ctx, svc.Authorizer, PermChannelRead, auth.Target{Scope: store.ServerScope()}); err != nil {
			return nil, err
		}
		out := &typeList{}
		registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
		for _, t := range s.Types() {
			ct := ChannelType{ChannelType: t}
			if t.Settings != nil {
				schema := inline(registry, huma.SchemaFromType(registry, reflect.TypeOf(t.Settings)))
				if ct.Schema, err = json.Marshal(schema); err != nil {
					return nil, err
				}
			}
			out.Body.Types = append(out.Body.Types, ct)
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "listNotifications", Method: http.MethodGet, Path: api.Prefix + "/notifications", Tags: tags, Security: api.Protected,
		Summary:     "List what was sent",
		Description: "Notifications sent, being sent, held back by quiet hours or given up on, newest first, without their messages.",
	}
	api.Describe(&op, api.Info{Permission: string(PermChannelRead), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *deliveriesInput) (*deliveryList, error) {
		s, err := service(svc)
		if err != nil {
			return nil, err
		}
		if err := api.Authorize(ctx, svc.Authorizer, PermChannelRead, auth.Target{Scope: store.ServerScope()}); err != nil {
			return nil, err
		}
		list, next, err := s.Deliveries(ctx, notify.DeliveryFilter{Channel: in.Channel, State: notify.NotificationState(in.State), Cursor: in.Cursor, Limit: in.Limit})
		if err != nil {
			return nil, err
		}
		out := &deliveryList{}
		out.Body.Notifications, out.Body.Next = list, next
		if out.Body.Notifications == nil {
			out.Body.Notifications = []*notify.Notification{}
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getNotification", Method: http.MethodGet, Path: api.Prefix + "/notifications/{id}", Tags: tags, Security: api.Protected,
		Summary:     "Show a notification and its message",
		Description: "Returns one notification with the message it carries: where it goes, how sending it went, and when it is sent or tried again.",
	}
	api.Describe(&op, api.Info{Permission: string(PermChannelRead), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *deliveryInput) (*deliveryOutput, error) {
		s, err := service(svc)
		if err != nil {
			return nil, err
		}
		actor, err := api.Actor(ctx)
		if err != nil {
			return nil, err
		}
		// One that doesn't exist answers as one of someone else's does.
		hidden := i18n.ErrNotFound.New("what", "Notification "+string(in.ID))
		n, err := s.Notification(ctx, in.ID)
		if notify.ErrDeliveryNotFound.Is(err) {
			return nil, hidden
		}
		if err != nil {
			return nil, err
		}
		// People see their own; the rest is for those who see channels.
		if person, ok := notify.PersonOf(actor); !ok || n.Person != person {
			if err := api.Authorize(ctx, svc.Authorizer, PermChannelRead, auth.Target{Scope: store.ServerScope()}); err != nil {
				return nil, hidden
			}
		}
		return &deliveryOutput{Body: n}, nil
	})

	op = huma.Operation{
		OperationID: "getNotificationPreferences", Method: http.MethodGet, Path: api.Prefix + "/notifications/preferences", Tags: tags, Security: api.Protected,
		Summary:     "Show your notification preferences",
		Description: "What you hear about by email and when: the events, how urgent they have to be and your quiet hours.",
	}
	api.Describe(&op, api.Info{Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*preferencesOutput, error) {
		s, person, err := me(ctx, svc)
		if err != nil {
			return nil, err
		}
		p, err := s.Preferences(ctx, person)
		if err != nil {
			return nil, err
		}
		return &preferencesOutput{Body: p}, nil
	})

	op = huma.Operation{
		OperationID: "setNotificationPreferences", Method: http.MethodPut, Path: api.Prefix + "/notifications/preferences", Tags: tags, Security: api.Protected,
		Summary: "Change your notification preferences",
		Description: "Replaces your notification preferences, as a change set like any other: previewed with dryRun, recorded in the audit log " +
			"and undone by an admin. Your preferences are a core.notification_preferences resource that only you change.",
	}
	api.Describe(&op, api.Info{
		Kind: notify.PreferencesKind.Name(), Operation: "update", Permission: string(auth.KindPermission(notify.PreferencesKind.Name(), "update")),
		Risk: store.RiskLow, DryRun: true, Reversible: true,
	})
	huma.Register(a, op, func(ctx context.Context, in *preferencesInput) (*api.ChangeSetOutput, error) {
		s, person, err := me(ctx, svc)
		if err != nil {
			return nil, err
		}
		if svc.Pipeline == nil {
			return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
		}
		actor, err := api.Actor(ctx)
		if err != nil {
			return nil, err
		}
		// A retry is answered before the preferences are read. The key is
		// the person's: the actor the change is made as only adds a grant.
		if earlier, err := api.Replayed(ctx, svc.Pipeline, in.ChangeParams); earlier != nil || err != nil {
			return earlier, err
		}
		req, err := s.PreferencesRequest(ctx, person, in.Body)
		if err != nil {
			return nil, err
		}
		req.Actor, req.DryRun, req.IdempotencyKey = notify.PreferencesActor(actor), in.DryRun, in.IdempotencyKey
		cs, err := svc.Pipeline.Submit(ctx, req)
		if err != nil {
			return nil, err
		}
		status := http.StatusAccepted
		if cs.DryRun {
			status = http.StatusOK
		}
		return &api.ChangeSetOutput{Status: status, Body: cs}, nil
	})

	op = huma.Operation{
		OperationID: "sendTestNotification", Method: http.MethodPost, Path: api.Prefix + "/notifications/preferences/test", Tags: tags, Security: api.Protected,
		Summary:     "Email yourself a test notification",
		Description: "Sends a test notification to your email address, as your preferences send them, quiet hours aside.",
	}
	api.Describe(&op, api.Info{Risk: store.RiskLow})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*sendTestOutput, error) {
		s, person, err := me(ctx, svc)
		if err != nil {
			return nil, err
		}
		n, err := s.Send(ctx, notify.Recipient{Person: person}, notify.Message{
			Title: "Test notification", Severity: events.SeverityCritical,
			Body: "This is a test of your notification preferences. If it arrived, email notifications reach you.",
		})
		if err != nil {
			return nil, err
		}
		return &sendTestOutput{Body: n}, nil
	})
}

// me returns the service and the signed-in person.
func me(ctx context.Context, svc *modules.Services) (*notify.Service, store.ID, error) {
	s, err := service(svc)
	if err != nil {
		return nil, "", err
	}
	actor, err := api.Actor(ctx)
	if err != nil {
		return nil, "", err
	}
	person, ok := notify.PersonOf(actor)
	if !ok {
		return nil, "", notify.ErrNotAPerson.New()
	}
	return s, person, nil
}

// inline replaces references to named schemas with the schemas, so a
// form gets everything in one document.
func inline(registry huma.Registry, s *huma.Schema) *huma.Schema {
	if s == nil {
		return nil
	}
	if s.Ref != "" {
		if target := registry.SchemaFromRef(s.Ref); target != nil {
			return inline(registry, target)
		}
		return s
	}
	c := *s
	if c.Items != nil {
		c.Items = inline(registry, c.Items)
	}
	if len(c.Properties) > 0 {
		props := make(map[string]*huma.Schema, len(c.Properties))
		for k, v := range c.Properties {
			props[k] = inline(registry, v)
		}
		c.Properties = props
	}
	return &c
}
