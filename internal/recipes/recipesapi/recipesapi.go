// Package recipesapi adds the catalog's operations to the API: the items
// catalogs offer and their recipes, and installing, upgrading and
// uninstalling them, each as a change set that can be previewed first.
// Catalogs and installations are kinds, whose operations the API
// generates.
package recipesapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func init() {
	api.RegisterExtension(api.Extension{Name: "catalog", Operations: register, Reserved: []string{"catalog"}})
}

var tags = []string{"Catalog"}

func parts(svc *modules.Services) (*recipes.Catalogs, *recipes.Engine, error) {
	if svc == nil || svc.Catalogs == nil || svc.Recipes == nil {
		return nil, nil, i18n.ErrUnavailable.New("what", "The catalog")
	}
	return svc.Catalogs, svc.Recipes, nil
}

type itemsInput struct {
	Kind     recipes.ItemKind `query:"kind" enum:"app,runtime,service,stack,dns-template,automation,tuning-preset,model,theme,addon" doc:"Only items of this kind"`
	Category string           `query:"category" maxLength:"64" doc:"Only items in this category"`
	Query    string           `query:"q" maxLength:"200" doc:"Words the name, id or summary contains"`
	Hidden   bool             `query:"hidden" doc:"Also list items marked broken or unmaintained"`
}

type itemList struct {
	Body struct {
		Items []recipes.ListedItem `json:"items" doc:"The items of every catalog that is on, featured first and then by name"`
	}
}

type itemInput struct {
	Catalog store.ID `path:"catalog" doc:"The catalog's ID"`
	Item    string   `path:"item" doc:"The item's id"`
}

// ItemDetail is an item with its recipe.
type ItemDetail struct {
	recipes.ListedItem
	// Recipe is the recipe as recipe.schema.json describes it.
	Recipe json.RawMessage `json:"recipe" doc:"The item's recipe, as spec/schemas/recipe.schema.json describes it"`
	// Fits is empty when the item can be installed here, and says why not
	// otherwise.
	Fits string `json:"fits,omitempty" doc:"Why the item can't be installed on this server, when it can't"`
	// Notice is shown before installing software that isn't open source.
	Notice string `json:"notice,omitempty" doc:"What installing it means, for software that isn't open source"`
}

type itemOutput struct {
	Body ItemDetail
}

type iconOutput struct {
	ContentType string `header:"Content-Type"`
	Policy      string `header:"Content-Security-Policy"`
	Cache       string `header:"Cache-Control"`
	Body        []byte
}

type installInput struct {
	api.ChangeParams
	Body recipes.InstallInput
}

type upgradeInput struct {
	ID store.ID `path:"id" doc:"The installation's ID"`
	api.ChangeParams
	Body recipes.UpgradeInput
}

type uninstallInput struct {
	ID store.ID `path:"id" doc:"The installation's ID"`
	api.ChangeParams
	Body recipes.UninstallInput
}

