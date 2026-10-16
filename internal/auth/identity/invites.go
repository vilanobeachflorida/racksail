package identity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// How long sign-in links last: a week to start using a new user, a day to
// get back in.
const (
	activateTTL = 7 * 24 * time.Hour
	resetTTL    = 24 * time.Hour
)

// IssuedLink is a sign-in link, shown once to whoever made it.
type IssuedLink struct {
	// Code is what the link carries.
	Code string `json:"code" doc:"What the link carries; anyone who has it can choose the user's password until it expires"`
	// Path is the link on the panel; the URL is the panel's address and
	// this path.
	Path      string    `json:"path" doc:"Where the link goes on the panel"`
	URL       string    `json:"url,omitempty" doc:"The whole link, when the panel knows its own name"`
	Purpose   string    `json:"purpose" enum:"activate,reset" doc:"activate for a new user's first password, reset to choose a new one"`
	ExpiresAt time.Time `json:"expiresAt" doc:"When the link stops working"`
}

// issueLink makes a sign-in link for u, replacing any earlier one.
func (s *Service) issueLink(ctx context.Context, actor *auth.Actor, u *User, purpose string) (IssuedLink, error) {
	if err := s.opts.Store.DeleteInvites(ctx, u.ID); err != nil {
		return IssuedLink{}, err
	}
	ttl := resetTTL
	if purpose == InviteActivate {
		ttl = activateTTL
	}
	now := s.now()
	secret := auth.NewSecret(auth.InvitePrefix)
	inv := &Invite{ID: store.NewID(PrefixInvite), User: u.ID, Hash: auth.HashSecret(secret), Purpose: purpose, CreatedBy: actor.Person(), CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.opts.Store.CreateInvite(ctx, inv); err != nil {
		return IssuedLink{}, err
	}
	link := IssuedLink{Code: secret, Path: "/activate#" + secret, Purpose: purpose, ExpiresAt: inv.ExpiresAt}
	if s.opts.Hostname != "" && s.opts.Hostname != "localhost" {
		link.URL = "https://" + s.opts.Hostname + link.Path
	}
	s.publish(ctx, UserLinkIssued, u, actor, "A sign-in link was made for "+u.Username+".", UserEvent{Username: u.Username, Detail: purpose})
	return link, nil
}

// IssueLink makes a sign-in link for a user: to start using a new user, or
// to choose a new password. It needs core.user.reset where they belong,
// and every grant they hold.
func (s *Service) IssueLink(ctx context.Context, actor *auth.Actor, id store.ID) (IssuedLink, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserReset)
	if err != nil {
		return IssuedLink{}, err
	}
	if err := s.helps(ctx, actor, u, auth.PermUserReset, "Make a sign-in link for "+u.Username); err != nil {
		return IssuedLink{}, err
	}
	purpose := InviteReset
	if u.PasswordHash == "" && u.LastSignInAt.IsZero() {
		purpose = InviteActivate
	}
	link, err := s.issueLink(ctx, actor, u, purpose)
	if err != nil {
		return IssuedLink{}, err
	}
	s.audit(ctx, actor, string(auth.PermUserReset), u.ID, u.Home, events.OutcomeSuccess, "", "Make a sign-in link for "+u.Username)
	return link, nil
}

// LinkInfo is what a sign-in link's page shows before the person chooses a
// password.
type LinkInfo struct {
	Username  string    `json:"username" doc:"The user the link is for"`
	Name      string    `json:"name,omitempty" doc:"The person's name"`
	Purpose   string    `json:"purpose" enum:"activate,reset" doc:"activate for a new user's first password, reset to choose a new one"`
	ExpiresAt time.Time `json:"expiresAt" doc:"When the link stops working"`
}

func (s *Service) invite(ctx context.Context, code string) (*Invite, *User, error) {
	if !strings.HasPrefix(code, auth.InvitePrefix) {
		return nil, nil, auth.ErrInviteInvalid.New()
	}
	inv, err := s.opts.Store.InviteByHash(ctx, auth.HashSecret(code))
	if err != nil || s.now().After(inv.ExpiresAt) {
		return nil, nil, auth.ErrInviteInvalid.New()
	}
	u, err := s.User(ctx, inv.User)
	if err != nil {
		return nil, nil, auth.ErrInviteInvalid.New()
	}
	return inv, u, nil
}

// LookupLink says whose a sign-in link is.
func (s *Service) LookupLink(ctx context.Context, code string, client auth.Client) (LinkInfo, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return LinkInfo{}, err
	}
	inv, u, err := s.invite(ctx, code)
	if err != nil {
		return LinkInfo{}, err
	}
	return LinkInfo{Username: u.Username, Name: u.Name, Purpose: inv.Purpose, ExpiresAt: inv.ExpiresAt}, nil
}

// UseLinkInput chooses a password with a sign-in link.
type UseLinkInput struct {
	Code     string `json:"code" doc:"What the sign-in link carries"`
	Password string `json:"password" maxLength:"1024" doc:"The new password"`
}

