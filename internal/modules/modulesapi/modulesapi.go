// Package modulesapi adds the module manager's operations to the API: the
// modules with their state, what they need and cost and what they install,
// turning one on with the modules it needs, turning it off, removing it
// with or without its data and exporting its resources first, and the
// profiles that turn several on at once. Each change is a change set that
// can be previewed first. The actions of core.module resources, which the
// kind's operations carry, change one module at a time.
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

type changeModuleInput struct {
	ID string `path:"id" doc:"The module's id, such as web"`
	api.ChangeParams
}

type removeModuleInput struct {
	ID string `path:"id" doc:"The module's id, such as mail"`
	api.ChangeParams
	Body modules.RemoveInput
}

type softwareOutput struct {
	Body struct {
		Software []modules.Software `json:"software"`
	}
}

type exportOutput struct {
	Disposition string `header:"Content-Disposition"`
	Body        *modules.Export
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
	return may(ctx, svc, "read")
}

// may checks that the signed-in actor may use an operation of core.module,
// before anything is read for it.
func may(ctx context.Context, svc *modules.Services, operation string) error {
	return api.Authorize(ctx, svc.Authorizer, auth.KindPermission(modules.Kind.Name(), operation), auth.Target{Scope: store.ServerScope()})
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
		if err := may(ctx, svc, modules.ActionTurnOn); err != nil {
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

	op = huma.Operation{
		OperationID: "turnOnModule", Method: http.MethodPost, Path: api.Prefix + "/modules/{id}/actions/turn_on", Tags: tags, Security: api.Protected,
		Summary: "Turn a module on, with the modules it needs",
		Description: "Turns on a module and the modules it needs that are off, dependencies first, as one change set. The preview lists what the module " +
			"does once something uses it: the software it downloads then, from where and how large, its packages, services and ports, and its memory and disk.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: modules.ActionTurnOn, Permission: string(auth.KindPermission(modules.Kind.Name(), modules.ActionTurnOn)), Risk: store.RiskLow, DryRun: true, Reversible: true})
	huma.Register(a, op, func(ctx context.Context, in *changeModuleInput) (*api.ChangeSetOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := may(ctx, svc, modules.ActionTurnOn); err != nil {
			return nil, err
		}
		req, err := m.TurnOnRequest(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
	})

	op = huma.Operation{
		OperationID: "turnOffModule", Method: http.MethodPost, Path: api.Prefix + "/modules/{id}/actions/turn_off", Tags: tags, Security: api.Protected,
		Summary: "Turn a module off",
		Description: "Stops a module and hides it, keeping its software, resources, settings and data, as a change set. A module that resources use " +
			"can only be turned off when it can keep them dormant.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: modules.ActionTurnOff, Permission: string(auth.KindPermission(modules.Kind.Name(), modules.ActionTurnOff)), Risk: store.RiskMedium, DryRun: true, Reversible: true})
	huma.Register(a, op, func(ctx context.Context, in *changeModuleInput) (*api.ChangeSetOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := may(ctx, svc, modules.ActionTurnOff); err != nil {
			return nil, err
		}
		req, err := m.TurnOffRequest(in.ID)
		if err != nil {
			return nil, err
		}
		return api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
	})

	op = huma.Operation{
		OperationID: "removeModule", Method: http.MethodPost, Path: api.Prefix + "/modules/{id}/actions/remove", Tags: tags, Security: api.Protected,
		Summary: "Remove a module",
		Description: "Turns a module off and uninstalls its software, as one change set. With keepData its resources and their data stay, and come " +
			"back when it is turned on again; without it they are deleted in the same change set, after a snapshot. The person removing it types " +
			"its name to confirm, and GET /api/v1/modules/{id}/export keeps a copy of its resources first.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: modules.ActionRemove, Permission: string(auth.KindPermission(modules.Kind.Name(), modules.ActionRemove)), Risk: store.RiskHigh, DryRun: true, Destructive: true})
	huma.Register(a, op, func(ctx context.Context, in *removeModuleInput) (*api.ChangeSetOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := may(ctx, svc, modules.ActionRemove); err != nil {
			return nil, err
		}
		req, err := m.RemoveRequest(ctx, in.ID, in.Body.Confirm, in.Body.KeepData)
		if err != nil {
			return nil, err
		}
		return api.Submit(ctx, svc.Pipeline, in.ChangeParams, req)
	})

	op = huma.Operation{
		OperationID: "listModuleSoftware", Method: http.MethodGet, Path: api.Prefix + "/modules/{id}/software", Tags: tags, Security: api.Protected,
		Summary: "List what a module installs",
		Description: "The software a module installs the first time something needs it: where each comes from, when it is installed, the packages " +
			"it brings in, how much it downloads and takes on disk, and whether it is installed already. The server's package index gives the sizes " +
			"where it can, and the module's manifest otherwise.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: "software", Permission: string(api.ReadPermission(modules.Kind.Name())), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *moduleInput) (*softwareOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := mayRead(ctx, svc); err != nil {
			return nil, err
		}
		list, err := m.FirstUse(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &softwareOutput{}
		out.Body.Software = list
		return out, nil
	})

	op = huma.Operation{
		OperationID: "exportModule", Method: http.MethodGet, Path: api.Prefix + "/modules/{id}/export", Tags: tags, Security: api.Protected,
		Summary: "Export a module's resources",
		Description: "Every resource of the module's kinds with its settings and labels, as a JSON file to keep before removing the module with its data. " +
			"Fields that refer to secrets are left out and named, since secret values never leave the panel this way.",
	}
	api.Describe(&op, api.Info{Kind: modules.Kind.Name(), Operation: "export", Permission: string(api.ReadPermission(modules.Kind.Name())), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *moduleInput) (*exportOutput, error) {
		m, err := manager(svc)
		if err != nil {
			return nil, err
		}
		if err := mayRead(ctx, svc); err != nil {
			return nil, err
		}
		// The export holds resources of every owner, so it is for those
		// who may read them all.
		for _, kind := range m.KindsOf(in.ID) {
			if err := api.Authorize(ctx, svc.Authorizer, api.ReadPermission(kind), auth.Target{Scope: store.ServerScope()}); err != nil {
				return nil, err
			}
		}
		export, err := m.Export(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		name := "racksail-" + export.Module + "-" + export.Exported.Format("20060102-150405") + ".json"
		return &exportOutput{Disposition: `attachment; filename="` + name + `"`, Body: export}, nil
	})
}
