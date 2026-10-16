package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// SignInInput is a sign-in with a username and password.
type SignInInput struct {
	Username string `json:"username" maxLength:"254" doc:"The username"`
	Password string `json:"password" maxLength:"1024" doc:"The password"`
	// Remember keeps the session when the browser closes.
	Remember bool `json:"remember,omitempty" doc:"Stay signed in when the browser closes, until the session's time is up"`
}

// SignInResult is how far signing in got: signed in, or waiting for a
// second step.
type SignInResult struct {
	Issued *Issued
	// Challenge identifies the sign-in while it waits for a second step.
	Challenge string
	// Methods are the second steps the person can take: totp, passkey,
	// recovery and mfa:<name>.
	Methods []string
	// Titles name the methods add-ons provide, by method, for the screen
	// that offers them.
	Titles map[string]string
	User   *User
}

// LoginAttempt is what login policies at auth.login.validate decide on.
type LoginAttempt struct {
	User      store.ID `json:"user"`
	Username  string   `json:"username"`
	Method    string   `json:"method" doc:"password, passkey, provider:<name>, setup or invite"`
	IP        string   `json:"ip"`
	UserAgent string   `json:"userAgent"`
	// Grants are the user's, for policies that only let some roles in.
	Grants []auth.Grant `json:"grants"`
}

// signInActor is how an attempt shows in the audit log before anyone is
// signed in.
func signInActor(u *User, username string, client auth.Client) *auth.Actor {
	a := &auth.Actor{Type: auth.ActorUser, Name: username, Source: auth.SourceUI, IP: client.IP}
	if u != nil {
		a.ID, a.User, a.Name = u.ID, u.ID, u.Username
	}
	return a
}

// SignIn signs someone in with a username and password. When their user
// takes a second step, and the browser isn't trusted to skip it, the result
// carries a challenge instead of a session. deviceSecret is the browser's
// trusted-device cookie, if it has one.
func (s *Service) SignIn(ctx context.Context, in SignInInput, deviceSecret string, client auth.Client) (*SignInResult, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	if !loopback(client.IP) && !Allowed(s.Policy().Networks(), client.IP) {
		return nil, auth.ErrAddressNotAllowed.New("address", client.IP, "what", "the panel")
	}
	username := NormalizeUsername(in.Username)
	u, err := s.opts.Store.UserByName(ctx, username)
	switch {
	case store.ErrNotFound.Is(err):
		u = nil
	case err != nil:
		return nil, err
	}
	if u == nil || u.PasswordHash == "" {
		// People who sign in through an outside directory have no password
		// here; nobody else gets told whether the name exists.
		linked, perr := s.signInWithProviders(ctx, username, in.Password, u)
		if perr != nil {
			if !auth.ErrBadCredentials.Is(perr) {
				return nil, perr
			}
			if u == nil {
				auth.WasteTime(ctx, in.Password)
			}
			s.audit(ctx, signInActor(u, username, client), "auth.sign_in", idOf(u), homeScope(u), events.OutcomeFailed, "wrong username or password", "Sign in as "+username)
			if u != nil {
				s.failed(ctx, u, client)
			}
			return nil, auth.ErrBadCredentials.New()
		}
		return s.afterFirstStep(ctx, linked.user, linked.method, in.Remember, deviceSecret, client)
	}
	if u.Locked(s.now()) {
		s.audit(ctx, signInActor(u, username, client), "auth.sign_in", u.ID, u.Home, events.OutcomeDenied, "locked out", "Sign in as "+username)
		return nil, s.lockedError(u)
	}
	ok, rehash, err := auth.VerifyPassword(ctx, u.PasswordHash, in.Password)
	if errors.Is(err, auth.ErrHashFormat) {
		// A hash nothing can check is a password nobody can use.
		s.opts.Log.Warn("a user's password hash is in a format Racksail doesn't read", "user", u.ID)
		ok, err = false, nil
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		s.audit(ctx, signInActor(u, username, client), "auth.sign_in", u.ID, u.Home, events.OutcomeFailed, "wrong password", "Sign in as "+username)
		s.failed(ctx, u, client)
		return nil, auth.ErrBadCredentials.New()
	}
	if rehash {
		if hash, err := auth.HashPassword(ctx, in.Password); err == nil {
			if err := s.opts.Store.SetPassword(ctx, u.ID, hash, u.MustChangePassword, u.PasswordChangedAt); err != nil {
				s.opts.Log.Warn("replacing a password hash", "user", u.ID, "error", err)
			}
		}
	}
	return s.afterFirstStep(ctx, u, "password", in.Remember, deviceSecret, client)
}

