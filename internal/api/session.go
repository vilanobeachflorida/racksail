package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// ModuleState is where a module is in its life, as the session document
// says.
type ModuleState string

// Schema lists the states, so clients know them.
func (ModuleState) Schema(r huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"off", "on", "active", "stopping", "removing"}}
}

// Preferences are a person's display preferences, kept with their user.
type Preferences struct {
	Mode     string `json:"mode,omitempty" enum:"system,light,dark" doc:"Light, dark or the system's"`
	Contrast string `json:"contrast,omitempty" enum:"system,standard,high"`
	Density  string `json:"density,omitempty" enum:"theme,comfortable,compact"`
	TextSize string `json:"textSize,omitempty" enum:"default,small,large,larger,largest"`
	Motion   string `json:"motion,omitempty" enum:"system,reduce,full"`
}

// SessionUser is who is signed in.
type SessionUser struct {
	ID       store.ID    `json:"id"`
	Name     string      `json:"name" doc:"The person's name, or their username"`
	Username string      `json:"username,omitempty"`
	Email    string      `json:"email,omitempty"`
	Home     store.Scope `json:"home" doc:"Where the person belongs"`
}

// SessionAccount is an account the person's grants name.
type SessionAccount struct {
	ID   store.ID `json:"id"`
	Name string   `json:"name"`
	Slug string   `json:"slug"`
}

// SessionToken is the API token a request came with.
type SessionToken struct {
	ID   store.ID `json:"id"`
	Name string   `json:"name"`
}

// Session is what the panel tells the web UI, and the racksail command,
// about whoever is signed in: who they are, what they may do, which modules
// are on, and what they must do first.
type Session struct {
	User SessionUser `json:"user"`
	// Permissions are the person's permission patterns, as their roles
	// grant them, such as * or *.read.
	Permissions []string `json:"permissions" doc:"What the person may do somewhere, as permission names and patterns; the panel checks each request against where it applies"`
	// Modules says each module's state; modules left out are off.
	Modules     map[string]ModuleState `json:"modules"`
	Preferences *Preferences           `json:"preferences,omitempty"`
	// Token is the API token the request came with, when it did.
	Token *SessionToken `json:"token,omitempty"`
	// Impersonation says who is viewing the panel as the person.
	Impersonation *identity.Impersonation `json:"impersonation,omitempty"`
	// Pending is what the person must do before anything else.
	Pending        string     `json:"pending,omitempty" enum:"two-factor,password" doc:"What the person must do before anything else: set up a second step, or choose a new password"`
	SteppedUpUntil *time.Time `json:"steppedUpUntil,omitempty" doc:"Until when sensitive actions go ahead without confirming it's them again"`
	StepUpMethods  []string   `json:"stepUpMethods" doc:"How the person can confirm it's them: password, totp or passkey"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty" doc:"When the session ends at the latest"`
	// Accounts are the accounts the person's grants name, for switching
	// between them.
	Accounts []SessionAccount `json:"accounts"`
	// Dev says the panel is racksail dev, which signs everyone in.
	Dev bool `json:"dev,omitempty" doc:"Whether this is racksail dev, which signs everyone in as its developer"`
}

type sessionOutput struct {
	Body Session
}

type preferencesInput struct {
	Body Preferences
}

type stepUpInput struct {
	Body identity.StepUpInput
}

type stepUpOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie" doc:"The session cookie, which changes"`
	Body      struct {
		SteppedUpUntil time.Time `json:"steppedUpUntil" doc:"Until when sensitive actions go ahead"`
	}
}

