package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// UserSpec is who a user is and what they may do. How they sign in isn't
// part of it: passwords, passkeys, second steps, sessions and API tokens
// are their credentials, which the identity service keeps apart.
type UserSpec struct {
	Username string `json:"username" minLength:"2" maxLength:"64" pattern:"^[a-z0-9][a-z0-9._@-]*$" doc:"What the person signs in with: lowercase letters, digits, dots, dashes, underscores and @" example:"ada"`
	Name     string `json:"name,omitempty" maxLength:"128" doc:"The person's name, as the panel shows it" example:"Ada Lovelace"`
	Email    string `json:"email,omitempty" maxLength:"254" doc:"Where the person gets notifications" example:"ada@example.com"`
	// Grants never reach outside where the user belongs.
	Grants    []auth.Grant `json:"grants,omitempty" maxItems:"100" doc:"What the user may do, and where. Grants never reach outside where the user belongs, and nobody gives a grant they couldn't use themselves"`
	Disabled  bool         `json:"disabled,omitempty" doc:"Turn the user off: they can't sign in, their sessions end and their API tokens stop working"`
	Locale    string       `json:"locale,omitempty" maxLength:"35" doc:"The panel's language for the person, such as en or pt-BR; empty follows their browser"`
	AllowFrom []string     `json:"allowFrom,omitempty" maxItems:"100" doc:"Addresses and networks the user may sign in from; empty allows anywhere the sign-in policy does"`
	SSHKeys   []UserSSHKey `json:"sshKeys,omitempty" maxItems:"50" doc:"SSH public keys that let the person reach the files and shells their roles allow"`
}

// UserSSHKey is one of a user's SSH public keys.
type UserSSHKey struct {
	ID        store.ID `json:"id" doc:"The key's ID, which starts with sshkey_ and comes from its fingerprint"`
	Name      string   `json:"name" minLength:"1" maxLength:"64" doc:"What the key is for, such as the laptop it's on"`
	PublicKey string   `json:"publicKey" minLength:"1" maxLength:"16384" doc:"The public key as an authorized_keys line, without options"`
}

// UserStatus is what the panel last saw of how the user signs in.
type UserStatus struct {
	store.Status
	PasswordSet        bool       `json:"passwordSet" doc:"Whether the user has a password"`
	MustChangePassword bool       `json:"mustChangePassword,omitempty" doc:"Whether they choose a new password when they next sign in"`
	TwoFactor          bool       `json:"twoFactor" doc:"Whether signing in takes a second step: an authenticator app, a passkey or a method from an add-on"`
	LockedUntil        *time.Time `json:"lockedUntil,omitempty" doc:"Until when failed sign-ins keep the user out"`
	LastSignInAt       *time.Time `json:"lastSignInAt,omitempty" doc:"When they last signed in"`
	LastSignInIP       string     `json:"lastSignInIp,omitempty" doc:"Where they last signed in from"`
}

// ProfileInput is what people change about themselves.
type ProfileInput struct {
	Name   string `json:"name" maxLength:"128" doc:"The name the panel shows"`
	Email  string `json:"email,omitempty" maxLength:"254" doc:"Where notifications go"`
	Locale string `json:"locale,omitempty" maxLength:"35" doc:"The panel's language, such as en or pt-BR; empty follows the browser"`
}

// SSHKeyInput is a new SSH key.
type SSHKeyInput struct {
	Name string `json:"name,omitempty" maxLength:"64" doc:"What the key is for; the key's comment when left out"`
	// PublicKey is the key as in an authorized_keys file or a .pub file.
	PublicKey string `json:"publicKey" minLength:"1" maxLength:"16384" doc:"The public key, as in a .pub file or an authorized_keys line, without options"`
}

// SSHKeyRef names one of a user's SSH keys.
type SSHKeyRef struct {
	ID store.ID `json:"id" doc:"The key's ID, which starts with sshkey_"`
}

