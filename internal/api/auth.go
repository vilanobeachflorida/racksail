package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The cookie that carries a sign-in through an outside provider's page and
// back. It is Lax, unlike the session's, because the browser comes back
// from another site.
const providerStateCookie = "racksail_signin_state"

// Paths the cookies are sent to: the session's everywhere, the trusted
// browser's only to signing in, where it skips the second step.
const (
	authPath     = Prefix + "/auth"
	providerPath = Prefix + "/auth/providers"
)

type originKey struct{}

// originOf is the page the request comes from, as the browser sees it:
// the scheme and host it used, which passkeys are bound to.
func originOf(ctx context.Context) string {
	o, _ := ctx.Value(originKey{}).(string)
	return o
}

func withOrigin(ctx context.Context, r *http.Request, c auth.Client) context.Context {
	scheme := "http"
	if c.Secure {
		scheme = "https"
	}
	return context.WithValue(ctx, originKey{}, scheme+"://"+r.Host)
}

func sessionCookie(issued *identity.Issued, secure bool) http.Cookie {
	c := http.Cookie{
		Name: auth.SessionCookie, Value: issued.Secret, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
	if issued.Session.Persistent {
		c.MaxAge = int(time.Until(issued.Session.ExpiresAt).Seconds())
		c.Expires = issued.Session.ExpiresAt
	}
	return c
}

func clearCookie(name, path string, secure bool) http.Cookie {
	return http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode}
}

// issuedCookies are the cookies of a session handed to the browser.
func issuedCookies(issued *identity.Issued, secure bool) []http.Cookie {
	out := []http.Cookie{sessionCookie(issued, secure)}
	if issued.DeviceSecret != "" {
		out = append(out, http.Cookie{
			Name: auth.DeviceCookie, Value: issued.DeviceSecret, Path: authPath, HttpOnly: true, Secure: secure,
			SameSite: http.SameSiteStrictMode, Expires: issued.DeviceExpires, MaxAge: int(time.Until(issued.DeviceExpires).Seconds()),
		})
	}
	return out
}

// SignInAnswer says how far signing in got.
type SignInAnswer struct {
	SignedIn bool `json:"signedIn" doc:"Whether the person is signed in now"`
	// Challenge is set when a second step is needed.
	Challenge string   `json:"challenge,omitempty" doc:"Send it back with the second step"`
	Methods   []string `json:"methods,omitempty" doc:"The second steps the person can take: totp, passkey, recovery and mfa:<id>"`
	// Titles name the second steps add-ons provide.
	Titles map[string]string `json:"titles,omitempty" doc:"What to call each second step an add-on provides, by method"`
	Name   string            `json:"name,omitempty" doc:"Who is signing in, to greet them"`
	// Pending says what the person must do first once signed in.
	Pending string `json:"pending,omitempty" enum:"two-factor,password" doc:"What the person must do before anything else"`
}

type signInOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie" doc:"The session cookie, and the trusted-browser cookie when the browser was trusted"`
	Body      SignInAnswer
}

func answer(res *identity.SignInResult, secure bool) *signInOutput {
	out := &signInOutput{}
	if res.User != nil {
		out.Body.Name = res.User.DisplayName()
	}
	if res.Issued != nil {
		out.Body.SignedIn = true
		out.Body.Pending = res.Issued.Session.Pending
		out.SetCookie = issuedCookies(res.Issued, secure)
		return out
	}
	out.Body.Challenge, out.Body.Methods, out.Body.Titles = res.Challenge, res.Methods, res.Titles
	return out
}

type signInInput struct {
	Device string `cookie:"racksail_device" doc:"A trusted browser's cookie"`
	Body   identity.SignInInput
}

type secondStepInput struct {
	Body identity.SecondStepInput
}

type challengeInput struct {
	Body struct {
		Challenge string `json:"challenge" doc:"The challenge the first step answered with"`
	}
}

type mfaStartInput struct {
	Body struct {
		Challenge string   `json:"challenge" doc:"The challenge the first step answered with"`
		Method    store.ID `json:"method" doc:"The second step to start, from the methods the first step listed, without its mfa: prefix"`
	}
}

