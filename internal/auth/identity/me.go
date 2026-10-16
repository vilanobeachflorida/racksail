package identity

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Names of the secrets kept for each user, under the user's ID.
const (
	totpSecret    = "totp"
	totpPending   = "totp.pending"
	alertSettings = "alerts"
	// recoverySecret is the key the user's recovery codes are hashed with,
	// so their hashes in the panel database give nothing away on their own.
	recoverySecret = "recovery.key"
)

// userSecrets are the secrets kept for a user, which go with them.
var userSecrets = []string{totpSecret, totpPending, alertSettings, recoverySecret}

func secretRef(user store.ID, name string) secrets.Ref { return secrets.NewRef(user, name) }

// Me is the signed-in person's own view of their user.
type Me struct {
	UserView
	Security Security `json:"security" doc:"How the person signs in"`
}

// Security is how someone signs in.
type Security struct {
	TOTP          bool     `json:"totp" doc:"Whether an authenticator app is set up"`
	Passkeys      int      `json:"passkeys" doc:"How many passkeys can sign in"`
	RecoveryCodes int      `json:"recoveryCodes" doc:"How many unused recovery codes are left"`
	Methods       []string `json:"methods" doc:"Second steps from add-ons that are on"`
	Devices       int      `json:"devices" doc:"How many browsers are trusted to skip the second step"`
	Links         []string `json:"links" doc:"The outside providers the person can sign in with"`
	// Required says the policy asks the person's role for a second step.
	Required     bool   `json:"required" doc:"Whether the sign-in policy requires a second step for the person's role"`
	AlertChannel string `json:"alertChannel,omitempty" doc:"Where sign-in alerts go"`
}

// MyProfile returns the actor's own user.
func (s *Service) MyProfile(ctx context.Context, actor *auth.Actor) (Me, error) {
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return Me{}, err
	}
	sec, err := s.security(ctx, u)
	if err != nil {
		return Me{}, err
	}
	return Me{UserView: s.view(u, sec.TOTP || sec.Passkeys > 0 || len(sec.Methods) > 0), Security: sec}, nil
}

func (s *Service) security(ctx context.Context, u *User) (Security, error) {
	st := s.opts.Store
	sec := Security{TOTP: u.TOTP, Methods: []string{}, Links: []string{}, Required: s.needsTwoFactor(u), AlertChannel: u.AlertChannel}
	keys, err := st.Passkeys(ctx, u.ID)
	if err != nil {
		return sec, err
	}
	sec.Passkeys = len(keys)
	if sec.RecoveryCodes, err = st.RecoveryCodesLeft(ctx, u.ID); err != nil {
		return sec, err
	}
	methods, err := st.MFAMethods(ctx, u.ID)
	if err != nil {
		return sec, err
	}
	for _, m := range methods {
		sec.Methods = append(sec.Methods, m.Method)
	}
	devices, err := st.Devices(ctx, u.ID)
	if err != nil {
		return sec, err
	}
	sec.Devices = len(devices)
	links, err := st.Links(ctx, u.ID)
	if err != nil {
		return sec, err
	}
	for _, l := range links {
		sec.Links = append(sec.Links, string(l.Provider))
	}
	return sec, nil
}

// UpdateProfile is the change that sets the actor's own name, email
// address and language.
func (s *Service) UpdateProfile(actor *auth.Actor, in ProfileInput) (changeset.Request, error) {
	if err := ownChange(actor, "change their profile"); err != nil {
		return changeset.Request{}, err
	}
	if fields := checkProfile(in, ""); len(fields) > 0 {
		return changeset.Request{}, i18n.Invalid(fields...)
	}
	in.Name, in.Email, in.Locale = strings.TrimSpace(in.Name), strings.TrimSpace(in.Email), strings.TrimSpace(in.Locale)
	input, err := json.Marshal(in)
	if err != nil {
		return changeset.Request{}, err
	}
	return changeset.Request{
		Title:   "Change your profile",
		Changes: []changeset.Change{{Op: changeset.OpAction, Kind: KindUser, ID: actor.Person(), Action: "update_profile", Input: input}},
	}, nil
}

// Preferences returns the display preferences the actor keeps.
func (s *Service) Preferences(ctx context.Context, actor *auth.Actor) (json.RawMessage, error) {
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	return u.Preferences, nil
}

