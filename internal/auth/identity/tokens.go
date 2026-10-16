package identity

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// maxTokens is how many API tokens one user may have.
const maxTokens = 100

// TokenInput describes a new API token.
type TokenInput struct {
	Name string `json:"name" minLength:"1" maxLength:"64" doc:"What the token is for, such as deploys from CI" example:"Deploys from CI"`
	// Permissions narrow the token; empty means whatever its user may do.
	Permissions []string `json:"permissions,omitempty" doc:"What the token may do, as permission names or patterns such as web.* or *.read; leave it out for whatever you may do"`
	// Accounts narrow the token to some accounts.
	Accounts  []store.ID `json:"accounts,omitempty" doc:"The accounts the token may act on; leave it out for wherever you may"`
	AllowFrom []string   `json:"allowFrom,omitempty" doc:"Addresses and networks the token may be used from; leave it out for anywhere"`
	// ExpiresAt ends the token; the sign-in policy may require one.
	ExpiresAt *time.Time `json:"expiresAt,omitempty" doc:"When the token stops working; leave it out to keep it until it's revoked, unless the sign-in policy limits how long tokens last"`
}

// CreatedToken is a new API token, with the secret shown once.
type CreatedToken struct {
	TokenView
	Token string `json:"token" doc:"The token itself, for the Authorization header as Bearer <token>. It is shown this once"`
}

