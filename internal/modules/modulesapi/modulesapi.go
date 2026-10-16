// Package modulesapi adds the module manager's operations to the API: the
// modules with their state, what they need and cost, and the profiles that
// turn several on at once. Turning one module on, off or removing it are
// the actions of its core.module resource, which the kind's operations
// carry, previews included.
package modulesapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func init() {
	api.RegisterExtension(api.Extension{Name: "modules", Operations: register, Reserved: []string{"modules"}})
}

var tags = []string{"Modules"}

type moduleList struct {
	Body struct {
		Modules []*modules.ModuleInfo `json:"modules"`
	}
}

type moduleInput struct {
	ID string `path:"id" doc:"The module's id, such as web"`
}

type moduleOutput struct {
	Body *modules.ModuleInfo
}

// Profile is a profile with the modules it turns on.
type Profile struct {
	modules.ProfileInfo
	Modules []string `json:"modules" doc:"The modules it turns on, dependencies first"`
}

type profileList struct {
	Body struct {
		Profiles []Profile `json:"profiles"`
	}
}

type applyProfileInput struct {
	Profile modules.Profile `path:"profile" enum:"all-in-one,web-hosting,home-server,virtualization-host,mail-server,dns-server,minimal"`
	api.ChangeParams
}

// ProfileChange is the change set that turns a profile on, with the
// modules it leaves off.
type ProfileChange struct {
	ChangeSet *changeset.ChangeSet `json:"changeSet"`
	Skipped   []modules.Skipped    `json:"skipped,omitempty" doc:"Modules of the profile that can't run on this server, with why"`
}

type applyProfileOutput struct {
	Status int
	Body   ProfileChange
}

func manager(svc *modules.Services) (*modules.Manager, error) {
	if svc == nil || svc.Modules == nil {
		return nil, i18n.ErrUnavailable.New("what", "The module manager")
	}
	return svc.Modules, nil
}

// mayRead checks that the signed-in actor may see the modules: a server
// admin, since modules belong to the server.
func mayRead(ctx context.Context, svc *modules.Services) error {
	return api.Authorize(ctx, svc.Authorizer, api.ReadPermission(modules.Kind.Name()), auth.Target{Scope: store.ServerScope()})
}

func register(a huma.API, svc *modules.Services) {
	op := huma.Operation{
		OperationID: "listModules", Method: http.MethodGet, Path: api.Prefix + "/modules", Tags: tags, Security: api.Protected,
		Summary:     "List the modules",
		Description: "Every module the panel has, with its state, what it needs and costs, the hardware it can't run without and what uses it.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: "list", Permission: string(api.ReadPermission(modules.Kind.Name())), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*moduleList, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := mayRead(ctx, svc); err != nil {
			return nil, err
		}
		list, err := m.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &moduleList{}
		out.Body.Modules = list
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getModule", Method: http.MethodGet, Path: api.Prefix + "/modules/{id}", Tags: tags, Security: api.Protected,
		Summary: "Describe a module",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: "get", Permission: string(api.ReadPermission(modules.Kind.Name())), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *moduleInput) (*moduleOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := mayRead(ctx, svc); err != nil {
			return nil, err
		}
		info, err := m.Info(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &moduleOutput{Body: info}, nil
	})

	op = huma.Operation{
		OperationID: "listProfiles", Method: http.MethodGet, Path: api.Prefix + "/modules/profiles", Tags: tags, Security: api.Protected,
		Summary:     "List the profiles",
		Description: "The bundles of modules the setup wizard offers, with the modules each turns on.",
	}
	api.Describe(&op, api.Info{Permission: string(api.ReadPermission(modules.Kind.Name())), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*profileList, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := mayRead(ctx, svc); err != nil {
			return nil, err
		}
		out := &profileList{}
		for _, p := range modules.Profiles() {
			out.Body.Profiles = append(out.Body.Profiles, Profile{ProfileInfo: p, Modules: m.ProfileModules(p.ID)})
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "applyProfile", Method: http.MethodPost, Path: api.Prefix + "/modules/profiles/{profile}", Tags: tags, Security: api.Protected,
		Summary: "Turn a profile's modules on",
		Description: "Turns on every module of the profile that is off and can run on this server, dependencies first, as one change set. " +
			"Modules the hardware can't run are left off and listed.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: modules.ActionTurnOn, Permission: string(auth.KindPermission(modules.Kind.Name(), modules.ActionTurnOn)), Risk: store.RiskMedium, DryRun: true, Long: true})
	huma.Register(a, op, func(ctx context.Context, in *applyProfileInput) (*applyProfileOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		req, skipped, err := m.ProfileRequest(ctx, in.Profile)
		if err != nil {
			return nil, err
		}
		res, err := api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
		if err != nil {
			return nil, err
		}
		return &applyProfileOutput{Status: res.Status, Body: ProfileChange{ChangeSet: res.Body, Skipped: skipped}}, nil
	})
}
