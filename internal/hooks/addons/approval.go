package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Who may approve what an add-on asks for, and what an add-on from a
// catalog may be, are checked as the add-on is installed or changed,
// however the change reaches the pipeline.

// CatalogItems reads the add-ons catalogs offer; the recipe engine's
// catalog client is one.
type CatalogItems interface {
	// AddonItem returns an add-on item of a catalog named by its ID or
	// name, with its recipe and manifest checked against the catalog's
	// signed index.
	AddonItem(ctx context.Context, catalog, item string) (*recipes.AddonItem, error)
}

// adminOnly lists the approved permissions only a server admin may
// approve: core.override, which loosens invariants, secrets.read, which
// reads every secret on the server, and system hooks, which patch the
// configuration of its services.
func adminOnly(p hooks.Permissions) []string {
	var out []string
	if p.Override {
		out = append(out, string(auth.PermOverride))
	}
	if p.Secrets {
		out = append(out, string(auth.PermSecretsRead))
	}
	if slices.Contains(p.Hooks, hooks.System) {
		out = append(out, "system hooks")
	}
	return out
}

// serverAdmin reports whether an actor holds the admin role on the whole
// server.
func serverAdmin(a *auth.Actor) bool {
	if a == nil {
		return false
	}
	for _, g := range a.Grants {
		if g.Role == auth.RoleAdmin && g.Scope.Level == store.ScopeServer && g.Object == "" {
			return true
		}
	}
	return false
}

// sameProgram reports whether two specs run the same add-on from the same
// place.
func sameProgram(a, b AddonSpec) bool {
	return a.ID == b.ID && a.Manifest == b.Manifest && a.Package == b.Package && a.Catalog == b.Catalog && a.Item == b.Item
}

func addonName(spec AddonSpec) string {
	if man, err := hooks.ParseAddonManifest([]byte(spec.Manifest)); err == nil {
		return man.Name
	}
	return spec.ID
}

// specs returns the spec a change writes and the one it replaces, which is
// empty for a create. A spec before that can't be read counts as empty, so
// nothing passes for having been approved already.
func specs(ch *changeset.ResourceChange) (after, before AddonSpec, ok bool) {
	if json.Unmarshal(ch.After, &after) != nil {
		return after, before, false
	}
	if len(bytes.TrimSpace(ch.Before)) > 0 && json.Unmarshal(ch.Before, &before) != nil {
		before = AddonSpec{}
	}
	return after, before, true
}

// checkApproval refuses a change that gives a server's add-on, or a new
// program of one, a permission only a server admin may approve, unless a
// server admin makes it. Giving up such a permission, or changing what
// doesn't widen it, such as turning the add-on off, needs no admin.
func (r *Runtime) checkApproval(ctx context.Context, c *hooks.Context) error {
	ch := c.Change
	if ch == nil || (ch.Op != changeset.OpCreate && ch.Op != changeset.OpUpdate) {
		return nil
	}
	after, before, ok := specs(ch)
	if !ok {
		// The kind's own validation says what is wrong with the spec.
		return nil
	}
	need := adminOnly(after.Approved)
	if len(need) == 0 || serverAdmin(c.Actor) {
		return nil
	}
	had := adminOnly(before.Approved)
	for _, p := range need {
		if ch.Op == changeset.OpCreate || !slices.Contains(had, p) || !sameProgram(before, after) {
			return ErrAdminApproval.New("permission", p, "addon", addonName(after))
		}
	}
	return nil
}

// checkCatalog refuses an add-on that names a catalog, as every account's
// add-on must, unless its manifest is the one the catalog's signed index
// lists for the item and its program is one the item's recipe pins.
// Changes that keep the program, such as turning it off, and an undo that
// puts back a program it ran before, were checked when it was first
// installed.
func (r *Runtime) checkCatalog(ctx context.Context, c *hooks.Context) error {
	ch := c.Change
	if ch == nil || (ch.Op != changeset.OpCreate && ch.Op != changeset.OpUpdate) {
		return nil
	}
	after, before, ok := specs(ch)
	if !ok {
		return nil
	}
	if after.Catalog == "" && ch.Kind != AccountAddon.Name() {
		// A server admin's own add-on, from no catalog.
		return nil
	}
	if ch.Op == changeset.OpUpdate && sameProgram(before, after) {
		return nil
	}
	if ran, err := r.ranBefore(ctx, ch, after); err != nil || ran {
		return err
	}
	name := addonName(after)
	refuse := func(reason string) error {
		return ErrNotInCatalog.New("addon", name, "catalog", after.Catalog, "reason", reason)
	}
	if r.opts.Catalogs == nil {
		return refuse("catalogs aren't read here")
	}
	it, err := r.opts.Catalogs.AddonItem(ctx, after.Catalog, after.Item)
	if err != nil {
		return refuse(strings.TrimSuffix(i18n.From(err).Message, "."))
	}
	if string(it.Manifest) != after.Manifest {
		return refuse("its manifest isn't the one the catalog signed")
	}
	pkg := after.Package
	if !slices.ContainsFunc(it.Recipe.Downloads, func(d recipes.Download) bool {
		return d.URL == pkg.URL && d.SHA256 == pkg.SHA256 && d.Size == pkg.Size
	}) {
		return refuse("its program isn't one the catalog's recipe pins")
	}
	return nil
}

// ranBefore reports whether an add-on's resource had the same program in
// an earlier version, as when an undo puts one back.
func (r *Runtime) ranBefore(ctx context.Context, ch *changeset.ResourceChange, spec AddonSpec) (bool, error) {
	if r.opts.Store == nil {
		return false, nil
	}
	versions, err := r.opts.Store.History(ctx, ch.ID)
	if store.ErrNotFound.Is(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, v := range versions {
		var old AddonSpec
		if v.Kind == ch.Kind && v.Scope == ch.Scope && json.Unmarshal(v.Spec, &old) == nil && sameProgram(old, spec) {
			return true, nil
		}
	}
	return false, nil
}
