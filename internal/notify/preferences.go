package notify

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// PreferencesSpec is the spec of core.notification_preferences: one
// person's notification preferences.
type PreferencesSpec struct {
	// Person is who they belong to. Only that person changes them.
	Person store.ID `json:"person" doc:"The person the preferences belong to"`
	NotificationPreferences
}

// PreferencesStatus is what the panel last did with a person's
// preferences.
type PreferencesStatus struct {
	store.Status
}

// PreferencesKind is core.notification_preferences. A person changes their
// own through PUT /api/v1/notifications/preferences, which, like every
// change, goes through the change pipeline.
var PreferencesKind = store.Define[PreferencesSpec, PreferencesStatus](store.KindInfo{
	Name: "core.notification_preferences", Prefix: "npref", Scope: store.ScopeServer,
	Title: "Notification preferences", Plural: "notification-preferences",
	Description: "What one person hears about by email, and when: the events, how urgent they have to be and their quiet hours. " +
		"Preferences belong to their person, who changes them with PUT /api/v1/notifications/preferences.",
	Indexes: []store.Index{{Name: "person", Path: "person", Unique: true}},
	Risks:   store.Risks{Create: store.RiskLow, Update: store.RiskLow, Delete: store.RiskLow},
	UI: store.UIHints{
		Icon: "bell", TitleField: "person", Hidden: true,
		Fields: map[string]store.FieldHint{"person": {CreateOnly: true}, "quietHours": {Advanced: true}},
	},
}).WithValidator(validatePreferences)

func init() {
	store.RegisterKind(PreferencesKind)
}

func validatePreferences(ctx context.Context, obj, old *store.Object[PreferencesSpec, PreferencesStatus]) error {
	var fields []i18n.FieldError
	if _, err := store.ParseID(string(obj.Spec.Person)); err != nil {
		fields = append(fields, i18n.Field("spec.person", i18n.ErrBadValue, "reason", "name the person by their ID"))
	}
	if old != nil && old.Spec.Person != obj.Spec.Person {
		fields = append(fields, i18n.Field("spec.person", i18n.ErrImmutable))
	}
	fields = append(fields, obj.Spec.check("spec.")...)
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// ErrNotYours refuses a change to someone else's preferences.
var ErrNotYours = i18n.Define("notify.not_your_preferences", http.StatusForbidden, "Notification preferences belong to their person, who alone changes them.")

// Hooks are the service's own hooks: a person's preferences are changed
// by that person alone, whatever grants a change carries.
func (s *Service) Hooks() []hooks.Registration {
	own := func(ctx context.Context, c *hooks.Context) error {
		if c.Change == nil || c.Actor == nil || c.Actor.Type == auth.ActorSystem {
			return nil
		}
		var spec PreferencesSpec
		if err := json.Unmarshal(c.Change.After, &spec); err != nil {
			return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", "the spec isn't a person's preferences"))
		}
		if person, ok := PersonOf(c.Actor); !ok || person != spec.Person {
			return ErrNotYours.New()
		}
		return nil
	}
	var out []hooks.Registration
	for _, op := range []string{"create", "update"} {
		out = append(out, hooks.Registration{
			Point: hooks.KindPoint(PreferencesKind.Name(), op, changeset.StageValidate), Kind: hooks.Validate,
			Owner: hooks.OwnerCore + ":notify", Security: true, Priority: -1000, OnValidate: own,
		})
	}
	return out
}

// preferencesOf returns a person's preferences resource, or nil when they
// never chose.
func (s *Service) preferencesOf(ctx context.Context, rd store.Reader, person store.ID) (*store.Object[PreferencesSpec, PreferencesStatus], error) {
	objs, _, err := PreferencesKind.List(ctx, rd, store.Query{Index: map[string]string{"person": string(person)}, Limit: 1})
	if err != nil || len(objs) == 0 {
		return nil, err
	}
	return objs[0], nil
}

// allPreferences returns everyone's preferences, by person.
func (s *Service) allPreferences(ctx context.Context) (map[store.ID]*NotificationPreferences, error) {
	out := map[store.ID]*NotificationPreferences{}
	cursor := ""
	for {
		objs, next, err := PreferencesKind.List(ctx, s.opts.Store, store.Query{Cursor: cursor, Limit: store.MaxLimit})
		if err != nil {
			return nil, err
		}
		for _, obj := range objs {
			if !obj.Deleted() {
				p := obj.Spec.NotificationPreferences
				out[obj.Spec.Person] = &p
			}
		}
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// Preferences returns a person's preferences; a person who never chose
// gets none.
func (s *Service) Preferences(ctx context.Context, person store.ID) (MyPreferences, error) {
	obj, err := s.preferencesOf(ctx, s.opts.Store, person)
	if err != nil || obj == nil {
		return MyPreferences{}, err
	}
	updated := obj.UpdatedAt
	return MyPreferences{NotificationPreferences: obj.Spec.NotificationPreferences, UpdatedAt: &updated}, nil
}

// PreferencesRequest builds the change that sets a person's preferences:
// a create the first time, an update after.
func (s *Service) PreferencesRequest(ctx context.Context, person store.ID, p NotificationPreferences) (changeset.Request, error) {
	raw, err := json.Marshal(PreferencesSpec{Person: person, NotificationPreferences: p})
	if err != nil {
		return changeset.Request{}, err
	}
	req := changeset.Request{Title: "Change notification preferences"}
	obj, err := s.preferencesOf(ctx, s.opts.Store, person)
	if err != nil {
		return changeset.Request{}, err
	}
	if obj == nil {
		req.Changes = []changeset.Change{{Op: changeset.OpCreate, Kind: PreferencesKind.Name(), Scope: store.ServerScope(), Spec: raw}}
	} else {
		req.Changes = []changeset.Change{{Op: changeset.OpUpdate, Kind: PreferencesKind.Name(), ID: obj.ID, Revision: obj.Revision, Spec: raw}}
	}
	return req, nil
}

// PreferencesActor is a person as they change their own preferences: with
// a grant on preferences, which the service's hooks hold to theirs alone.
// Preferences belong to the server, where most people hold no grant.
func PreferencesActor(a *auth.Actor) *auth.Actor {
	c := a.Clone()
	c.Grants = append(c.Grants, auth.Grant{
		Role: auth.RoleCollaborator, Scope: store.ServerScope(),
		Permissions: []auth.Permission{
			auth.KindPermission(PreferencesKind.Name(), "read"), auth.KindPermission(PreferencesKind.Name(), "create"),
			auth.KindPermission(PreferencesKind.Name(), "update"),
		},
	})
	return c
}