// The actions of users, besides creating, changing and deleting them.
// Everyone may take them on their own user.
var userActions = []store.Action{
	{
		Name: "update_profile", Title: "Change profile", Summary: "Change a user's name, email address and language",
		Description: "Changes the name the panel shows, where notifications go and the panel's language. Everyone may change their own.",
		Risk:        store.RiskLow, Input: ProfileInput{}, Reversible: true, Past: "profile_updated",
	},
	{
		Name: "add_ssh_key", Title: "Add SSH key", Summary: "Add an SSH public key to a user",
		Description: "Adds an SSH public key, as in a .pub file: Ed25519, ECDSA or RSA of 2048 bits or more, without options. " +
			"The person gets an alert. Everyone may add keys to their own user.",
		Risk: store.RiskMedium, Input: SSHKeyInput{}, Reversible: true, Past: "ssh_key_added",
	},
	{
		Name: "remove_ssh_key", Title: "Remove SSH key", Summary: "Remove one of a user's SSH keys",
		Description: "Removes an SSH key, as when the laptop it was on is lost. Everyone may remove keys from their own user.",
		Risk:        store.RiskMedium, Input: SSHKeyRef{}, Reversible: true, Past: "ssh_key_removed",
	},
}

// UserKind is core.user.
var UserKind = store.Define[UserSpec, UserStatus](store.KindInfo{
	Name: KindUser, Prefix: PrefixUser, Scope: store.ScopeServer, Levels: []store.ScopeLevel{store.ScopeReseller, store.ScopeAccount},
	Title: "User", Plural: "users",
	Description: "Someone who signs in to the panel: their username, name and email address, what they may do and where, and their SSH keys. " +
		"A user belongs to the server, a reseller or an account, which decides who manages them, and their roles never reach outside it. " +
		"How they sign in, their password, passkeys, second steps, sessions and API tokens, is kept apart from their settings and never part of a change set.",
	Actions: userActions,
	Indexes: []store.Index{
		{Name: "username", Path: "username", Unique: true, Fold: true, Conflict: auth.ErrUsernameTaken},
		{Name: "email", Path: "email", Fold: true},
		{Name: "role", Path: "grants.role"},
		{Name: "grantOn", Path: "grants.scope.id"},
	},
	// Roles decide what someone may do anywhere, and turning a user off
	// locks them out.
	Risks:   store.Risks{Create: store.RiskHigh, Update: store.RiskHigh, Delete: store.RiskHigh},
	Confirm: true,
	UI: store.UIHints{
		Icon: "user-round", TitleField: "name", SubtitleField: "username", Hidden: true,
		Columns: []store.Column{
			{Field: "spec.username"}, {Field: "spec.name"}, {Field: "spec.email"},
			{Field: "status.lastSignInAt", Format: "date"}, {Field: "spec.disabled", Format: "badge"},
		},
		Fields: map[string]store.FieldHint{
			"username": {Order: 0}, "name": {Order: 1}, "email": {Order: 2}, "grants": {Order: 3}, "locale": {Order: 4, Advanced: true},
			"allowFrom": {Order: 5, Advanced: true}, "disabled": {Order: 6, Widget: "toggle"}, "sshKeys": {Order: 7, Advanced: true},
		},
	},
}).WithDefaults(defaultUser).WithValidator(validateUser)

func init() {
	store.RegisterKind(UserKind)
}

