package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// maxPasskeys is how many passkeys one user may register.
const maxPasskeys = 20

// webUser is a user as WebAuthn sees them. The user handle is the user's
// ID, so a discoverable passkey says whose it is.
type webUser struct {
	u     *User
	creds []webauthn.Credential
}

func (w webUser) WebAuthnID() []byte                         { return []byte(w.u.ID) }
func (w webUser) WebAuthnName() string                       { return w.u.Username }
func (w webUser) WebAuthnDisplayName() string                { return w.u.DisplayName() }
func (w webUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

// PasskeysAvailable reports whether passkeys work for a browser at origin,
// and if not, the error that says why.
func (s *Service) PasskeysAvailable(origin string) error {
	_, err := s.relyingParty(origin)
	return err
}

// relyingParty returns the WebAuthn relying party for a browser at origin.
// Passkeys are bound to the panel's name, or the name the browser used when
// the panel has none set; addresses can't have passkeys, and outside
// localhost they need HTTPS.
func (s *Service) relyingParty(origin string) (*webauthn.WebAuthn, error) {
	unavailable := auth.ErrPasskeysUnavailable.New("hostname", s.hostnameHint())
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" {
		return nil, unavailable
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil {
		return nil, unavailable
	}
	rpID := host
	if name := strings.ToLower(s.opts.Hostname); name != "" && host != "localhost" {
		if host != name && !strings.HasSuffix(host, "."+name) {
			return nil, unavailable
		}
		rpID = name
	}
	if u.Scheme != "https" && host != "localhost" {
		return nil, unavailable
	}
	return webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: s.opts.Issuer, RPOrigins: []string{u.Scheme + "://" + u.Host},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification: protocol.VerificationRequired,
		},
	})
}

func (s *Service) hostnameHint() string {
	if s.opts.Hostname != "" {
		return s.opts.Hostname
	}
	return "panel.example.com"
}

func (s *Service) webUserOf(ctx context.Context, u *User) (webUser, []*Passkey, error) {
	keys, err := s.opts.Store.Passkeys(ctx, u.ID)
	if err != nil {
		return webUser{}, nil, err
	}
	w := webUser{u: u}
	for _, k := range keys {
		var c webauthn.Credential
		if err := json.Unmarshal(k.Credential, &c); err != nil {
			return webUser{}, nil, fmt.Errorf("identity: passkey %s: %w", k.ID, err)
		}
		w.creds = append(w.creds, c)
	}
	return w, keys, nil
}

func credentialID(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }

func passkeyError(err error) error {
	if i18n.From(err).Code != i18n.ErrInternal.Code {
		return err
	}
	reason := "the browser's answer didn't check out"
	var perr *protocol.Error
	if ok := asProtocolError(err, &perr); ok && perr.Details != "" {
		reason = strings.TrimSuffix(perr.Details, ".")
	}
	return auth.ErrPasskeyFailed.Wrap(err, "reason", reason)
}

