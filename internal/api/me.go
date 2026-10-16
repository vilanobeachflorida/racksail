package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type meOutput struct {
	Body identity.Me
}

type profileInput struct {
	ChangeParams
	Body identity.ProfileInput
}

type passwordInput struct {
	Body identity.PasswordInput
}

type totpOutput struct {
	Body struct {
		Secret string `json:"secret" doc:"The secret, for typing into the app by hand"`
		URI    string `json:"uri" doc:"The otpauth:// link the QR code holds"`
		QR     string `json:"qr" doc:"The QR code, as a data: URL of a PNG image"`
	}
}

type codeInput struct {
	Body struct {
		Code string `json:"code" minLength:"6" maxLength:"16" doc:"A code from the authenticator app"`
	}
}

type recoveryCodesOutput struct {
	Body struct {
		Codes []string `json:"codes" doc:"The recovery codes, shown this once; each works once in place of a second step"`
	}
}

type idPath struct {
	ID store.ID `path:"id"`
}

type passkeysOutput struct {
	Body struct {
		Passkeys []identity.PasskeyView `json:"passkeys"`
	}
}

type passkeyRegisterInput struct {
	Body struct {
		Ceremony   string          `json:"ceremony"`
		Name       string          `json:"name,omitempty" maxLength:"64" doc:"What to call the passkey, such as the device it's on"`
		Credential json.RawMessage `json:"credential" doc:"The new passkey, as PublicKeyCredential.toJSON() gives it"`
	}
}

type passkeyOutput struct {
	Body identity.PasskeyView
}

type renameInput struct {
	ID   store.ID `path:"id"`
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"64"`
	}
}

type sessionsOutput struct {
	Body struct {
		Sessions []identity.SessionView `json:"sessions"`
	}
}

type endedOutput struct {
	Body struct {
		Ended int `json:"ended" doc:"How many sessions were ended"`
	}
}

type devicesInput struct {
	Device string `cookie:"racksail_device"`
}

type devicesOutput struct {
	Body struct {
		Devices []identity.DeviceView `json:"devices"`
	}
}

type tokensOutput struct {
	Body struct {
		Tokens []identity.TokenView `json:"tokens"`
	}
}

type tokenInput struct {
	Body identity.TokenInput
}

type createdTokenOutput struct {
	Status int
	Body   identity.CreatedToken
}

type sshKeysOutput struct {
	Body struct {
		Keys []auth.SSHKey `json:"keys"`
	}
}

type sshKeyInput struct {
	ChangeParams
	Body identity.SSHKeyInput
}

type sshKeyPath struct {
	ChangeParams
	ID store.ID `path:"id" doc:"The key's ID, which starts with sshkey_"`
}

type linkInput struct {
	Provider store.ID `path:"provider" doc:"The sign-in provider's ID"`
	Body     identity.LinkInput
}

type linkedOutput struct {
	Body identity.LinkView
}

// LinkStart is where to send the browser to link an identity at a
// provider with its own page.
type LinkStart struct {
	Location string `json:"location" doc:"The provider's page, which sends the browser back to the panel"`
}

type linkStartOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie" doc:"The cookie that carries the link through the provider's page and back"`
	Body      LinkStart
}

type linksOutput struct {
	Body struct {
		Links []identity.LinkView `json:"links"`
	}
}

type linkProviderPath struct {
	Provider store.ID `path:"provider" doc:"The sign-in provider's ID"`
}

type mfaOptionsOutput struct {
	Body struct {
		Methods []identity.MFAOption `json:"methods"`
	}
}

type mfaFinishInput struct {
	ID   store.ID `path:"id"`
	Body struct {
		Ceremony string `json:"ceremony"`
		Response string `json:"response,omitempty" maxLength:"256" doc:"What the method asked the person for; empty for methods without input"`
	}
}

// AlertChannelTypeView is a kind of channel security alerts can go to, with
// the schema of its settings.
type AlertChannelTypeView struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Secrets  []string        `json:"secrets,omitempty" doc:"The settings that hold secrets, which are kept encrypted with the rest and never shown again"`
	Settings json.RawMessage `json:"settings,omitempty" doc:"The JSON Schema of the channel's settings"`
}

