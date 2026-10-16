package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// An installation's spec says where its recipe came from, and the panel
// works from that recipe: it checks the downloads the recipe pins and runs
// its health checks. So an installation is only ever what the engine
// builds: a recipe the catalog's signed index or the module offers, with
// the resources it created where it is installed. However a change reaches
// the pipeline, through the API, an automation, an add-on or the
// assistant, a spec that says otherwise is refused.

// Errors of installations written other than by the engine.
var (
	ErrNotFromSource = i18n.Define("recipes.not_from_source", http.StatusForbidden,
		"{name} doesn't match what {source} offers: {reason}. Install, upgrade and uninstall it from its catalog or module.")
	ErrForeignResource = i18n.Define("recipes.foreign_resource", http.StatusForbidden,
		"{name} lists {resource}, which isn't one of its own resources. An installation only lists what it created where it is installed.")
)

// validateInstallation checks an installation against the recipe it
// carries: that the recipe is the one its checksum names, that the item,
// version and license are the recipe's, that the inputs are ones the recipe
// asks for, without passwords or secrets, and that the resources are ones
// it creates. Where the recipe came from can't change.
func validateInstallation(ctx context.Context, obj, old *store.Object[InstallationSpec, InstallationStatus]) error {
	s := obj.Spec
	var fields []i18n.FieldError
	bad := func(path, reason string) {
		fields = append(fields, i18n.Field(path, i18n.ErrBadValue, "reason", reason))
	}
	switch s.Source {
	case SourceCatalog:
		if s.Catalog.Prefix() != CatalogKind.Info().Prefix || !s.Catalog.Valid() {
			bad("spec.catalog", "name the catalog the recipe came from")
		}
		if s.Module != "" {
			bad("spec.module", "a recipe from a catalog comes from no module")
		}
	case SourceModule:
		if s.Module == "" {
			bad("spec.module", "name the module that ships the recipe")
		}
		if s.Catalog != "" || s.CatalogName != "" || s.IndexSHA256 != "" {
			bad("spec.catalog", "a module's recipe comes from no catalog")
		}
	}
	if old != nil {
		for _, f := range []struct {
			path    string
			changed bool
		}{
			{"spec.source", old.Spec.Source != s.Source}, {"spec.catalog", old.Spec.Catalog != s.Catalog},
			{"spec.module", old.Spec.Module != s.Module}, {"spec.item", old.Spec.Item != s.Item},
		} {
			if f.changed {
				fields = append(fields, i18n.Field(f.path, i18n.ErrImmutable))
			}
		}
	}
	if hashOf([]byte(s.Recipe)) != s.RecipeSHA256 {
		bad("spec.recipeSha256", "it isn't the SHA-256 of the recipe")
	}
	r, err := Parse([]byte(s.Recipe))
	if err != nil {
		bad("spec.recipe", reasonOf(err))
		return invalid(fields)
	}
	for _, f := range []struct {
		path       string
		have, want any
	}{
		{"spec.item", s.Item, r.ID}, {"spec.itemKind", s.ItemKind, r.Kind}, {"spec.name", s.Name, r.Name},
		{"spec.version", s.Version, r.Version}, {"spec.revision", s.Revision, r.Revision},
		{"spec.license", s.License, r.License}, {"spec.licenseClass", s.LicenseClass, r.LicenseClass},
	} {
		if f.have != f.want {
			bad(f.path, fmt.Sprintf("the recipe says %v", f.want))
		}
	}
	for name, v := range s.Inputs {
		path := "spec.inputs." + name
		i := slices.IndexFunc(r.Inputs, func(in Input) bool { return in.Name == name })
		switch {
		case i < 0:
			bad(path, "the recipe doesn't ask for it")
		case r.Inputs[i].Type == "password" || r.Inputs[i].Type == "secret":
			bad(path, "passwords and secrets aren't kept with the installation")
		default:
			if err := checkValue(r.Inputs[i], v); err != nil {
				bad(path, err.Error())
			}
		}
	}
	seen := map[string]bool{}
	for i, res := range s.Resources {
		path := fmt.Sprintf("spec.resources[%d]", i)
		j := slices.IndexFunc(r.Resources, func(d Resource) bool { return d.Name == res.Name })
		switch {
		case j < 0:
			bad(path+".name", "the recipe creates no "+res.Name)
		case seen[res.Name]:
			bad(path+".name", res.Name+" is listed twice")
		case r.Resources[j].Kind != res.Kind:
			bad(path+".kind", "the recipe's "+res.Name+" is a "+r.Resources[j].Kind)
		}
		seen[res.Name] = true
		if !res.ID.Valid() {
			bad(path+".id", "it isn't a resource ID")
		}
	}
	return invalid(fields)
}

func invalid(fields []i18n.FieldError) error {
	if len(fields) == 0 {
		return nil
	}
	return i18n.Invalid(fields...)
}

// Hooks are the engine's own hooks: an installation created or changed by
// any change, not only by the engine's requests, must be what its catalog's
// signed index or its module offers, and list only its own resources.
func (e *Engine) Hooks() []hooks.Registration {
	var out []hooks.Registration
	for _, k := range []store.Kind{InstallationKind, ServerInstallationKind} {
		for _, op := range []string{"create", "update"} {
			out = append(out, hooks.Registration{
				Point: hooks.KindPoint(k.Info().Name, op, changeset.StageValidate), Kind: hooks.Validate,
				Owner: hooks.OwnerCore + ":recipes", Security: true, Priority: -1000, Deadline: 5 * time.Second,
				OnValidate: e.checkProvenance,
			})
		}
	}
	return out
}