// SetPreferences keeps the actor's display preferences, which are theirs
// alone and nothing else about their user.
func (s *Service) SetPreferences(ctx context.Context, actor *auth.Actor, prefs json.RawMessage) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if len(prefs) > 8<<10 {
		return i18n.ErrTooLarge.New("limit", "8 KiB")
	}
	var check map[string]any
	if err := json.Unmarshal(prefs, &check); err != nil {
		return i18n.ErrBadRequest.New("reason", "preferences are a JSON object")
	}
	if _, err := s.User(ctx, actor.Person()); err != nil {
		return err
	}
	return s.opts.Store.SetPreferences(ctx, actor.Person(), prefs)
}

// PasswordInput changes the actor's password.
type PasswordInput struct {
	Current string `json:"current,omitempty" maxLength:"1024" doc:"Your current password; not needed when you have none, or when a sign-in link let you in to choose one"`
	New     string `json:"new" maxLength:"1024" doc:"The new password"`
	// EndOtherSessions signs the person out everywhere else.
	EndOtherSessions bool `json:"endOtherSessions,omitempty" doc:"Sign out everywhere else too"`
}

// ChangePassword sets a new password for the actor. Unless they have no
// password yet, or must choose a new one, the current one has to check
// out.
func (s *Service) ChangePassword(ctx context.Context, actor *auth.Actor, in PasswordInput) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "change their password")
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	if u.PasswordHash != "" && actor.Pending != PendingPassword {
		ok, _, err := auth.VerifyPassword(ctx, u.PasswordHash, in.Current)
		if err != nil && !errors.Is(err, auth.ErrHashFormat) {
			return err
		}
		if !ok {
			s.audit(ctx, actor, "core.user.change_password", u.ID, u.Home, events.OutcomeFailed, "wrong current password", "Change their password")
			s.failed(ctx, u, auth.Client{IP: actor.IP})
			return auth.ErrWrongPassword.New()
		}
	}
	if err := auth.CheckPasswordStrength(in.New, u.Username); err != nil {
		return i18n.Invalid(i18n.Field("new", auth.ErrWeakPassword, "reason", i18n.From(err).Params["reason"]))
	}
	if in.New == in.Current {
		return i18n.Invalid(i18n.Field("new", i18n.ErrBadValue, "reason", "it's the same as the current one"))
	}
	hash, err := auth.HashPassword(ctx, in.New)
	if err != nil {
		return err
	}
	if err := s.opts.Store.SetPassword(ctx, u.ID, hash, false, s.now()); err != nil {
		return err
	}
	s.refresh(u.ID, "password")
	if err := s.clearPending(ctx, actor, PendingPassword, u); err != nil {
		return err
	}
	if in.EndOtherSessions {
		s.opts.Store.DeleteSessions(ctx, u.ID, store.ID(actor.SessionID))
	}
	s.audit(ctx, actor, "core.user.change_password", u.ID, u.Home, events.OutcomeSuccess, "", "Change their password")
	s.publish(ctx, UserPasswordChanged, u, actor, u.Username+" changed their password.", UserEvent{Username: u.Username, IP: actor.IP})
	s.alert(ctx, u, "Your password was changed", "The password of "+u.Username+" was changed from "+actor.IP+
		". If you didn't change it, use a recovery code or ask an admin to help you back in.", events.SeverityNotice)
	return nil
}

// clearPending lifts what the session had to do first, once it is done,
// and works out whether something else is still due.
func (s *Service) clearPending(ctx context.Context, actor *auth.Actor, done string, u *User) error {
	if actor.SessionID == "" || actor.Pending != done {
		return nil
	}
	fresh, err := s.User(ctx, u.ID)
	if err != nil {
		return err
	}
	pending := s.pendingFor(ctx, fresh, s.secondFactors(ctx, fresh))
	if err := s.opts.Store.SetSessionPending(ctx, store.ID(actor.SessionID), pending); err != nil {
		return err
	}
	actor.Pending = pending
	return nil
}