// describeAlertSecrets says of a channel's secret settings how a person's
// alert settings keep them: encrypted with the rest, not apart in a
// change's secrets as the panel's own channels do.
func describeAlertSecrets(reg huma.Registry, schema *huma.Schema, names []string) {
	if schema.Ref != "" {
		schema = reg.SchemaFromRef(schema.Ref)
	}
	if schema == nil {
		return
	}
	for _, name := range names {
		if p := schema.Properties[name]; p != nil {
			p.Description = "Kept encrypted with the rest of the settings, and never shown again"
		}
	}
}

type alertsOutput struct {
	Body struct {
		Channel string                 `json:"channel,omitempty" doc:"Where security alerts go; empty when they're off"`
		Types   []AlertChannelTypeView `json:"types" doc:"Where they can go"`
	}
}

type alertsInput struct {
	Body identity.AlertChannelInput
}

// selfAccess says who may use an operation on their own user: anyone
// signed in, API tokens included, which can see but not change how one
// signs in; only a person in the panel; or only one who confirmed it's them
// recently.
type selfAccess int

const (
	anyone selfAccess = iota
	inPanel
	confirmed
)

// self describes an operation on the person's own user.
func self(op *huma.Operation, risk store.Risk, access selfAccess) {
	Describe(op, Info{Risk: risk, Session: access >= inPanel, StepUp: access == confirmed})
}

