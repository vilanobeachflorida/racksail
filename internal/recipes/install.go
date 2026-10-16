package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// InstalledResource is a resource an installation created.
type InstalledResource struct {
	// Name is the resource's name in the recipe.
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	ID   store.ID `json:"id"`
}

// Sources of a recipe.
const (
	SourceCatalog = "catalog"
	SourceModule  = "module"
)

// InstallationSpec is an installed recipe and where it came from: the
// recipe as it was installed, the catalog and index that offered it, and
// the resources it created.
type InstallationSpec struct {
	Source string `json:"source" enum:"catalog,module" doc:"Where the recipe came from"`
	// Catalog, CatalogName and Official say which catalog offered it.
	Catalog     store.ID `json:"catalog,omitempty"`
	CatalogName string   `json:"catalogName,omitempty"`
	Official    bool     `json:"official,omitempty" doc:"Whether it came from Racksail's own catalog"`
	// Module is the module that ships the recipe.
	Module       string       `json:"module,omitempty"`
	Item         string       `json:"item" doc:"The item's id"`
	ItemKind     ItemKind     `json:"itemKind" enum:"app,runtime,service,stack,dns-template,automation,tuning-preset,model,theme,addon"`
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Revision     int          `json:"revision,omitempty"`
	License      string       `json:"license"`
	LicenseClass LicenseClass `json:"licenseClass" enum:"open-source,free-to-use,proprietary"`
	// RecipeSHA256 and IndexSHA256 pin exactly what was installed.
	RecipeSHA256 string `json:"recipeSha256"`
	IndexSHA256  string `json:"indexSha256,omitempty"`
	// Recipe is the recipe as installed, so upgrading and uninstalling
	// need nothing from the catalog.
	Recipe string `json:"recipe" maxLength:"262144"`
	// Inputs are the values given, without passwords and secrets.
	Inputs    map[string]any      `json:"inputs,omitempty"`
	Resources []InstalledResource `json:"resources"`
}