func (s *Server) registerSession() {
	tags := []string{"Session"}
	op := huma.Operation{
		OperationID: "getSession", Method: http.MethodGet, Path: Prefix + "/session", Tags: tags, Security: Protected,
		Summary: "See who is signed in",
		Description: "Says who is signed in and what they may do, which modules are on, their display preferences, who is viewing the panel as them, " +
			"what they must do first and how long confirming it's them lasts. The web UI reads it before anything else; `racksail whoami` shows it.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*sessionOutput, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		doc, err := s.session(ctx, actor)
		if err != nil {
			return nil, err
		}
		return &sessionOutput{Body: doc}, nil
	})

	op = huma.Operation{
		OperationID: "saveSessionPreferences", Method: http.MethodPut, Path: Prefix + "/session/preferences", Tags: tags, Security: Protected,
		Summary:       "Keep display preferences",
		Description:   "Keeps the person's display preferences with their user, so they follow them to other browsers: light or dark, contrast, density, text size and motion.",
		DefaultStatus: http.StatusNoContent,
	}
	Describe(&op, Info{Risk: store.RiskLow})
	huma.Register(s.api, op, func(ctx context.Context, in *preferencesInput) (*struct{}, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		if actor.Person() == "" {
			return nil, i18n.ErrUnsupported.New("what", "Keeping preferences", "reason", "nobody in particular is signed in")
		}
		raw, err := json.Marshal(in.Body)
		if err != nil {
			return nil, err
		}
		return nil, id.SetPreferences(ctx, actor, raw)
	})

	op = huma.Operation{
		OperationID: "stepUp", Method: http.MethodPost, Path: Prefix + "/session/step-up", Tags: tags, Security: Protected,
		Summary: "Confirm it's you",
		Description: "Confirms it's the person, with their password, a code from their authenticator app or a passkey, so sensitive actions go ahead for the minutes the sign-in policy says. " +
			"The session cookie changes. While viewing the panel as someone, it's the admin who confirms.",
	}
	Describe(&op, Info{Risk: store.RiskLow, Session: true})
	huma.Register(s.api, op, func(ctx context.Context, in *stepUpInput) (*stepUpOutput, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		issued, err := id.StepUp(ctx, actor, in.Body, c, originOf(ctx))
		if err != nil {
			return nil, err
		}
		out := &stepUpOutput{}
		if issued != nil {
			out.SetCookie = []http.Cookie{sessionCookie(issued, c.Secure)}
		}
		out.Body.SteppedUpUntil = actor.SteppedUpAt.Add(id.Policy().StepUp())
		if issued != nil {
			out.Body.SteppedUpUntil = issued.Session.SteppedUpAt.Add(id.Policy().StepUp())
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "beginStepUpPasskey", Method: http.MethodPost, Path: Prefix + "/session/step-up/passkey", Tags: tags, Security: Protected,
		Summary:     "Confirm it's you with a passkey",
		Description: "Returns the options for navigator.credentials.get() to confirm it's the person with one of their passkeys; send the answer to the step-up operation.",
	}
	Describe(&op, Info{Risk: store.RiskRead, Session: true})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*passkeyOptionsOutput, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		ceremony, options, err := id.BeginStepUpPasskey(ctx, actor, originOf(ctx))
		if err != nil {
			return nil, err
		}
		return &passkeyOptionsOutput{Body: PasskeyOptions{Ceremony: ceremony, Options: options}}, nil
	})

	op = huma.Operation{
		OperationID: "stopImpersonating", Method: http.MethodDelete, Path: Prefix + "/session/impersonation", Tags: tags, Security: Protected,
		Summary:       "Stop viewing the panel as someone else",
		Description:   "Ends viewing the panel as another user and goes back to your own session.",
		DefaultStatus: http.StatusNoContent,
	}
	Describe(&op, Info{Risk: store.RiskLow, Session: true})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*cookieOnly, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		issued, err := id.StopImpersonating(ctx, actor)
		if err != nil {
			return nil, err
		}
		return &cookieOnly{SetCookie: []http.Cookie{sessionCookie(issued, auth.ClientFrom(ctx).Secure)}}, nil
	})
}

// session builds the session document of an actor.
func (s *Server) session(ctx context.Context, actor *auth.Actor) (Session, error) {
	doc := Session{
		User:        SessionUser{ID: actor.Person(), Name: actor.Name, Home: store.ServerScope()},
		Permissions: auth.Patterns(actor), Modules: map[string]ModuleState{}, StepUpMethods: []string{}, Accounts: []SessionAccount{},
		Pending: actor.Pending, Dev: s.opts.Dev,
	}
	if s.opts.ModuleStates != nil {
		for id, st := range s.opts.ModuleStates() {
			if st != modules.StateOff {
				doc.Modules[id] = ModuleState(st)
			}
		}
	}
	if actor.Type == auth.ActorToken {
		doc.Token = &SessionToken{ID: actor.ID, Name: actor.Name}
	}
	id := s.opts.Identity
	if id == nil {
		return doc, nil
	}
	if u, err := id.Store().User(ctx, actor.Person()); err == nil {
		doc.User = SessionUser{ID: u.ID, Name: u.DisplayName(), Username: u.Username, Email: u.Email, Home: u.Home}
		if len(u.Preferences) > 0 {
			var p Preferences
			if json.Unmarshal(u.Preferences, &p) == nil {
				doc.Preferences = &p
			}
		}
	} else if !store.ErrNotFound.Is(err) {
		return doc, err
	}
	doc.Impersonation = id.ImpersonationOf(ctx, actor)
	if actor.Type == auth.ActorUser {
		doc.StepUpMethods = id.StepUpMethods(ctx, actor)
		if id.SteppedUp(actor) {
			until := actor.SteppedUpAt.Add(id.Policy().StepUp())
			doc.SteppedUpUntil = &until
		}
		if x, err := id.SessionOf(ctx, actor); err == nil {
			doc.ExpiresAt = &x.ExpiresAt
		}
	}
	doc.Accounts = s.sessionAccounts(ctx, actor)
	return doc, nil
}

// sessionAccounts lists the accounts the actor's grants name, by name.
func (s *Server) sessionAccounts(ctx context.Context, actor *auth.Actor) []SessionAccount {
	out := []SessionAccount{}
	st := s.store()
	if st == nil {
		return out
	}
	seen := map[store.ID]bool{}
	for _, g := range actor.Grants {
		if g.Scope.Level != store.ScopeAccount || seen[g.Scope.ID] {
			continue
		}
		seen[g.Scope.ID] = true
		obj, err := account.Kind.Get(ctx, st, g.Scope.ID)
		if err != nil || obj.Deleted() {
			continue
		}
		out = append(out, SessionAccount{ID: obj.ID, Name: obj.Spec.Name, Slug: obj.Spec.Slug})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