// UseLink sets the password a sign-in link's person chose. The link works
// once. Someone with a second step set up still signs in as usual
// afterwards; anyone else is signed in at once.
func (s *Service) UseLink(ctx context.Context, in UseLinkInput, client auth.Client) (*SignInResult, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	inv, u, err := s.invite(ctx, in.Code)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckPasswordStrength(in.Password, u.Username); err != nil {
		return nil, i18n.Invalid(i18n.Field("password", auth.ErrWeakPassword, "reason", i18n.From(err).Params["reason"]))
	}
	if err := s.opts.Store.UseInvite(ctx, inv.ID); err != nil {
		return nil, auth.ErrInviteInvalid.New()
	}
	hash, err := auth.HashPassword(ctx, in.Password)
	if err != nil {
		return nil, err
	}
	if err := s.opts.Store.SetPassword(ctx, u.ID, hash, false, s.now()); err != nil {
		return nil, err
	}
	// Getting back in clears the lockout, and ends the sessions of whoever
	// may have had the old password.
	if err := s.opts.Store.Unlock(ctx, u.ID); err != nil {
		return nil, err
	}
	s.refresh(u.ID, "password")
	actor := signInActor(u, u.Username, client)
	if inv.Purpose == InviteReset {
		s.opts.Store.DeleteSessions(ctx, u.ID, "")
	}
	u.PasswordHash, u.MustChangePassword = hash, false
	s.audit(ctx, actor, "core.user.change_password", u.ID, u.Home, events.OutcomeSuccess, "", "Choose a password with a sign-in link")
	s.publish(ctx, UserPasswordChanged, u, actor, u.Username+" chose a password with a sign-in link.", UserEvent{Username: u.Username, Detail: inv.Purpose, IP: client.IP})
	if len(s.secondFactors(ctx, u)) > 0 {
		return &SignInResult{User: u}, nil
	}
	if err := s.admit(ctx, u, "invite", client); err != nil {
		return nil, err
	}
	issued, err := s.signedIn(ctx, u, "invite", "", false, client, true)
	if err != nil {
		return nil, err
	}
	return &SignInResult{Issued: issued, User: u}, nil
}

// InstallerAccount is the first admin as the installer's answers give it:
// a username, and a crypt(3) password hash or SSH keys.
type InstallerAccount struct {
	Username     string   `json:"username"`
	PasswordHash string   `json:"passwordHash,omitempty"`
	SSHKeys      []string `json:"sshKeys,omitempty"`
}

// firstAdminGrants are what the first admin gets: the whole server, and
// viewing the panel as customers to help them.
func firstAdminGrants() []auth.Grant {
	return []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope(), Permissions: []auth.Permission{auth.PermImpersonate}}}
}

// hasUsers reports whether the panel has any user yet.
func (s *Service) hasUsers(ctx context.Context) (bool, error) {
	if s.opts.Resources == nil {
		return false, nil
	}
	list, _, err := UserKind.List(ctx, s.opts.Resources, store.Query{Limit: 1, IncludeDeleted: true})
	return len(list) > 0, err
}

// Bootstrap makes sure the panel has a way to get its first admin. A
// panel with users needs nothing. Otherwise, when the installer left the
// admin's account at answersFile, that user is created and the file
// removed; when it didn't, a one-time setup code is made and returned, for
// the panel to print. Only its hash is kept.
func (s *Service) Bootstrap(ctx context.Context, answersFile string) (string, error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	exists, err := s.hasUsers(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return "", s.setupDone(ctx)
	}
	if answersFile != "" {
		data, err := os.ReadFile(answersFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", err
		default:
			var acct InstallerAccount
			if err := json.Unmarshal(data, &acct); err != nil {
				return "", errors.Join(errors.New("identity: reading the installer's account"), err)
			}
			if err := s.createInstallerAdmin(ctx, acct); err != nil {
				return "", err
			}
			if err := os.Remove(answersFile); err != nil {
				s.opts.Log.Warn("removing the installer's account file", "file", answersFile, "error", err)
			}
			return "", nil
		}
	}
	code := auth.NewCode(4)
	if err := s.opts.Store.SetSetupCode(ctx, auth.HashCode(code)); err != nil {
		return "", err
	}
	return code, nil
}

