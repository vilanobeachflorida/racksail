// Package hooksapi adds the extension system's operations to the API: the
// hook points and the hooks attached to them, switching a hook the circuit
// breaker turned off back on, the rule tester, safe mode, the UI entries
// add-ons declare, and the routes and screens add-ons serve themselves.
package hooksapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/hooks/rules"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func init() {
	api.RegisterExtension(api.Extension{
		Name: "hooks", Operations: register, Reserved: []string{"hooks", "rules", "safe-mode", "addons"},
		Routes: map[string]func(svc *modules.Services) http.Handler{
			api.Prefix + "/addons/{addon}/{path...}":             addonRoutes,
			"GET " + api.Prefix + "/addons/{addon}/ui/{path...}": addonScreens,
		},
	})
}

// Permissions of the extension system. Hooks belong to the server, so only
// server-wide grants carry them.
const (
	PermHookRead     auth.Permission = "core.hook.read"
	PermHookEnable   auth.Permission = "core.hook.enable"
	PermSafeModeRead auth.Permission = "core.safe_mode.read"
	PermSafeMode     auth.Permission = "core.safe_mode.update"
)

var tags = []string{"Extensions"}

func serverWide(ctx context.Context, svc *modules.Services, perm auth.Permission) error {
	return api.Authorize(ctx, svc.Authorizer, perm, auth.Target{Scope: store.ServerScope()})
}

func runner(svc *modules.Services) (*hooks.Runner, error) {
	if svc == nil || svc.Hooks == nil || svc.Hooks.Registry == nil {
		return nil, i18n.ErrUnavailable.New("what", "The hook pipeline")
	}
	return svc.Hooks, nil
}

type pointsInput struct {
	Prefix string `query:"prefix" maxLength:"200" doc:"Only points that start with this, such as web.site."`
}

type pointList struct {
	Body struct {
		Points []hooks.PointInfo `json:"points"`
	}
}

type hookList struct {
	Body struct {
		Hooks []hooks.HookInfo `json:"hooks"`
	}
}

type enableInput struct {
	Body struct {
		Owner string      `json:"owner" doc:"Who registered the hook, such as addon:site-guard"`
		Point hooks.Point `json:"point" doc:"The point it is attached to"`
	}
}

type testRuleInput struct {
	Body rules.TestRequest
}

type testRuleOutput struct {
	Body *rules.TestResult
}

type safeModeOutput struct {
	Body addons.SafeModeStatus
}

type enterSafeModeInput struct {
	Body struct {
		Reason string `json:"reason" minLength:"1" maxLength:"500" doc:"Why, for the audit log and whoever turns it off" example:"An add-on keeps the panel from saving changes."`
	}
}

// UIEntry is a UI entry an add-on declares.
type UIEntry struct {
	Addon string            `json:"addon" doc:"The add-on, as it runs: its id, and the account for account add-ons"`
	Label string            `json:"label" doc:"The add-on's name and version"`
	Point hooks.Point       `json:"point" enum:"ui.nav,ui.tab,ui.card,ui.command,ui.settings,ui.column"`
	UI    hooks.UIExtension `json:"ui"`
}

type entriesInput struct {
	Point hooks.Point `query:"point" enum:"ui.nav,ui.tab,ui.card,ui.command,ui.settings,ui.column" doc:"Only entries at this place"`
}

type entryList struct {
	Body struct {
		Entries []UIEntry `json:"entries"`
	}
}

var uiPoints = []hooks.Point{hooks.ExtendNav, hooks.ExtendTab, hooks.ExtendCard, hooks.ExtendCommand, hooks.ExtendSettings, hooks.ExtendColumn}

// rulePermission is the permission to keep rules for an owner.
func rulePermission(owner store.Scope, operation string) auth.Permission {
	switch owner.Level {
	case store.ScopeReseller:
		return auth.KindPermission(rules.ResellerPolicy.Name(), operation)
	case store.ScopeAccount:
		return auth.KindPermission(rules.AccountPolicy.Name(), operation)
	}
	return auth.KindPermission(rules.Policy.Name(), operation)
}