var (
	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{1,63}$`)
	localePattern   = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)
	emailPattern    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

// NormalizeUsername is how usernames are compared: trimmed and lowercase.
func NormalizeUsername(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func defaultUser(ctx context.Context, obj *store.Object[UserSpec, UserStatus]) error {
	normalize(&obj.Spec)
	return nil
}

// normalize tidies what people type: usernames in lowercase, no spaces
// around names, and lists that are there even when empty.
func normalize(spec *UserSpec) {
	spec.Username = NormalizeUsername(spec.Username)
	spec.Name, spec.Email, spec.Locale = strings.TrimSpace(spec.Name), strings.TrimSpace(spec.Email), strings.TrimSpace(spec.Locale)
	if spec.Grants == nil {
		spec.Grants = []auth.Grant{}
	}
}

// checkSpec checks what a user's settings look like, apart from who may
// set them.
func checkSpec(spec UserSpec) []i18n.FieldError {
	var fields []i18n.FieldError
	if !usernamePattern.MatchString(spec.Username) {
		fields = append(fields, i18n.Field("spec.username", i18n.ErrBadValue, "reason", "use 2 to 64 lowercase letters, digits, dots, dashes, underscores or @, starting with a letter or digit"))
	}
	fields = append(fields, checkProfile(ProfileInput{Name: spec.Name, Email: spec.Email, Locale: spec.Locale}, "spec.")...)
	if _, err := ParseNetworks(spec.AllowFrom); err != nil {
		fields = append(fields, i18n.Field("spec.allowFrom", i18n.ErrBadValue, "reason", err.Error()))
	}
	for i, g := range spec.Grants {
		if err := g.Validate(); err != nil {
			for _, f := range i18n.From(err).Fields {
				f.Path = fmt.Sprintf("spec.grants[%d].%s", i, f.Path)
				fields = append(fields, f)
			}
		}
	}
	seen := map[string]int{}
	for i, k := range spec.SSHKeys {
		where := fmt.Sprintf("spec.sshKeys[%d]", i)
		parsed, _, err := ParseSSHKey(k.PublicKey)
		if err != nil {
			fields = append(fields, i18n.Field(where+".publicKey", auth.ErrSSHKeyInvalid, "reason", i18n.From(err).Params["reason"]))
			continue
		}
		if j, dup := seen[parsed.Fingerprint]; dup {
			fields = append(fields, i18n.Field(where+".publicKey", auth.ErrSSHKeyExists, "name", spec.SSHKeys[j].Name))
		}
		seen[parsed.Fingerprint] = i
		if k.ID != keyID(parsed.Fingerprint) {
			fields = append(fields, i18n.Field(where+".id", i18n.ErrBadValue, "reason", "a key's ID comes from its fingerprint: "+string(keyID(parsed.Fingerprint))))
		}
		if strings.TrimSpace(k.Name) == "" || utf8.RuneCountInString(k.Name) > 64 || strings.ContainsAny(k.Name, "\x00\n\r") {
			fields = append(fields, i18n.Field(where+".name", i18n.ErrBadValue, "reason", "use 1 to 64 characters on one line"))
		}
	}
	return fields
}

// checkProfile checks a name, an email address and a language; prefix says
// where the fields are in the request.
func checkProfile(in ProfileInput, prefix string) []i18n.FieldError {
	var fields []i18n.FieldError
	name, email := strings.TrimSpace(in.Name), strings.TrimSpace(in.Email)
	if utf8.RuneCountInString(name) > 128 || strings.ContainsAny(name, "\x00\n\r") {
		fields = append(fields, i18n.Field(prefix+"name", i18n.ErrBadValue, "reason", "use at most 128 characters on one line"))
	}
	if email != "" && (len(email) > 254 || !emailPattern.MatchString(email)) {
		fields = append(fields, i18n.Field(prefix+"email", i18n.ErrBadValue, "reason", "it isn't an email address"))
	}
	if l := strings.TrimSpace(in.Locale); l != "" && !localePattern.MatchString(l) {
		fields = append(fields, i18n.Field(prefix+"locale", i18n.ErrBadValue, "reason", "use a language tag such as en or pt-BR"))
	}
	return fields
}

// keyID is the ID an SSH key has on every user, from its fingerprint, so
// a key added in a preview has the ID it gets once applied.
func keyID(fingerprint string) store.ID {
	sum := sha256.Sum256([]byte(fingerprint))
	var u uuid.UUID
	copy(u[:], sum[:16])
	// A UUID of version 8, whose bits are the hash's.
	u[6] = u[6]&0x0f | 0x80
	u[8] = u[8]&0x3f | 0x80
	return store.FromUUID(PrefixSSHKey, u)
}

// validateUser checks a user's settings, and, in the change pipeline, that
// whoever changes them may: grants stay where the user belongs and the
// actor could use each grant they give or take away, nobody turns
// themselves off, and the server keeps an admin who can sign in.
func validateUser(ctx context.Context, obj, old *store.Object[UserSpec, UserStatus]) error {
	spec := obj.Spec
	fields := checkSpec(spec)
	if len(spec.Grants) > 100 {
		fields = append(fields, i18n.Field("spec.grants", i18n.ErrBadValue, "reason", "a user can have at most 100 grants"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return nil
	}
	az, ok := auth.AuthorizerFrom(ctx)
	if !ok {
		return nil
	}
	gc, ok := az.(auth.GrantChecker)
	if !ok {
		return fmt.Errorf("identity: the authorizer can't check grants")
	}
	for i, g := range spec.Grants {
		within, err := gc.Covers(ctx, obj.Scope, g.Scope)
		if err != nil {
			return err
		}
		if !within {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.grants[%d].scope", i), i18n.ErrBadValue, "reason", "it is outside where the user belongs"))
		}
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	added, removed := spec.Grants, []auth.Grant(nil)
	if old != nil {
		added, removed = diffGrants(old.Spec.Grants, spec.Grants)
	}
	for _, g := range append(added, removed...) {
		if err := gc.CanGrant(ctx, actor, g); err != nil {
			return err
		}
	}
	if old == nil {
		return nil
	}
	if err := ownSignIn(actor, obj.ID, old.Spec, spec); err != nil {
		return err
	}
	if spec.Disabled && !old.Spec.Disabled && (actor.Person() == obj.ID || actor.Impersonator == obj.ID) {
		return auth.ErrSelfDelete.New()
	}
	if adminGrants(old.Spec.Grants) && !old.Spec.Disabled && (!adminGrants(spec.Grants) || spec.Disabled) {
		return keepAnAdmin(ctx, obj.ID, old.Spec.Username)
	}
	return nil
}

// ownSignIn refuses a change to how a user signs in, their username, email
// address, allowed addresses or SSH keys, that they make to themselves other
// than in their own session in the panel. Through an API token, or with
// someone viewing the panel as them, such a change would give a way in that
// outlasts the token or the viewing: an SSH key, or an email address an
// outside provider's sign-in is matched by. Others change them with the
// permissions for it, and the person is told about SSH keys someone added.
func ownSignIn(actor *auth.Actor, id store.ID, old, next UserSpec) error {
	if actor.Person() != id || (actor.Type == auth.ActorUser && actor.Impersonator == "") {
		return nil
	}
	if old.Username == next.Username && strings.EqualFold(old.Email, next.Email) && slices.Equal(old.AllowFrom, next.AllowFrom) &&
		sameKeys(old.SSHKeys, next.SSHKeys) {
		return nil
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "change how they sign in")
	}
	return auth.ErrPanelOnly.New()
}

// sameKeys reports whether two lists hold the same SSH keys, whatever their
// names and order.
func sameKeys(a, b []UserSSHKey) bool {
	if len(a) != len(b) {
		return false
	}
	ids := map[store.ID]bool{}
	for _, k := range a {
		ids[k.ID] = true
	}
	for _, k := range b {
		if !ids[k.ID] {
			return false
		}
	}
	return true
}

// keepAnAdmin refuses a change that leaves the server without an admin who
// can sign in, as the change set would leave it.
func keepAnAdmin(ctx context.Context, leaving store.ID, username string) error {
	r, ok := store.ReaderFrom(ctx)
	if !ok {
		return nil
	}
	left, err := adminsLeft(ctx, r, leaving)
	if err != nil {
		return err
	}
	if !left {
		return auth.ErrLastAdmin.New("user", username)
	}
	return nil
}

// adminsLeft reports whether a user other than except is an admin of the
// whole server who can sign in.
func adminsLeft(ctx context.Context, r store.Reader, except store.ID) (bool, error) {
	q := store.Query{Kind: KindUser, Index: map[string]string{"role": string(auth.RoleAdmin)}, Limit: store.MaxLimit}
	for {
		page, err := r.List(ctx, q)
		if err != nil {
			return false, err
		}
		for _, item := range page.Items {
			var spec UserSpec
			if err := json.Unmarshal(item.Spec, &spec); err != nil {
				return false, err
			}
			if item.ID != except && !item.Deleted() && !spec.Disabled && adminGrants(spec.Grants) {
				return true, nil
			}
		}
		if page.Next == "" {
			return false, nil
		}
		q.Cursor = page.Next
	}
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
