// Package addons runs add-ons: packages from a catalog that extend the
// panel from a sandboxed process of their own.
//
// An installed add-on is a core.addon resource, or a core.account_addon
// one when an account owner installed it for their account. Its reconciler
// unpacks the add-on's program, writes its systemd service from the
// racksail-addon@ template and starts its socket, and the runtime attaches
// the hooks, providers and extensions its manifest declares to the hook
// pipeline. The panel calls the add-on over its Unix socket with only the
// data its permissions grant, and the add-on calls the panel's API through
// the gateway socket with a token limited to those permissions.
//
// Safe mode turns every add-on off: from the console, as a boot option,
// and on its own when an add-on keeps crashing as it starts.
package addons

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Package is an add-on's program: an archive downloaded over https and
// checked against its SHA-256 before it is unpacked into a directory of its
// own, with an executable called addon at its top.
type Package struct {
	URL             string `json:"url" format:"uri" pattern:"^https://" doc:"Where the archive is downloaded from"`
	SHA256          string `json:"sha256" pattern:"^[0-9a-f]{64}$" doc:"The archive's SHA-256, which the download must match"`
	Size            int64  `json:"size" minimum:"1" maximum:"536870912" doc:"The archive's size in bytes"`
	Format          string `json:"format" enum:"tar.gz,tar.xz,tar.zst,zip" doc:"The archive's format"`
	StripComponents int    `json:"stripComponents,omitempty" minimum:"0" maximum:"4" doc:"Leading directories to drop from the archive's paths"`
}

// AddonSpec is an installed add-on: its manifest, its program and the
// permissions the person installing it approved.
type AddonSpec struct {
	// ID is the add-on's id, as in its manifest.
	ID string `json:"id" pattern:"^[a-z0-9][a-z0-9-]{1,62}$" doc:"The add-on's id, as in its manifest"`
	// Manifest is the add-on's addon.yaml.
	Manifest string  `json:"manifest" minLength:"1" maxLength:"262144" doc:"The add-on's addon.yaml"`
	Package  Package `json:"package" doc:"The add-on's program: an archive and the checksum it must match"`
	// Catalog and Item say where it was installed from.
	Catalog string `json:"catalog,omitempty" doc:"The catalog it was installed from"`
	Item    string `json:"item,omitempty" doc:"Its item in that catalog"`
	// Approved are the permissions the person installing it approved; they
	// cover everything the manifest asks for.
	Approved hooks.Permissions `json:"approved" doc:"The permissions the person installing the add-on approved"`
	// Enabled runs the add-on; off, it keeps its settings and doesn't run.
	Enabled bool `json:"enabled" doc:"Whether the add-on runs"`
}

// State is how an add-on is doing.
type State string

// Add-on states.
const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	// StateSafeMode says safe mode keeps it from running.
	StateSafeMode State = "safe_mode"
	StateFailed   State = "failed"
)

// AddonStatus is what the runtime last saw of an add-on.
type AddonStatus struct {
	store.Status
	State   State  `json:"state,omitempty" enum:"running,stopped,safe_mode,failed" doc:"Whether the add-on runs"`
	Version string `json:"version,omitempty" doc:"The version whose program runs"`
	// Hooks counts the hooks, providers and extensions attached.
	Hooks int `json:"hooks,omitempty" doc:"How many hooks, providers and extensions are attached"`
	// Hosts are the hosts it may reach and the addresses they resolved to
	// when it was last reconciled, which its service may connect to.
	Hosts []Host `json:"hosts,omitempty" doc:"The hosts the add-on may reach and the addresses they resolved to"`
	// Health is what the add-on last answered when asked how it is.
	Health    string     `json:"health,omitempty" enum:"ok,degraded,unreachable" doc:"What the add-on last answered when asked how it is"`
	CheckedAt *time.Time `json:"checkedAt,omitempty" doc:"When it was last asked"`
	// Slice, MemoryMB and CPUPercent are where its service runs and what
	// it may use, as last set up: an account's add-on runs in the account's
	// slice, within what its plan allows. Its service starts again when
	// they change.
	Slice      string `json:"slice,omitempty" doc:"The slice its service runs in: the account's for an account's add-on, racksail.slice otherwise"`
	MemoryMB   int    `json:"memoryMB,omitempty" doc:"The most memory its service may use, in MB"`
	CPUPercent int    `json:"cpuPercent,omitempty" doc:"The most CPU time its service may use, in percent of one CPU"`
}