// CreateToken makes an API token for the actor. It acts with at most the
// actor's own grants, narrowed to the permissions and accounts it names.
func (s *Service) CreateToken(ctx context.Context, actor *auth.Actor, in TokenInput) (CreatedToken, error) {
	if err := requirePerson(actor); err != nil {
		return CreatedToken{}, err
	}
	if actor.Impersonator != "" {
		return CreatedToken{}, auth.ErrImpersonating.New("user", actor.Name, "action", "make API tokens for them")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return CreatedToken{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	var fields []i18n.FieldError
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 64 {
		fields = append(fields, i18n.Field("name", i18n.ErrBadValue, "reason", "use 1 to 64 characters"))
	}
	for i, p := range in.Permissions {
		switch {
		case !auth.ValidPattern(p):
			fields = append(fields, i18n.Field(fmt.Sprintf("permissions[%d]", i), i18n.ErrBadValue, "reason", "use a permission name or a pattern such as web.* or *.read"))
		case auth.Permission(p).Special() && !holdsByName(u, auth.Permission(p)):
			fields = append(fields, i18n.Field(fmt.Sprintf("permissions[%d]", i), i18n.ErrBadValue, "reason", "you don't hold it yourself"))
		}
	}
	if len(in.Permissions) > 50 {
		fields = append(fields, i18n.Field("permissions", i18n.ErrBadValue, "reason", "name at most 50"))
	}
	for i, a := range in.Accounts {
		ok, err := s.reachesAccount(ctx, u, a)
		if err != nil {
			return CreatedToken{}, err
		}
		if !ok {
			fields = append(fields, i18n.Field(fmt.Sprintf("accounts[%d]", i), i18n.ErrBadValue, "reason", "your grants don't reach it"))
		}
	}
	if _, err := ParseNetworks(in.AllowFrom); err != nil {
		fields = append(fields, i18n.Field("allowFrom", i18n.ErrBadValue, "reason", err.Error()))
	}
	now := s.now()
	var expires time.Time
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.UTC()
		if !expires.After(now) {
			fields = append(fields, i18n.Field("expiresAt", i18n.ErrBadValue, "reason", "it has to be in the future"))
		}
	}
	if max := s.Policy().TokenMaxDays; max > 0 {
		limit := now.Add(time.Duration(max) * 24 * time.Hour)
		switch {
		case expires.IsZero():
			fields = append(fields, i18n.Field("expiresAt", i18n.ErrRequired))
		case expires.After(limit):
			fields = append(fields, i18n.Field("expiresAt", i18n.ErrBadValue, "reason", fmt.Sprintf("tokens last at most %d days here", max)))
		}
	}
	if len(fields) > 0 {
		return CreatedToken{}, i18n.Invalid(fields...)
	}
	existing, err := s.opts.Store.Tokens(ctx, u.ID)
	if err != nil {
		return CreatedToken{}, err
	}
	if len(existing) >= maxTokens {
		return CreatedToken{}, auth.ErrTooMany.New("count", itoa(len(existing)), "what", "API tokens")
	}
	secret := auth.NewSecret(auth.TokenPrefix)
	t := &Token{
		ID: store.NewID(PrefixToken), User: u.ID, Name: in.Name, Hash: auth.HashSecret(secret), Hint: secret[:len(auth.TokenPrefix)+4],
		Permissions: nonNil(in.Permissions), Accounts: nonNilIDs(in.Accounts), AllowFrom: nonNil(in.AllowFrom), ExpiresAt: expires, CreatedAt: now,
	}
	if err := s.opts.Store.CreateToken(ctx, t); err != nil {
		return CreatedToken{}, err
	}
	s.audit(ctx, actor, "core.token.create", t.ID, u.Home, events.OutcomeSuccess, "", "Create the API token "+t.Name)
	s.publishToken(ctx, TokenCreated, u, actor, t, u.Username+" created the API token "+t.Name+".")
	s.alert(ctx, u, "An API token was created", "The API token "+t.Name+" can now act as "+u.Username+". If you didn't create it, revoke it from your security settings.", events.SeverityNotice)
	return CreatedToken{TokenView: tokenView(t), Token: secret}, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilIDs(v []store.ID) []store.ID {
	if v == nil {
		return []store.ID{}
	}
	return v
}

// holdsByName reports whether one of u's grants carries a special
// permission by name.
func holdsByName(u *User, perm auth.Permission) bool {
	for _, g := range u.Grants {
		for _, p := range g.Permissions {
			if p == perm {
				return true
			}
		}
	}
	return false
}

// reachesAccount reports whether one of u's grants reaches an account.
func (s *Service) reachesAccount(ctx context.Context, u *User, acct store.ID) (bool, error) {
	if !acct.Valid() {
		return false, nil
	}
	for _, g := range u.Grants {
		ok, err := s.covers(ctx, g.Scope, store.AccountScope(acct))
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

func (s *Service) publishToken(ctx context.Context, t events.Type, u *User, actor *auth.Actor, tok *Token, message string) {
	s.publish(ctx, t, u, actor, message, TokenEvent{Name: tok.Name, User: u.ID})
}

// MyTokens lists the actor's API tokens.
func (s *Service) MyTokens(ctx context.Context, actor *auth.Actor) ([]TokenView, error) {
	return s.tokensOf(ctx, actor.Person())
}

func (s *Service) tokensOf(ctx context.Context, user store.ID) ([]TokenView, error) {
	list, err := s.opts.Store.Tokens(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]TokenView, 0, len(list))
	for _, t := range list {
		out = append(out, tokenView(t))
	}
	return out, nil
}

// RevokeMyToken revokes one of the actor's tokens. A token may revoke
// itself.
func (s *Service) RevokeMyToken(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	return s.revoke(ctx, actor, actor.Person(), id)
}

func (s *Service) revoke(ctx context.Context, actor *auth.Actor, user, id store.ID) error {
	t, err := s.opts.Store.Token(ctx, id)
	if err != nil || t.User != user {
		return notFound(string(id))
	}
	if err := s.opts.Store.DeleteToken(ctx, user, id); err != nil {
		return err
	}
	u, err := s.opts.Store.User(ctx, user)
	if err != nil {
		return err
	}
	s.audit(ctx, actor, "core.token.revoke", t.ID, u.Home, events.OutcomeSuccess, "", "Revoke the API token "+t.Name)
	s.publishToken(ctx, TokenRevoked, u, actor, t, "The API token "+t.Name+" of "+u.Username+" was revoked.")
	return nil
}

// UserTokens lists another user's tokens, for whoever may help them.
func (s *Service) UserTokens(ctx context.Context, actor *auth.Actor, id store.ID) ([]TokenView, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserReset)
	if err != nil {
		return nil, err
	}
	return s.tokensOf(ctx, u.ID)
}

// RevokeUserToken revokes another user's token.
func (s *Service) RevokeUserToken(ctx context.Context, actor *auth.Actor, user, id store.ID) error {
	u, err := s.visible(ctx, actor, user, auth.PermUserReset)
	if err != nil {
		return err
	}
	return s.revoke(ctx, actor, u.ID, id)
}
