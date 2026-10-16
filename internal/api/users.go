package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type userPath struct {
	ID store.ID `path:"id" doc:"The user's ID, which starts with user_"`
}

type resetInput struct {
	ID   store.ID `path:"id" doc:"The user's ID, which starts with user_"`
	Body identity.ResetInput
}

type resetOutput struct {
	Body identity.ResetResult
}

type userPasswordInput struct {
	ID   store.ID `path:"id" doc:"The user's ID, which starts with user_"`
	Body identity.PasswordSetting
}

type linkOutput struct {
	Body identity.IssuedLink
}

type userTokenPath struct {
	ID    store.ID `path:"id" doc:"The user's ID"`
	Token store.ID `path:"token" doc:"The token's ID, which starts with tok_"`
}

// MatrixEntry is one permission and how far each role reaches with it.
type MatrixEntry struct {
	Permission  auth.Permission           `json:"permission"`
	Description string                    `json:"description"`
	Roles       map[auth.Role]auth.Access `json:"roles" doc:"For each role: everywhere, scope (where the grant applies), named (only when a grant names it) or none"`
}

type rolesOutput struct {
	Body struct {
		Roles  []auth.RoleInfo `json:"roles"`
		Matrix []MatrixEntry   `json:"matrix" doc:"Every permission, and how far each role reaches with it"`
		// Self are what everyone may do to their own user.
		Self []auth.Permission `json:"self" doc:"What everyone may do to their own user, whatever their roles"`
	}
}

// OpenAccountInput opens an account, and gives it an owner.
type OpenAccountInput struct {
	Slug     string                `json:"slug" minLength:"3" maxLength:"16" pattern:"^[a-z][a-z0-9]*$" doc:"The account's short name, which is also its Linux user" example:"acme"`
	Name     string                `json:"name" minLength:"1" maxLength:"128" doc:"What the account is called" example:"Acme Inc."`
	Reseller store.ID              `json:"reseller,omitempty" doc:"The reseller whose customer the account is; leave it out for the server's own"`
	Limits   account.AccountLimits `json:"limits,omitzero" doc:"What the account's processes may use together, enforced by the kernel; empty means no limit"`
	Note     string                `json:"note,omitempty" maxLength:"1000" doc:"Notes for the people who run the server"`
	// Owner adds a user who owns the account, with a sign-in link to
	// choose their password.
	Owner *AccountOwner `json:"owner,omitempty" doc:"A user to own the account, who gets a sign-in link to choose their password"`
}

// AccountOwner is the user an account is opened with.
type AccountOwner struct {
	Username string `json:"username" minLength:"2" maxLength:"64" doc:"What the owner signs in with"`
	Name     string `json:"name,omitempty" maxLength:"128" doc:"The owner's name, as the panel shows it"`
	Email    string `json:"email,omitempty" maxLength:"254" doc:"Where the owner gets notifications"`
}

type openAccountInput struct {
	ChangeParams
	Body OpenAccountInput
}

// OpenedAccount is an account opened, with its owner.
type OpenedAccount struct {
	// ChangeSet adds the account and its owner together, so both are
	// there or neither is.
	ChangeSet *changeset.ChangeSet `json:"changeSet"`
	Owner     *identity.UserView   `json:"owner,omitempty"`
	// Link is the owner's sign-in link, shown once.
	Link *identity.IssuedLink `json:"link,omitempty"`
}

type openAccountOutput struct {
	Status int
	Body   OpenedAccount
}

// expectedFor is how long an account opened for a reseller is taken to be
// theirs before it is in the store, which covers a preview applied later.
const expectedFor = time.Hour

// userBase is where users' operations live: the generated ones of
// core.user, and those that help people sign in.
var userBase = KindPath(identity.UserKind.Info())

