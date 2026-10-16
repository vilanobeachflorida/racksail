// Package account defines accounts, the tenants of a Racksail server: the
// kind core.account, its reconciler, which gives every account a Linux user,
// a home under /srv/racksail/accounts and a systemd slice with the
// account's limits, and the Ownership that tells the authorizer which
// reseller each account belongs to.
//
// An account owns itself: its scope is the account scope with its own ID,
// so a grant on the account covers the account resource too. Its own people
// may read it but only resellers and admins change it (auth.AboveAccount).
package account

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// AccountSpec is what an account should be.
type AccountSpec struct {
	Slug string `json:"slug" minLength:"3" maxLength:"16" pattern:"^[a-z][a-z0-9]*$" doc:"The account's short name, which is also the name of its Linux user: lowercase letters and digits, 3 to 16 characters, starting with a letter. It can't be changed." example:"acme"`
	Name string `json:"name" minLength:"1" maxLength:"128" doc:"What the account is called, such as the customer's name" example:"Acme Inc."`
	// Reseller is the reseller whose customer the account is.
	Reseller store.ID      `json:"reseller,omitempty" doc:"The reseller whose customer the account is; empty for the server's own customers. It can't be changed."`
	Limits   AccountLimits `json:"limits,omitzero" doc:"What the account's processes may use together, enforced by the kernel; empty means no limit"`
	Note     string        `json:"note,omitempty" maxLength:"1000" doc:"Notes for the people who run the server"`
}

// AccountLimits are what all of an account's processes may use together. They
// become the directives of the account's systemd slice.
type AccountLimits struct {
	MemoryHigh int64 `json:"memoryHigh,omitempty" minimum:"0" doc:"Memory in bytes above which the account's processes are slowed down and their memory reclaimed"`
	MemoryMax  int64 `json:"memoryMax,omitempty" minimum:"0" doc:"Memory in bytes the account's processes can never use more of"`
	CPU        int   `json:"cpu,omitempty" minimum:"0" maximum:"100000" doc:"CPU time, as a percentage of one CPU: 200 is two whole CPUs"`
	CPUWeight  int   `json:"cpuWeight,omitempty" minimum:"0" maximum:"10000" doc:"The account's share of CPU time when the CPUs are busy, from 1 to 10000, where 100 is the default"`
	IOWeight   int   `json:"ioWeight,omitempty" minimum:"0" maximum:"10000" doc:"The account's share of disk time when disks are busy, from 1 to 10000, where 100 is the default"`
	Tasks      int   `json:"tasks,omitempty" minimum:"0" maximum:"4194304" doc:"How many processes and threads the account may run at once"`
}

// AccountStatus is what the reconciler last saw of an account.
type AccountStatus struct {
	store.Status
	UID   int    `json:"uid,omitempty" doc:"The account's Linux user and group ID"`
	User  string `json:"user,omitempty" doc:"The account's Linux user"`
	Home  string `json:"home,omitempty" doc:"Where the account's files live"`
	Slice string `json:"slice,omitempty" doc:"The systemd slice the account's services run in"`
}

// Errors.
var (
	ErrSlugTaken    = i18n.Define("account.slug_taken", http.StatusConflict, "{value} is already the short name of another account.")
	ErrSlugReserved = i18n.Define("account.slug_reserved", http.StatusUnprocessableEntity, "{slug} is the name of a system user, so an account can't use it.")
	ErrNotEmpty     = i18n.Define("account.not_empty", http.StatusConflict, "The account still has {what}. Delete them first.")
	ErrOwnScope     = i18n.Define("account.own_scope", http.StatusUnprocessableEntity, "An account belongs to itself: give it the same ID in id and in scope, or leave both out and use the open operation.")
)

// Kind is core.account.
var Kind = store.Define[AccountSpec, AccountStatus](store.KindInfo{
	Name: "core.account", Prefix: "acct", Scope: store.ScopeAccount,
	Title: "Account", Plural: "accounts",
	Description: "A customer or tenant: the sites, mail, databases and files that belong together, and the people who manage them. " +
		"On the server an account is a Linux user with a home under /srv/racksail/accounts and a systemd slice, so the kernel enforces its limits " +
		"and its services can't see other accounts.",
	Indexes: []store.Index{
		{Name: "slug", Path: "slug", Unique: true, Fold: true, Conflict: ErrSlugTaken},
		{Name: "reseller", Path: "reseller"},
	},
	// Deleting an account removes its files; turning limits down can stop
	// its sites.
	Risks: store.Risks{Create: store.RiskLow, Update: store.RiskMedium, Delete: store.RiskCritical},
	UI: store.UIHints{
		Icon: "building-2", TitleField: "name", SubtitleField: "slug",
		Columns: []store.Column{{Field: "spec.slug"}, {Field: "spec.reseller", Format: "link"}, {Field: "status.phase", Format: "status"}, {Field: "createdAt", Format: "date"}},
		Groups: []store.FieldGroup{
			{Name: "Account", Fields: []string{"name", "slug", "reseller", "note"}},
			{Name: "Limits", Fields: []string{"limits"}},
		},
		Fields: map[string]store.FieldHint{
			"name":     {Order: 0},
			"slug":     {Order: 1, CreateOnly: true},
			"reseller": {Order: 2, CreateOnly: true, Widget: "resource", Advanced: true},
			"note":     {Order: 3, Widget: "textarea", Advanced: true},
			"limits":   {Order: 4},
		},
	},
}).WithValidator(validate)

