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

type listUsersInput struct {
	Home   string    `query:"home" doc:"Only users who belong here: server, reseller:<id> or account:<id>"`
	Grant  string    `query:"grant" doc:"Only users with a grant on this scope: server, reseller:<id> or account:<id>"`
	Role   auth.Role `query:"role" enum:"admin,reseller,owner,collaborator,readonly" doc:"Only users with this role somewhere"`
	Search string    `query:"q" doc:"Only users whose username, name or email contains this text"`
	PageParams
}

type usersOutput struct {
	Body struct {
		Users []identity.UserView `json:"users"`
		Next  string              `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type userInput struct {
	Body identity.UserInput
}

type userOutput struct {
	Status int
	Body   identity.UserView
}

type userPath struct {
	ID store.ID `path:"id" doc:"The user's ID, which starts with user_"`
}

type updateUserInput struct {
	ID       store.ID `path:"id" doc:"The user's ID, which starts with user_"`
	Revision int64    `query:"revision" doc:"The revision you read; when someone changed the user since, the change fails with a conflict"`
	Body     identity.UserInput
}

type resetInput struct {
	ID   store.ID `path:"id" doc:"The user's ID, which starts with user_"`
	Body identity.ResetInput
}

type resetOutput struct {
	Body identity.ResetResult
}

type linkOutput struct {
	Body identity.IssuedLink
}

type userTokenPath struct {
	ID    store.ID `path:"id" doc:"The user's ID"`
	Token store.ID `path:"token" doc:"The token's ID, which starts with tok_"`
}

type userKeyPath struct {
	ID  store.ID `path:"id" doc:"The user's ID"`
	Key store.ID `path:"key" doc:"The key's ID, which starts with sshkey_"`
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

func scopeParam(name, value string) (*store.Scope, error) {
	if value == "" {
		return nil, nil
	}
	sc, err := store.ParseScope(value)
	if err != nil {
		return nil, badParam(name, "use server, reseller:<id> or account:<id>")
	}
	return &sc, nil
}

func (s *Server) registerUsers() {
	tags := []string{"Users"}
	base := Prefix + "/users"
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
		OperationID: "listUsers", Method: http.MethodGet, Path: base, Summary: "List users",
		Description: "Lists the users you manage, by username: those who belong where your core.user.read reaches. Filter by where they belong, where they have a grant, role and search text.",
	}, Info{Permission: string(auth.PermUserRead), Risk: store.RiskRead}), func(ctx context.Context, in *listUsersInput) (*usersOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		q := identity.UserQuery{Role: in.Role, Search: in.Search, Cursor: in.Cursor, Limit: in.Limit}
		home, err := scopeParam("home", in.Home)
		if err != nil {
			return nil, err
		}
		if home != nil {
			q.Homes = []store.Scope{*home}
		}
		if q.Grant, err = scopeParam("grant", in.Grant); err != nil {
			return nil, err
		}
		users, next, err := id.ListUsers(ctx, actor, q)
		if err != nil {
			if i18n.ErrForbidden.Is(err) {
				refused(ctx, auth.PermUserRead, identity.KindUser, "", store.ServerScope(), err)
			}
			return nil, err
		}
		out := &usersOutput{}
		out.Body.Users, out.Body.Next = users, next
		return out, nil
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "createUser", Method: http.MethodPost, Path: base, Summary: "Add a user",
		Description: "Adds a user where you manage users, with the roles you give them, which can't be more than you have there yourself. " +
			"Without a first password, make them a sign-in link to choose one.",
		DefaultStatus: http.StatusCreated,
	}, Info{Permission: string(auth.PermUserCreate), Risk: store.RiskHigh, StepUp: true}), func(ctx context.Context, in *userInput) (*userOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		u, err := id.CreateUser(ctx, actor, in.Body)
		return &userOutput{Status: http.StatusCreated, Body: u}, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "getUser", Method: http.MethodGet, Path: base + "/{id}", Summary: "Get a user",
		Description: "Returns a user you manage: their details, roles and how they sign in, never their secrets.",
	}, Info{Permission: string(auth.PermUserRead), Risk: store.RiskRead}), func(ctx context.Context, in *userPath) (*userOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		u, err := id.GetUser(ctx, actor, in.ID)
		return &userOutput{Status: http.StatusOK, Body: u}, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "updateUser", Method: http.MethodPut, Path: base + "/{id}", Summary: "Change a user",
		Description: "Changes a user's details and roles, or turns them off, which signs them out everywhere. You can only give or take away roles you have yourself, " +
			"and the last admin who can sign in stays one. Send the revision you read.",
	}, Info{Permission: string(auth.PermUserUpdate), Risk: store.RiskHigh, StepUp: true}), func(ctx context.Context, in *updateUserInput) (*userOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		u, err := id.UpdateUser(ctx, actor, in.ID, in.Body, in.Revision)
		return &userOutput{Status: http.StatusOK, Body: u}, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "deleteUser", Method: http.MethodDelete, Path: base + "/{id}", Summary: "Delete a user",
		Description:   "Deletes a user with their sessions, API tokens, passkeys and SSH keys. You can't delete yourself, nor someone with roles you don't have.",
		DefaultStatus: http.StatusNoContent,
	}, Info{Permission: string(auth.PermUserDelete), Risk: store.RiskHigh, Destructive: true, StepUp: true}), func(ctx context.Context, in *userPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.DeleteUser(ctx, actor, in.ID)
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "resetUser", Method: http.MethodPost, Path: base + "/{id}/reset", Summary: "Help a user back in",
		Description: "Helps a user who lost their way in: makes a sign-in link to choose a new password, removes their second steps, signs them out everywhere, " +
			"revokes their API tokens or lifts a lockout, as you choose.",
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskHigh, StepUp: true}), func(ctx context.Context, in *resetInput) (*resetOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		res, err := id.ResetUser(ctx, actor, in.ID, in.Body)
		return &resetOutput{Body: res}, err
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "issueSignInLink", Method: http.MethodPost, Path: base + "/{id}/link", Summary: "Make a sign-in link",
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
		OperationID: "listUserSessions", Method: http.MethodGet, Path: base + "/{id}/sessions", Summary: "List where a user is signed in",
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
		OperationID: "listUserTokens", Method: http.MethodGet, Path: base + "/{id}/tokens", Summary: "List a user's API tokens",
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
		OperationID: "revokeUserToken", Method: http.MethodDelete, Path: base + "/{id}/tokens/{token}", Summary: "Revoke a user's API token",
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
		OperationID: "listUserSSHKeys", Method: http.MethodGet, Path: base + "/{id}/ssh-keys", Summary: "List a user's SSH keys",
		Description: "Lists a user's SSH public keys.",
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
		OperationID: "removeUserSSHKey", Method: http.MethodDelete, Path: base + "/{id}/ssh-keys/{key}", Summary: "Remove a user's SSH key",
		Description:   "Removes one of a user's SSH keys, as when a laptop with it was lost.",
		DefaultStatus: http.StatusNoContent,
	}, Info{Permission: string(auth.PermUserReset), Risk: store.RiskMedium}), func(ctx context.Context, in *userKeyPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.RemoveUserSSHKey(ctx, actor, in.ID, in.Key)
	})

	huma.Register(s.api, op(huma.Operation{
		OperationID: "impersonateUser", Method: http.MethodPost, Path: base + "/{id}/impersonate", Summary: "View the panel as a user",
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
			"Collaborators get the permissions their grant names instead of read-only access.",
	}
	Describe(&rolesOp, Info{Risk: store.RiskRead})
	huma.Register(s.api, rolesOp, func(ctx context.Context, _ *struct{}) (*rolesOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		out := &rolesOutput{}
		out.Body.Roles = auth.Roles()
		out.Body.Matrix = s.PermissionMatrix()
		return out, nil
	})

	openOp := huma.Operation{
		OperationID: "openCoreAccount", Method: http.MethodPost, Path: KindPath(account.Kind.Info()) + "/open", Tags: []string{"Accounts"}, Security: Protected,
		Summary: "Open an account",
		Description: "Opens an account, which becomes a Linux user, a home under /srv/racksail/accounts and a systemd slice with its limits, and gives it an owner who gets a sign-in link. " +
			"Admins open accounts for anyone; resellers for themselves. With dryRun=true the answer is the preview and nobody is added.",
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

// openAccount opens an account and, unless it's a dry run, adds its owner.
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
	res, err := Submit(ctx, p, in.ChangeParams, changeset.Request{
		Title:   "Open the account " + in.Body.Name,
		Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: account.Kind.Name(), ID: id, Scope: store.AccountScope(id), Spec: spec}},
	})
	if err != nil {
		return nil, err
	}
	out := &openAccountOutput{Status: res.Status, Body: OpenedAccount{ChangeSet: res.Body}}
	if in.DryRun || in.Body.Owner == nil {
		return out, nil
	}
	ids, err := s.requireIdentity()
	if err != nil {
		return nil, err
	}
	o := in.Body.Owner
	owner, err := ids.CreateUser(ctx, actor, identity.UserInput{
		Username: o.Username, Name: o.Name, Email: o.Email, Home: &store.Scope{Level: store.ScopeAccount, ID: id},
		Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(id)}},
	})
	if err != nil {
		return nil, err
	}
	link, err := ids.IssueLink(ctx, actor, owner.ID)
	if err != nil {
		return nil, err
	}
	out.Body.Owner, out.Body.Link = &owner, &link
	return out, nil
}