func (s *Server) registerUsers() {
	tags := []string{"Users"}
	withActor := func(ctx context.Context) (*auth.Actor, *identity.Service, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, nil, err
		}
		id, err := s.requireIdentity()
		return actor, id, err
	}
	op := func(o huma.Operation, info Info) huma.Operation {
		o.Tags, o.Security = tags, Protected
		Describe(&o, info)
		return o
	}

	huma.Register(s.api, op(huma.Operation{
		OperationID: "resetUser", Method: http.MethodPost, Path: userBase + "/{id}/reset", Summary: "Help a user back in",
		Description: "Helps a user who lost their way in: makes a sign-in link to choose a new password, removes their second steps, signs them out everywhere, " +
			"revokes their API tokens or lifts a lockout, as you choose. You need every role they have.",
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskHigh, StepUp: true}), func(ctx context.Context, in *resetInput) (*resetOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		res, err := id.ResetUser(ctx, actor, in.ID, in.Body)
		return &resetOutput{Body: res}, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "setUserPassword", Method: http.MethodPut, Path: userBase + "/{id}/password", Summary: "Set a user's password",
		Description: "Sets a user's password, which signs them out everywhere, or, without a password, only says whether they choose a new one " +
			"when they next sign in. Passwords are never kept as they are, only as Argon2id hashes. You need every role they have.",
		DefaultStatus: http.StatusNoContent,
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskHigh, StepUp: true}), func(ctx context.Context, in *userPasswordInput) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.SetUserPassword(ctx, actor, in.ID, in.Body)
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "issueSignInLink", Method: http.MethodPost, Path: userBase + "/{id}/link", Summary: "Make a sign-in link",
		Description: "Makes a sign-in link for a user to choose their password: a week to start using a new user, a day to get back in. It's shown once and replaces any earlier link.",
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskHigh, StepUp: true}), func(ctx context.Context, in *userPath) (*linkOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		link, err := id.IssueLink(ctx, actor, in.ID)
		return &linkOutput{Body: link}, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "listUserSessions", Method: http.MethodGet, Path: userBase + "/{id}/sessions", Summary: "List where a user is signed in",
		Description: "Lists a user's sessions, for helping them or looking into a problem.",
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskRead}), func(ctx context.Context, in *userPath) (*sessionsOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.UserSessions(ctx, actor, in.ID)
		out := &sessionsOutput{}
		out.Body.Sessions = list
		return out, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "listUserSSHKeys", Method: http.MethodGet, Path: userBase + "/{id}/ssh-keys", Summary: "List a user's SSH keys",
		Description: "Lists a user's SSH public keys as the panel reads them: each key's type, size and fingerprint. " +
			"They are part of the user's settings, which add_ssh_key and remove_ssh_key change.",
	}, Info{Permission: string(auth.PermUserRead), Risk: store.RiskRead}), func(ctx context.Context, in *userPath) (*sshKeysOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.UserSSHKeys(ctx, actor, in.ID)
		out := &sshKeysOutput{}
		out.Body.Keys = list
		return out, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "listUserTokens", Method: http.MethodGet, Path: userBase + "/{id}/tokens", Summary: "List a user's API tokens",
		Description: "Lists a user's API tokens, without their secrets.",
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskRead}), func(ctx context.Context, in *userPath) (*tokensOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.UserTokens(ctx, actor, in.ID)
		out := &tokensOutput{}
		out.Body.Tokens = list
		return out, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "revokeUserToken", Method: http.MethodDelete, Path: userBase + "/{id}/tokens/{token}", Summary: "Revoke a user's API token",
		Description:   "Stops one of a user's API tokens working, at once.",
		DefaultStatus: http.StatusNoContent,
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskMedium}), func(ctx context.Context, in *userTokenPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.RevokeUserToken(ctx, actor, in.ID, in.Token)
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "impersonateUser", Method: http.MethodPost, Path: userBase + "/{id}/impersonate", Summary: "View the panel as a user",
		Description: "Starts viewing the panel as one of your customers' users, to see what they see and help them. Everything you do meanwhile is recorded with both your names, " +
			"the panel shows a banner the whole time, and it ends after an hour at most. It needs core.impersonate, granted by name; admins of the whole server can't be impersonated.",
		DefaultStatus: http.StatusNoContent,
	}, Info{Permission: string(auth.PermImpersonate), Risk: store.RiskHigh, StepUp: true, Session: true}), func(ctx context.Context, in *userPath) (*cookieOnly, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		issued, err := id.Impersonate(ctx, actor, in.ID, c)
		if err != nil {
			return nil, err
		}
		return &cookieOnly{SetCookie: []http.Cookie{sessionCookie(issued, c.Secure)}}, nil
	})

	rolesOp := huma.Operation{
		OperationID: "listRoles", Method: http.MethodGet, Path: Prefix + "/roles", Tags: tags, Security: Protected,
		Summary: "List roles and permissions",
		Description: "Lists the roles, what each carries and where it can be given, and every permission with how far each role reaches with it: the permission matrix. " +
			"Collaborators get the permissions their grant names instead of read-only access, and everyone may see their own user and change their profile and SSH keys.",
	}
	Describe(&rolesOp, Info{Risk: store.RiskRead})
	huma.Register(s.api, rolesOp, func(ctx context.Context, _ *struct{}) (*rolesOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		out := &rolesOutput{}
		out.Body.Roles = auth.Roles()
		out.Body.Matrix = s.PermissionMatrix()
		out.Body.Self = auth.SelfPermissions
		return out, nil
	})

	openOp := huma.Operation{
		OperationID: "openCoreAccount", Method: http.MethodPost, Path: KindPath(account.Kind.Info()) + "/open", Tags: []string{"Accounts"}, Security: Protected,
		Summary: "Open an account",
		Description: "Opens an account, which becomes a Linux user, a home under /srv/racksail/accounts and a systemd slice with its limits, and gives it an owner who gets a sign-in link. " +
			"The account and its owner are one change set, so both are added or neither is. Admins open accounts for anyone; resellers for themselves. " +
			"With dryRun=true the answer is the preview and nothing is added.",
		Responses: map[string]*huma.Response{
			"200": jsonResponse("The preview, for a dry run", s.schemaOf(typeOfOpened, "OpenedAccount")),
			"202": jsonResponse("The account's change set, applied to the settings, with its owner", s.schemaOf(typeOfOpened, "OpenedAccount")),
		},
	}
	s.withProblems(&openOp)
	Describe(&openOp, Info{Permission: "core.account.create", Risk: store.RiskLow, DryRun: true, Reversible: true})
	huma.Register(s.api, openOp, func(ctx context.Context, in *openAccountInput) (*openAccountOutput, error) {
		return s.openAccount(ctx, in)
	})
}