func asProtocolError(err error, target **protocol.Error) bool {
	for err != nil {
		if p, ok := err.(*protocol.Error); ok {
			*target = p
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// BeginPasskeyRegistration starts adding a passkey to the actor's user. The
// options go to navigator.credentials.create().
func (s *Service) BeginPasskeyRegistration(ctx context.Context, actor *auth.Actor, origin string) (string, json.RawMessage, error) {
	if err := requirePerson(actor); err != nil {
		return "", nil, err
	}
	if actor.Impersonator != "" {
		return "", nil, auth.ErrImpersonating.New("user", actor.Name, "action", "add passkeys")
	}
	rp, err := s.relyingParty(origin)
	if err != nil {
		return "", nil, err
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return "", nil, err
	}
	w, keys, err := s.webUserOf(ctx, u)
	if err != nil {
		return "", nil, err
	}
	if len(keys) >= maxPasskeys {
		return "", nil, auth.ErrTooMany.New("count", itoa(len(keys)), "what", "passkeys")
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range w.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, session, err := rp.BeginRegistration(w, webauthn.WithExclusions(exclude))
	if err != nil {
		return "", nil, passkeyError(err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		return "", nil, err
	}
	options, err := json.Marshal(creation)
	if err != nil {
		return "", nil, err
	}
	return s.challenges.put(&challenge{user: u.ID, purpose: "passkey-register", data: data}), options, nil
}

// FinishPasskeyRegistration keeps the passkey the browser made.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, actor *auth.Actor, ceremony, name string, response json.RawMessage, origin string) (PasskeyView, error) {
	if err := requirePerson(actor); err != nil {
		return PasskeyView{}, err
	}
	c, err := s.challenges.get(ceremony, "passkey-register")
	if err != nil {
		return PasskeyView{}, err
	}
	if !s.challenges.consume(ceremony) || c.user != actor.Person() {
		return PasskeyView{}, auth.ErrChallengeExpired.New()
	}
	rp, err := s.relyingParty(origin)
	if err != nil {
		return PasskeyView{}, err
	}
	u, err := s.User(ctx, c.user)
	if err != nil {
		return PasskeyView{}, err
	}
	w, _, err := s.webUserOf(ctx, u)
	if err != nil {
		return PasskeyView{}, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(c.data, &session); err != nil {
		return PasskeyView{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return PasskeyView{}, passkeyError(err)
	}
	cred, err := rp.CreateCredential(w, session, parsed)
	if err != nil {
		return PasskeyView{}, passkeyError(err)
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return PasskeyView{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	now := s.now()
	p := &Passkey{ID: store.NewID(PrefixPasskey), User: u.ID, Name: truncate(name, 64), CredentialID: credentialID(cred.ID), Credential: raw, CreatedAt: now}
	if err := s.opts.Store.AddPasskey(ctx, p); err != nil {
		return PasskeyView{}, err
	}
	s.refresh(u.ID, "passkey")
	s.audit(ctx, actor, "core.user.add_passkey", u.ID, u.Home, events.OutcomeSuccess, "", "Add the passkey "+p.Name)
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" added a passkey.", UserEvent{Username: u.Username, Detail: "passkey added: " + p.Name})
	s.alert(ctx, u, "A passkey was added", "The passkey "+p.Name+" can now sign in as "+u.Username+". If you didn't add it, remove it from your security settings and change your password.", events.SeverityNotice)
	return passkeyView(p), nil
}

// MyPasskeys lists the actor's passkeys.
func (s *Service) MyPasskeys(ctx context.Context, actor *auth.Actor) ([]PasskeyView, error) {
	keys, err := s.opts.Store.Passkeys(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	out := make([]PasskeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, passkeyView(k))
	}
	return out, nil
}

// RenamePasskey gives one of the actor's passkeys a new name.
func (s *Service) RenamePasskey(ctx context.Context, actor *auth.Actor, id store.ID, name string) (PasskeyView, error) {
	if err := requirePerson(actor); err != nil {
		return PasskeyView{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return PasskeyView{}, i18n.Invalid(i18n.Field("name", i18n.ErrBadValue, "reason", "use 1 to 64 characters"))
	}
	keys, err := s.opts.Store.Passkeys(ctx, actor.Person())
	if err != nil {
		return PasskeyView{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			k.Name = name
			if err := s.opts.Store.UpdatePasskey(ctx, k); err != nil {
				return PasskeyView{}, err
			}
			return passkeyView(k), nil
		}
	}
	return PasskeyView{}, notFound(string(id))
}

// RemovePasskey removes one of the actor's passkeys, unless it is the last
// way they have to sign in.
func (s *Service) RemovePasskey(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "remove passkeys")
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	keys, err := s.opts.Store.Passkeys(ctx, u.ID)
	if err != nil {
		return err
	}
	var name string
	for _, k := range keys {
		if k.ID == id {
			name = k.Name
		}
	}
	if name == "" {
		return notFound(string(id))
	}
	if u.PasswordHash == "" && len(keys) == 1 && !s.linked(ctx, u) {
		return auth.ErrLastSignInMethod.New()
	}
	if err := s.opts.Store.DeletePasskey(ctx, u.ID, id); err != nil {
		return err
	}
	s.refresh(u.ID, "passkey")
	s.audit(ctx, actor, "core.user.remove_passkey", u.ID, u.Home, events.OutcomeSuccess, "", "Remove the passkey "+name)
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" removed a passkey.", UserEvent{Username: u.Username, Detail: "passkey removed: " + name})
	s.alert(ctx, u, "A passkey was removed", "The passkey "+name+" no longer signs in as "+u.Username+".", events.SeverityNotice)
	return nil
}

// linked reports whether u signs in through an outside provider.
func (s *Service) linked(ctx context.Context, u *User) bool {
	links, err := s.opts.Store.Links(ctx, u.ID)
	return err == nil && len(links) > 0
}

// BeginPasskeySignIn starts signing in with a passkey alone: the browser
// offers the passkeys it holds for the panel, and the one chosen says whose
// it is. The options go to navigator.credentials.get().
func (s *Service) BeginPasskeySignIn(ctx context.Context, origin string, client auth.Client) (string, json.RawMessage, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return "", nil, err
	}
	rp, err := s.relyingParty(origin)
	if err != nil {
		return "", nil, err
	}
	assertion, session, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, passkeyError(err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		return "", nil, err
	}
	options, err := json.Marshal(assertion)
	if err != nil {
		return "", nil, err
	}
	return s.challenges.put(&challenge{purpose: "passkey-signin", data: data}), options, nil
}

// FinishPasskeySignIn signs in with the passkey the browser chose. A
// passkey that checks the person's fingerprint, face or PIN is both steps
// at once, and works while failed passwords lock the user out.
func (s *Service) FinishPasskeySignIn(ctx context.Context, ceremony string, response json.RawMessage, origin string, remember bool, client auth.Client) (*SignInResult, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	c, err := s.challenges.get(ceremony, "passkey-signin")
	if err != nil {
		return nil, err
	}
	if !s.challenges.consume(ceremony) {
		return nil, auth.ErrChallengeExpired.New()
	}
	rp, err := s.relyingParty(origin)
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(c.data, &session); err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, passkeyError(err)
	}
	var found *User
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		key, err := s.opts.Store.PasskeyByCredential(ctx, credentialID(rawID))
		if err != nil {
			return nil, auth.ErrPasskeyUnknown.New()
		}
		if string(userHandle) != string(key.User) {
			return nil, auth.ErrPasskeyUnknown.New()
		}
		u, err := s.User(ctx, key.User)
		if err != nil {
			return nil, auth.ErrPasskeyUnknown.New()
		}
		w, _, err := s.webUserOf(ctx, u)
		if err != nil {
			return nil, err
		}
		found = u
		return w, nil
	}
	_, cred, err := rp.ValidatePasskeyLogin(handler, session, parsed)
	if err != nil {
		if found != nil {
			s.audit(ctx, signInActor(found, found.Username, client), "auth.sign_in", found.ID, found.Home, events.OutcomeFailed, "the passkey didn't check out", "Sign in as "+found.Username)
		}
		if auth.ErrPasskeyUnknown.Is(err) {
			return nil, err
		}
		return nil, passkeyError(err)
	}
	if err := s.usedPasskey(ctx, found, cred); err != nil {
		return nil, err
	}
	if err := s.admit(ctx, found, "passkey", client); err != nil {
		return nil, err
	}
	issued, err := s.signedIn(ctx, found, "passkey", "", remember, client, true)
	if err != nil {
		return nil, err
	}
	return &SignInResult{Issued: issued, User: found}, nil
}

// usedPasskey keeps what an assertion changed about a passkey, its counter
// and flags, and refuses one whose counter says it was copied.
func (s *Service) usedPasskey(ctx context.Context, u *User, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		s.opts.Log.Warn("a passkey's counter went backwards, so it may have been copied", "user", u.ID)
		return auth.ErrPasskeyFailed.New("reason", "the passkey's counter says it may have been copied")
	}
	key, err := s.opts.Store.PasskeyByCredential(ctx, credentialID(cred.ID))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	key.Credential, key.LastUsedAt = raw, s.now()
	return s.opts.Store.UpdatePasskey(ctx, key)
}

// BeginSecondStepPasskey asks for one of the user's passkeys as the second
// step of a sign-in.
func (s *Service) BeginSecondStepPasskey(ctx context.Context, challengeSecret, origin string) (json.RawMessage, error) {
	c, err := s.challenges.get(challengeSecret, "signin")
	if err != nil {
		return nil, err
	}
	u, err := s.User(ctx, c.user)
	if err != nil {
		return nil, auth.ErrChallengeExpired.New()
	}
	options, data, err := s.beginAssertion(ctx, u, origin)
	if err != nil {
		return nil, err
	}
	if err := s.challenges.setData(challengeSecret, "signin", data); err != nil {
		return nil, err
	}
	return options, nil
}

// beginAssertion asks the browser for one of u's passkeys.
func (s *Service) beginAssertion(ctx context.Context, u *User, origin string) (json.RawMessage, []byte, error) {
	rp, err := s.relyingParty(origin)
	if err != nil {
		return nil, nil, err
	}
	w, _, err := s.webUserOf(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	if len(w.creds) == 0 {
		return nil, nil, auth.ErrPasskeyUnknown.New()
	}
	assertion, session, err := rp.BeginLogin(w, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return nil, nil, passkeyError(err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		return nil, nil, err
	}
	options, err := json.Marshal(assertion)
	return options, data, err
}

// finishPasskey checks a passkey answer for u against the ceremony a
// challenge kept, which answers once.
func (s *Service) finishPasskey(ctx context.Context, u *User, data []byte, response json.RawMessage, origin string) (*webauthn.Credential, error) {
	if len(data) == 0 {
		return nil, auth.ErrChallengeExpired.New()
	}
	rp, err := s.relyingParty(origin)
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, passkeyError(err)
	}
	w, _, err := s.webUserOf(ctx, u)
	if err != nil {
		return nil, err
	}
	cred, err := rp.ValidateLogin(w, session, parsed)
	if err != nil {
		return nil, passkeyError(err)
	}
	if err := s.usedPasskey(ctx, u, cred); err != nil {
		return nil, err
	}
	return cred, nil
}