// registerMe adds what people do about themselves: their profile,
// password, second steps, sessions, trusted browsers, API tokens, SSH keys,
// linked identities and security alerts.
func (s *Server) registerMe() {
	tags := []string{"Your account"}
	base := Prefix + "/me"
	reg := func(op huma.Operation, risk store.Risk, access selfAccess) huma.Operation {
		op.Tags, op.Security = tags, Protected
		self(&op, risk, access)
		return op
	}
	withActor := func(ctx context.Context) (*auth.Actor, *identity.Service, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, nil, err
		}
		id, err := s.requireIdentity()
		return actor, id, err
	}
	// ownRecords is withActor for reading the caller's own records, which an
	// API token narrowed to other things doesn't reach.
	ownRecords := func(ctx context.Context) (*auth.Actor, *identity.Service, error) {
		actor, id, err := withActor(ctx)
		if err == nil {
			err = id.ReadOwn(ctx, actor)
		}
		return actor, id, err
	}

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "getMe", Method: http.MethodGet, Path: base, Summary: "See your user",
		Description: "Returns your user: your details, your roles, and how you sign in.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*meOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		me, err := id.MyProfile(ctx, actor)
		return &meOutput{Body: me}, err
	})

	updateMe := reg(huma.Operation{
		OperationID: "updateMe", Method: http.MethodPut, Path: base, Summary: "Change your details",
		Description: "Changes your name, email address and language, as a change to your user like any other: with dryRun=true the answer is its preview.",
		Responses:   changeSetResponses(s, "The change set, applied to your user"),
	}, store.RiskLow, inPanel)
	Describe(&updateMe, Info{Risk: store.RiskLow, DryRun: true, Reversible: true, Session: true})
	huma.Register(s.api, updateMe, func(ctx context.Context, in *profileInput) (*ChangeSetOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return s.submitOwn(ctx, in.ChangeParams, func() (changeset.Request, error) { return id.UpdateProfile(actor, in.Body) })
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "changeMyPassword", Method: http.MethodPut, Path: base + "/password", Summary: "Change your password",
		Description:   "Sets a new password, after checking the current one. It can also sign you out everywhere else.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskMedium, inPanel), func(ctx context.Context, in *passwordInput) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.ChangePassword(ctx, actor, in.Body)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "beginTotp", Method: http.MethodPost, Path: base + "/totp", Summary: "Set up an authenticator app",
		Description: "Makes a secret for an authenticator app, as a QR code to scan and as text to type in. It counts once a code from the app checks out.",
	}, store.RiskLow, confirmed), func(ctx context.Context, _ *struct{}) (*totpOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		key, err := id.BeginTOTP(ctx, actor)
		if err != nil {
			return nil, err
		}
		out := &totpOutput{}
		out.Body.Secret, out.Body.URI, out.Body.QR = key.Secret, key.URI, key.QR
		return out, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "confirmTotp", Method: http.MethodPost, Path: base + "/totp/confirm", Summary: "Turn the authenticator app on",
		Description: "Turns the authenticator app on with a code from it. When you have no recovery codes yet, the answer has your first ones, shown this once.",
	}, store.RiskMedium, inPanel), func(ctx context.Context, in *codeInput) (*recoveryCodesOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		codes, err := id.ConfirmTOTP(ctx, actor, in.Body.Code)
		if err != nil {
			return nil, err
		}
		out := &recoveryCodesOutput{}
		out.Body.Codes = nonNilStrings(codes)
		return out, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "removeTotp", Method: http.MethodDelete, Path: base + "/totp", Summary: "Remove the authenticator app",
		Description:   "Turns the authenticator app off. Without any second step left, your recovery codes and trusted browsers go too.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskHigh, confirmed), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.RemoveTOTP(ctx, actor)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "newRecoveryCodes", Method: http.MethodPost, Path: base + "/recovery-codes", Summary: "Make new recovery codes",
		Description: "Replaces your recovery codes with ten new ones, shown this once. Each works once, in place of a second step, when you can't use yours.",
	}, store.RiskMedium, confirmed), func(ctx context.Context, _ *struct{}) (*recoveryCodesOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		codes, err := id.NewRecoveryCodes(ctx, actor)
		if err != nil {
			return nil, err
		}
		out := &recoveryCodesOutput{}
		out.Body.Codes = codes
		return out, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMyPasskeys", Method: http.MethodGet, Path: base + "/passkeys", Summary: "List your passkeys",
		Description: "Lists the passkeys that sign you in, with when each was last used.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*passkeysOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MyPasskeys(ctx, actor)
		out := &passkeysOutput{}
		out.Body.Passkeys = list
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "beginPasskeyRegistration", Method: http.MethodPost, Path: base + "/passkeys", Summary: "Add a passkey",
		Description: "Returns the options for navigator.credentials.create() to make a passkey for the panel; send the new passkey to the finish operation.",
	}, store.RiskLow, confirmed), func(ctx context.Context, _ *struct{}) (*passkeyOptionsOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		ceremony, options, err := id.BeginPasskeyRegistration(ctx, actor, originOf(ctx))
		if err != nil {
			return nil, err
		}
		return &passkeyOptionsOutput{Body: PasskeyOptions{Ceremony: ceremony, Options: options}}, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "finishPasskeyRegistration", Method: http.MethodPost, Path: base + "/passkeys/finish", Summary: "Keep a new passkey",
		Description: "Keeps the passkey the browser made. It then signs you in on its own, and works as a second step after your password.",
	}, store.RiskMedium, inPanel), func(ctx context.Context, in *passkeyRegisterInput) (*passkeyOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		view, err := id.FinishPasskeyRegistration(ctx, actor, in.Body.Ceremony, in.Body.Name, in.Body.Credential, originOf(ctx))
		return &passkeyOutput{Body: view}, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "renamePasskey", Method: http.MethodPatch, Path: base + "/passkeys/{id}", Summary: "Rename a passkey",
		Description: "Gives one of your passkeys a new name.",
	}, store.RiskLow, inPanel), func(ctx context.Context, in *renameInput) (*passkeyOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		view, err := id.RenamePasskey(ctx, actor, in.ID, in.Body.Name)
		return &passkeyOutput{Body: view}, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "removePasskey", Method: http.MethodDelete, Path: base + "/passkeys/{id}", Summary: "Remove a passkey",
		Description:   "Stops one of your passkeys signing you in. The last way you have to sign in can't be removed.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskHigh, confirmed), func(ctx context.Context, in *idPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.RemovePasskey(ctx, actor, in.ID)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMySessions", Method: http.MethodGet, Path: base + "/sessions", Summary: "List where you're signed in",
		Description: "Lists your sessions, the browsers you're signed in from, with their address and when each was last used. The one asking is marked.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*sessionsOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MySessions(ctx, actor)
		out := &sessionsOutput{}
		out.Body.Sessions = list
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "endMySession", Method: http.MethodDelete, Path: base + "/sessions/{id}", Summary: "Sign out one session",
		Description:   "Ends one of your sessions, signing that browser out.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskLow, inPanel), func(ctx context.Context, in *idPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.EndMySession(ctx, actor, in.ID)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "endMyOtherSessions", Method: http.MethodPost, Path: base + "/sessions/end-others", Summary: "Sign out everywhere else",
		Description: "Ends every session of yours but the one asking.",
	}, store.RiskMedium, inPanel), func(ctx context.Context, _ *struct{}) (*endedOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		n, err := id.EndOtherSessions(ctx, actor)
		out := &endedOutput{}
		out.Body.Ended = n
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMyDevices", Method: http.MethodGet, Path: base + "/devices", Summary: "List your trusted browsers",
		Description: "Lists the browsers you trusted to skip the second sign-in step, until when.",
	}, store.RiskRead, anyone), func(ctx context.Context, in *devicesInput) (*devicesOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MyDevices(ctx, actor, in.Device)
		out := &devicesOutput{}
		out.Body.Devices = list
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "forgetMyDevice", Method: http.MethodDelete, Path: base + "/devices/{id}", Summary: "Stop trusting a browser",
		Description:   "Stops one browser skipping the second sign-in step.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskLow, inPanel), func(ctx context.Context, in *idPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.ForgetDevice(ctx, actor, in.ID)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "forgetMyDevices", Method: http.MethodDelete, Path: base + "/devices", Summary: "Stop trusting every browser",
		Description:   "Stops every browser you trusted skipping the second sign-in step.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskLow, inPanel), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.ForgetDevice(ctx, actor, "")
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMyTokens", Method: http.MethodGet, Path: base + "/tokens", Summary: "List your API tokens",
		Description: "Lists your API tokens with what each may do, where, until when, and when each was last used. Their secrets are never shown again.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*tokensOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MyTokens(ctx, actor)
		out := &tokensOutput{}
		out.Body.Tokens = list
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "createMyToken", Method: http.MethodPost, Path: base + "/tokens", Summary: "Create an API token",
		Description: "Makes an API token for scripts and the racksail command. It acts as you, with at most your own permissions, narrowed to those and the accounts you name, " +
			"from the addresses you name, until it expires or you revoke it. The token is shown this once.",
		DefaultStatus: http.StatusCreated,
	}, store.RiskHigh, confirmed), func(ctx context.Context, in *tokenInput) (*createdTokenOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		created, err := id.CreateToken(ctx, actor, in.Body)
		if err != nil {
			return nil, err
		}
		return &createdTokenOutput{Status: http.StatusCreated, Body: created}, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "revokeMyToken", Method: http.MethodDelete, Path: base + "/tokens/{id}", Summary: "Revoke an API token",
		Description: "Stops one of your API tokens working, at once. In the panel you can revoke any of yours; with an API token, only that token itself, " +
			"as racksail logout --revoke does.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskMedium, anyone), func(ctx context.Context, in *idPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.RevokeMyToken(ctx, actor, in.ID)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMySSHKeys", Method: http.MethodGet, Path: base + "/ssh-keys", Summary: "List your SSH keys",
		Description: "Lists your SSH public keys, which let you reach the files and shells your roles allow over SSH and SFTP.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*sshKeysOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MySSHKeys(ctx, actor)
		out := &sshKeysOutput{}
		out.Body.Keys = list
		return out, err
	})

	addKey := reg(huma.Operation{
		OperationID: "addMySSHKey", Method: http.MethodPost, Path: base + "/ssh-keys", Summary: "Add an SSH key",
		Description: "Adds an SSH public key to your user, as in a .pub file: Ed25519, ECDSA or RSA of 2048 bits or more, without options. " +
			"It's a change to your user like any other: with dryRun=true the answer is its preview.",
		Responses: changeSetResponses(s, "The change set, applied to your user"),
	}, store.RiskMedium, confirmed)
	Describe(&addKey, Info{Risk: store.RiskMedium, DryRun: true, Reversible: true, Session: true, StepUp: true})
	huma.Register(s.api, addKey, func(ctx context.Context, in *sshKeyInput) (*ChangeSetOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return s.submitOwn(ctx, in.ChangeParams, func() (changeset.Request, error) { return id.AddSSHKey(actor, in.Body) })
	})

	removeKey := reg(huma.Operation{
		OperationID: "removeMySSHKey", Method: http.MethodDelete, Path: base + "/ssh-keys/{id}", Summary: "Remove an SSH key",
		Description: "Removes one of your SSH keys, as a change to your user like any other: with dryRun=true the answer is its preview.",
		Responses:   changeSetResponses(s, "The change set, applied to your user"),
	}, store.RiskMedium, confirmed)
	Describe(&removeKey, Info{Risk: store.RiskMedium, DryRun: true, Reversible: true, Session: true, StepUp: true})
	huma.Register(s.api, removeKey, func(ctx context.Context, in *sshKeyPath) (*ChangeSetOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return s.submitOwn(ctx, in.ChangeParams, func() (changeset.Request, error) { return id.RemoveSSHKey(ctx, actor, in.ID) })
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMyLinks", Method: http.MethodGet, Path: base + "/links", Summary: "List your outside sign-ins",
		Description: "Lists the outside providers, such as a company directory, you sign in with.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*linksOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MyLinks(ctx, actor)
		out := &linksOutput{}
		out.Body.Links = list
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "linkMyIdentity", Method: http.MethodPost, Path: base + "/links/{provider}", Summary: "Sign in with a provider from now on",
		Description: "Links your account at a provider that checks passwords, such as a company directory, to your user: the panel checks your username and password there, " +
			"and doesn't keep them. From then on you can sign in with them.",
	}, store.RiskMedium, confirmed), func(ctx context.Context, in *linkInput) (*linkedOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		view, err := id.LinkMine(ctx, actor, in.Provider, in.Body, auth.ClientFrom(ctx))
		return &linkedOutput{Body: view}, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "startLinkingMyIdentity", Method: http.MethodPost, Path: base + "/links/{provider}/start", Summary: "Start linking a provider",
		Description: "Starts linking your account at a provider with its own sign-in page, such as an OpenID Connect provider, to your user. " +
			"Send the browser to the location in the answer; the provider sends it back to the panel, which links the account and shows your security settings.",
	}, store.RiskMedium, confirmed), func(ctx context.Context, in *linkProviderPath) (*linkStartOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		c := auth.ClientFrom(ctx)
		to, state, err := id.BeginLinkMine(ctx, actor, in.Provider, providerCallback(ctx, in.Provider))
		if err != nil {
			return nil, err
		}
		out := &linkStartOutput{Body: LinkStart{Location: to}}
		out.SetCookie = []http.Cookie{providerState(state, c.Secure), linkingCookie(true, c.Secure)}
		return out, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "unlinkMyIdentity", Method: http.MethodDelete, Path: base + "/links/{provider}", Summary: "Stop signing in with a provider",
		Description:   "Stops an outside provider signing you in. The last way you have to sign in can't be removed.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskMedium, confirmed), func(ctx context.Context, in *linkProviderPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.UnlinkMine(ctx, actor, in.Provider)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "listMyMfa", Method: http.MethodGet, Path: base + "/mfa", Summary: "List second steps from add-ons",
		Description: "Lists the second sign-in steps add-ons provide, and whether you have each on.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*mfaOptionsOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		list, err := id.MFAOptions(ctx, actor)
		out := &mfaOptionsOutput{}
		out.Body.Methods = list
		return out, err
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "beginMfa", Method: http.MethodPost, Path: base + "/mfa/{id}", Summary: "Turn on a second step from an add-on",
		Description: "Starts a check with a second step an add-on provides; it is on once the check passes.",
	}, store.RiskLow, confirmed), func(ctx context.Context, in *idPath) (*mfaOutput, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		ceremony, ch, err := id.BeginMFA(ctx, actor, in.ID)
		if err != nil {
			return nil, err
		}
		out := &mfaOutput{}
		out.Body.Ceremony, out.Body.Prompt, out.Body.Input = ceremony, ch.Prompt, ch.Input
		return out, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "finishMfa", Method: http.MethodPost, Path: base + "/mfa/{id}/finish", Summary: "Finish turning on a second step",
		Description:   "Checks the response to the method's check, and turns it on when it passes. A method without input answers auth.pending until the person approves.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskMedium, inPanel), func(ctx context.Context, in *mfaFinishInput) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.FinishMFA(ctx, actor, in.Body.Ceremony, in.Body.Response)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "disableMfa", Method: http.MethodDelete, Path: base + "/mfa/{id}", Summary: "Turn off a second step from an add-on",
		Description:   "Turns off a second sign-in step an add-on provides.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskHigh, confirmed), func(ctx context.Context, in *idPath) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.DisableMFA(ctx, actor, in.ID)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "getMyAlerts", Method: http.MethodGet, Path: base + "/alerts", Summary: "See where your security alerts go",
		Description: "Says where your security alerts go, sign-ins from new devices, lockouts and changes to how you sign in, and the channels they can go to.",
	}, store.RiskRead, anyone), func(ctx context.Context, _ *struct{}) (*alertsOutput, error) {
		actor, id, err := ownRecords(ctx)
		if err != nil {
			return nil, err
		}
		me, err := id.MyProfile(ctx, actor)
		if err != nil {
			return nil, err
		}
		out := &alertsOutput{}
		out.Body.Channel, out.Body.Types = me.Security.AlertChannel, []AlertChannelTypeView{}
		for _, t := range id.AlertChannelTypes() {
			v := AlertChannelTypeView{ID: t.ID, Title: t.Title, Secrets: t.Secrets}
			if t.Settings != nil {
				reg := huma.NewMapRegistry("#/$defs/", httpapi.SchemaNamer)
				settings := reg.Schema(reflect.TypeOf(t.Settings), true, "Settings")
				describeAlertSecrets(reg, settings, t.Secrets)
				if schema, err := standaloneIn(reg, settings); err == nil {
					v.Settings = schema
				}
			}
			out.Body.Types = append(out.Body.Types, v)
		}
		return out, nil
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "setMyAlerts", Method: http.MethodPut, Path: base + "/alerts", Summary: "Choose where your security alerts go",
		Description:   "Sends your security alerts to a notification channel, after opening it once to check its settings, or turns them off. The settings are kept encrypted and never shown again.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskMedium, confirmed), func(ctx context.Context, in *alertsInput) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.SetAlertChannel(ctx, actor, in.Body)
	})

	huma.Register(s.api, reg(huma.Operation{
		OperationID: "testMyAlerts", Method: http.MethodPost, Path: base + "/alerts/test", Summary: "Send a test alert",
		Description:   "Sends a test alert to your channel and says how it went.",
		DefaultStatus: http.StatusNoContent,
	}, store.RiskLow, inPanel), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		actor, id, err := withActor(ctx)
		if err != nil {
			return nil, err
		}
		return nil, id.TestAlert(ctx, actor)
	})
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// changeSetResponses documents the answers of an operation that answers
// with a change set: the preview of a dry run, or the applied change set.
func changeSetResponses(s *Server, applied string) map[string]*huma.Response {
	changeSet := s.schemaOf(typeOfChangeSet, "ChangeSet")
	return map[string]*huma.Response{
		"200": jsonResponse("The change set without applying it, for a dry run", changeSet),
		"202": jsonResponse(applied, changeSet),
	}
}

// submitOwn sends a change of the person's own user through the change
// pipeline. It names no revision, so the pipeline plans it again when
// something else changed the user while it was planned.
func (s *Server) submitOwn(ctx context.Context, params ChangeParams, build func() (changeset.Request, error)) (*ChangeSetOutput, error) {
	p := s.pipeline()
	if p == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	req, err := build()
	if err != nil {
		return nil, err
	}
	return Submit(ctx, p, params, req)
}