func register(a huma.API, svc *modules.Services) {
	op := huma.Operation{
		OperationID: "listHookPoints", Method: http.MethodGet, Path: api.Prefix + "/hooks/points", Tags: tags, Security: api.Protected,
		Summary:     "List the hook points",
		Description: "Every point hooks can attach to: those of the change pipeline for every kind and its actions, and those modules declare.",
	}
	api.Describe(&op, api.Info{Permission: string(PermHookRead), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *pointsInput) (*pointList, error) {
		r, err := runner(svc)
		if err != nil {
			return nil, err
		}
		if err := serverWide(ctx, svc, PermHookRead); err != nil {
			return nil, err
		}
		out := &pointList{}
		out.Body.Points = r.Registry.Describe(in.Prefix)
		return out, nil
	})

	op = huma.Operation{
		OperationID: "listHooks", Method: http.MethodGet, Path: api.Prefix + "/hooks", Tags: tags, Security: api.Protected,
		Summary:     "List the hooks",
		Description: "Every hook attached, in the order hooks run, with whether the circuit breaker switched it off.",
	}
	api.Describe(&op, api.Info{Permission: string(PermHookRead), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*hookList, error) {
		r, err := runner(svc)
		if err != nil {
			return nil, err
		}
		if err := serverWide(ctx, svc, PermHookRead); err != nil {
			return nil, err
		}
		out := &hookList{}
		out.Body.Hooks = r.Registry.Hooks()
		return out, nil
	})

	op = huma.Operation{
		OperationID: "enableHook", Method: http.MethodPost, Path: api.Prefix + "/hooks/enable", Tags: tags, Security: api.Protected,
		Summary:     "Switch a hook back on",
		Description: "Switches a hook the circuit breaker turned off back on, after it failed too many times in a row. The audit log records who did.",
	}
	api.Describe(&op, api.Info{Permission: string(PermHookEnable), Risk: store.RiskMedium})
	huma.Register(a, op, func(ctx context.Context, in *enableInput) (*struct{}, error) {
		r, err := runner(svc)
		if err != nil {
			return nil, err
		}
		if err := serverWide(ctx, svc, PermHookEnable); err != nil {
			return nil, err
		}
		if err := r.Registry.Enable(ctx, in.Body.Owner, in.Body.Point); err != nil {
			return nil, err
		}
		if actor, err := api.Actor(ctx); err == nil && svc.Audit != nil {
			if err := svc.Audit.Record(ctx, events.AuditRecord{
				Actor: *actor, Action: string(PermHookEnable), Scope: store.ServerScope(), Outcome: events.OutcomeSuccess,
				Summary: "Switched " + in.Body.Owner + " back on at " + string(in.Body.Point), RequestID: events.RequestID(ctx),
			}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})

	op = huma.Operation{
		OperationID: "testRule", Method: http.MethodPost, Path: api.Prefix + "/rules/test", Tags: tags, Security: api.Protected,
		Summary: "Try a rule",
		Description: "Runs a rule on a change you describe, as the hook pipeline would, without saving the rule or making the change: " +
			"whether it applies, what a validate rule decides, what a default rule fills in and what the evaluation cost.",
	}
	api.Describe(&op, api.Info{Permission: string(auth.KindPermission(rules.Policy.Name(), "read")), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *testRuleInput) (*testRuleOutput, error) {
		if svc == nil || svc.Rules == nil {
			return nil, i18n.ErrUnavailable.New("what", "The rule engine")
		}
		owner := in.Body.Owner
		if owner.Level == "" {
			owner = store.ServerScope()
		}
		if err := api.Authorize(ctx, svc.Authorizer, rulePermission(owner, "read"), auth.Target{Scope: owner}); err != nil {
			return nil, err
		}
		actor, err := api.Actor(ctx)
		if err != nil {
			return nil, err
		}
		req := in.Body
		req.Owner = owner
		res, err := svc.Rules.Test(ctx, actor, req)
		if err != nil {
			return nil, err
		}
		return &testRuleOutput{Body: res}, nil
	})

	safeMode := func() (*addons.Runtime, error) {
		if svc == nil || svc.Addons == nil || svc.Addons.SafeMode() == nil {
			return nil, i18n.ErrUnavailable.New("what", "Safe mode")
		}
		return svc.Addons, nil
	}
	op = huma.Operation{
		OperationID: "getSafeMode", Method: http.MethodGet, Path: api.Prefix + "/safe-mode", Tags: tags, Security: api.Protected,
		Summary: "Say whether safe mode is on",
	}
	api.Describe(&op, api.Info{Permission: string(PermSafeModeRead), Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*safeModeOutput, error) {
		rt, err := safeMode()
		if err != nil {
			return nil, err
		}
		if err := serverWide(ctx, svc, PermSafeModeRead); err != nil {
			return nil, err
		}
		return &safeModeOutput{Body: rt.SafeMode().Status()}, nil
	})

	op = huma.Operation{
		OperationID: "enterSafeMode", Method: http.MethodPost, Path: api.Prefix + "/safe-mode", Tags: tags, Security: api.Protected,
		Summary:     "Turn safe mode on",
		Description: "Stops every add-on and keeps them out of every change until safe mode is turned off.",
	}
	api.Describe(&op, api.Info{Permission: string(PermSafeMode), Risk: store.RiskHigh})
	huma.Register(a, op, func(ctx context.Context, in *enterSafeModeInput) (*safeModeOutput, error) {
		rt, err := safeMode()
		if err != nil {
			return nil, err
		}
		if err := serverWide(ctx, svc, PermSafeMode); err != nil {
			return nil, err
		}
		actor, err := api.Actor(ctx)
		if err != nil {
			return nil, err
		}
		if err := rt.EnterSafeMode(ctx, actor, in.Body.Reason); err != nil {
			return nil, err
		}
		return &safeModeOutput{Body: rt.SafeMode().Status()}, nil
	})

	op = huma.Operation{
		OperationID: "leaveSafeMode", Method: http.MethodDelete, Path: api.Prefix + "/safe-mode", Tags: tags, Security: api.Protected,
		Summary:     "Turn safe mode off",
		Description: "Lets add-ons run again, unless the server was started with the racksail.safe_mode boot option, which keeps it on until the next boot without it.",
	}
	api.Describe(&op, api.Info{Permission: string(PermSafeMode), Risk: store.RiskHigh})
	huma.Register(a, op, func(ctx context.Context, in *struct{}) (*safeModeOutput, error) {
		rt, err := safeMode()
		if err != nil {
			return nil, err
		}
		if err := serverWide(ctx, svc, PermSafeMode); err != nil {
			return nil, err
		}
		actor, err := api.Actor(ctx)
		if err != nil {
			return nil, err
		}
		st, err := rt.LeaveSafeMode(ctx, actor)
		if err != nil {
			return nil, err
		}
		return &safeModeOutput{Body: st}, nil
	})

	op = huma.Operation{
		OperationID: "listAddonEntries", Method: http.MethodGet, Path: api.Prefix + "/addons/entries", Tags: tags, Security: api.Protected,
		Summary:     "List add-ons' UI entries",
		Description: "The navigation entries, tabs, cards, commands, settings pages and columns the add-ons you may use declare.",
	}
	api.Describe(&op, api.Info{Risk: store.RiskRead})
	huma.Register(a, op, func(ctx context.Context, in *entriesInput) (*entryList, error) {
		r, err := runner(svc)
		if err != nil {
			return nil, err
		}
		actor, err := api.Actor(ctx)
		if err != nil {
			return nil, err
		}
		out := &entryList{Body: struct {
			Entries []UIEntry `json:"entries"`
		}{Entries: []UIEntry{}}}
		points := uiPoints
		if in.Point != "" {
			points = []hooks.Point{in.Point}
		}
		for _, p := range points {
			for _, reg := range r.Registry.Extensions(p) {
				inst := strings.TrimPrefix(reg.Owner, hooks.OwnerAddon+":")
				if reg.Extension == nil || reg.Extension.UI == nil || !mayUse(ctx, svc, actor, inst) {
					continue
				}
				out.Body.Entries = append(out.Body.Entries, UIEntry{Addon: inst, Label: reg.Name(), Point: p, UI: *reg.Extension.UI})
			}
		}
		sort.SliceStable(out.Body.Entries, func(i, j int) bool { return out.Body.Entries[i].Label < out.Body.Entries[j].Label })
		return out, nil
	})
}

// mayUse reports whether an actor may use an add-on: everyone signed in
// may use a server's add-ons, and an account's add-on those who may see
// the account's add-ons.
func mayUse(ctx context.Context, svc *modules.Services, actor *auth.Actor, inst string) bool {
	if svc.Addons == nil {
		return false
	}
	_, _, grants, _, ok := svc.Addons.Running(inst)
	if !ok {
		return false
	}
	if grants.Scope.Level == store.ScopeServer || grants.Scope.Level == "" {
		return true
	}
	if svc.Authorizer == nil {
		return false
	}
	return svc.Authorizer.Authorize(ctx, actor, api.ReadPermission(addons.AccountAddon.Name()), auth.Target{Scope: grants.Scope}) == nil
}

// problem answers with an error as problem details.
func problem(w http.ResponseWriter, err error) {
	p := httpapi.FromError(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}

// addon finds a running add-on the signed-in actor may use.
func addon(r *http.Request, svc *modules.Services) (string, error) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		return "", i18n.ErrUnauthenticated.New()
	}
	inst := r.PathValue("addon")
	if svc == nil || svc.Addons == nil || !mayUse(r.Context(), svc, actor, inst) {
		return "", i18n.ErrNotFound.New("what", "Add-on "+inst)
	}
	return inst, nil
}

// addonRoutes passes requests to the API routes an add-on declares and
// serves itself, as the signed-in actor, and nothing it didn't declare.
func addonRoutes(svc *modules.Services) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inst, err := addon(r, svc)
		if err != nil {
			problem(w, err)
			return
		}
		var ext *hooks.Extension
		for _, reg := range svc.Hooks.Registry.Extensions(hooks.ExtendRoutes) {
			if reg.Owner == hooks.OwnerAddon+":"+inst {
				ext = reg.Extension
			}
		}
		path := "/" + r.PathValue("path")
		declared := ext != nil && slices.ContainsFunc(ext.Routes, func(rt hooks.AddonRoute) bool {
			return strings.EqualFold(rt.Method, r.Method) && routeMatches(rt.Path, path)
		})
		if !declared {
			problem(w, i18n.ErrNotFound.New("what", r.Method+" "+path))
			return
		}
		ext.Handler.ServeHTTP(w, r)
	})
}

// routeMatches matches a path against a declared route, where {name}
// stands for one segment.
func routeMatches(pattern, path string) bool {
	ps, xs := strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(xs) {
		return false
	}
	for i := range ps {
		if strings.HasPrefix(ps[i], "{") && strings.HasSuffix(ps[i], "}") && xs[i] != "" {
			continue
		}
		if ps[i] != xs[i] {
			return false
		}
	}
	return true
}

// screenPolicy is what an add-on's screen may do in the frame the panel
// shows it in: run its own scripts and reach its own routes, and nothing
// of the panel's.
const screenPolicy = "sandbox allow-scripts allow-forms; default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'self'; base-uri 'none'"

// addonScreens serves the screens an add-on declares, in a sandbox.
func addonScreens(svc *modules.Services) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inst, err := addon(r, svc)
		if err != nil {
			problem(w, err)
			return
		}
		_, _, _, client, ok := svc.Addons.Running(inst)
		if !ok {
			problem(w, i18n.ErrNotFound.New("what", "Add-on "+inst))
			return
		}
		w.Header().Set("Content-Security-Policy", screenPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		client.Proxy("/ui/", nil).ServeHTTP(w, r)
	})
}