func idOf(u *User) store.ID {
	if u == nil {
		return ""
	}
	return u.ID
}

func homeScope(u *User) store.Scope {
	if u == nil {
		return store.ServerScope()
	}
	return u.Home
}

func (s *Service) lockedError(u *User) error {
	minutes := int(u.LockedUntil.Sub(s.now()).Minutes()) + 1
	return auth.ErrLocked.New("minutes", itoa(minutes))
}

// failed counts a failed attempt against a user, and locks them out once
// they reach the policy's limit.
func (s *Service) failed(ctx context.Context, u *User, client auth.Client) {
	p := s.Policy()
	failures := u.FailedSignIns + 1
	var lockedUntil time.Time
	if failures >= p.LockoutAttempts {
		lockedUntil = s.now().Add(p.Lockout())
		failures = 0
	}
	if err := s.opts.Store.RecordSignIn(ctx, u.ID, failures, lockedUntil, time.Time{}, ""); err != nil {
		s.opts.Log.Warn("recording a failed sign-in", "user", u.ID, "error", err)
	}
	// The log line is what fail2ban-style bans of the network module read.
	s.opts.Log.Warn("sign-in failed", "user", u.Username, "address", client.IP)
	if !lockedUntil.IsZero() {
		s.publish(ctx, UserLockedOut, u, signInActor(u, u.Username, client), u.Username+" was locked out after failed sign-ins.",
			UserEvent{Username: u.Username, IP: client.IP, Device: DescribeAgent(client.UserAgent)})
		s.alert(ctx, u, "Your user was locked out",
			"Someone failed to sign in as "+u.Username+" "+itoa(p.LockoutAttempts)+" times in a row, from "+client.IP+
				". Signing in with a password is blocked for "+itoa(p.LockoutMinutes)+" minutes; passkeys still work. If it wasn't you, choose a stronger password.",
			events.SeverityWarning)
	}
}

// afterFirstStep goes on once the first step checked out: the login
// policies, then a second step or the session.
func (s *Service) afterFirstStep(ctx context.Context, u *User, method string, remember bool, deviceSecret string, client auth.Client) (*SignInResult, error) {
	if err := s.admit(ctx, u, method, client); err != nil {
		return nil, err
	}
	factors := s.secondFactors(ctx, u)
	if len(factors) > 0 {
		if d := s.trustedDevice(ctx, u, deviceSecret, client); d != nil {
			issued, err := s.signedIn(ctx, u, method, "device", remember, client, false)
			return &SignInResult{Issued: issued, User: u}, err
		}
		secret := s.challenges.put(&challenge{user: u.ID, purpose: "signin", method: method, persistent: remember})
		return &SignInResult{Challenge: secret, Methods: factors, Titles: s.methodTitles(ctx, factors), User: u}, nil
	}
	issued, err := s.signedIn(ctx, u, method, "", remember, client, true)
	return &SignInResult{Issued: issued, User: u}, err
}