// firstAdmin adds the first admin through the change pipeline, as the
// part of the panel named who, with their password hash, which is kept
// before their user exists so the user is never there without it.
func (s *Service) firstAdmin(ctx context.Context, who string, client auth.Client, spec UserSpec, hash, title string) (*User, error) {
	// Nobody signed in hands out the first admin's grants, which the panel
	// holds itself for the purpose.
	actor := auth.System(who)
	actor.Grants, actor.IP = firstAdminGrants(), client.IP
	normalize(&spec)
	spec.Grants = firstAdminGrants()
	if fields := checkSpec(spec); len(fields) > 0 {
		return nil, i18n.Invalid(fields...)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	id := store.NewID(PrefixUser)
	if hash != "" {
		if err := s.opts.Store.SetPassword(ctx, id, hash, false, s.now()); err != nil {
			return nil, err
		}
	}
	_, err = s.submit(ctx, changeset.Request{
		Actor: actor, Title: title,
		Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: KindUser, ID: id, Scope: store.ServerScope(), Spec: raw}},
	})
	if err != nil {
		if delErr := s.opts.Store.DeleteUser(context.WithoutCancel(ctx), id); delErr != nil {
			s.opts.Log.Warn("removing the password of a first admin who wasn't added", "error", delErr)
		}
		return nil, err
	}
	if err := s.setupDone(ctx); err != nil {
		return nil, err
	}
	return s.User(ctx, id)
}

func (s *Service) createInstallerAdmin(ctx context.Context, acct InstallerAccount) error {
	if acct.PasswordHash != "" && !auth.KnownHash(acct.PasswordHash) {
		return errors.New("identity: the installer's password hash is in a format Racksail doesn't read")
	}
	spec := UserSpec{Username: acct.Username, Name: acct.Username}
	for _, line := range acct.SSHKeys {
		key, err := newSSHKey(SSHKeyInput{PublicKey: line})
		if err != nil {
			s.opts.Log.Warn("an SSH key from the installer wasn't accepted", "error", err)
			continue
		}
		if !slices.ContainsFunc(spec.SSHKeys, func(k UserSSHKey) bool { return k.ID == key.ID }) {
			spec.SSHKeys = append(spec.SSHKeys, key)
		}
	}
	// The crypt(3) hash works for the first sign-in, which replaces it with
	// an Argon2id one.
	_, err := s.firstAdmin(ctx, "installer", auth.Client{}, spec, acct.PasswordHash, "Add the first admin from the installer")
	return err
}

// SetupRequired reports whether the panel still waits for its first admin.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	exists, err := s.hasUsers(ctx)
	return !exists, err
}

// SetupInput creates the first admin with the setup code.
type SetupInput struct {
	Code     string `json:"code" maxLength:"64" doc:"The setup code from the panel's log or the server's console"`
	Username string `json:"username" minLength:"2" maxLength:"64" doc:"The admin's username"`
	Name     string `json:"name,omitempty" maxLength:"128" doc:"The admin's name"`
	Email    string `json:"email,omitempty" maxLength:"254" doc:"The admin's email address"`
	Password string `json:"password" maxLength:"1024" format:"password" doc:"The admin's password"`
}

// Setup creates the first admin with the one-time setup code and signs
// them in.
func (s *Service) Setup(ctx context.Context, in SetupInput, client auth.Client) (*Issued, error) {
	if err := s.limiter.take(client.IP); err != nil {
		return nil, err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	exists, err := s.hasUsers(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, auth.ErrSetupDone.New()
	}
	want, err := s.opts.Store.SetupCode(ctx)
	if err != nil {
		return nil, err
	}
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(auth.HashCode(in.Code))) != 1 {
		s.opts.Log.Warn("a wrong setup code was tried", "address", client.IP)
		return nil, auth.ErrBadSetupCode.New()
	}
	spec := UserSpec{Username: in.Username, Name: in.Name, Email: in.Email}
	normalize(&spec)
	fields := checkSpec(spec)
	if err := auth.CheckPasswordStrength(in.Password, spec.Username); err != nil {
		fields = append(fields, i18n.Field("password", auth.ErrWeakPassword, "reason", i18n.From(err).Params["reason"]))
	}
	if len(fields) > 0 {
		return nil, i18n.Invalid(fields...)
	}
	hash, err := auth.HashPassword(ctx, in.Password)
	if err != nil {
		return nil, err
	}
	u, err := s.firstAdmin(ctx, "setup", client, spec, hash, "Add the first admin with the setup code")
	if err != nil {
		return nil, err
	}
	return s.signedIn(ctx, u, "setup", "", false, client, true)
}

// EnsureUser returns the user with a username, adding an admin of the
// whole server by that name when there is none, as racksail dev does for
// the developer it signs everyone in as.
func (s *Service) EnsureUser(ctx context.Context, username string) (*User, error) {
	u, err := s.userByName(ctx, username)
	if err == nil || !store.ErrNotFound.Is(err) {
		return u, err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.firstAdmin(ctx, "dev", auth.Client{}, UserSpec{Username: username, Name: username}, "", "Add "+username)
}

// Actor returns the actor a user acts as when signed in without a
// session, as racksail dev signs everyone in as its developer.
func (s *Service) Actor(ctx context.Context, id store.ID, client auth.Client) (*auth.Actor, error) {
	u, err := s.User(ctx, id)
	if err != nil {
		return nil, err
	}
	return &auth.Actor{
		Type: auth.ActorUser, ID: u.ID, Name: u.Username, User: u.ID, Source: auth.SourceUI, IP: client.IP,
		Grants: u.Clone().Grants, SteppedUpAt: s.now(),
	}, nil
}