func decodeInstallation(raw json.RawMessage) (InstallationSpec, error) {
	var s InstallationSpec
	err := json.Unmarshal(raw, &s)
	return s, err
}

// canonical is an installation's spec as JSON with its fields in one
// order, to tell whether two specs say the same.
func canonical(s InstallationSpec) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// checkProvenance refuses an installation whose recipe isn't what its
// source offers, or that lists another scope's resources. Writing a spec
// the installation had before, as an undo does, is allowed: it was checked
// when it was first written, against what its source offered then.
func (e *Engine) checkProvenance(ctx context.Context, c *hooks.Context) error {
	ch := c.Change
	if ch == nil || (ch.Op != changeset.OpCreate && ch.Op != changeset.OpUpdate) {
		return nil
	}
	spec, err := decodeInstallation(ch.After)
	if err != nil {
		return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", "the spec isn't an installation's"))
	}
	restored, err := e.restores(ctx, ch, spec)
	if err != nil || restored {
		return err
	}
	if err := e.fromSource(ctx, spec); err != nil {
		return err
	}
	return e.ownResources(ctx, ch, spec)
}

// restores reports whether a change writes a spec the installation already
// has, or had before.
func (e *Engine) restores(ctx context.Context, ch *changeset.ResourceChange, spec InstallationSpec) (bool, error) {
	want := canonical(spec)
	if ch.Op == changeset.OpUpdate {
		if before, err := decodeInstallation(ch.Before); err == nil && canonical(before) == want {
			return true, nil
		}
	}
	versions, err := e.opts.Store.History(ctx, ch.ID)
	if store.ErrNotFound.Is(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, v := range versions {
		if v.Kind != ch.Kind || v.Scope != ch.Scope {
			continue
		}
		if old, err := decodeInstallation(v.Spec); err == nil && canonical(old) == want {
			return true, nil
		}
	}
	return false, nil
}

// fromSource checks an installation's recipe against where it says it came
// from: the item its catalog's signed index lists, from the index the panel
// checked last, or the recipe its module ships.
func (e *Engine) fromSource(ctx context.Context, s InstallationSpec) error {
	refuse := func(source, reason string) error {
		return ErrNotFromSource.New("name", s.Name, "source", source, "reason", reason)
	}
	switch s.Source {
	case SourceCatalog:
		cat, err := CatalogKind.Get(ctx, e.opts.Store, s.Catalog)
		if store.ErrNotFound.Is(err) {
			return refuse("its catalog", "the catalog it names isn't one this server has")
		}
		if err != nil {
			return err
		}
		where := "the " + cat.Spec.Title + " catalog"
		if e.opts.Catalogs == nil {
			return refuse(where, "catalogs aren't read here")
		}
		idx, err := e.opts.Catalogs.Index(ctx, cat.ID)
		if err != nil {
			return refuse(where, reasonOf(err))
		}
		i := slices.IndexFunc(idx.Items, func(it IndexItem) bool { return it.ID == s.Item })
		switch {
		case i < 0:
			return refuse(where, "its index has no item "+s.Item)
		case idx.Items[i].SHA256 != s.RecipeSHA256:
			return refuse(where, "its signed index lists another recipe for "+s.Item)
		case s.IndexSHA256 != cat.Status.IndexSHA256:
			return refuse(where, "it names another index than the one the panel checked")
		case s.CatalogName != cat.Spec.Name || s.Official != e.opts.Catalogs.official(cat.Spec):
			return refuse(where, "it says it comes from another catalog")
		}
	case SourceModule:
		where := "the " + s.Module + " module"
		if e.opts.ModuleRecipes == nil {
			return refuse(where, "modules' recipes aren't read here")
		}
		fsys, ok := e.opts.ModuleRecipes(s.Module)
		if !ok {
			return refuse(where, "there is no such module")
		}
		data, err := fs.ReadFile(fsys, s.Item+".yaml")
		switch {
		case err != nil:
			return refuse(where, "it ships no recipe "+s.Item)
		case hashOf(data) != s.RecipeSHA256:
			return refuse(where, "it ships another recipe for "+s.Item)
		case !s.Official:
			return refuse(where, "what a module ships is Racksail's own")
		}
	default:
		return refuse("its source", "it names no catalog or module")
	}
	return nil
}

// ownResources checks the resources an installation lists: each has an ID
// of its kind, and one that exists already is of that kind and where the
// installation is. Those that don't exist yet are the ones the same change
// set creates after it.
func (e *Engine) ownResources(ctx context.Context, ch *changeset.ResourceChange, s InstallationSpec) error {
	for _, res := range s.Resources {
		foreign := ErrForeignResource.New("name", s.Name, "resource", res.Name)
		k, ok := e.opts.Kinds.Get(res.Kind)
		if !ok || res.ID.Prefix() != k.Info().Prefix {
			return foreign
		}
		r, err := e.opts.Store.Get(ctx, res.ID)
		if store.ErrNotFound.Is(err) {
			continue
		}
		if err != nil {
			return err
		}
		if r.Kind != res.Kind || r.Scope != ch.Scope {
			return foreign
		}
	}
	return nil
}