// admit applies what keeps a user out even with the right credentials: a
// user turned off, the allowlists and login policies at
// auth.login.validate.
func (s *Service) admit(ctx context.Context, u *User, method string, client auth.Client) error {
	actor := signInActor(u, u.Username, client)
	if u.Disabled {
		s.audit(ctx, actor, "auth.sign_in", u.ID, u.Home, events.OutcomeDenied, "the user is turned off", "Sign in as "+u.Username)
		return auth.ErrDisabled.New()
	}
	if err := s.checkAddress(u, client.IP); err != nil {
		s.audit(ctx, actor, "auth.sign_in", u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Sign in as "+u.Username)
		return err
	}
	if s.opts.Hooks != nil {
		attempt := LoginAttempt{User: u.ID, Username: u.Username, Method: method, IP: client.IP, UserAgent: client.UserAgent, Grants: u.Clone().Grants}
		if err := s.opts.Hooks.Check(ctx, hooks.AuthLoginValidate, actor, attempt); err != nil {
			s.audit(ctx, actor, "auth.sign_in", u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Sign in as "+u.Username)
			return err
		}
	}
	return nil
}

// methodTitles names the second steps add-ons provide, from their
// providers' titles.
func (s *Service) methodTitles(ctx context.Context, methods []string) map[string]string {
	var out map[string]string
	for _, m := range methods {
		id, ok := strings.CutPrefix(m, "mfa:")
		if !ok {
			continue
		}
		p, err := s.provider(ctx, store.ID(id), "mfa")
		if err != nil {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[m] = p.Spec.Title
	}
	return out
}

// secondFactors lists the second steps u has set up.
func (s *Service) secondFactors(ctx context.Context, u *User) []string {
	var out []string
	if u.TOTP {
		out = append(out, "totp")
	}
	if keys, err := s.opts.Store.Passkeys(ctx, u.ID); err == nil && len(keys) > 0 {
		out = append(out, "passkey")
	}
	if methods, err := s.opts.Store.MFAMethods(ctx, u.ID); err == nil {
		for _, m := range methods {
			out = append(out, "mfa:"+m.Method)
		}
	}
	if len(out) > 0 {
		if n, err := s.opts.Store.RecoveryCodesLeft(ctx, u.ID); err == nil && n > 0 {
			out = append(out, "recovery")
		}
	}
	return out
}

// trustedDevice returns the browser's trusted device, when it belongs to u
// and hasn't expired.
func (s *Service) trustedDevice(ctx context.Context, u *User, secret string, client auth.Client) *Device {
	if secret == "" || !strings.HasPrefix(secret, auth.DevicePrefix) || s.Policy().RememberDeviceDays == 0 {
		return nil
	}
	d, err := s.opts.Store.DeviceByHash(ctx, auth.HashSecret(secret))
	if err != nil || d.User != u.ID || s.now().After(d.ExpiresAt) {
		return nil
	}
	if err := s.opts.Store.TouchDevice(ctx, d.ID, s.now(), client.IP); err != nil {
		s.opts.Log.Warn("recording a trusted browser's use", "device", d.ID, "error", err)
	}
	return d
}

// signedIn starts a session for someone who got through every step,
// records it and sends the sign-in alert. reset clears failed attempts.
func (s *Service) signedIn(ctx context.Context, u *User, method, factor string, persistent bool, client auth.Client, reset bool) (*Issued, error) {
	newDevice := s.newDevice(ctx, u, factor, client)
	issued, err := s.issue(ctx, u, method, factor, persistent, client, nil)
	if err != nil {
		return nil, err
	}
	if err := s.opts.Store.RecordSignIn(ctx, u.ID, 0, time.Time{}, s.now(), client.IP); err != nil {
		s.opts.Log.Warn("recording a sign-in", "user", u.ID, "error", err)
	}
	actor := signInActor(u, u.Username, client)
	how := method
	if factor != "" {
		how += " and " + factor
	}
	s.audit(ctx, actor, "auth.sign_in", u.ID, u.Home, events.OutcomeSuccess, "", "Sign in with "+how+" from "+DescribeAgent(client.UserAgent))
	s.publish(ctx, UserSignedIn, u, actor, u.Username+" signed in.", SignInEvent{
		Username: u.Username, Method: method, SecondFactor: factor, IP: client.IP, Device: DescribeAgent(client.UserAgent), NewDevice: newDevice,
	})
	switch s.Policy().SignInAlerts {
	case "every":
		s.signInAlert(ctx, u, client)
	case "new-device":
		if newDevice {
			s.signInAlert(ctx, u, client)
		}
	}
	return issued, nil
}

// newDevice reports whether someone signs in from a browser and address
// their user hasn't been seen with.
func (s *Service) newDevice(ctx context.Context, u *User, factor string, client auth.Client) bool {
	if factor == "device" || u.LastSignInIP == client.IP {
		return false
	}
	if u.LastSignInAt.IsZero() {
		// The first sign-in ever isn't news.
		return false
	}
	sessions, err := s.opts.Store.Sessions(ctx, u.ID)
	if err != nil {
		return false
	}
	agent := DescribeAgent(client.UserAgent)
	for _, x := range sessions {
		if x.IP == client.IP && DescribeAgent(x.UserAgent) == agent {
			return false
		}
	}
	return true
}

func (s *Service) signInAlert(ctx context.Context, u *User, client auth.Client) {
	s.alert(ctx, u, "New sign-in to your account",
		u.Username+" signed in from "+DescribeAgent(client.UserAgent)+" at "+client.IP+", "+s.now().Format(time.RFC1123)+
			". If it wasn't you, change your password and sign out everywhere else from your security settings.",
		events.SeverityNotice)
}

// SecondStepInput is the second step of a sign-in.
type SecondStepInput struct {
	Challenge string `json:"challenge" doc:"The challenge the first step answered with"`
	// Method is totp, recovery, passkey or mfa:<name>.
	Method string `json:"method" pattern:"^(totp|recovery|passkey|mfa:[a-z0-9_]+)$" doc:"The second step: totp, recovery, passkey or mfa:<name>"`
	// Code is the authenticator app's code, a recovery code, or the
	// response an add-on's method asks for.
	Code string `json:"code,omitempty" maxLength:"64" doc:"The code from the authenticator app, a recovery code, or what an add-on's method asked for"`
	// Credential is the browser's answer to a passkey request, as
	// PublicKeyCredential.toJSON() gives it.
	Credential json.RawMessage `json:"credential,omitempty" doc:"The passkey's answer, as the browser's PublicKeyCredential.toJSON() gives it"`
	// Trust lets this browser skip the second step for a while.
	Trust bool `json:"trust,omitempty" doc:"Trust this browser, so it skips the second step for the days the sign-in policy says"`
}

// SecondStep finishes a sign-in that waited for a second step. Wrong codes
// count towards the user's lockout, and a challenge takes five at most.
func (s *Service) SecondStep(ctx context.Context, in SecondStepInput, client auth.Client, origin string) (*SignInResult, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	c, err := s.challenges.get(in.Challenge, "signin")
	if err != nil {
		return nil, err
	}
	u, err := s.opts.Store.User(ctx, c.user)
	if err != nil {
		s.challenges.drop(in.Challenge)
		return nil, auth.ErrChallengeExpired.New()
	}
	if u.Locked(s.now()) && in.Method != "passkey" {
		return nil, s.lockedError(u)
	}
	var factor string
	switch {
	case in.Method == "totp":
		err = s.checkTOTP(ctx, u, in.Code)
		factor = "totp"
	case in.Method == "recovery":
		err = s.useRecoveryCode(ctx, u, in.Code, client)
		factor = "recovery"
	case in.Method == "passkey":
		_, err = s.finishPasskey(ctx, u, c, in.Credential, origin)
		factor = "passkey"
	case strings.HasPrefix(in.Method, "mfa:"):
		err = s.verifyMFA(ctx, u, c, strings.TrimPrefix(in.Method, "mfa:"), in.Code)
		factor = in.Method
	default:
		return nil, i18n.Invalid(i18n.Field("method", i18n.ErrBadValue, "reason", "use totp, recovery, passkey or mfa:<name>"))
	}
	if auth.ErrPending.Is(err) {
		return nil, err
	}
	if err != nil {
		s.challenges.fail(in.Challenge)
		s.audit(ctx, signInActor(u, u.Username, client), "auth.sign_in", u.ID, u.Home, events.OutcomeFailed, "wrong "+in.Method, "Sign in as "+u.Username)
		s.failed(ctx, u, client)
		return nil, err
	}
	s.challenges.drop(in.Challenge)
	if err := s.admit(ctx, u, c.method, client); err != nil {
		return nil, err
	}
	issued, err := s.signedIn(ctx, u, c.method, factor, c.persistent, client, true)
	if err != nil {
		return nil, err
	}
	if in.Trust {
		if err := s.trust(ctx, u, issued, client); err != nil {
			return nil, err
		}
	}
	return &SignInResult{Issued: issued, User: u}, nil
}

// trust remembers the browser, so it skips the second step until the
// policy's days run out.
func (s *Service) trust(ctx context.Context, u *User, issued *Issued, client auth.Client) error {
	days := s.Policy().RememberDevice()
	if days == 0 {
		return nil
	}
	now := s.now()
	secret := auth.NewSecret(auth.DevicePrefix)
	d := &Device{
		ID: store.NewID(PrefixDevice), User: u.ID, Hash: auth.HashSecret(secret), Name: DescribeAgent(client.UserAgent), IP: client.IP,
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(days),
	}
	if err := s.opts.Store.AddDevice(ctx, d); err != nil {
		return err
	}
	issued.DeviceSecret, issued.DeviceExpires = secret, d.ExpiresAt
	return nil
}

// useRecoveryCode signs someone in with one of their recovery codes, which
// then no longer works, and tells them how many are left.
func (s *Service) useRecoveryCode(ctx context.Context, u *User, code string, client auth.Client) error {
	ok, err := s.opts.Store.UseRecoveryCode(ctx, u.ID, auth.HashCode(code))
	if err != nil {
		return err
	}
	if !ok {
		return auth.ErrBadCode.New()
	}
	left, _ := s.opts.Store.RecoveryCodesLeft(ctx, u.ID)
	s.publish(ctx, UserRecoveryUsed, u, signInActor(u, u.Username, client), u.Username+" signed in with a recovery code.",
		UserEvent{Username: u.Username, Detail: itoa(left) + " left", IP: client.IP, Device: DescribeAgent(client.UserAgent)})
	s.alert(ctx, u, "A recovery code was used",
		"Someone signed in as "+u.Username+" with a recovery code from "+client.IP+". You have "+itoa(left)+
			" left. If it wasn't you, change your password and make new recovery codes.", events.SeverityWarning)
	return nil
}