// PasskeyOptions are what the browser gets to ask for a passkey: the
// options for navigator.credentials, with binary values base64url-encoded,
// as PublicKeyCredential.parseRequestOptionsFromJSON() and
// parseCreationOptionsFromJSON() read them.
type PasskeyOptions struct {
	// Ceremony is set when the request has to send it back.
	Ceremony string          `json:"ceremony,omitempty" doc:"Send it back with the passkey's answer"`
	Options  json.RawMessage `json:"options" doc:"The options for navigator.credentials, with publicKey inside"`
}

type passkeyOptionsOutput struct {
	Body PasskeyOptions
}

type passkeyFinishInput struct {
	Body struct {
		Ceremony   string          `json:"ceremony" doc:"The ceremony the step's first half answered with"`
		Credential json.RawMessage `json:"credential" doc:"The passkey's answer, as PublicKeyCredential.toJSON() gives it"`
		Remember   bool            `json:"remember,omitempty" doc:"Stay signed in when the browser closes"`
	}
}

type mfaOutput struct {
	Body struct {
		Ceremony string `json:"ceremony,omitempty" doc:"Send it back to finish turning the method on"`
		Prompt   string `json:"prompt" doc:"What to tell the person"`
		Input    bool   `json:"input" doc:"Whether the person types a response; otherwise ask again until it says yes or no"`
	}
}

// SignInOptions is what the sign-in screen offers.
type SignInOptions struct {
	SetupRequired bool `json:"setupRequired" doc:"Whether the panel still waits for its first admin"`
	// Passkeys says whether passkeys work at the address the browser used.
	Passkeys  bool                    `json:"passkeys" doc:"Whether passkeys work at the address the panel was opened at"`
	Providers []identity.SignInOption `json:"providers" doc:"Outside providers people can sign in with"`
}

type signInOptionsOutput struct {
	Body SignInOptions
}

type setupStatusOutput struct {
	Body struct {
		Required bool `json:"required" doc:"Whether the panel still waits for its first admin"`
	}
}

type setupInput struct {
	Body identity.SetupInput
}

type providerStartInput struct {
	ID       store.ID `path:"id" doc:"The sign-in provider's ID"`
	Remember bool     `query:"remember" doc:"Stay signed in when the browser closes"`
	// Device is read here, where the browser leaves from the panel's own
	// page: coming back from the provider's site, it doesn't send it.
	Device string `cookie:"racksail_device" doc:"A trusted browser's cookie"`
}