// BeginTOTP starts setting up an authenticator app: the secret to scan or
// type in, which counts once a code from it checks out.
func (s *Service) BeginTOTP(ctx context.Context, actor *auth.Actor) (*auth.TOTPKey, error) {
	if err := requirePerson(actor); err != nil {
		return nil, err
	}
	if actor.Impersonator != "" {
		return nil, auth.ErrImpersonating.New("user", actor.Name, "action", "set up an authenticator app")
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	if u.TOTP {
		return nil, auth.ErrTOTPExists.New()
	}
	label := u.Username
	if s.opts.Hostname != "" {
		label += "@" + s.opts.Hostname
	}
	key, err := auth.NewTOTPKey(s.opts.Issuer, label)
	if err != nil {
		return nil, err
	}
	if err := s.opts.Secrets.Put(ctx, secretRef(u.ID, totpPending), []byte(key.Secret)); err != nil {
		return nil, err
	}
	return key, nil
}

// ConfirmTOTP turns the authenticator app on once a code from it checks
// out, and returns the person's first recovery codes when they have none.
func (s *Service) ConfirmTOTP(ctx context.Context, actor *auth.Actor, code string) ([]string, error) {
	if err := requirePerson(actor); err != nil {
		return nil, err
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	if u.TOTP {
		return nil, auth.ErrTOTPExists.New()
	}
	pending, err := s.opts.Secrets.Get(ctx, secretRef(u.ID, totpPending))
	if err != nil {
		return nil, auth.ErrTOTPNotStarted.New()
	}
	step, ok := auth.CheckTOTP(string(pending), code, s.now(), 0)
	if !ok {
		return nil, auth.ErrBadCode.New()
	}
	if err := s.opts.Secrets.Put(ctx, secretRef(u.ID, totpSecret), pending); err != nil {
		return nil, err
	}
	s.opts.Secrets.Delete(ctx, secretRef(u.ID, totpPending))
	if err := s.opts.Store.SetTOTP(ctx, u.ID, true, step); err != nil {
		return nil, err
	}
	s.refresh(u.ID, "two-factor")
	var codes []string
	if left, err := s.opts.Store.RecoveryCodesLeft(ctx, u.ID); err == nil && left == 0 {
		if codes, err = s.newRecoveryCodes(ctx, u.ID); err != nil {
			return nil, err
		}
	}
	u.TOTP = true
	if err := s.clearPending(ctx, actor, PendingTwoFactor, u); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "core.user.enable_totp", u.ID, u.Home, events.OutcomeSuccess, "", "Set up an authenticator app")
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" set up an authenticator app.", UserEvent{Username: u.Username, Detail: "authenticator app added"})
	s.alert(ctx, u, "An authenticator app was set up", "Signing in as "+u.Username+" now asks for a code from an authenticator app.", events.SeverityNotice)
	return codes, nil
}

// RemoveTOTP turns the actor's authenticator app off.
func (s *Service) RemoveTOTP(ctx context.Context, actor *auth.Actor) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "remove their authenticator app")
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	if !u.TOTP {
		return notFound("The authenticator app")
	}
	if err := s.opts.Store.SetTOTP(ctx, u.ID, false, 0); err != nil {
		return err
	}
	s.refresh(u.ID, "two-factor")
	s.opts.Secrets.Delete(ctx, secretRef(u.ID, totpSecret))
	u.TOTP = false
	if len(s.secondFactors(ctx, u)) == 0 {
		// Without any second step, recovery codes and trusted browsers
		// have nothing left to stand in for.
		s.opts.Store.ReplaceRecoveryCodes(ctx, u.ID, nil)
		s.opts.Store.DeleteDevices(ctx, u.ID)
	}
	s.audit(ctx, actor, "core.user.disable_totp", u.ID, u.Home, events.OutcomeSuccess, "", "Remove their authenticator app")
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" removed their authenticator app.", UserEvent{Username: u.Username, Detail: "authenticator app removed"})
	s.alert(ctx, u, "An authenticator app was removed", "Signing in as "+u.Username+" no longer asks for a code from an authenticator app.", events.SeverityWarning)
	return nil
}

// checkTOTP checks a code from u's authenticator app; each code counts once.
func (s *Service) checkTOTP(ctx context.Context, u *User, code string) error {
	if !u.TOTP {
		return auth.ErrBadCode.New()
	}
	secret, err := s.opts.Secrets.Get(ctx, secretRef(u.ID, totpSecret))
	if err != nil {
		return err
	}
	step, ok := auth.CheckTOTP(string(secret), code, s.now(), u.TOTPStep)
	if !ok {
		return auth.ErrBadCode.New()
	}
	if err := s.opts.Store.UseTOTPStep(ctx, u.ID, step); err != nil {
		if errors.Is(err, ErrStepUsed) {
			return auth.ErrBadCode.New()
		}
		return err
	}
	return nil
}