// Error codes.
var (
	ErrNotApproved = i18n.Define("addons.not_approved", http.StatusUnprocessableEntity, "{addon} asks for {permission}, which wasn't approved.")
	ErrManifest    = i18n.Define("addons.manifest", http.StatusUnprocessableEntity, "The add-on's manifest can't be used: {reason}.")
	ErrTenancy     = i18n.Define("addons.tenancy", http.StatusForbidden, "{addon} can't be installed for an account: {reason}.")
	ErrSafeMode    = i18n.Define("addons.safe_mode", http.StatusConflict, "Safe mode is on, so no add-on runs: {reason}")
	ErrRefused     = i18n.Define("addons.refused", http.StatusForbidden, "{reason}")
	ErrFieldDenied = i18n.Define("addons.field_denied", http.StatusForbidden, "{addon} may not change {field} of a {kind}.")
	ErrUnknown     = i18n.Define("addons.unknown", http.StatusNotFound, "There is no add-on called {addon}.")
	// ErrAdminApproval refuses approving what only a server admin may
	// approve to someone who isn't one.
	ErrAdminApproval = i18n.Define("addons.admin_approval", http.StatusForbidden, "Only a server admin can approve {permission} for {addon}.")
	// ErrNotInCatalog refuses an add-on that isn't what the catalog it
	// names offers.
	ErrNotInCatalog = i18n.Define("addons.not_in_catalog", http.StatusForbidden, "{addon} isn't what the {catalog} catalog offers: {reason}.")
	// ErrNoAccount keeps an account's add-on from running where it would
	// escape its account.
	ErrNoAccount = i18n.Define("addons.no_account", http.StatusConflict, "{addon} can't run: {reason}.")
)

var addonUI = store.UIHints{
	Icon: "puzzle", TitleField: "id", SubtitleField: "item",
	Columns: []store.Column{{Field: "spec.id"}, {Field: "status.version"}, {Field: "spec.enabled", Format: "badge"}, {Field: "status.state", Format: "status"}, {Field: "status.health", Format: "badge"}},
	Fields: map[string]store.FieldHint{
		"id": {Order: 0, CreateOnly: true}, "manifest": {Order: 1, Widget: "code"}, "enabled": {Order: 2, Widget: "toggle"},
		"approved": {Order: 3}, "package": {Order: 4, Advanced: true}, "catalog": {Advanced: true}, "item": {Advanced: true},
	},
}

// An add-on runs code on the server, so installing or changing one is
// never a small thing.
var addonRisks = store.Risks{Create: store.RiskHigh, Update: store.RiskHigh, Delete: store.RiskMedium}

// The kinds of installed add-ons: those a server admin installed, which
// may see and do whatever was approved across the server, and those an
// account owner installed, which only ever see and change their account.
var (
	Addon = store.Define[AddonSpec, AddonStatus](store.KindInfo{
		Name: "core.addon", Prefix: "addon", Scope: store.ScopeServer, Title: "Add-on", Plural: "addons",
		Description: "An add-on a server admin installed: a program from a catalog that runs in a sandboxed service of its own and takes part " +
			"in the panel through hooks, providers, API routes and screens, with the permissions that were approved for it.",
		Indexes: []store.Index{{Name: "addon", Path: "id", Unique: true}},
		Risks:   addonRisks, UI: addonUI,
	}).WithValidator(validateAddon[AddonStatus](false))
	AccountAddon = store.Define[AddonSpec, AddonStatus](store.KindInfo{
		Name: "core.account_addon", Prefix: "acaddon", Scope: store.ScopeAccount, Title: "Account add-on", Plural: "account-addons",
		Description: "An add-on an account owner installed for their account. It only sees and changes what belongs to the account, " +
			"and can never patch services' configuration or hold core.override.",
		Indexes: []store.Index{{Name: "addon", Path: "id", Unique: true, PerScope: true}},
		Risks:   addonRisks, UI: addonUI,
	}).WithValidator(validateAddon[AddonStatus](true))
)

func init() {
	store.RegisterKind(Addon)
	store.RegisterKind(AccountAddon)
}

// Kinds are the kinds of installed add-ons.
func Kinds() []*store.KindOf[AddonSpec, AddonStatus] {
	return []*store.KindOf[AddonSpec, AddonStatus]{Addon, AccountAddon}
}

