package identity

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// UserInput is what people send to add a user or change one.
type UserInput struct {
	Username string `json:"username" minLength:"2" maxLength:"64" pattern:"^[a-z0-9][a-z0-9._@-]*$" doc:"What the person signs in with: lowercase letters, digits, dots, dashes, underscores and @" example:"ada"`
	Name     string `json:"name,omitempty" maxLength:"128" doc:"The person's name, as the panel shows it" example:"Ada Lovelace"`
	Email    string `json:"email,omitempty" maxLength:"254" doc:"Where the person gets notifications" example:"ada@example.com"`
	// Home is where the user belongs. It is set when the user is added and
	// never changes; left out, it is the scope all the user's grants share.
	Home      *store.Scope `json:"home,omitempty" doc:"Where the user belongs, and so who manages them: the server, a reseller or an account. Set when the user is added; it never changes"`
	Grants    []auth.Grant `json:"grants" doc:"What the user may do, and where"`
	Disabled  bool         `json:"disabled,omitempty" doc:"Turn the user off: they can't sign in and their API tokens stop working"`
	Locale    string       `json:"locale,omitempty" maxLength:"35" doc:"The language of the panel for them, such as en or pt-BR; empty follows their browser"`
	AllowFrom []string     `json:"allowFrom,omitempty" doc:"Addresses and networks the user may sign in from; empty allows anywhere the sign-in policy does"`
	// Password is the first password of a new user. Without one, the user
	// chooses theirs through a sign-in link.
	Password string `json:"password,omitempty" maxLength:"256" doc:"A first password for a new user; leave it out to send them a sign-in link instead"`
	// MustChangePassword makes a new user choose their own password when
	// they first sign in.
	MustChangePassword bool `json:"mustChangePassword,omitempty" doc:"Ask the user to choose a new password when they first sign in"`
}