func register(a huma.API, svc *modules.Services) {
	op := huma.Operation{
		OperationID: "listCatalogItems", Method: http.MethodGet, Path: api.Prefix + "/catalog/items", Tags: tags, Security: api.Protected,
		Summary:     "List what catalogs offer",
		Description: "The items of every catalog that is on, featured items and the official catalog's first, each with its license class and requirements.",
	}
	api.Describe(&op, api.Info{Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *itemsInput) (*itemList, error) {
		cats, _, err := parts(svc)
		if err != nil {
			return nil, err
		}
		if _, err := api.Actor(ctx); err != nil {
			return nil, err
		}
		items, err := cats.Items(ctx, recipes.ItemFilter{Kind: in.Kind, Category: in.Category, Query: in.Query, Hidden: in.Hidden})
		if err != nil {
			return nil, err
		}
		out := &itemList{}
		out.Body.Items = items
		if out.Body.Items == nil {
			out.Body.Items = []recipes.ListedItem{}
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getCatalogItem", Method: http.MethodGet, Path: api.Prefix + "/catalog/items/{catalog}/{item}", Tags: tags, Security: api.Protected,
		Summary:     "Describe an item",
		Description: "An item with its recipe, checked against the catalog's signed index: the inputs the install wizard asks for, what it needs and whether it fits this server.",
	}
	api.Describe(&op, api.Info{Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *itemInput) (*itemOutput, error) {
		cats, engine, err := parts(svc)
		if err != nil {
			return nil, err
		}
		if _, err := api.Actor(ctx); err != nil {
			return nil, err
		}
		r, _, it, err := cats.Recipe(ctx, in.Catalog, in.Item)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		out := &itemOutput{Body: ItemDetail{ListedItem: *it, Recipe: raw}}
		if err := engine.Fit(ctx, r); err != nil {
			out.Body.Fits = i18n.From(err).Message
		}
		if r.LicenseClass != recipes.OpenSource {
			out.Body.Notice = i18n.From(recipes.ErrLicenseNotice.New("name", r.Name, "class", strings.ReplaceAll(string(r.LicenseClass), "-", " "), "license", r.License)).Message
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getCatalogItemIcon", Method: http.MethodGet, Path: api.Prefix + "/catalog/items/{catalog}/{item}/icon", Tags: tags, Security: api.Protected,
		Summary: "Fetch an item's icon",
	}
	api.Describe(&op, api.Info{Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *itemInput) (*iconOutput, error) {
		cats, _, err := parts(svc)
		if err != nil {
			return nil, err
		}
		if _, err := api.Actor(ctx); err != nil {
			return nil, err
		}
		it, err := cats.Item(ctx, in.Catalog, in.Item)
		if err != nil {
			return nil, err
		}
		if it.Icon == "" {
			return nil, i18n.ErrNotFound.New("what", "The icon of "+it.Name)
		}
		data, err := cats.File(ctx, in.Catalog, in.Item, it.Icon)
		if err != nil {
			return nil, err
		}
		ct := "image/png"
		if path.Ext(it.Icon) == ".svg" {
			ct = "image/svg+xml"
		}
		// Icons are checked when the catalog is built; the policy keeps an
		// SVG inert whatever it holds.
		return &iconOutput{ContentType: ct, Policy: "default-src 'none'; style-src 'unsafe-inline'; sandbox", Cache: "private, max-age=86400", Body: data}, nil
	})

	op = huma.Operation{
		OperationID: "installCatalogItem", Method: http.MethodPost, Path: api.Prefix + "/catalog/install", Tags: tags, Security: api.Protected,
		Summary: "Install an item",
		Description: "Installs an item from a catalog, or a recipe a module ships, into an account or onto the server, as one change set: " +
			"its resources and the installation that records where it came from. Software that isn't open source needs its license accepted.",
	}
	api.Describe(&op, api.Info{Kind: recipes.InstallationKind.Name(), Operation: "create", Risk: store.RiskMedium, DryRun: true, Long: true, Reversible: true})
	huma.Register(a, op, func(ctx context.Context, in *installInput) (*api.ChangeSetOutput, error) {
		_, engine, err := parts(svc)
		if err != nil {
			return nil, err
		}
		kind, err := recipes.InstallationKindFor(in.Body.Scope)
		if err != nil {
			return nil, err
		}
		// Who may install there is settled before the recipe is read.
		if err := api.Authorize(ctx, svc.Authorizer, auth.KindPermission(kind.Info().Name, "create"), auth.Target{Scope: in.Body.Scope}); err != nil {
			return nil, err
		}
		req, err := engine.InstallRequest(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
	})

	op = huma.Operation{
		OperationID: "upgradeInstallation", Method: http.MethodPost, Path: api.Prefix + "/catalog/installations/{id}/upgrade", Tags: tags, Security: api.Protected,
		Summary: "Upgrade an installation",
		Description: "Moves an installation to the newest version its catalog or module offers, as one change set: the data is snapshotted first " +
			"when the recipe asks, resources both versions have are updated, new ones created, and dropped ones that hold no data removed.",
	}
	api.Describe(&op, api.Info{Kind: recipes.InstallationKind.Name(), Operation: "update", Risk: store.RiskMedium, DryRun: true, Long: true})
	huma.Register(a, op, func(ctx context.Context, in *upgradeInput) (*api.ChangeSetOutput, error) {
		_, engine, err := parts(svc)
		if err != nil {
			return nil, err
		}
		if err := mayChange(ctx, svc, engine, in.ID, "update"); err != nil {
			return nil, err
		}
		req, err := engine.UpgradeRequest(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
	})

	op = huma.Operation{
		OperationID: "uninstallInstallation", Method: http.MethodPost, Path: api.Prefix + "/catalog/installations/{id}/uninstall", Tags: tags, Security: api.Protected,
		Summary: "Uninstall an installation",
		Description: "Removes what an installation created, in reverse order, as one change set. What holds data, such as databases and " +
			"site files, is kept unless you choose otherwise, or the recipe does.",
	}
	api.Describe(&op, api.Info{Kind: recipes.InstallationKind.Name(), Operation: "delete", Risk: store.RiskHigh, DryRun: true, Long: true, Destructive: true})
	huma.Register(a, op, func(ctx context.Context, in *uninstallInput) (*api.ChangeSetOutput, error) {
		_, engine, err := parts(svc)
		if err != nil {
			return nil, err
		}
		if err := mayChange(ctx, svc, engine, in.ID, "delete"); err != nil {
			return nil, err
		}
		req, err := engine.UninstallRequest(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
	})
}

// mayChange authorizes an operation on an installation before anything is
// read for it, such as its recipe or what its catalog offers now, which
// would say things about installations of other accounts. One the actor
// may not see answers as if it didn't exist.
func mayChange(ctx context.Context, svc *modules.Services, engine *recipes.Engine, id store.ID, operation string) error {
	inst, err := engine.Installation(ctx, id)
	if err != nil {
		return err
	}
	if err := api.AuthorizeRead(ctx, svc.Authorizer, inst.Meta); err != nil {
		return err
	}
	return api.Authorize(ctx, svc.Authorizer, auth.KindPermission(inst.Kind, operation), auth.Target{Scope: inst.Scope, Object: inst.ID})
}