func validateAddon[T any](account bool) func(ctx context.Context, obj, old *store.Object[AddonSpec, T]) error {
	return func(ctx context.Context, obj, old *store.Object[AddonSpec, T]) error {
		if _, err := Check(obj.Spec, account); err != nil {
			e := i18n.From(err)
			path := "spec"
			switch e.Code {
			case ErrManifest.Code, ErrTenancy.Code:
				path = "spec.manifest"
			case ErrNotApproved.Code:
				path = "spec.approved"
			}
			return i18n.Invalid(i18n.Field(path, ErrManifest, "reason", e.Message))
		}
		if old != nil && old.Spec.ID != obj.Spec.ID {
			return i18n.Invalid(i18n.Field("spec.id", i18n.ErrImmutable))
		}
		return nil
	}
}

// Check parses an add-on's manifest and checks it against the rest of its
// spec: the same id, a program that can be downloaded, permissions that
// cover what it asks for and, for account add-ons, nothing only a server
// admin can grant and a catalog item it comes from.
func Check(spec AddonSpec, account bool) (*hooks.AddonManifest, error) {
	man, err := hooks.ParseAddonManifest([]byte(spec.Manifest))
	if err != nil {
		return nil, ErrManifest.Wrap(err, "reason", trimReason(err))
	}
	if man.ID != spec.ID {
		return nil, ErrManifest.New("reason", fmt.Sprintf("its id is %s, not %s", man.ID, spec.ID))
	}
	a := system.Archive{URL: spec.Package.URL, SHA256: spec.Package.SHA256, Size: spec.Package.Size, Format: spec.Package.Format,
		StripComponents: spec.Package.StripComponents, Dest: AddonDir(spec.ID, man.Version)}
	if err := a.Validate(); err != nil {
		return nil, ErrManifest.Wrap(err, "reason", "its program can't be downloaded: "+err.Error())
	}
	if account {
		if err := man.AccountInstalled(); err != nil {
			return nil, ErrTenancy.Wrap(err, "addon", man.Name, "reason", trimReason(err))
		}
		if spec.Approved.Override || slices.Contains(spec.Approved.Hooks, hooks.System) {
			return nil, ErrTenancy.New("addon", man.Name, "reason", "system hooks and core.override are only for add-ons a server admin installs")
		}
		// The catalog's signed index says what the add-on is; the runtime's
		// hooks check it against the catalog when it is installed.
		if spec.Catalog == "" || spec.Item == "" {
			return nil, ErrTenancy.New("addon", man.Name, "reason", "an account's add-ons are installed from a catalog, which names them by catalog and item")
		}
	}
	if missing := Missing(man.Permissions, spec.Approved); missing != "" {
		return nil, ErrNotApproved.New("addon", man.Name, "permission", missing)
	}
	return man, nil
}

func trimReason(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && strings.Contains(msg, "schema") {
		return strings.TrimSpace(msg[i+2:])
	}
	return msg
}

// Missing returns the first permission asked for that approved doesn't
// grant, described for people, or "" when approved covers them all.
func Missing(asked, approved hooks.Permissions) string {
	for _, h := range asked.Hooks {
		if !slices.Contains(approved.Hooks, h) {
			return string(h) + " hooks"
		}
	}
	covered := func(list []hooks.DataScope, want hooks.DataScope) bool {
		for _, s := range list {
			if s.Kind != want.Kind {
				continue
			}
			if len(s.Fields) == 0 {
				return true
			}
			if len(want.Fields) == 0 {
				return false
			}
			ok := true
			for _, f := range want.Fields {
				ok = ok && slices.Contains(s.Fields, f)
			}
			if ok {
				return true
			}
		}
		return false
	}
	for _, r := range asked.Read {
		if !covered(approved.Read, r) {
			return "reading " + r.Kind
		}
	}
	for _, w := range asked.Write {
		if !covered(approved.Write, w) {
			return "changing " + w.Kind
		}
	}
	if asked.Secrets && !approved.Secrets {
		return "reading secrets"
	}
	for _, h := range asked.Network {
		if !slices.Contains(approved.Network, h) {
			return "reaching " + h
		}
	}
	if asked.Override && !approved.Override {
		return "core.override"
	}
	return ""
}