var (
	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{1,63}$`)
	localePattern   = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)
	emailPattern    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

// NormalizeUsername is how usernames are compared: trimmed and lowercase.
func NormalizeUsername(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// checkDetails checks what a user's details look like, apart from who may
// set them.
func checkDetails(in *UserInput) []i18n.FieldError {
	var fields []i18n.FieldError
	in.Username = NormalizeUsername(in.Username)
	in.Name, in.Email = strings.TrimSpace(in.Name), strings.TrimSpace(in.Email)
	if !usernamePattern.MatchString(in.Username) {
		fields = append(fields, i18n.Field("username", i18n.ErrBadValue, "reason", "use 2 to 64 lowercase letters, digits, dots, dashes, underscores or @, starting with a letter or digit"))
	}
	if utf8.RuneCountInString(in.Name) > 128 || strings.ContainsAny(in.Name, "\x00\n\r") {
		fields = append(fields, i18n.Field("name", i18n.ErrBadValue, "reason", "use at most 128 characters on one line"))
	}
	if in.Email != "" && (len(in.Email) > 254 || !emailPattern.MatchString(in.Email)) {
		fields = append(fields, i18n.Field("email", i18n.ErrBadValue, "reason", "it isn't an email address"))
	}
	if in.Locale != "" && !localePattern.MatchString(in.Locale) {
		fields = append(fields, i18n.Field("locale", i18n.ErrBadValue, "reason", "use a language tag such as en or pt-BR"))
	}
	if _, err := ParseNetworks(in.AllowFrom); err != nil {
		fields = append(fields, i18n.Field("allowFrom", i18n.ErrBadValue, "reason", err.Error()))
	}
	if len(in.Grants) > 100 {
		fields = append(fields, i18n.Field("grants", i18n.ErrBadValue, "reason", "a user can have at most 100 grants"))
	}
	for i, g := range in.Grants {
		if err := g.Validate(); err != nil {
			for _, f := range i18n.From(err).Fields {
				f.Path = fmt.Sprintf("grants[%d].%s", i, f.Path)
				fields = append(fields, f)
			}
		}
	}
	return fields
}

// homeOf is the scope a new user belongs to when the request doesn't say:
// the one all their grants share.
func homeOf(in *UserInput) (store.Scope, error) {
	if in.Home != nil {
		return *in.Home, nil
	}
	if len(in.Grants) == 0 {
		return store.Scope{}, i18n.Invalid(i18n.Field("grants", i18n.ErrRequired))
	}
	home := in.Grants[0].Scope
	for _, g := range in.Grants[1:] {
		if g.Scope != home {
			return store.Scope{}, i18n.Invalid(i18n.Field("home", i18n.ErrRequired))
		}
	}
	return home, nil
}

// checkGrants makes sure the user's grants stay within their home and the
// actor may hand each of them out.
func (s *Service) checkGrants(ctx context.Context, actor *auth.Actor, home store.Scope, grants []auth.Grant) error {
	gc, err := s.grantChecker()
	if err != nil {
		return err
	}
	var fields []i18n.FieldError
	for i, g := range grants {
		ok, err := gc.Covers(ctx, home, g.Scope)
		if err != nil {
			return err
		}
		if !ok {
			fields = append(fields, i18n.Field(fmt.Sprintf("grants[%d].scope", i), i18n.ErrBadValue, "reason", "it is outside where the user belongs"))
		}
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	for _, g := range grants {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			return err
		}
	}
	return nil
}

// ListUsers returns a page of the users actor may see, those who belong
// where their core.user.read reaches.
func (s *Service) ListUsers(ctx context.Context, actor *auth.Actor, q UserQuery) ([]UserView, string, error) {
	if actor == nil {
		return nil, "", i18n.ErrUnauthenticated.New()
	}
	scopes, all, err := s.reach(ctx, actor, auth.PermUserRead)
	if err != nil {
		return nil, "", err
	}
	if !all {
		if len(scopes) == 0 {
			return nil, "", i18n.ErrForbidden.New("action", string(auth.PermUserRead))
		}
		if q.Homes == nil {
			q.Homes = scopes
		} else {
			q.Homes = slices.DeleteFunc(slices.Clone(q.Homes), func(h store.Scope) bool { return !slices.Contains(scopes, h) })
		}
	}
	users, next, err := s.opts.Store.ListUsers(ctx, q)
	if err != nil {
		return nil, "", err
	}
	out := make([]UserView, 0, len(users))
	for _, u := range users {
		out = append(out, s.view(u, s.hasTwoFactor(ctx, u)))
	}
	return out, next, nil
}

// reach returns the scopes actor's grants carry perm on, or all.
func (s *Service) reach(ctx context.Context, actor *auth.Actor, perm auth.Permission) ([]store.Scope, bool, error) {
	if sa, ok := s.opts.Authorizer.(auth.ScopeAuthorizer); ok {
		return sa.Scopes(ctx, actor, perm)
	}
	if s.authorize(ctx, actor, perm, store.ServerScope()) == nil {
		return nil, true, nil
	}
	var out []store.Scope
	for _, g := range actor.Grants {
		if g.Object == "" && s.authorize(ctx, actor, perm, g.Scope) == nil && !slices.Contains(out, g.Scope) {
			out = append(out, g.Scope)
		}
	}
	return out, false, nil
}

// visible reads a user actor may see: themselves, or one who belongs where
// actor holds perm. Anyone else is reported as not found, so users of
// other accounts don't reveal that they exist.
func (s *Service) visible(ctx context.Context, actor *auth.Actor, id store.ID, perm auth.Permission) (*User, error) {
	if actor == nil {
		return nil, i18n.ErrUnauthenticated.New()
	}
	u, err := s.opts.Store.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if actor.Person() == u.ID && perm == auth.PermUserRead {
		return u, nil
	}
	if err := s.authorize(ctx, actor, auth.PermUserRead, u.Home); err != nil {
		return nil, notFound(string(id))
	}
	if perm != auth.PermUserRead {
		if err := s.authorize(ctx, actor, perm, u.Home); err != nil {
			s.audit(ctx, actor, string(perm), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Change "+u.Username)
			return nil, err
		}
	}
	return u, nil
}

// GetUser returns one user actor may see.
func (s *Service) GetUser(ctx context.Context, actor *auth.Actor, id store.ID) (UserView, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserRead)
	if err != nil {
		return UserView{}, err
	}
	return s.view(u, s.hasTwoFactor(ctx, u)), nil
}

// hasTwoFactor reports whether signing in as u takes a second step.
func (s *Service) hasTwoFactor(ctx context.Context, u *User) bool {
	return len(s.secondFactors(ctx, u)) > 0
}

// CreateUser adds a user. actor needs core.user.create where the user will
// belong, and may only hand out grants they could use themselves.
func (s *Service) CreateUser(ctx context.Context, actor *auth.Actor, in UserInput) (UserView, error) {
	if actor == nil {
		return UserView{}, i18n.ErrUnauthenticated.New()
	}
	fields := checkDetails(&in)
	home, err := homeOf(&in)
	if err != nil {
		return UserView{}, err
	}
	if err := home.Validate(); err != nil {
		fields = append(fields, i18n.Field("home", i18n.ErrBadValue, "reason", err.Error()))
	}
	if in.Password != "" {
		if err := auth.CheckPasswordStrength(in.Password, in.Username); err != nil {
			fields = append(fields, i18n.Field("password", auth.ErrWeakPassword, "reason", i18n.From(err).Params["reason"]))
		}
	}
	if len(fields) > 0 {
		return UserView{}, i18n.Invalid(fields...)
	}
	if err := s.authorize(ctx, actor, auth.PermUserCreate, home); err != nil {
		s.audit(ctx, actor, string(auth.PermUserCreate), "", home, events.OutcomeDenied, i18n.From(err).Message, "Add "+in.Username)
		return UserView{}, err
	}
	if err := s.checkGrants(ctx, actor, home, in.Grants); err != nil {
		s.audit(ctx, actor, string(auth.PermUserCreate), "", home, events.OutcomeDenied, i18n.From(err).Message, "Add "+in.Username)
		return UserView{}, err
	}
	now := s.now()
	u := &User{
		ID: store.NewID(PrefixUser), Username: in.Username, Name: in.Name, Email: in.Email, Home: home, Grants: in.Grants,
		Disabled: in.Disabled, Locale: in.Locale, AllowFrom: in.AllowFrom, CreatedAt: now, UpdatedAt: now,
	}
	if u.Grants == nil {
		u.Grants = []auth.Grant{}
	}
	if err := s.opts.Store.CreateUser(ctx, u); err != nil {
		return UserView{}, err
	}
	if in.Password != "" {
		hash, err := auth.HashPassword(ctx, in.Password)
		if err != nil {
			return UserView{}, err
		}
		if err := s.opts.Store.SetPassword(ctx, u.ID, hash, in.MustChangePassword, now); err != nil {
			return UserView{}, err
		}
		u.PasswordHash, u.MustChangePassword = hash, in.MustChangePassword
	}
	s.audit(ctx, actor, string(auth.PermUserCreate), u.ID, u.Home, events.OutcomeSuccess, "", "Add "+u.Username+" with "+describeGrants(u.Grants))
	s.publish(ctx, UserCreated, u, actor, u.Username+" was added.", UserEvent{Username: u.Username, Detail: describeGrants(u.Grants)})
	return s.view(u, false), nil
}

// describeGrants says what grants give, for audit records and events.
func describeGrants(grants []auth.Grant) string {
	if len(grants) == 0 {
		return "no roles"
	}
	parts := make([]string, len(grants))
	for i, g := range grants {
		p := string(g.Role) + " on " + g.Scope.String()
		if g.Object != "" {
			p += " (" + string(g.Object) + ")"
		}
		parts[i] = p
	}
	return strings.Join(parts, ", ")
}

// UpdateUser changes a user's details and grants. Grants that are added or
// taken away must be ones actor could hand out, and the last admin who can
// sign in stays one.
func (s *Service) UpdateUser(ctx context.Context, actor *auth.Actor, id store.ID, in UserInput, revision int64) (UserView, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserUpdate)
	if err != nil {
		return UserView{}, err
	}
	fields := checkDetails(&in)
	if in.Home != nil && *in.Home != u.Home {
		fields = append(fields, i18n.Field("home", i18n.ErrImmutable))
	}
	if in.Password != "" {
		fields = append(fields, i18n.Field("password", i18n.ErrBadValue, "reason", "set passwords with a sign-in link or the password operation"))
	}
	if len(fields) > 0 {
		return UserView{}, i18n.Invalid(fields...)
	}
	if in.Grants == nil {
		in.Grants = []auth.Grant{}
	}
	added, removed := diffGrants(u.Grants, in.Grants)
	if err := s.checkGrants(ctx, actor, u.Home, added); err != nil {
		s.audit(ctx, actor, string(auth.PermUserUpdate), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Change "+u.Username)
		return UserView{}, err
	}
	gc, err := s.grantChecker()
	if err != nil {
		return UserView{}, err
	}
	for _, g := range removed {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			s.audit(ctx, actor, string(auth.PermUserUpdate), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Change "+u.Username)
			return UserView{}, err
		}
	}
	if in.Disabled && !u.Disabled && actor.Person() == u.ID {
		return UserView{}, auth.ErrSelfDelete.New()
	}
	next := u.Clone()
	next.Username, next.Name, next.Email, next.Grants = in.Username, in.Name, in.Email, in.Grants
	next.Disabled, next.Locale, next.AllowFrom, next.UpdatedAt = in.Disabled, in.Locale, in.AllowFrom, s.now()
	if u.Admin() && !u.Disabled && (!next.Admin() || next.Disabled) {
		if err := s.keepAnAdmin(ctx, u); err != nil {
			return UserView{}, err
		}
	}
	if err := s.opts.Store.UpdateUser(ctx, next, revision); err != nil {
		return UserView{}, err
	}
	if next.Disabled && !u.Disabled {
		// A user turned off is signed out everywhere at once.
		s.opts.Store.DeleteSessions(ctx, u.ID, "")
		s.forget(ctx, u.ID)
	}
	summary := "Change " + u.Username
	if len(added)+len(removed) > 0 {
		summary += ": roles now " + describeGrants(next.Grants)
	}
	s.audit(ctx, actor, string(auth.PermUserUpdate), u.ID, u.Home, events.OutcomeSuccess, "", summary)
	s.publish(ctx, UserUpdated, next, actor, next.Username+" was changed.", UserEvent{Username: next.Username, Detail: describeGrants(next.Grants)})
	return s.view(next, s.hasTwoFactor(ctx, next)), nil
}

// diffGrants returns the grants next has that prev doesn't, and the other
// way round.
func diffGrants(prev, next []auth.Grant) (added, removed []auth.Grant) {
	same := func(a, b auth.Grant) bool {
		return a.Role == b.Role && a.Scope == b.Scope && a.Object == b.Object &&
			slices.Equal(a.Permissions, b.Permissions) && slices.Equal(a.Limit, b.Limit)
	}
	for _, g := range next {
		if !slices.ContainsFunc(prev, func(p auth.Grant) bool { return same(p, g) }) {
			added = append(added, g)
		}
	}
	for _, g := range prev {
		if !slices.ContainsFunc(next, func(n auth.Grant) bool { return same(n, g) }) {
			removed = append(removed, g)
		}
	}
	return added, removed
}

// keepAnAdmin refuses to take away the last admin who can sign in.
func (s *Service) keepAnAdmin(ctx context.Context, leaving *User) error {
	q := UserQuery{Role: auth.RoleAdmin, Limit: store.MaxLimit}
	for {
		admins, next, err := s.opts.Store.ListUsers(ctx, q)
		if err != nil {
			return err
		}
		for _, a := range admins {
			if a.ID != leaving.ID && a.Admin() && !a.Disabled {
				return nil
			}
		}
		if next == "" {
			return auth.ErrLastAdmin.New("user", leaving.Username)
		}
		q.Cursor = next
	}
}

// DeleteUser deletes a user with their sessions, tokens, keys and passkeys.
func (s *Service) DeleteUser(ctx context.Context, actor *auth.Actor, id store.ID) error {
	u, err := s.visible(ctx, actor, id, auth.PermUserDelete)
	if err != nil {
		return err
	}
	if actor.Person() == u.ID || actor.Impersonator == u.ID {
		return auth.ErrSelfDelete.New()
	}
	gc, err := s.grantChecker()
	if err != nil {
		return err
	}
	for _, g := range u.Grants {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			s.audit(ctx, actor, string(auth.PermUserDelete), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Delete "+u.Username)
			return err
		}
	}
	if u.Admin() && !u.Disabled {
		if err := s.keepAnAdmin(ctx, u); err != nil {
			return err
		}
	}
	if err := s.opts.Store.DeleteUser(ctx, u.ID); err != nil {
		return err
	}
	s.forget(ctx, u.ID)
	if s.opts.Secrets != nil {
		for _, name := range []string{totpSecret, totpPending, alertSettings} {
			s.opts.Secrets.Delete(ctx, secretRef(u.ID, name))
		}
	}
	s.audit(ctx, actor, string(auth.PermUserDelete), u.ID, u.Home, events.OutcomeSuccess, "", "Delete "+u.Username)
	s.publish(ctx, UserDeleted, u, actor, u.Username+" was deleted.", UserEvent{Username: u.Username})
	return nil
}

// ResetInput says how to help a user back in.
type ResetInput struct {
	// Link makes a sign-in link the user chooses a new password with.
	Link bool `json:"link,omitempty" doc:"Make a sign-in link for choosing a new password; it is shown once"`
	// RemoveTwoFactor removes the user's authenticator app, passkeys,
	// recovery codes, trusted browsers and add-on methods.
	RemoveTwoFactor bool `json:"removeTwoFactor,omitempty" doc:"Remove their authenticator app, passkeys, recovery codes, trusted browsers and methods from add-ons"`
	EndSessions     bool `json:"endSessions,omitempty" doc:"Sign them out everywhere"`
	RevokeTokens    bool `json:"revokeTokens,omitempty" doc:"Revoke all their API tokens"`
	Unlock          bool `json:"unlock,omitempty" doc:"Lift a lockout after failed sign-ins"`
}

// ResetResult is what helping a user back in did.
type ResetResult struct {
	Link          *IssuedLink `json:"link,omitempty"`
	SessionsEnded int         `json:"sessionsEnded"`
	TokensRevoked int         `json:"tokensRevoked"`
}

// ResetUser helps a user back in. actor needs core.user.reset where the
// user belongs and every grant the user holds.
func (s *Service) ResetUser(ctx context.Context, actor *auth.Actor, id store.ID, in ResetInput) (ResetResult, error) {
	var out ResetResult
	u, err := s.visible(ctx, actor, id, auth.PermUserReset)
	if err != nil {
		return out, err
	}
	gc, err := s.grantChecker()
	if err != nil {
		return out, err
	}
	for _, g := range u.Grants {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			s.audit(ctx, actor, string(auth.PermUserReset), u.ID, u.Home, events.OutcomeDenied, i18n.From(err).Message, "Help "+u.Username+" back in")
			return out, err
		}
	}
	if actor.Impersonator != "" {
		return out, auth.ErrImpersonating.New("user", actor.Name, "action", "help users back in")
	}
	var did []string
	if in.RemoveTwoFactor {
		if err := s.removeTwoFactor(ctx, u); err != nil {
			return out, err
		}
		did = append(did, "removed their second steps")
		s.publish(ctx, UserTwoFactorChanged, u, actor, u.Username+"'s second sign-in steps were removed.", UserEvent{Username: u.Username, Detail: "removed"})
	}
	if in.EndSessions {
		n, err := s.opts.Store.DeleteSessions(ctx, u.ID, "")
		if err != nil {
			return out, err
		}
		s.forget(ctx, u.ID)
		out.SessionsEnded = n
		did = append(did, fmt.Sprintf("ended %d sessions", n))
	}
	if in.RevokeTokens {
		n, err := s.opts.Store.DeleteTokens(ctx, u.ID)
		if err != nil {
			return out, err
		}
		out.TokensRevoked = n
		did = append(did, fmt.Sprintf("revoked %d API tokens", n))
	}
	if in.Unlock {
		if err := s.opts.Store.RecordSignIn(ctx, u.ID, 0, time.Time{}, time.Time{}, ""); err != nil {
			return out, err
		}
		did = append(did, "lifted the lockout")
	}
	if in.Link {
		link, err := s.issueLink(ctx, actor, u, InviteReset)
		if err != nil {
			return out, err
		}
		out.Link = &link
		did = append(did, "made a sign-in link")
	}
	if len(did) == 0 {
		return out, i18n.ErrBadRequest.New("reason", "choose something to do")
	}
	s.audit(ctx, actor, string(auth.PermUserReset), u.ID, u.Home, events.OutcomeSuccess, "", "Help "+u.Username+" back in: "+strings.Join(did, ", "))
	return out, nil
}

// removeTwoFactor takes away every second step a user has.
func (s *Service) removeTwoFactor(ctx context.Context, u *User) error {
	st := s.opts.Store
	if err := st.SetTOTP(ctx, u.ID, false, 0); err != nil {
		return err
	}
	if s.opts.Secrets != nil {
		s.opts.Secrets.Delete(ctx, secretRef(u.ID, totpSecret))
		s.opts.Secrets.Delete(ctx, secretRef(u.ID, totpPending))
	}
	keys, err := st.Passkeys(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := st.DeletePasskey(ctx, u.ID, k.ID); err != nil && !store.ErrNotFound.Is(err) {
			return err
		}
	}
	if err := st.ReplaceRecoveryCodes(ctx, u.ID, nil); err != nil {
		return err
	}
	if err := st.DeleteDevices(ctx, u.ID); err != nil {
		return err
	}
	methods, err := st.MFAMethods(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, m := range methods {
		if err := st.DisableMFA(ctx, u.ID, m.Method); err != nil && !store.ErrNotFound.Is(err) {
			return err
		}
	}
	return nil
}