func (s *Service) newRecoveryCodes(ctx context.Context, user store.ID) ([]string, error) {
	key, err := s.recoveryKey(ctx, user, true)
	if err != nil {
		return nil, err
	}
	codes := auth.NewRecoveryCodes()
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.KeyedCodeHash(key, c)
	}
	if err := s.opts.Store.ReplaceRecoveryCodes(ctx, user, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// recoveryKey returns the key a user's recovery codes are hashed with, from
// the secrets store, making it first when create says so. Without one, and
// without create, it returns nil: the user has no codes that could match.
func (s *Service) recoveryKey(ctx context.Context, user store.ID, create bool) ([]byte, error) {
	if s.opts.Secrets == nil {
		return nil, i18n.ErrUnavailable.New("what", "The secrets store")
	}
	ref := secretRef(user, recoverySecret)
	key, err := s.opts.Secrets.Get(ctx, ref)
	switch {
	case err == nil && len(key) == auth.CodeKeySize:
		return key, nil
	case err != nil && !secrets.ErrNotFound.Is(err):
		return nil, err
	case !create:
		return nil, nil
	}
	key = auth.NewCodeKey()
	if err := s.opts.Secrets.Put(ctx, ref, key); err != nil {
		return nil, err
	}
	return key, nil
}

// NewRecoveryCodes replaces the actor's recovery codes. It takes a second
// step to be set up first, since the codes stand in for one.
func (s *Service) NewRecoveryCodes(ctx context.Context, actor *auth.Actor) ([]string, error) {
	if err := requirePerson(actor); err != nil {
		return nil, err
	}
	if actor.Impersonator != "" {
		return nil, auth.ErrImpersonating.New("user", actor.Name, "action", "make recovery codes")
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(s.secondFactors(ctx, u), func(f string) bool { return f != "recovery" }) {
		return nil, auth.ErrTOTPNotStarted.New()
	}
	codes, err := s.newRecoveryCodes(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "core.user.new_recovery_codes", u.ID, u.Home, events.OutcomeSuccess, "", "Make new recovery codes")
	s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+" made new recovery codes.", UserEvent{Username: u.Username, Detail: "new recovery codes"})
	return codes, nil
}

// StepUpInput confirms it's the person, for sensitive actions: their
// password, a code from their authenticator app, or a passkey.
type StepUpInput struct {
	Password   string          `json:"password,omitempty" maxLength:"1024" doc:"The person's password"`
	Code       string          `json:"code,omitempty" maxLength:"64" doc:"A code from the authenticator app"`
	Ceremony   string          `json:"ceremony,omitempty" doc:"The ceremony the passkey request started"`
	Credential json.RawMessage `json:"credential,omitempty" doc:"The passkey's answer, as PublicKeyCredential.toJSON() gives it"`
}

// stepUpUser is who confirms it's them: the person, or the admin viewing
// the panel as them.
func (s *Service) stepUpUser(ctx context.Context, actor *auth.Actor) (*User, error) {
	if actor.Type != auth.ActorUser || actor.SessionID == "" {
		return nil, auth.ErrStepUpUnavailable.New()
	}
	id := actor.ID
	if actor.Impersonator != "" {
		id = actor.Impersonator
	}
	return s.User(ctx, id)
}

// BeginStepUpPasskey asks for one of the person's passkeys to confirm it's
// them.
func (s *Service) BeginStepUpPasskey(ctx context.Context, actor *auth.Actor, origin string) (string, json.RawMessage, error) {
	u, err := s.stepUpUser(ctx, actor)
	if err != nil {
		return "", nil, err
	}
	options, data, err := s.beginAssertion(ctx, u, origin)
	if err != nil {
		return "", nil, err
	}
	return s.challenges.put(&challenge{user: u.ID, purpose: "passkey-step-up", data: data}), options, nil
}

// StepUp confirms it's the person, which lets them take sensitive actions
// for the minutes the policy says. The session gets a new secret, as with
// every change of what it may do.
func (s *Service) StepUp(ctx context.Context, actor *auth.Actor, in StepUpInput, client auth.Client, origin string) (*Issued, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	u, err := s.stepUpUser(ctx, actor)
	if err != nil {
		return nil, err
	}
	if u.Locked(s.now()) && in.Ceremony == "" {
		return nil, s.lockedError(u)
	}
	var how string
	switch {
	case in.Ceremony != "":
		c, cerr := s.challenges.get(in.Ceremony, "passkey-step-up")
		if cerr != nil {
			return nil, cerr
		}
		if !s.challenges.consume(in.Ceremony) || c.user != u.ID {
			return nil, auth.ErrChallengeExpired.New()
		}
		how = "passkey"
		_, err = s.finishPasskey(ctx, u, c.data, in.Credential, origin)
	case in.Code != "":
		err, how = s.checkTOTP(ctx, u, in.Code), "totp"
	case in.Password != "":
		how = "password"
		ok, _, verr := auth.VerifyPassword(ctx, u.PasswordHash, in.Password)
		switch {
		case verr != nil && !errors.Is(verr, auth.ErrHashFormat):
			return nil, verr
		case u.PasswordHash == "" || !ok:
			err = auth.ErrWrongPassword.New()
		}
	default:
		return nil, i18n.ErrBadRequest.New("reason", "send a password, a code or a passkey's answer")
	}
	if err != nil {
		s.audit(ctx, actor, "auth.step_up", u.ID, u.Home, events.OutcomeFailed, "wrong "+how, "Confirm it's them")
		s.failed(ctx, u, client)
		return nil, err
	}
	x, err := s.SessionOf(ctx, actor)
	if err != nil {
		return nil, err
	}
	if x.Parent != "" {
		// Viewing as someone, the confirmation is the impersonator's.
		if x, err = s.opts.Store.Session(ctx, x.Parent); err != nil {
			return nil, err
		}
	}
	// A copy of the cookie from before gains nothing from the confirmation.
	now := s.now()
	issued, err := s.newSecret(ctx, x, func(x *Session) { x.SteppedUpAt = now })
	if err != nil {
		return nil, err
	}
	if err := s.opts.Store.Unlock(ctx, u.ID); err != nil {
		s.opts.Log.Warn("clearing failed sign-ins", "user", u.ID, "error", err)
	}
	s.audit(ctx, actor, "auth.step_up", u.ID, u.Home, events.OutcomeSuccess, "", "Confirm it's them with "+how)
	if actor.Impersonator != "" {
		// The impersonated session keeps its own cookie; the parent's new
		// secret is only needed once the impersonation ends.
		return nil, nil
	}
	actor.SteppedUpAt = now
	return issued, nil
}

// SteppedUp reports whether actor confirmed it's them recently enough for a
// sensitive action.
func (s *Service) SteppedUp(actor *auth.Actor) bool {
	return actor != nil && actor.SteppedUpWithin(s.Policy().StepUp(), s.now())
}

// RequireStepUp refuses a sensitive action from someone in the panel
// unless they confirmed it's them recently. API tokens are scoped to what
// they may do instead, and can't confirm anything; what they mustn't do at
// all refuses them itself (requirePerson).
func (s *Service) RequireStepUp(actor *auth.Actor) error {
	switch {
	case actor == nil:
		return i18n.ErrUnauthenticated.New()
	case actor.Type != auth.ActorUser:
		return nil
	case !s.SteppedUp(actor):
		return auth.ErrStepUpRequired.New()
	}
	return nil
}

// requirePerson refuses API tokens, for changes to how a person signs in,
// which a stolen token mustn't be able to make.
func requirePerson(actor *auth.Actor) error {
	if actor == nil {
		return i18n.ErrUnauthenticated.New()
	}
	if actor.Type != auth.ActorUser {
		return auth.ErrPanelOnly.New()
	}
	return nil
}

// StepUpMethods lists how the person can confirm it's them.
func (s *Service) StepUpMethods(ctx context.Context, actor *auth.Actor) []string {
	u, err := s.stepUpUser(ctx, actor)
	if err != nil {
		return []string{}
	}
	out := []string{}
	if u.PasswordHash != "" {
		out = append(out, "password")
	}
	if u.TOTP {
		out = append(out, "totp")
	}
	if keys, err := s.opts.Store.Passkeys(ctx, u.ID); err == nil && len(keys) > 0 {
		out = append(out, "passkey")
	}
	return out
}
