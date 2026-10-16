package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

const hello = `
format: 1
kind: app
id: hello
name: Hello
version: "1.0.0"
summary: A site that says hello.
license: MIT
licenseClass: open-source
maintainers: [{name: Racksail maintainers}]
requires: {modules: [web]}
inputs:
  - {name: domain, type: domain, label: Domain, required: true}
resources:
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}"], php: "8.4", workers: 1, database: ""}
health:
  - {type: resource, target: site}
`

// write previews one change to an installation made by hand.
func (p *panel) write(t *testing.T, kind store.Kind, op changeset.Op, id store.ID, scope store.Scope, s InstallationSpec) error {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	ch := changeset.Change{Op: op, Kind: kind.Info().Name, ID: id, Spec: raw}
	if op == changeset.OpCreate {
		ch.Scope = scope
	}
	_, err = p.engine.Submit(context.Background(), changeset.Request{Actor: p.admin, DryRun: true, Changes: []changeset.Change{ch}})
	return err
}

// An installation is only what the engine builds: a recipe its catalog's
// signed index or its module offers, listing its own resources. One written
// by hand that says anything else is refused, through whatever change it
// comes, while putting back what an installation was, as an undo does,
// isn't.
func TestInstallationsAreWhatTheirSourceOffers(t *testing.T) {
	ctx := context.Background()
	srv := healthServer(t)
	c := newCatalogServer(t)
	c.add(checkedRecipe(srv), nil)
	shipped := fstest.MapFS{"hello.yaml": {Data: []byte(hello)}}
	p := newPanel(t, func(o *EngineOptions) {
		o.ModuleRecipes = func(id string) (fs.FS, bool) { return shipped, id == "web" }
	})
	cat := p.addCatalog(t, c, c.srv.URL)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	mine, theirs := store.AccountScope(store.NewID("acct")), store.AccountScope(store.NewID("acct"))
	req, err := p.recipes.InstallRequest(ctx, InstallInput{
		Source: Source{Catalog: cat, Item: "wordpress"}, Scope: mine, Inputs: map[string]any{"domain": "blog.example.com", "dbPassword": "correct horse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs := p.submit(t, req); cs.State != changeset.StateApplied {
		t.Fatalf("what the engine builds installs: %s %s", cs.State, cs.Error)
	}
	inst, err := InstallationKind.Get(ctx, p.store, req.Changes[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	// A recipe of one's own, with the checksum to match, isn't the one the
	// catalog signed.
	forged := inst.Spec
	forged.Recipe = strings.Replace(inst.Spec.Recipe, srv.URL, "https://attacker.example", 1)
	forged.RecipeSHA256 = hashOf([]byte(forged.Recipe))
	forged.Resources = nil
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", theirs, forged); !errors.Is(err, ErrNotFromSource) || !strings.Contains(i18n.From(err).Message, "lists another recipe") {
		t.Errorf("a recipe the catalog didn't sign: %v", err)
	}
	// Nor is a recipe whose checksum is some other recipe's.
	lie := forged
	lie.RecipeSHA256 = inst.Spec.RecipeSHA256
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", theirs, lie); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("a checksum that isn't the recipe's: %v", err)
	}
	// Nor is saying it comes from somewhere it doesn't: Racksail's own
	// catalog, another index, another catalog or a module.
	for name, change := range map[string]func(*InstallationSpec){
		"official":      func(s *InstallationSpec) { s.Official = true },
		"another index": func(s *InstallationSpec) { s.IndexSHA256 = strings.Repeat("0", 64) },
		"another name":  func(s *InstallationSpec) { s.CatalogName = "official" },
		"no catalog":    func(s *InstallationSpec) { s.Catalog = store.NewID("cat") },
		"a module": func(s *InstallationSpec) {
			s.Source, s.Catalog, s.CatalogName, s.IndexSHA256, s.Module = SourceModule, "", "", "", "web"
		},
	} {
		s := inst.Spec
		s.Resources = nil
		change(&s)
		if err := p.write(t, InstallationKind, changeset.OpCreate, "", theirs, s); !errors.Is(err, ErrNotFromSource) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Nor anything the recipe it carries doesn't say.
	for name, change := range map[string]func(*InstallationSpec){
		"another version": func(s *InstallationSpec) { s.Version = "9.9.9" },
		"another license": func(s *InstallationSpec) { s.LicenseClass = Proprietary },
		"a password":      func(s *InstallationSpec) { s.Inputs = map[string]any{"dbPassword": "kept"} },
		"another input":   func(s *InstallationSpec) { s.Inputs = map[string]any{"shell": "rm -rf /"} },
		"a bad input":     func(s *InstallationSpec) { s.Inputs = map[string]any{"domain": "not a domain"} },
		"another kind": func(s *InstallationSpec) {
			s.Resources = []InstalledResource{{Name: "site", Kind: "databases.database", ID: store.NewID("db")}}
		},
		"an unknown one": func(s *InstallationSpec) {
			s.Resources = []InstalledResource{{Name: "cache", Kind: "web.site", ID: store.NewID("site")}}
		},
	} {
		s := inst.Spec
		s.Resources = nil
		change(&s)
		if err := p.write(t, InstallationKind, changeset.OpCreate, "", theirs, s); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The catalog's own recipe, in another account, can't claim this one's
	// database and site, whose status its health checks would read.
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", theirs, inst.Spec); !errors.Is(err, ErrForeignResource) {
		t.Errorf("another account's resources: %v", err)
	}
	stolen := inst.Spec
	stolen.Resources = []InstalledResource{{Name: "site", Kind: "web.site", ID: inst.Spec.Resources[0].ID}}
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", mine, stolen); !errors.Is(err, ErrForeignResource) {
		t.Errorf("a database listed as a site: %v", err)
	}
	// Changing the installation by hand is held to the same.
	changed := inst.Spec
	changed.Recipe, changed.RecipeSHA256 = forged.Recipe, forged.RecipeSHA256
	if err := p.write(t, InstallationKind, changeset.OpUpdate, inst.ID, store.Scope{}, changed); !errors.Is(err, ErrNotFromSource) {
		t.Errorf("an update to a recipe of one's own: %v", err)
	}
	moved := inst.Spec
	moved.Item = "ghost"
	if err := p.write(t, InstallationKind, changeset.OpUpdate, inst.ID, store.Scope{}, moved); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("moving it to another item: %v", err)
	}
	// Writing what it already is changes nothing about where it came from.
	if err := p.write(t, InstallationKind, changeset.OpUpdate, inst.ID, store.Scope{}, inst.Spec); err != nil {
		t.Errorf("writing it unchanged: %v", err)
	}

	// A module's recipe is the one it ships.
	req, err = p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Module: "web", Item: "hello"}, Scope: mine, Inputs: map[string]any{"domain": "hello.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if cs := p.submit(t, req); cs.State != changeset.StateApplied {
		t.Fatalf("installing from a module: %s %s", cs.State, cs.Error)
	}
	fromModule, _ := InstallationKind.Get(ctx, p.store, req.Changes[0].ID)
	other := fromModule.Spec
	other.Recipe = strings.Replace(hello, "says hello", "says something else", 1)
	other.RecipeSHA256, other.Resources = hashOf([]byte(other.Recipe)), nil
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", mine, other); !errors.Is(err, ErrNotFromSource) || !strings.Contains(i18n.From(err).Message, "web module") {
		t.Errorf("a recipe the module doesn't ship: %v", err)
	}
	other = fromModule.Spec
	other.Official, other.Resources = false, nil
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", mine, other); !errors.Is(err, ErrNotFromSource) {
		t.Errorf("a module's recipe that isn't official: %v", err)
	}
	other = fromModule.Spec
	other.Resources = nil
	if err := p.write(t, ServerInstallationKind, changeset.OpCreate, "", store.ServerScope(), other); err != nil {
		t.Errorf("the module's recipe, on the server: %v", err)
	}

	// Once the catalog moved on, the first version isn't offered any more,
	// yet undoing the upgrade puts it back.
	c.add(strings.Replace(checkedRecipe(srv), `version: "6.8.3"`, `version: "6.9.0"`, 1), nil)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	up, err := p.recipes.UpgradeRequest(ctx, inst.ID, UpgradeInput{})
	if err != nil {
		t.Fatal(err)
	}
	upgraded := p.submit(t, up)
	if upgraded.State != changeset.StateApplied {
		t.Fatalf("upgrade: %s %s", upgraded.State, upgraded.Error)
	}
	if err := p.write(t, InstallationKind, changeset.OpCreate, "", store.AccountScope(store.NewID("acct")), func() InstallationSpec {
		s := inst.Spec
		s.Resources = nil
		return s
	}()); !errors.Is(err, ErrNotFromSource) {
		t.Errorf("a version the catalog no longer offers, installed anew: %v", err)
	}
	undo, err := p.engine.Undo(ctx, changeset.UndoRequest{Actor: p.admin, ChangeSet: upgraded.ID})
	if err != nil {
		t.Fatalf("undoing the upgrade: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := p.jobs.Wait(wctx, undo.Job); err != nil {
		t.Fatal(err)
	}
	if back, _ := InstallationKind.Get(ctx, p.store, inst.ID); back.Spec.Version != "6.8.3" || back.Spec.RecipeSHA256 != inst.Spec.RecipeSHA256 {
		t.Errorf("after the undo = %s %s", back.Spec.Version, back.Spec.RecipeSHA256)
	}
}