func init() {
	store.RegisterKind(Kind)
}

// permCreate is the permission to create accounts.
const permCreate = auth.Permission("core.account.create")

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9]{2,15}$`)

// reservedSlugs are the users a Debian server already has, or may get from
// the packages Racksail installs, and names that read as the panel's own.
var reservedSlugs = []string{
	"root", "daemon", "bin", "sys", "sync", "games", "man", "lp", "mail", "news", "uucp", "proxy", "backup", "list",
	"irc", "gnats", "nobody", "admin", "administrator", "operator", "debian", "ubuntu", "postfix", "postgres", "mysql",
	"redis", "valkey", "memcache", "knot", "unbound", "cyrus", "rspamd", "clamav", "dovecot", "docker", "podman", "incus",
	"samba", "nut", "sshd", "ntp", "polkitd", "messagebus", "tss", "racksail", "panel", "agent", "edge", "system", "staff",
	"www", "web", "ftp", "sftp", "git", "user", "users", "guest", "support", "help", "localhost", "hostmaster",
	"postmaster", "webmaster", "abuse", "security", "noreply",
}

// Reserved reports whether slug can't be an account's short name.
func Reserved(slug string) bool { return slices.Contains(reservedSlugs, slug) }

func validate(ctx context.Context, obj, old *store.Object[AccountSpec, AccountStatus]) error {
	var fields []i18n.FieldError
	if obj.Scope.ID != obj.ID {
		return ErrOwnScope.New()
	}
	switch {
	case !slugPattern.MatchString(obj.Spec.Slug):
		fields = append(fields, i18n.Field("spec.slug", i18n.ErrBadValue, "reason", "use 3 to 16 lowercase letters and digits, starting with a letter"))
	case Reserved(obj.Spec.Slug):
		fields = append(fields, i18n.Field("spec.slug", ErrSlugReserved, "slug", obj.Spec.Slug))
	case old != nil && old.Spec.Slug != obj.Spec.Slug:
		fields = append(fields, i18n.Field("spec.slug", i18n.ErrImmutable))
	}
	if old != nil && old.Spec.Reseller != obj.Spec.Reseller {
		fields = append(fields, i18n.Field("spec.reseller", i18n.ErrImmutable))
	} else if r := obj.Spec.Reseller; r != "" && !r.Valid() {
		fields = append(fields, i18n.Field("spec.reseller", i18n.ErrBadValue, "reason", "it isn't a reseller's ID"))
	}
	l := obj.Spec.Limits
	if l.MemoryHigh > 0 && l.MemoryMax > 0 && l.MemoryHigh > l.MemoryMax {
		fields = append(fields, i18n.Field("spec.limits.memoryHigh", i18n.ErrBadValue, "reason", "it can't be above the memory maximum"))
	}
	if l.MemoryMax > 0 && l.MemoryMax < 32<<20 {
		fields = append(fields, i18n.Field("spec.limits.memoryMax", i18n.ErrBadValue, "reason", "an account needs at least 32 MiB"))
	}
	if l.Tasks > 0 && l.Tasks < 16 {
		fields = append(fields, i18n.Field("spec.limits.tasks", i18n.ErrBadValue, "reason", "an account needs room for at least 16 processes"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	if old == nil {
		return checkOwner(ctx, obj.Spec.Reseller)
	}
	return nil
}

// checkOwner makes sure whoever creates an account may create it for the
// reseller it names: a reseller only for themselves, an admin for anyone.
// It runs in the change pipeline, which gives the context the actor and
// the authorizer; without them there is nobody to check.
func checkOwner(ctx context.Context, reseller store.ID) error {
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return nil
	}
	az, ok := auth.AuthorizerFrom(ctx)
	if !ok {
		return nil
	}
	scope := store.ServerScope()
	if reseller != "" {
		scope = store.ResellerScope(reseller)
	}
	if err := az.Authorize(ctx, actor, permCreate, auth.Target{Scope: scope}); err != nil {
		if reseller == "" {
			return i18n.Invalid(i18n.Field("spec.reseller", i18n.ErrRequired))
		}
		return i18n.Invalid(i18n.Field("spec.reseller", i18n.ErrBadValue, "reason", fmt.Sprintf("you can't create accounts for %s", reseller)))
	}
	return nil
}