// HealthResult is how one health check went.
type HealthResult struct {
	Type      string    `json:"type"`
	Target    string    `json:"target"`
	OK        bool      `json:"ok"`
	Message   string    `json:"message,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// InstallationStatus is what the panel last found of an installation.
type InstallationStatus struct {
	store.Status
	// Applied is the version the resources were last brought to.
	Applied   string             `json:"applied,omitempty"`
	Downloads []VerifiedDownload `json:"downloads,omitempty" doc:"Upstream files checked against the recipe"`
	Health    []HealthResult     `json:"health,omitempty"`
	CheckedAt *time.Time         `json:"checkedAt,omitempty"`
	// Upgrade is a newer version the catalog offers.
	Upgrade string `json:"upgrade,omitempty" doc:"A newer version the catalog offers"`
}

// InstallationHealth is how the health checks went.
type InstallationHealth struct {
	Healthy bool           `json:"healthy"`
	Health  []HealthResult `json:"health"`
}

func installationInfo(name, prefix, plural string, scope store.ScopeLevel, title string) store.KindInfo {
	return store.KindInfo{
		Name: name, Prefix: prefix, Scope: scope, Title: title, Plural: plural,
		Description: "Something installed from a recipe: what it is, where the recipe came from and which resources it created. " +
			"Install, upgrade and uninstall it through the catalog; deleting it only forgets it and leaves what it created.",
		Actions: []store.Action{{
			Name: "check", Title: "Check health", Summary: "Run the recipe's health checks now",
			Description: "Runs the health checks the recipe declares, as the panel does every few minutes, and records how they went.",
			Risk:        store.RiskRead, Output: InstallationHealth{},
		}},
		Indexes: []store.Index{{Name: "item", Path: "item"}, {Name: "catalog", Path: "catalog"}},
		Risks:   store.Risks{Create: store.RiskMedium, Update: store.RiskMedium, Delete: store.RiskLow},
		UI: store.UIHints{
			Icon: "package", TitleField: "name", SubtitleField: "version",
			Columns: []store.Column{{Field: "spec.version"}, {Field: "spec.catalogName", Format: "badge"}, {Field: "status.phase", Format: "status"}, {Field: "status.upgrade"}},
			Fields:  map[string]store.FieldHint{"recipe": {Widget: "code", Advanced: true}},
		},
	}
}

// InstallationKind is core.installation, for what is installed into an
// account, such as an app; ServerInstallationKind is for what belongs to
// the server, such as a runtime, a service or a model.
var (
	InstallationKind       = store.Define[InstallationSpec, InstallationStatus](installationInfo("core.installation", "inst", "installations", store.ScopeAccount, "Installed app"))
	ServerInstallationKind = store.Define[InstallationSpec, InstallationStatus](installationInfo("core.server_installation", "sinst", "server-installations", store.ScopeServer, "Installed item"))
)

func init() {
	store.RegisterKind(CatalogKind)
	store.RegisterKind(InstallationKind)
	store.RegisterKind(ServerInstallationKind)
}

// installationKind returns the kind an installation in scope is.
func installationKind(scope store.Scope) (store.Kind, error) {
	switch scope.Level {
	case store.ScopeAccount:
		return InstallationKind, nil
	case store.ScopeServer:
		return ServerInstallationKind, nil
	}
	return nil, ErrScope.New()
}

// Errors of installs.
var (
	ErrScope          = i18n.Define("recipes.scope", http.StatusUnprocessableEntity, "Install into an account or onto the server.")
	ErrLicenseNotice  = i18n.Define("recipes.license_notice", http.StatusConflict, "{name} isn't open source: it is {class}, under {license}. Accept its license to install it.")
	ErrModuleOff      = i18n.Define("recipes.module_off", http.StatusConflict, "{name} needs the {module} module. Turn it on first.")
	ErrDoesntFit      = i18n.Define("recipes.doesnt_fit", http.StatusConflict, "{name} doesn't fit this server: {reason}.")
	ErrManualUpgrade  = i18n.Define("recipes.manual_upgrade", http.StatusConflict, "{name} is upgraded by hand: {notes}")
	ErrNoUpgrade      = i18n.Define("recipes.no_upgrade", http.StatusConflict, "{name} {version} is the newest version its catalog offers.")
	ErrNoRecipeSource = i18n.Define("recipes.no_source", http.StatusUnprocessableEntity, "Name a catalog or a module to install from.")
	ErrBroken         = i18n.Define("recipes.broken", http.StatusConflict, "{name} is marked broken in its catalog: {reason}.")
)

// EngineOptions configure the recipe engine.
type EngineOptions struct {
	Store    store.Store
	Kinds    *store.KindRegistry
	Catalogs *Catalogs
	// Checker runs health checks between the panel's own checks.
	Checker reconcile.Checker
	// Verifier checks upstream downloads; nil skips them.
	Verifier *Verifier
	// ModuleRecipes returns the recipes a module ships.
	ModuleRecipes func(module string) (fs.FS, bool)
	// ModuleOn says whether a module is on.
	ModuleOn func(id string) bool
	// DataKinds says whether resources of a kind hold data, which
	// uninstalling may keep; DataPaths says where, for snapshots.
	DataKinds func(kind string) bool
	DataPaths func(ctx context.Context, r *store.Resource) ([]string, error)
	Facts     func(ctx context.Context) (system.MachineFacts, error)
	Logger    *slog.Logger
	Now       func() time.Time
}

// Engine installs, upgrades and uninstalls recipes, each as one change set.
type Engine struct {
	opts EngineOptions
}

// NewEngine builds the recipe engine.
func NewEngine(opts EngineOptions) *Engine {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Engine{opts: opts}
}

// Source names where a recipe comes from.
type Source struct {
	Catalog store.ID `json:"catalog,omitempty" doc:"The catalog the item is from"`
	Module  string   `json:"module,omitempty" doc:"Or the module that ships its recipe"`
	Item    string   `json:"item" doc:"The item's id"`
}

// provenance is where a fetched recipe came from.
type provenance struct {
	spec InstallationSpec
	item *ListedItem
}

// Recipe fetches a recipe from its catalog, checked against the signed
// index, or from the module that ships it.
func (e *Engine) Recipe(ctx context.Context, src Source) (*Recipe, *provenance, error) {
	switch {
	case src.Catalog != "":
		if e.opts.Catalogs == nil {
			return nil, nil, ErrNoRecipeSource.New()
		}
		r, data, it, err := e.opts.Catalogs.Recipe(ctx, src.Catalog, src.Item)
		if err != nil {
			return nil, nil, err
		}
		cat, err := CatalogKind.Get(ctx, e.opts.Store, src.Catalog)
		if err != nil {
			return nil, nil, err
		}
		p := &provenance{item: it, spec: InstallationSpec{
			Source: SourceCatalog, Catalog: src.Catalog, CatalogName: it.CatalogName, Official: it.Official,
			RecipeSHA256: hashOf(data), IndexSHA256: cat.Status.IndexSHA256, Recipe: string(data),
		}}
		return r, p, nil
	case src.Module != "":
		if e.opts.ModuleRecipes == nil {
			return nil, nil, ErrNoRecipeSource.New()
		}
		fsys, ok := e.opts.ModuleRecipes(src.Module)
		if !ok {
			return nil, nil, i18n.ErrUnknownName.New("what", "module", "name", src.Module)
		}
		data, err := fs.ReadFile(fsys, src.Item+".yaml")
		if err != nil {
			return nil, nil, ErrNoItem.New("catalog", src.Module, "item", src.Item)
		}
		r, err := Parse(data)
		if err != nil {
			return nil, nil, err
		}
		return r, &provenance{spec: InstallationSpec{Source: SourceModule, Module: src.Module, Official: true, RecipeSHA256: hashOf(data), Recipe: string(data)}}, nil
	}
	return nil, nil, ErrNoRecipeSource.New()
}

// Fit checks a recipe's requirements against the server: the modules it
// needs, memory, architecture and hardware features.
func (e *Engine) Fit(ctx context.Context, r *Recipe) error {
	for _, m := range r.Requires.Modules {
		if e.opts.ModuleOn != nil && !e.opts.ModuleOn(m) {
			return ErrModuleOff.New("name", r.Name, "module", m)
		}
	}
	if e.opts.Facts == nil {
		return nil
	}
	facts, err := e.opts.Facts(ctx)
	if err != nil {
		return err
	}
	switch {
	case r.Requires.MemoryMB > 0 && facts.MemoryBytes > 0 && int64(r.Requires.MemoryMB)<<20 > facts.MemoryBytes:
		return ErrDoesntFit.New("name", r.Name, "reason", fmt.Sprintf("it needs %d MiB of memory and the server has %d", r.Requires.MemoryMB, facts.MemoryBytes>>20))
	case len(r.Requires.Arch) > 0 && facts.Arch != "" && !slices.Contains(r.Requires.Arch, facts.Arch):
		return ErrDoesntFit.New("name", r.Name, "reason", "it runs on "+strings.Join(r.Requires.Arch, " or ")+", not "+facts.Arch)
	}
	for _, f := range r.Requires.Features {
		ok := true
		switch f {
		case "kvm":
			ok = facts.KVM
		case "iommu":
			ok = facts.IOMMU
		case "tpm":
			ok = facts.TPM
		case "virtualization":
			ok = facts.CPU.Virtualization
		default:
			ok = facts.CPU.HasFlag(f)
		}
		if !ok {
			return ErrDoesntFit.New("name", r.Name, "reason", "it needs "+f)
		}
	}
	return nil
}

// InstallInput asks for an item to be installed.
type InstallInput struct {
	Source
	// Scope is the account to install into, or the server.
	Scope  store.Scope    `json:"scope" doc:"The account to install into, or the server"`
	Inputs map[string]any `json:"inputs,omitempty" doc:"Values for the recipe's inputs"`
	// AcceptLicense confirms the notice about software that isn't open
	// source.
	AcceptLicense bool `json:"acceptLicense,omitempty" doc:"Accept the license of software that isn't open source"`
}

// publicInputs drops passwords and secrets from input values.
func publicInputs(r *Recipe, values map[string]any) map[string]any {
	out := map[string]any{}
	for _, in := range r.Inputs {
		if in.Type == "password" || in.Type == "secret" {
			continue
		}
		if v, ok := values[in.Name]; ok {
			out[in.Name] = v
		}
	}
	return out
}

func (e *Engine) admit(r *Recipe, p *provenance, accept bool) error {
	if p.item != nil && p.item.Broken {
		return ErrBroken.New("name", r.Name, "reason", "its install test fails")
	}
	if r.LicenseClass != OpenSource && !accept {
		return ErrLicenseNotice.New("name", r.Name, "class", strings.ReplaceAll(string(r.LicenseClass), "-", " "), "license", r.License)
	}
	return nil
}

// InstallRequest builds the change set that installs an item: its
// resources in dependency order, then the installation that records it
// and whose checks confirm it works.
func (e *Engine) InstallRequest(ctx context.Context, in InstallInput) (changeset.Request, error) {
	kind, err := installationKind(in.Scope)
	if err != nil {
		return changeset.Request{}, err
	}
	r, p, err := e.Recipe(ctx, in.Source)
	if err != nil {
		return changeset.Request{}, err
	}
	if err := e.admit(r, p, in.AcceptLicense); err != nil {
		return changeset.Request{}, err
	}
	if err := e.Fit(ctx, r); err != nil {
		return changeset.Request{}, err
	}
	values, err := r.Values(in.Inputs)
	if err != nil {
		return changeset.Request{}, err
	}
	req, err := r.Request(values, in.Scope, e.opts.Kinds)
	if err != nil {
		return changeset.Request{}, err
	}
	instID := store.NewID(kind.Info().Prefix)
	spec := p.spec
	spec.Item, spec.ItemKind, spec.Name, spec.Version, spec.Revision = r.ID, r.Kind, r.Name, r.Version, r.Revision
	spec.License, spec.LicenseClass, spec.Inputs = r.License, r.LicenseClass, publicInputs(r, values)
	ordered, _ := r.Order()
	for i, ch := range req.Changes {
		ch.Labels["racksail.dev/installation"] = string(instID)
		req.Changes[i] = ch
		spec.Resources = append(spec.Resources, InstalledResource{Name: ordered[i].Name, Kind: ch.Kind, ID: ch.ID})
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return changeset.Request{}, err
	}
	req.Changes = append(req.Changes, changeset.Change{
		Op: changeset.OpCreate, Kind: kind.Info().Name, ID: instID, Scope: in.Scope, Spec: raw,
		Labels: map[string]string{"racksail.dev/recipe": r.ID},
	})
	req.Title = "Install " + r.Name + " " + r.Version
	return req, nil
}

// installation returns an installation of either kind.
func (e *Engine) installation(ctx context.Context, id store.ID) (*store.Object[InstallationSpec, InstallationStatus], store.Kind, error) {
	switch id.Prefix() {
	case InstallationKind.Info().Prefix:
		obj, err := InstallationKind.Get(ctx, e.opts.Store, id)
		return obj, InstallationKind, err
	case ServerInstallationKind.Info().Prefix:
		obj, err := ServerInstallationKind.Get(ctx, e.opts.Store, id)
		return obj, ServerInstallationKind, err
	}
	return nil, nil, i18n.ErrNotFound.New("what", string(id))
}

// UpgradeInput asks for an installation to move to the newest version its
// source offers.
type UpgradeInput struct {
	// Inputs replace values given at install; passwords and secrets the
	// new version asks for anew must be given here.
	Inputs        map[string]any `json:"inputs,omitempty" doc:"Values for inputs the new version adds or that should change"`
	AcceptLicense bool           `json:"acceptLicense,omitempty"`
}

// UpgradeRequest builds the change set that upgrades an installation: the
// installation first, which snapshots the data when the recipe asks, then
// updates of the resources both versions have, new ones, and the removal
// of those the new version dropped that hold no data.
func (e *Engine) UpgradeRequest(ctx context.Context, id store.ID, in UpgradeInput) (changeset.Request, error) {
	inst, kind, err := e.installation(ctx, id)
	if err != nil {
		return changeset.Request{}, err
	}
	src := Source{Catalog: inst.Spec.Catalog, Module: inst.Spec.Module, Item: inst.Spec.Item}
	r, p, err := e.Recipe(ctx, src)
	if err != nil {
		return changeset.Request{}, err
	}
	if p.spec.RecipeSHA256 == inst.Spec.RecipeSHA256 || r.Version == inst.Spec.Version && r.Revision <= inst.Spec.Revision {
		return changeset.Request{}, ErrNoUpgrade.New("name", inst.Spec.Name, "version", inst.Spec.Version)
	}
	if r.Upgrade.Strategy == "manual" {
		return changeset.Request{}, ErrManualUpgrade.New("name", r.Name, "notes", r.Upgrade.Notes)
	}
	if err := e.admit(r, p, in.AcceptLicense || r.LicenseClass == inst.Spec.LicenseClass); err != nil {
		return changeset.Request{}, err
	}
	if err := e.Fit(ctx, r); err != nil {
		return changeset.Request{}, err
	}
	given := map[string]any{}
	for k, v := range inst.Spec.Inputs {
		if slices.ContainsFunc(r.Inputs, func(i Input) bool { return i.Name == k }) {
			given[k] = v
		}
	}
	for k, v := range in.Inputs {
		given[k] = v
	}
	// Passwords given at install aren't kept; resources keep theirs.
	keep := map[string]bool{}
	for _, input := range r.Inputs {
		if (input.Type == "password" || input.Type == "secret") && given[input.Name] == nil {
			keep[input.Name] = true
			if input.Required {
				given[input.Name] = "kept"
			}
		}
	}
	values, err := r.Values(given)
	if err != nil {
		return changeset.Request{}, err
	}
	fresh, err := r.Request(values, inst.Scope, e.opts.Kinds)
	if err != nil {
		return changeset.Request{}, err
	}
	ordered, _ := r.Order()
	existing := map[string]InstalledResource{}
	for _, res := range inst.Spec.Resources {
		existing[res.Name] = res
	}
	ids := map[store.ID]store.ID{}
	var changes []changeset.Change
	var resources []InstalledResource
	for i, ch := range fresh.Changes {
		name := ordered[i].Name
		if old, ok := existing[name]; ok && old.Kind == ch.Kind {
			ids[ch.ID] = old.ID
			ch.ID, ch.Op, ch.Scope = old.ID, changeset.OpUpdate, store.Scope{}
			for path := range ch.Secrets {
				if secretInput(ordered[i], path, keep) != "" {
					delete(ch.Secrets, path)
				}
			}
			delete(existing, name)
		} else {
			// A new resource has no secret to keep, so it needs the value.
			for path := range ch.Secrets {
				if input := secretInput(ordered[i], path, keep); input != "" {
					return changeset.Request{}, ErrMissingInput.New("input", input)
				}
			}
		}
		ch.Labels["racksail.dev/installation"] = string(inst.ID)
		resources = append(resources, InstalledResource{Name: name, Kind: ch.Kind, ID: ch.ID})
		changes = append(changes, ch)
	}
	// References to resources that existed before point at them.
	for i := range changes {
		changes[i].Spec = replaceIDs(changes[i].Spec, ids)
	}
	if len(keep) > 0 {
		if err := e.keepSecrets(ctx, changes, ordered, keep); err != nil {
			return changeset.Request{}, err
		}
	}
	var dropped []changeset.Change
	for _, res := range inst.Spec.Resources {
		if _, gone := existing[res.Name]; !gone {
			continue
		}
		if e.opts.DataKinds != nil && e.opts.DataKinds(res.Kind) {
			dropped = append(dropped, changeset.Change{Op: changeset.OpUpdate, Kind: res.Kind, ID: res.ID, Labels: map[string]string{"racksail.dev/kept-from": inst.Spec.Item}})
			continue
		}
		dropped = append(dropped, changeset.Change{Op: changeset.OpDelete, Kind: res.Kind, ID: res.ID})
	}
	spec := inst.Spec
	spec.Version, spec.Revision, spec.Name, spec.ItemKind = r.Version, r.Revision, r.Name, r.Kind
	spec.License, spec.LicenseClass = r.License, r.LicenseClass
	spec.RecipeSHA256, spec.IndexSHA256, spec.Recipe = p.spec.RecipeSHA256, p.spec.IndexSHA256, p.spec.Recipe
	spec.Inputs = publicInputs(r, values)
	spec.Resources = resources
	raw, err := json.Marshal(spec)
	if err != nil {
		return changeset.Request{}, err
	}
	req := changeset.Request{Title: "Upgrade " + r.Name + " to " + r.Version}
	req.Changes = append(req.Changes, changeset.Change{Op: changeset.OpUpdate, Kind: kind.Info().Name, ID: inst.ID, Spec: raw})
	req.Changes = append(req.Changes, changes...)
	req.Changes = append(req.Changes, dropped...)
	return req, nil
}

// secretInput returns which of the inputs whose value is kept a
// resource's secret field takes, if any.
func secretInput(res Resource, path string, keep map[string]bool) string {
	parts := strings.Split(path, ".")
	var v any = res.Spec
	for _, p := range parts {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[p]
	}
	s, _ := v.(string)
	for _, m := range refPattern.FindAllStringSubmatch(s, -1) {
		if m[1] == "inputs" && keep[m[2]] {
			return m[2]
		}
	}
	return ""
}

// keepSecrets puts the existing secret references back into updated specs
// for the fields whose input wasn't given again.
func (e *Engine) keepSecrets(ctx context.Context, changes []changeset.Change, ordered []Resource, keep map[string]bool) error {
	for i, ch := range changes {
		if ch.Op != changeset.OpUpdate {
			continue
		}
		k, ok := e.opts.Kinds.Get(ch.Kind)
		if !ok {
			continue
		}
		current, err := e.opts.Store.Get(ctx, ch.ID)
		if err != nil {
			return err
		}
		var doc, cur map[string]any
		if err := json.Unmarshal(ch.Spec, &doc); err != nil || doc == nil {
			continue
		}
		json.Unmarshal(current.Spec, &cur)
		for _, path := range store.SensitiveFields(k.Info()) {
			if secretInput(ordered[i], path, keep) == "" {
				continue
			}
			if v, ok := lookup(cur, path); ok {
				set(doc, path, v)
			}
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		changes[i].Spec = raw
	}
	return nil
}

func lookup(doc map[string]any, path string) (any, bool) {
	var v any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[p]; !ok {
			return nil, false
		}
	}
	return v, true
}

func set(doc map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		child, ok := doc[p].(map[string]any)
		if !ok {
			child = map[string]any{}
			doc[p] = child
		}
		doc = child
	}
	doc[parts[len(parts)-1]] = v
}

// replaceIDs points references to new resources at the ones they stand
// for.
func replaceIDs(spec []byte, ids map[store.ID]store.ID) []byte {
	s := string(spec)
	for from, to := range ids {
		s = strings.ReplaceAll(s, string(from), string(to))
	}
	return []byte(s)
}

// UninstallInput says what uninstalling keeps.
type UninstallInput struct {
	// KeepData keeps the resources that hold data, such as databases,
	// detached from the installation; the recipe's default when nil.
	KeepData *bool `json:"keepData,omitempty" doc:"Keep what holds data, such as databases and site files; the recipe decides when left out"`
}

// UninstallRequest builds the change set that uninstalls an installation:
// its resources in reverse order, except those that hold data when they
// are kept, and the installation itself.
func (e *Engine) UninstallRequest(ctx context.Context, id store.ID, in UninstallInput) (changeset.Request, error) {
	inst, kind, err := e.installation(ctx, id)
	if err != nil {
		return changeset.Request{}, err
	}
	keepData := true
	if r, err := Parse([]byte(inst.Spec.Recipe)); err == nil {
		keepData = r.Uninstall.Keeps()
	}
	if in.KeepData != nil {
		keepData = *in.KeepData
	}
	req := changeset.Request{Title: "Uninstall " + inst.Spec.Name}
	for i := len(inst.Spec.Resources) - 1; i >= 0; i-- {
		res := inst.Spec.Resources[i]
		if _, err := e.opts.Store.Get(ctx, res.ID); store.ErrNotFound.Is(err) {
			continue
		}
		if keepData && e.opts.DataKinds != nil && e.opts.DataKinds(res.Kind) {
			req.Changes = append(req.Changes, changeset.Change{Op: changeset.OpUpdate, Kind: res.Kind, ID: res.ID, Labels: map[string]string{"racksail.dev/kept-from": inst.Spec.Item}})
			continue
		}
		req.Changes = append(req.Changes, changeset.Change{Op: changeset.OpDelete, Kind: res.Kind, ID: res.ID})
	}
	req.Changes = append(req.Changes, changeset.Change{Op: changeset.OpDelete, Kind: kind.Info().Name, ID: inst.ID})
	return req, nil
}

// checks are an installation's health checks with its inputs filled in.
func checks(inst *store.Object[InstallationSpec, InstallationStatus]) ([]HealthCheck, error) {
	r, err := Parse([]byte(inst.Spec.Recipe))
	if err != nil {
		return nil, err
	}
	ids := map[string]store.ID{}
	for _, res := range inst.Spec.Resources {
		ids[res.Name] = res.ID
	}
	var out []HealthCheck
	for _, hc := range r.Health {
		target, err := substitute(hc.Target, inst.Spec.Inputs, ids)
		if err != nil {
			return nil, err
		}
		hc.Target = fmt.Sprint(target)
		out = append(out, hc)
	}
	return out, nil
}

func checkTimeout(hc HealthCheck) time.Duration {
	if hc.TimeoutSeconds > 0 {
		return time.Duration(hc.TimeoutSeconds) * time.Second
	}
	return reconcile.DefaultCheckTimeout
}

// resourceReady checks a resource check: a resource of the installation,
// or all of them when the target is the recipe's id.
func resourceReady(ctx context.Context, rd store.Reader, inst *store.Object[InstallationSpec, InstallationStatus], target string) error {
	for _, res := range inst.Spec.Resources {
		if target != inst.Spec.Item && res.Name != target {
			continue
		}
		r, err := rd.Get(ctx, res.ID)
		if err != nil {
			return fmt.Errorf("%s: %s", res.Name, reasonOf(err))
		}
		var st store.Status
		json.Unmarshal(r.Status, &st)
		if st.Phase != store.PhaseReady {
			msg := fmt.Sprintf("%s is %s", res.Name, st.Phase)
			if st.Message != "" {
				msg += ": " + st.Message
			}
			return errors.New(msg)
		}
	}
	return nil
}

// Check runs an installation's health checks once, records how they went
// and returns that.
func (e *Engine) Check(ctx context.Context, id store.ID) (InstallationHealth, error) {
	inst, kind, err := e.installation(ctx, id)
	if err != nil {
		return InstallationHealth{}, err
	}
	list, err := checks(inst)
	if err != nil {
		return InstallationHealth{}, err
	}
	out := InstallationHealth{Healthy: true}
	now := e.opts.Now().UTC()
	for i, hc := range list {
		res := HealthResult{Type: hc.Type, Target: hc.Target, OK: true, CheckedAt: now}
		var err error
		if hc.Type == HealthResource {
			err = resourceReady(ctx, e.opts.Store, inst, hc.Target)
		} else if e.opts.Checker != nil {
			cctx, cancel := context.WithTimeout(ctx, min(checkTimeout(hc), 30*time.Second))
			err = e.opts.Checker.Check(cctx, inst.Server, reconcile.HealthCheck{Name: fmt.Sprintf("%s check %d", inst.Spec.Name, i+1), Type: hc.Type, Target: hc.Target, Expect: hc.Expect, Timeout: min(checkTimeout(hc), 30*time.Second)})
			cancel()
		}
		if err != nil {
			res.OK, res.Message = false, reasonOf(err)
			out.Healthy = false
		}
		out.Health = append(out.Health, res)
	}
	e.record(ctx, kind, inst.ID, func(st *InstallationStatus) {
		st.Health, st.CheckedAt = out.Health, &now
		if st.ObservedGeneration < inst.Generation {
			return
		}
		if out.Healthy {
			st.Phase, st.Message = store.PhaseReady, ""
		} else {
			st.Phase, st.Message = store.PhaseDegraded, "A health check doesn't pass."
		}
	})
	return out, nil
}

func (e *Engine) record(ctx context.Context, kind store.Kind, id store.ID, change func(st *InstallationStatus)) {
	err := e.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:recipes"}, func(tx store.Tx) error {
		r, err := tx.Get(ctx, id)
		if err != nil {
			return err
		}
		var st InstallationStatus
		if len(r.Status) > 0 {
			if err := json.Unmarshal(r.Status, &st); err != nil {
				return err
			}
		}
		change(&st)
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, id, raw)
	})
	if err != nil && !store.ErrNotFound.Is(err) {
		e.opts.Logger.Warn("recording an installation's health", "installation", id, "error", err)
	}
}

// checkLater checks an upgraded installation once its resources are
// applied, or after a few minutes whatever they are.
func (e *Engine) checkLater(ctx context.Context, id store.ID) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		inst, _, err := e.installation(ctx, id)
		if err != nil {
			return
		}
		settled := true
		for _, res := range inst.Spec.Resources {
			r, err := e.opts.Store.Get(ctx, res.ID)
			if err != nil {
				settled = false
				break
			}
			var st store.Status
			json.Unmarshal(r.Status, &st)
			if st.ObservedGeneration < r.Generation {
				settled = false
				break
			}
		}
		if settled {
			break
		}
		time.Sleep(time.Second)
	}
	if _, err := e.Check(ctx, id); err != nil {
		e.opts.Logger.Warn("checking an upgraded installation", "installation", id, "error", err)
	}
}

// labelChars are what snapshot labels can't hold.
var labelChars = regexp.MustCompile(`[^a-z0-9]+`)

// checkEvery is how often installations are checked.
const checkEvery = 5 * time.Minute

// Run checks every installation's health every few minutes, and looks for
// newer versions in their catalogs, until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(checkEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for _, k := range []store.Kind{InstallationKind, ServerInstallationKind} {
			cursor := ""
			for {
				page, err := e.opts.Store.List(ctx, store.Query{Kind: k.Info().Name, Cursor: cursor, Limit: store.MaxLimit})
				if err != nil {
					e.opts.Logger.Warn("listing installations", "error", err)
					break
				}
				for _, r := range page.Items {
					if r.Deleted() {
						continue
					}
					if _, err := e.Check(ctx, r.ID); err != nil && ctx.Err() == nil {
						e.opts.Logger.Warn("checking an installation", "installation", r.ID, "error", err)
					}
					e.findUpgrade(ctx, k, r.ID)
				}
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
		}
	}
}

// findUpgrade records a newer version the installation's catalog offers.
func (e *Engine) findUpgrade(ctx context.Context, kind store.Kind, id store.ID) {
	inst, _, err := e.installation(ctx, id)
	if err != nil || inst.Spec.Catalog == "" || e.opts.Catalogs == nil {
		return
	}
	it, err := e.opts.Catalogs.Item(ctx, inst.Spec.Catalog, inst.Spec.Item)
	if err != nil {
		return
	}
	upgrade := ""
	if it.Version != inst.Spec.Version {
		upgrade = it.Version
	}
	if inst.Status.Upgrade != upgrade {
		e.record(ctx, kind, id, func(st *InstallationStatus) { st.Upgrade = upgrade })
	}
}

// Reconcilers returns the reconcilers of the installation kinds.
func (e *Engine) Reconcilers() []reconcile.Reconciler {
	return []reconcile.Reconciler{
		reconcile.Typed(InstallationKind, installationReconciler{e: e}),
		reconcile.Typed(ServerInstallationKind, installationReconciler{e: e}),
	}
}

type installationReconciler struct {
	e *Engine
}

// Plan snapshots the data of an installation that is upgraded with the
// snapshot-then-replace strategy, before its resources change, and has a
// new install's health checks confirm it, once its resources are applied.
func (r installationReconciler) Plan(ctx context.Context, obj *store.Object[InstallationSpec, InstallationStatus], req reconcile.Request) (reconcile.Plan, error) {
	if obj.Deleted() {
		return reconcile.Plan{}, nil
	}
	recipe, err := Parse([]byte(obj.Spec.Recipe))
	if err != nil {
		return reconcile.Plan{}, err
	}
	var plan reconcile.Plan
	upgrading := obj.Status.Applied != "" && obj.Status.Applied != obj.Spec.Version
	if upgrading && recipe.Upgrade.Strategy == "snapshot-then-replace" && r.e.opts.DataPaths != nil {
		var paths []string
		for _, res := range obj.Spec.Resources {
			if r.e.opts.DataKinds == nil || !r.e.opts.DataKinds(res.Kind) {
				continue
			}
			rr, err := req.Read.Get(ctx, res.ID)
			if err != nil {
				continue
			}
			p, err := r.e.opts.DataPaths(ctx, rr)
			if err != nil {
				return reconcile.Plan{}, err
			}
			paths = append(paths, p...)
		}
		if len(paths) > 0 {
			label := strings.Trim(strings.ToLower(labelChars.ReplaceAllString("recipe_"+obj.Spec.Item+"_"+obj.Status.Applied, "_")), "_")
			if len(label) > 64 {
				label = label[:64]
			}
			plan.Steps = append(plan.Steps, agent.SnapshotPaths.Titled("Snapshot "+obj.Spec.Name+"'s data before the upgrade", agent.SnapshotPathsInput{Paths: paths, Label: label}))
		}
	}
	if !upgrading && obj.Status.Applied == "" {
		hcs, err := checks(obj)
		if err != nil {
			return reconcile.Plan{}, err
		}
		for i, hc := range hcs {
			if hc.Type == HealthResource {
				continue
			}
			plan.Checks = append(plan.Checks, reconcile.HealthCheck{
				Name: fmt.Sprintf("%s check %d", obj.Spec.Name, i+1), Type: hc.Type, Target: hc.Target, Expect: hc.Expect, Timeout: checkTimeout(hc),
			})
		}
	}
	return plan, nil
}

// Apply checks the upstream files the recipe pins, waits for the resources
// a resource check names and records the version applied.
func (r installationReconciler) Apply(ctx context.Context, obj *store.Object[InstallationSpec, InstallationStatus], plan reconcile.Plan, ex reconcile.Executor) (InstallationStatus, error) {
	st := obj.Status
	if obj.Deleted() {
		return st, nil
	}
	recipe, err := Parse([]byte(obj.Spec.Recipe))
	if err != nil {
		return st, err
	}
	if st.Applied != obj.Spec.Version && r.e.opts.Verifier != nil {
		var verified []VerifiedDownload
		for _, d := range recipe.Downloads {
			v, err := r.e.opts.Verifier.Verify(ctx, d)
			if err != nil {
				return st, fmt.Errorf("checking %s's upstream files: %w", obj.Spec.Name, err)
			}
			verified = append(verified, v)
		}
		st.Downloads = verified
	}
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, r.e.opts.Now()); err != nil {
		return st, err
	}
	if st.Applied != "" {
		// An upgrade is applied before the resources it changes, so its
		// health is checked once they had their turn.
		go r.e.checkLater(context.WithoutCancel(ctx), obj.ID)
		st.Applied = obj.Spec.Version
		return st, nil
	}
	for _, hc := range recipe.Health {
		if hc.Type != HealthResource {
			continue
		}
		deadline := time.Now().Add(checkTimeout(hc))
		for {
			err := resourceReady(ctx, r.e.opts.Store, obj, hc.Target)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return st, reconcile.ErrCheckFailed.Wrap(err, "check", hc.Target, "reason", err.Error())
			}
			select {
			case <-ctx.Done():
				return st, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	st.Applied = obj.Spec.Version
	if st.Upgrade == obj.Spec.Version {
		st.Upgrade = ""
	}
	return st, nil
}

func (installationReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (r installationReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if req.Action != "check" {
		return nil, fmt.Errorf("recipes: installations have no action called %q", req.Action)
	}
	return r.e.Check(ctx, req.Resource.ID)
}
