package identity

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Issued is a session handed to a browser: the secret its cookie holds,
// and, when the person chose to trust the browser, the device's.
type Issued struct {
	Secret  string
	Session *Session
	// DeviceSecret and DeviceExpires are set when the browser was trusted
	// just now.
	DeviceSecret  string
	DeviceExpires time.Time
}

// touchEvery is how often a session or token in use is written as used.
const touchEvery = time.Minute

// maxSessions is how many sessions a user keeps; signing in once more ends
// the one used longest ago.
const maxSessions = 50

var _ auth.Authenticator = (*Service)(nil)

// Authenticate implements auth.Authenticator: a bearer API token, or the
// session cookie. A request with neither is anonymous.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	client := s.clientOf(ctx, r)
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, value, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Bearer") {
			return nil, auth.ErrTokenInvalid.New()
		}
		return s.authenticateToken(ctx, strings.TrimSpace(value), client, r)
	}
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return s.authenticateSession(ctx, c.Value, client)
}

// clientOf is where the request comes from, as the API worked it out, or
// from the request itself.
func (s *Service) clientOf(ctx context.Context, r *http.Request) auth.Client {
	c := auth.ClientFrom(ctx)
	if c.IP == "" {
		c.IP = hostOf(r.RemoteAddr)
	}
	if c.UserAgent == "" {
		c.UserAgent = r.UserAgent()
	}
	return c
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// checkAddress applies the policy's allowlist, which never keeps out the
// server itself, then the user's own.
func (s *Service) checkAddress(u *User, ip string) error {
	if !loopback(ip) && !Allowed(s.Policy().Networks(), ip) {
		return auth.ErrAddressNotAllowed.New("address", ip, "what", "the panel")
	}
	nets, _ := ParseNetworks(u.AllowFrom)
	if !Allowed(nets, ip) {
		return auth.ErrAddressNotAllowed.New("address", ip, "what", u.Username)
	}
	return nil
}

func (s *Service) authenticateSession(ctx context.Context, secret string, client auth.Client) (*auth.Actor, error) {
	if !strings.HasPrefix(secret, auth.SessionPrefix) {
		return nil, auth.ErrSessionExpired.New()
	}
	x, err := s.opts.Store.SessionByHash(ctx, auth.HashSecret(secret))
	if store.ErrNotFound.Is(err) {
		return nil, auth.ErrSessionExpired.New()
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if now.After(x.ExpiresAt) || now.After(x.LastSeenAt.Add(s.Policy().Idle())) {
		s.opts.Store.DeleteSession(ctx, x.ID)
		return nil, auth.ErrSessionExpired.New()
	}
	u, err := s.opts.Store.User(ctx, x.User)
	if store.ErrNotFound.Is(err) {
		return nil, auth.ErrSessionExpired.New()
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		s.opts.Store.DeleteSessions(ctx, u.ID, "")
		return nil, auth.ErrDisabled.New()
	}
	if err := s.checkAddress(u, client.IP); err != nil {
		return nil, err
	}
	actor := &auth.Actor{
		Type: auth.ActorUser, ID: u.ID, Name: u.Username, User: u.ID, Source: auth.SourceUI, IP: client.IP,
		Grants: u.Clone().Grants, SessionID: string(x.ID), SteppedUpAt: x.SteppedUpAt, Pending: x.Pending,
	}
	if x.Impersonator != "" {
		// Viewing as someone ends with the session it started from.
		parent, err := s.opts.Store.Session(ctx, x.Parent)
		if err != nil || now.After(parent.ExpiresAt) {
			s.opts.Store.DeleteSession(ctx, x.ID)
			return nil, auth.ErrSessionExpired.New()
		}
		actor.Impersonator = x.Impersonator
		// Confirming it's them is the impersonator's, from their session.
		actor.SteppedUpAt = parent.SteppedUpAt
	}
	if s.due(string(x.ID), now) || x.IP != client.IP {
		x.LastSeenAt, x.IP = now, client.IP
		if err := s.opts.Store.UpdateSession(ctx, x); err != nil && !store.ErrNotFound.Is(err) {
			s.opts.Log.Warn("recording a session's use", "session", x.ID, "error", err)
		}
	}
	return actor, nil
}

// due reports whether a session or token was last written as used long
// enough ago to write it again.
func (s *Service) due(key string, now time.Time) bool {
	if last, ok := s.touched.Load(key); ok && now.Sub(last.(time.Time)) < touchEvery {
		return false
	}
	s.touched.Store(key, now)
	return true
}

// cliClient reports whether a request comes from the racksail command,
// which says so in its user agent.
func cliClient(r *http.Request) bool {
	return strings.HasPrefix(r.UserAgent(), "racksail/")
}

func (s *Service) authenticateToken(ctx context.Context, secret string, client auth.Client, r *http.Request) (*auth.Actor, error) {
	if !strings.HasPrefix(secret, auth.TokenPrefix) {
		return nil, auth.ErrTokenInvalid.New()
	}
	t, err := s.opts.Store.TokenByHash(ctx, auth.HashSecret(secret))
	if store.ErrNotFound.Is(err) {
		return nil, auth.ErrTokenInvalid.New()
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if t.Expired(now) {
		return nil, auth.ErrTokenInvalid.New()
	}
	u, err := s.opts.Store.User(ctx, t.User)
	if store.ErrNotFound.Is(err) {
		return nil, auth.ErrTokenInvalid.New()
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, auth.ErrDisabled.New()
	}
	if err := s.checkAddress(u, client.IP); err != nil {
		return nil, err
	}
	nets, _ := ParseNetworks(t.AllowFrom)
	if !Allowed(nets, client.IP) {
		return nil, auth.ErrAddressNotAllowed.New("address", client.IP, "what", "the token "+t.Name)
	}
	grants, err := s.tokenGrants(ctx, u, t)
	if err != nil {
		return nil, err
	}
	source := auth.SourceAPI
	if cliClient(r) {
		source = auth.SourceCLI
	}
	if s.due(string(t.ID), now) || t.LastUsedIP != client.IP {
		if err := s.opts.Store.TouchToken(ctx, t.ID, now, client.IP); err != nil && !store.ErrNotFound.Is(err) {
			s.opts.Log.Warn("recording a token's use", "token", t.ID, "error", err)
		}
	}
	return &auth.Actor{Type: auth.ActorToken, ID: t.ID, Name: t.Name, User: u.ID, Source: source, IP: client.IP, Grants: grants}, nil
}

// tokenGrants narrows a user's grants to what a token may do: its
// permissions, and its accounts, which grants on the server or a reseller
// reach as an owner would.
func (s *Service) tokenGrants(ctx context.Context, u *User, t *Token) ([]auth.Grant, error) {
	var out []auth.Grant
	for _, g := range u.Grants {
		n := g.Clone()
		// Special permissions stay with people, unless the token names one.
		n.Permissions = slices.DeleteFunc(n.Permissions, func(p auth.Permission) bool {
			return p.Special() && !slices.Contains(t.Permissions, string(p))
		})
		limit, ok := auth.NarrowLimit(g.Limit, t.Permissions)
		if !ok {
			continue
		}
		n.Limit = limit
		if len(t.Accounts) == 0 {
			out = append(out, n)
			continue
		}
		for _, a := range t.Accounts {
			scope := store.AccountScope(a)
			reaches, err := s.covers(ctx, g.Scope, scope)
			if err != nil {
				return nil, err
			}
			if !reaches {
				continue
			}
			on := n.Clone()
			on.Scope = scope
			if on.Role == auth.RoleAdmin || on.Role == auth.RoleReseller {
				on.Role = auth.RoleOwner
			}
			out = append(out, on)
		}
	}
	return out, nil
}

// pendingFor says what someone must do once signed in before anything
// else.
func (s *Service) pendingFor(ctx context.Context, u *User, factors []string) string {
	if u.MustChangePassword {
		return PendingPassword
	}
	if len(factors) == 0 && s.needsTwoFactor(u) {
		return PendingTwoFactor
	}
	return ""
}

// needsTwoFactor reports whether the policy asks u for a second step.
func (s *Service) needsTwoFactor(u *User) bool {
	required := s.Policy().RequireTwoFactor
	return slices.ContainsFunc(u.Grants, func(g auth.Grant) bool { return slices.Contains(required, g.Role) })
}

// issue starts a session for u.
func (s *Service) issue(ctx context.Context, u *User, method, secondFactor string, persistent bool, client auth.Client, adjust func(*Session)) (*Issued, error) {
	p := s.Policy()
	now := s.now()
	secret := auth.NewSecret(auth.SessionPrefix)
	x := &Session{
		ID: store.NewID(PrefixSession), User: u.ID, Hash: auth.HashSecret(secret), CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(p.Lifetime()), IP: client.IP, UserAgent: truncate(client.UserAgent, 512), Method: method,
		SecondFactor: secondFactor, Persistent: persistent,
	}
	x.Pending = s.pendingFor(ctx, u, s.secondFactors(ctx, u))
	if adjust != nil {
		adjust(x)
	}
	if err := s.opts.Store.CreateSession(ctx, x); err != nil {
		return nil, err
	}
	s.touched.Store(string(x.ID), now)
	if list, err := s.opts.Store.Sessions(ctx, u.ID); err == nil && len(list) > maxSessions {
		for _, old := range list[maxSessions:] {
			s.opts.Store.DeleteSession(ctx, old.ID)
		}
	}
	return &Issued{Secret: secret, Session: x}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SessionOf returns the session of a signed-in actor.
func (s *Service) SessionOf(ctx context.Context, actor *auth.Actor) (*Session, error) {
	if actor == nil || actor.SessionID == "" {
		return nil, i18n.ErrUnauthenticated.New()
	}
	return s.opts.Store.Session(ctx, store.ID(actor.SessionID))
}

// SignOut ends the actor's session. Signing out while viewing as someone
// ends the session it started from too.
func (s *Service) SignOut(ctx context.Context, actor *auth.Actor) error {
	x, err := s.SessionOf(ctx, actor)
	if store.ErrNotFound.Is(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.opts.Store.DeleteSession(ctx, x.ID); err != nil && !store.ErrNotFound.Is(err) {
		return err
	}
	if x.Parent != "" {
		s.opts.Store.DeleteSession(ctx, x.Parent)
	}
	s.audit(ctx, actor, "auth.sign_out", x.User, s.homeOf(ctx, x.User), events.OutcomeSuccess, "", "Sign out")
	return nil
}

func (s *Service) homeOf(ctx context.Context, user store.ID) store.Scope {
	if u, err := s.opts.Store.User(ctx, user); err == nil {
		return u.Home
	}
	return store.ServerScope()
}

// MySessions lists the actor's sessions, the current one marked.
func (s *Service) MySessions(ctx context.Context, actor *auth.Actor) ([]SessionView, error) {
	return s.sessionsOf(ctx, actor.Person(), store.ID(actor.SessionID))
}

func (s *Service) sessionsOf(ctx context.Context, user, current store.ID) ([]SessionView, error) {
	list, err := s.opts.Store.Sessions(ctx, user)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]SessionView, 0, len(list))
	for _, x := range list {
		if now.After(x.ExpiresAt) || now.After(x.LastSeenAt.Add(s.Policy().Idle())) {
			continue
		}
		out = append(out, sessionView(x, current))
	}
	return out, nil
}

// EndMySession ends one of the actor's sessions.
func (s *Service) EndMySession(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	x, err := s.opts.Store.Session(ctx, id)
	if err != nil || x.User != actor.Person() {
		return notFound(string(id))
	}
	if err := s.opts.Store.DeleteSession(ctx, id); err != nil {
		return err
	}
	u, err := s.opts.Store.User(ctx, x.User)
	if err == nil {
		s.audit(ctx, actor, "core.session.end", u.ID, u.Home, events.OutcomeSuccess, "", "End a session of "+u.Username+" on "+DescribeAgent(x.UserAgent))
		s.publish(ctx, SessionsEnded, u, actor, "A session of "+u.Username+" was ended.", UserEvent{Username: u.Username, Detail: "1", Device: DescribeAgent(x.UserAgent), IP: x.IP})
	}
	return nil
}

// EndOtherSessions ends every session of the actor's but the current one.
func (s *Service) EndOtherSessions(ctx context.Context, actor *auth.Actor) (int, error) {
	if err := requirePerson(actor); err != nil {
		return 0, err
	}
	if actor.Impersonator != "" {
		return 0, auth.ErrImpersonating.New("user", actor.Name, "action", "sign them out elsewhere")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return 0, err
	}
	n, err := s.opts.Store.DeleteSessions(ctx, u.ID, store.ID(actor.SessionID))
	if err != nil {
		return 0, err
	}
	s.audit(ctx, actor, "core.session.end", u.ID, u.Home, events.OutcomeSuccess, "", "Sign out everywhere else")
	s.publish(ctx, SessionsEnded, u, actor, u.Username+" signed out everywhere else.", UserEvent{Username: u.Username, Detail: itoa(n)})
	return n, nil
}

// UserSessions lists another user's sessions, for whoever may help them.
func (s *Service) UserSessions(ctx context.Context, actor *auth.Actor, id store.ID) ([]SessionView, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserReset)
	if err != nil {
		return nil, err
	}
	return s.sessionsOf(ctx, u.ID, store.ID(actor.SessionID))
}

// MyDevices lists the browsers the actor trusts.
func (s *Service) MyDevices(ctx context.Context, actor *auth.Actor, deviceSecret string) ([]DeviceView, error) {
	list, err := s.opts.Store.Devices(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	current := ""
	if deviceSecret != "" {
		current = auth.HashSecret(deviceSecret)
	}
	out := make([]DeviceView, 0, len(list))
	for _, d := range list {
		out = append(out, DeviceView{ID: d.ID, Name: d.Name, Current: d.Hash == current, IP: d.IP, CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt, ExpiresAt: d.ExpiresAt})
	}
	return out, nil
}

// ForgetDevice stops trusting one of the actor's browsers, or all of them
// when id is empty.
func (s *Service) ForgetDevice(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "change their trusted browsers")
	}
	if id == "" {
		return s.opts.Store.DeleteDevices(ctx, actor.Person())
	}
	return s.opts.Store.DeleteDevice(ctx, actor.Person(), id)
}

func itoa(n int) string { return strconv.Itoa(n) }