// openAccount opens an account and adds its owner in one change set, then
// makes the owner a sign-in link.
func (s *Server) openAccount(ctx context.Context, in *openAccountInput) (*openAccountOutput, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	p := s.pipeline()
	if p == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	scope := store.ServerScope()
	if in.Body.Reseller != "" {
		scope = store.ResellerScope(in.Body.Reseller)
	}
	if err := Authorize(ctx, s.authorizer(), "core.account.create", auth.Target{Scope: scope}); err != nil {
		return nil, err
	}
	id := store.NewID(account.Kind.Info().Prefix)
	if in.Body.Reseller != "" && s.opts.Accounts != nil {
		// The pipeline checks the new account as the reseller's before it is
		// in the store; it was just found to be theirs to open.
		s.opts.Accounts.Expect(id, in.Body.Reseller, expectedFor)
	}
	spec, err := json.Marshal(account.AccountSpec{Slug: in.Body.Slug, Name: in.Body.Name, Reseller: in.Body.Reseller, Limits: in.Body.Limits, Note: in.Body.Note})
	if err != nil {
		return nil, err
	}
	home := store.AccountScope(id)
	changes := []changeset.Change{{Op: changeset.OpCreate, Kind: account.Kind.Name(), ID: id, Scope: home, Spec: spec}}
	var ownerID store.ID
	if o := in.Body.Owner; o != nil {
		ownerID = store.NewID(identity.PrefixUser)
		owner, err := json.Marshal(identity.UserSpec{
			Username: o.Username, Name: o.Name, Email: o.Email, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: home}},
		})
		if err != nil {
			return nil, err
		}
		changes = append(changes, changeset.Change{Op: changeset.OpCreate, Kind: identity.KindUser, ID: ownerID, Scope: home, Spec: owner})
	}
	res, err := Submit(ctx, p, in.ChangeParams, changeset.Request{Title: "Open the account " + in.Body.Name, Changes: changes})
	if err != nil {
		return nil, err
	}
	out := &openAccountOutput{Status: res.Status, Body: OpenedAccount{ChangeSet: res.Body}}
	if in.DryRun || ownerID == "" {
		return out, nil
	}
	ids, err := s.requireIdentity()
	if err != nil {
		return nil, err
	}
	link, err := ids.IssueLink(ctx, actor, ownerID)
	if err != nil {
		return nil, err
	}
	view, err := ids.UserView(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	out.Body.Owner, out.Body.Link = &view, &link
	return out, nil
}