// An installation kept from before these checks, or written to the store
// directly, still can't read another account's resources: a check of one
// that isn't where the installation is fails, saying so, and reveals
// nothing of it.
func TestHealthChecksReadOnlyTheirOwnResources(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t, nil)
	theirs, mine := store.AccountScope(store.NewID("acct")), store.AccountScope(store.NewID("acct"))
	site := store.NewID("site")
	inst := store.NewID(InstallationKind.Info().Prefix)
	spec, _ := json.Marshal(InstallationSpec{
		Source: SourceModule, Module: "web", Item: "hello", ItemKind: KindApp, Name: "Hello", Version: "1.0.0", License: "MIT",
		LicenseClass: OpenSource, Recipe: hello, RecipeSHA256: hashOf([]byte(hello)), Official: true,
		Resources: []InstalledResource{{Name: "site", Kind: "web.site", ID: site}},
	})
	status, _ := json.Marshal(store.Status{Phase: store.PhaseFailed, Message: "their secret troubles"})
	if err := p.store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		if err := tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: site, Kind: "web.site", Scope: theirs}, Spec: json.RawMessage(`{"domains":["theirs.example"]}`)}); err != nil {
			return err
		}
		if err := tx.UpdateStatus(ctx, site, status); err != nil {
			return err
		}
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: inst, Kind: InstallationKind.Name(), Scope: mine}, Spec: spec})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := p.recipes.Check(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	if out.Healthy || len(out.Health) != 1 || !strings.Contains(out.Health[0].Message, "isn't one of this installation's resources") || strings.Contains(out.Health[0].Message, "secret") {
		t.Errorf("health = %+v", out)
	}
}