type redirectOutput struct {
	Status    int
	Location  string        `header:"Location"`
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

type providerCallbackInput struct {
	ID    store.ID `path:"id" doc:"The sign-in provider's ID"`
	State string   `cookie:"racksail_signin_state"`
	// Device comes only when the provider sent the browser back without
	// leaving the panel's site; otherwise the start kept it.
	Device string `cookie:"racksail_device"`
	// Linking is the cookie a link from the security settings carries, so
	// its errors go back there.
	Linking bool `cookie:"racksail_linking"`
}

// securityPage is where the browser goes back to after linking an outside
// identity.
const securityPage = "/settings/security"

// providerCallback is where a provider sends the browser back to.
func providerCallback(ctx context.Context, id store.ID) string {
	return originOf(ctx) + providerPath + "/" + url.PathEscape(string(id)) + "/callback"
}

// providerState is the cookie that carries a sign-in or a link through a
// provider's page and back.
func providerState(state string, secure bool) http.Cookie {
	return http.Cookie{Name: providerStateCookie, Value: state, Path: providerPath, MaxAge: 600, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
}

// linkingCookie says a trip through a provider's page links an identity,
// rather than signing in, so what goes wrong is shown in the security
// settings. Off, it clears what an earlier trip left.
func linkingCookie(on, secure bool) http.Cookie {
	c := http.Cookie{Name: linkingCookieName, Value: "1", Path: providerPath, MaxAge: 600, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
	if !on {
		c.Value, c.MaxAge = "", -1
	}
	return c
}

const linkingCookieName = "racksail_linking"

type linkLookupInput struct {
	Body struct {
		Code string `json:"code" maxLength:"128" doc:"What the sign-in link carries, after the #"`
	}
}

type linkLookupOutput struct {
	Body identity.LinkInfo
}

type useLinkInput struct {
	Body identity.UseLinkInput
}

// public describes an operation anyone may call, signed in or not. They
// are all about signing in to the panel, so the racksail command leaves
// them out.
func public(op *huma.Operation, risk store.Risk) {
	Describe(op, Info{Risk: risk, Session: true})
}

// registerAuth adds signing in: the first admin, passwords and second
// steps, passkeys, outside providers, sign-in links and signing out.
func (s *Server) registerAuth() {
	tags := []string{"Signing in"}
	op := huma.Operation{
		OperationID: "getSignInOptions", Method: http.MethodGet, Path: authPath + "/options", Tags: tags,
		Summary:     "See how to sign in",
		Description: "Says what the sign-in screen offers: whether the panel still waits for its first admin, whether passkeys work at the address it was opened at, and which outside providers people can sign in with. It needs no sign-in.",
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*signInOptionsOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		out := &signInOptionsOutput{}
		if out.Body.SetupRequired, err = id.SetupRequired(ctx); err != nil {
			return nil, err
		}
		out.Body.Passkeys = id.PasskeysAvailable(originOf(ctx)) == nil
		if out.Body.Providers, err = id.SignInOptions(ctx); err != nil {
			return nil, err
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getSetupStatus", Method: http.MethodGet, Path: authPath + "/setup", Tags: tags,
		Summary:     "Check whether the panel needs its first admin",
		Description: "Says whether the panel still waits for its first admin. Until then, the setup code that `racksail panel` printed when it started creates one. It needs no sign-in.",
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*setupStatusOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		out := &setupStatusOutput{}
		out.Body.Required, err = id.SetupRequired(ctx)
		return out, err
	})

	op = huma.Operation{
		OperationID: "setUpFirstAdmin", Method: http.MethodPost, Path: authPath + "/setup", Tags: tags,
		Summary: "Create the first admin",
		Description: "Creates the panel's first admin with the one-time setup code, which `racksail panel` prints when it starts and the server's console shows, and signs them in. " +
			"It works once, while the panel has no users.",
	}
	public(&op, store.RiskHigh)
	huma.Register(s.api, op, func(ctx context.Context, in *setupInput) (*signInOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		issued, err := id.Setup(ctx, in.Body, c)
		if err != nil {
			return nil, err
		}
		return answer(&identity.SignInResult{Issued: issued}, c.Secure), nil
	})

	op = huma.Operation{
		OperationID: "signIn", Method: http.MethodPost, Path: authPath + "/sign-in", Tags: tags,
		Summary: "Sign in with a password",
		Description: "Signs in with a username and password, checked here or by an outside directory the admin connected. When the user takes a second step, and the browser isn't trusted to skip it, " +
			"the answer has a challenge and the methods to choose from instead of the session cookie. Failed sign-ins lock a user out for a while, and each address may only try so often.",
	}
	public(&op, store.RiskLow)
	huma.Register(s.api, op, func(ctx context.Context, in *signInInput) (*signInOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		res, err := id.SignIn(ctx, in.Body, in.Device, c)
		if err != nil {
			return nil, err
		}
		return answer(res, c.Secure), nil
	})

	op = huma.Operation{
		OperationID: "signInSecondStep", Method: http.MethodPost, Path: authPath + "/sign-in/second-step", Tags: tags,
		Summary: "Take the second step of signing in",
		Description: "Finishes a sign-in with a code from an authenticator app, a recovery code, a passkey's answer or a method from an add-on. Each challenge takes five wrong answers at most, " +
			"and wrong answers count towards the user's lockout. With trust set, the browser skips the second step for the days the sign-in policy says.",
	}
	public(&op, store.RiskLow)
	huma.Register(s.api, op, func(ctx context.Context, in *secondStepInput) (*signInOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		res, err := id.SecondStep(ctx, in.Body, c, originOf(ctx))
		if err != nil {
			return nil, err
		}
		return answer(res, c.Secure), nil
	})

	op = huma.Operation{
		OperationID: "beginSecondStepPasskey", Method: http.MethodPost, Path: authPath + "/sign-in/second-step/passkey", Tags: tags,
		Summary:     "Ask for a passkey as the second step",
		Description: "Returns the options for navigator.credentials.get() to use one of the user's passkeys as the second step of a sign-in.",
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, in *challengeInput) (*passkeyOptionsOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		options, err := id.BeginSecondStepPasskey(ctx, in.Body.Challenge, originOf(ctx))
		if err != nil {
			return nil, err
		}
		return &passkeyOptionsOutput{Body: PasskeyOptions{Options: options}}, nil
	})

	op = huma.Operation{
		OperationID: "beginSecondStepMfa", Method: http.MethodPost, Path: authPath + "/sign-in/second-step/mfa", Tags: tags,
		Summary:     "Start a second step from an add-on",
		Description: "Starts a second step an add-on provides, such as a push to a phone, and says what to ask the person. Finish it with the second step operation.",
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, in *mfaStartInput) (*mfaOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		ch, err := id.BeginSecondStepMFA(ctx, in.Body.Challenge, in.Body.Method)
		if err != nil {
			return nil, err
		}
		out := &mfaOutput{}
		out.Body.Prompt, out.Body.Input = ch.Prompt, ch.Input
		return out, nil
	})

	op = huma.Operation{
		OperationID: "beginPasskeySignIn", Method: http.MethodPost, Path: authPath + "/passkey", Tags: tags,
		Summary:     "Start signing in with a passkey",
		Description: "Returns the options for navigator.credentials.get() to sign in with a passkey alone: the browser offers the passkeys it holds for the panel, and the one chosen says whose it is.",
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*passkeyOptionsOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		ceremony, options, err := id.BeginPasskeySignIn(ctx, originOf(ctx), auth.ClientFrom(ctx))
		if err != nil {
			return nil, err
		}
		return &passkeyOptionsOutput{Body: PasskeyOptions{Ceremony: ceremony, Options: options}}, nil
	})

	op = huma.Operation{
		OperationID: "finishPasskeySignIn", Method: http.MethodPost, Path: authPath + "/passkey/finish", Tags: tags,
		Summary: "Sign in with a passkey",
		Description: "Signs in with the passkey's answer. A passkey that checked the person's fingerprint, face or PIN is both sign-in steps at once, " +
			"and works while failed passwords keep the user locked out.",
	}
	public(&op, store.RiskLow)
	huma.Register(s.api, op, func(ctx context.Context, in *passkeyFinishInput) (*signInOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		res, err := id.FinishPasskeySignIn(ctx, in.Body.Ceremony, in.Body.Credential, originOf(ctx), in.Body.Remember, c)
		if err != nil {
			return nil, err
		}
		return answer(res, c.Secure), nil
	})

	op = huma.Operation{
		OperationID: "startProviderSignIn", Method: http.MethodGet, Path: providerPath + "/{id}/start", Tags: tags,
		Summary:     "Sign in through an outside provider",
		Description: "Sends the browser to an outside provider's own sign-in page, such as an OpenID Connect provider's, which sends it back to the callback.",
		Responses:   map[string]*huma.Response{"303": {Description: "Off to the provider's page"}},
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, in *providerStartInput) (*redirectOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		to, state, err := id.BeginProviderSignIn(ctx, in.ID, providerCallback(ctx, in.ID), in.Remember, in.Device, c)
		if err != nil {
			return nil, err
		}
		out := &redirectOutput{Status: http.StatusSeeOther, Location: to}
		out.SetCookie = []http.Cookie{providerState(state, c.Secure), linkingCookie(false, c.Secure)}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "finishProviderSignIn", Method: http.MethodGet, Path: providerPath + "/{id}/callback", Tags: tags,
		Summary: "Come back from an outside provider",
		Description: "Where an outside provider sends the browser back to. It finishes the sign-in and goes on to the panel, or to the sign-in screen when a second step is needed " +
			"or something went wrong, with what to show there after the #.",
		Responses: map[string]*huma.Response{"303": {Description: "On to the panel"}},
	}
	public(&op, store.RiskLow)
	huma.Register(s.api, op, func(ctx context.Context, in *providerCallbackInput) (*redirectOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		out := &redirectOutput{Status: http.StatusSeeOther, Location: "/"}
		out.SetCookie = []http.Cookie{
			{Name: providerStateCookie, Value: "", Path: providerPath, MaxAge: -1, HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteLaxMode},
			linkingCookie(false, c.Secure),
		}
		res, err := id.CompleteProviderSignIn(ctx, in.State, providerCallback(ctx, in.ID), queryOf(ctx), in.Device, c)
		switch {
		case err != nil && in.Linking:
			out.Location = securityPage + "#error=" + url.QueryEscape(string(i18n.From(err).Code)) + "&provider=" + url.QueryEscape(string(in.ID))
		case err != nil:
			// The browser lands on the sign-in screen, which says what
			// went wrong; the code is what the UI translates.
			out.Location = "/sign-in#error=" + url.QueryEscape(string(i18n.From(err).Code))
		case res.Linked != nil:
			out.Location = securityPage + "#linked=" + url.QueryEscape(string(res.Linked.ID))
		case res.Issued == nil:
			out.Location = "/sign-in#challenge=" + url.QueryEscape(res.Challenge) + "&methods=" + url.QueryEscape(strings.Join(res.Methods, ","))
		default:
			out.SetCookie = append(out.SetCookie, issuedCookies(res.Issued, c.Secure)...)
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "lookUpSignInLink", Method: http.MethodPost, Path: authPath + "/links/lookup", Tags: tags,
		Summary:     "See whose a sign-in link is",
		Description: "Says whose a sign-in link is and when it expires, for the page where the person chooses their password. The code is sent in the body, so it stays out of logs.",
	}
	public(&op, store.RiskRead)
	huma.Register(s.api, op, func(ctx context.Context, in *linkLookupInput) (*linkLookupOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		info, err := id.LookupLink(ctx, in.Body.Code, auth.ClientFrom(ctx))
		if err != nil {
			return nil, err
		}
		return &linkLookupOutput{Body: info}, nil
	})

	op = huma.Operation{
		OperationID: "useSignInLink", Method: http.MethodPost, Path: authPath + "/links/use", Tags: tags,
		Summary: "Choose a password with a sign-in link",
		Description: "Sets the password of the sign-in link's user, once. Someone without a second step is signed in at once; anyone else signs in as usual afterwards. " +
			"A link to get back in also ends the user's other sessions and lifts a lockout.",
	}
	public(&op, store.RiskMedium)
	huma.Register(s.api, op, func(ctx context.Context, in *useLinkInput) (*signInOutput, error) {
		id, err := s.requireIdentity()
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		res, err := id.UseLink(ctx, in.Body, c)
		if err != nil {
			return nil, err
		}
		return answer(res, c.Secure), nil
	})

	op = huma.Operation{
		OperationID: "signOut", Method: http.MethodPost, Path: authPath + "/sign-out", Tags: tags,
		Summary:     "Sign out",
		Description: "Ends the session the request belongs to and clears its cookie. Signed out already, it only clears the cookie.",
	}
	public(&op, store.RiskLow)
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*cookieOnly, error) {
		c := auth.ClientFrom(ctx)
		if actor, ok := auth.ActorFrom(ctx); ok && s.opts.Identity != nil && actor.SessionID != "" {
			if err := s.opts.Identity.SignOut(ctx, actor); err != nil {
				return nil, err
			}
		}
		return &cookieOnly{SetCookie: []http.Cookie{clearCookie(auth.SessionCookie, "/", c.Secure)}}, nil
	})
}

// cookieOnly answers 204 with cookies.
type cookieOnly struct {
	SetCookie []http.Cookie `header:"Set-Cookie" doc:"The session cookie"`
}

type queryKey struct{}

// queryOf is the request's query, for operations that pass it on whole,
// such as an outside provider's callback.
func queryOf(ctx context.Context) url.Values {
	q, _ := ctx.Value(queryKey{}).(url.Values)
	return q
}
