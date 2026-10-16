package rules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

type siteSpec struct {
	Domains []string          `json:"domains"`
	Headers map[string]string `json:"headers,omitempty"`
	Workers int               `json:"workers,omitempty"`
}

var siteKind = store.Define[siteSpec, store.Status](store.KindInfo{
	Name: "web.site", Prefix: "site", Scope: store.ScopeAccount, Title: "Site", Plural: "sites", Description: "A site.",
	Actions: []store.Action{{Name: "deploy", Summary: "Deploy the site.", Risk: store.RiskMedium}},
})

type accountSpec struct {
	Name string `json:"name"`
	Plan string `json:"plan,omitempty"`
}

var accountKind = store.Define[accountSpec, store.Status](store.KindInfo{
	Name: "hosting.account", Prefix: "acct", Scope: store.ScopeServer, Title: "Account", Plural: "accounts", Description: "An account.",
})

var resellerKind = store.Define[accountSpec, store.Status](store.KindInfo{
	Name: "hosting.reseller", Prefix: "rsl", Scope: store.ScopeServer, Title: "Reseller", Plural: "resellers", Description: "A reseller.",
})

type planSpec struct {
	MaxWorkers int `json:"maxWorkers"`
}

var planKind = store.Define[planSpec, store.Status](store.KindInfo{
	Name: "hosting.plan", Prefix: "plan", Scope: store.ScopeServer, Title: "Plan", Plural: "plans", Description: "A plan.",
})

type owners map[store.ID]store.ID

func (o owners) ResellerOf(ctx context.Context, account store.ID) (store.ID, error) {
	return o[account], nil
}

type env struct {
	store    *store.Memory
	engine   *changeset.Engine
	queue    jobs.Queue
	registry *hooks.Registry
	rules    *Engine
	admin    *auth.Actor
	owners   owners
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{siteKind, accountKind, resellerKind, planKind, Policy, ResellerPolicy, AccountPolicy} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st := store.NewMemory(kinds)
	reg := hooks.NewRegistry()
	for _, k := range kinds.All() {
		reg.DeclareKind(k)
	}
	e := &env{store: st, registry: reg, owners: owners{}}
	e.rules = NewEngine(Options{
		Registry: reg, Ownership: func() auth.Ownership { return e.owners },
		Facts: StoreFacts{Store: st, Machine: func(context.Context) (system.MachineFacts, error) {
			return system.MachineFacts{Hostname: "panel.example.com", Arch: "amd64", MemoryBytes: 8 << 30}, nil
		}},
	})
	recs := reconcile.NewRegistry()
	for _, r := range e.rules.Reconcilers() {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	e.queue = jobs.NewMemory(ctx, jobs.MemoryOptions{})
	engine, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Jobs: e.queue, Hooks: &hooks.Runner{Registry: reg}, Authorizer: auth.NewRoleAuthorizer(nil),
		Runner: &reconcile.Runner{Store: st, Agent: agent.NewLocal(sim.New(sim.Options{}).Backend(), nil), Registry: recs},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.engine = engine
	e.admin = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "Ada", Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	return e
}

func (e *env) submit(t *testing.T, actor *auth.Actor, changes ...changeset.Change) (*changeset.ChangeSet, error) {
	t.Helper()
	cs, err := e.engine.Submit(context.Background(), changeset.Request{Actor: actor, Changes: changes})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.queue.Wait(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	return e.engine.Get(ctx, cs.ID)
}

// owner puts an account or a reseller straight in the store, for what the
// tests make to belong to: resources only belong to owners that exist.
func (e *env) owner(t *testing.T, kind *store.KindOf[accountSpec, store.Status]) store.ID {
	t.Helper()
	id := store.NewID(kind.Info().Prefix)
	ctx := context.Background()
	if err := e.store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: id, Kind: kind.Name(), Scope: store.ServerScope()}, Spec: json.RawMessage(`{"name":"test"}`)})
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) rule(t *testing.T, kind string, scope store.Scope, spec hooks.PolicySpec) store.ID {
	t.Helper()
	raw, _ := json.Marshal(spec)
	cs, err := e.submit(t, e.admin, changeset.Change{Op: changeset.OpCreate, Kind: kind, Scope: scope, Spec: raw})
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != changeset.StateApplied {
		t.Fatalf("storing the rule: %+v", cs)
	}
	return cs.Changes[0].ID
}

func site(scope store.Scope, spec siteSpec) changeset.Change {
	raw, _ := json.Marshal(spec)
	return changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: scope, Spec: raw}
}

func TestRulesAreCheckedWhenSaved(t *testing.T) {
	e := newEnv(t)
	for name, spec := range map[string]hooks.PolicySpec{
		"syntax":                   {Name: "Broken", Type: hooks.PolicyValidate, Expression: "resource.spec.domains.exists(d, ", Message: "no"},
		"not a boolean":            {Name: "Number", Type: hooks.PolicyValidate, Expression: "1 + 2", Message: "no"},
		"unknown variable":         {Name: "Who", Type: hooks.PolicyValidate, Expression: "visitor.ip == '1.2.3.4'", Message: "no"},
		"no message":               {Name: "Quiet", Type: hooks.PolicyValidate, Expression: "true"},
		"default with expression":  {Name: "Mixed", Type: hooks.PolicyDefault, Expression: "true", Defaults: []hooks.FieldDefault{{Field: "a", Expression: "1"}}},
		"default on update":        {Name: "Later", Type: hooks.PolicyDefault, Operations: []string{"update"}, Defaults: []hooks.FieldDefault{{Field: "a", Expression: "1"}}},
		"broken default":           {Name: "Bad default", Type: hooks.PolicyDefault, Defaults: []hooks.FieldDefault{{Field: "a", Expression: "1 +"}}},
		"twice the same field":     {Name: "Twice", Type: hooks.PolicyDefault, Defaults: []hooks.FieldDefault{{Field: "a", Expression: "1"}, {Field: "a", Expression: "2"}}},
		"a field that isn't valid": {Name: "Path", Type: hooks.PolicyDefault, Defaults: []hooks.FieldDefault{{Field: "a..b", Expression: "1"}}},
	} {
		raw, _ := json.Marshal(spec)
		_, err := e.engine.Submit(context.Background(), changeset.Request{Actor: e.admin, DryRun: true, Changes: []changeset.Change{
			{Op: changeset.OpCreate, Kind: "core.policy", Scope: store.ServerScope(), Spec: raw},
		}})
		if !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// What a rule's JSON Schema finds wrong comes back on the field it is about,
// in the catalog's words, whether the rule is saved or tried out.
func TestRuleSchemaFindingsAreWorded(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for _, tc := range []struct {
		spec          hooks.PolicySpec
		path, message string
	}{
		{hooks.PolicySpec{Name: "Quiet", Type: hooks.PolicyValidate, Expression: "true"}, "message", "This field is required."},
		{hooks.PolicySpec{Name: "Empty", Type: hooks.PolicyDefault}, "defaults", "This field is required."},
		{hooks.PolicySpec{Name: strings.Repeat("n", 81), Type: hooks.PolicyValidate, Expression: "true", Message: "no"}, "name", "Use at most 80 characters."},
		{hooks.PolicySpec{Name: "Kinds", Type: hooks.PolicyValidate, Kinds: []string{"Web.Site"}, Expression: "true", Message: "no"}, "kinds[0]",
			"This value isn't valid: it isn't in the form this field takes."},
		{hooks.PolicySpec{Name: "Path", Type: hooks.PolicyDefault, Defaults: []hooks.FieldDefault{{Field: "a..b", Expression: "1"}}}, "defaults[0].field",
			"This value isn't valid: it isn't in the form this field takes."},
	} {
		raw, _ := json.Marshal(tc.spec)
		_, err := e.engine.Submit(ctx, changeset.Request{Actor: e.admin, DryRun: true, Changes: []changeset.Change{
			{Op: changeset.OpCreate, Kind: "core.policy", Scope: store.ServerScope(), Spec: raw},
		}})
		saved := i18n.From(err)
		_, err = e.rules.Test(ctx, e.admin, TestRequest{Rule: tc.spec, Owner: store.ServerScope(), Operation: "create", Kind: "web.site", Scope: store.ServerScope()})
		tried := i18n.From(err)
		for prefix, got := range map[string]*i18n.Error{"spec.": saved, "body.rule.": tried} {
			if got == nil || got.Code != i18n.ErrInvalid.Code || len(got.Fields) != 1 || got.Fields[0].Path != prefix+tc.path || got.Fields[0].Message != tc.message {
				t.Errorf("%s at %s%s: %+v", tc.spec.Name, prefix, tc.path, got)
			}
		}
	}
}

func TestValidateRules(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	acct := store.AccountScope(e.owner(t, accountKind))
	id := e.rule(t, "core.policy", store.ServerScope(), hooks.PolicySpec{
		Name: "No .invalid domains", Type: hooks.PolicyValidate, Enabled: true, Kinds: []string{"web.site"},
		Expression: `resource.kind == "web.site" && resource.spec.domains.exists(d, d.endsWith(".invalid"))`,
		Message:    "Domains ending in .invalid can't be used.",
	})
	_, err := e.submit(t, e.admin, site(acct, siteSpec{Domains: []string{"shop.invalid"}}))
	if !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "can't be used") || !strings.Contains(i18n.From(err).Message, "No .invalid domains") {
		t.Fatalf("refused: %v", err)
	}
	if cs, err := e.submit(t, e.admin, site(acct, siteSpec{Domains: []string{"shop.example"}})); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("allowed: %+v, %v", cs, err)
	}
	pol, _ := Policy.Get(ctx, e.store, id)
	if len(pol.Status.Points) != 1 || pol.Status.Points[0] != "web.site.*.validate" || pol.Status.Phase != store.PhaseReady {
		t.Errorf("status = %+v", pol.Status)
	}

	// Turning the rule off takes it out of the pipeline.
	pol.Spec.Enabled = false
	raw, _ := json.Marshal(pol.Spec)
	if _, err := e.submit(t, e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.policy", ID: id, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.submit(t, e.admin, site(acct, siteSpec{Domains: []string{"other.invalid"}})); err != nil {
		t.Errorf("a rule that is off refuses nothing: %v", err)
	}
	if owners := e.registry.Owners(); len(owners) != 0 {
		t.Errorf("hooks left = %v", owners)
	}
}

func TestRulesSeeTheRequestAndTheOldResource(t *testing.T) {
	e := newEnv(t)
	acct := store.AccountScope(e.owner(t, accountKind))
	e.rule(t, "core.policy", store.ServerScope(), hooks.PolicySpec{
		Name: "Domains only grow", Type: hooks.PolicyValidate, Enabled: true, Kinds: []string{"web.site"}, Operations: []string{"update"},
		Expression: `request.operation == "update" && size(resource.spec.domains) < size(oldResource.spec.domains) && request.actor.type == "user"`,
		Message:    "Domains can only be added.",
	})
	cs, err := e.submit(t, e.admin, site(acct, siteSpec{Domains: []string{"a.example", "b.example"}}))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(siteSpec{Domains: []string{"a.example"}})
	_, err = e.submit(t, e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "web.site", ID: cs.Changes[0].ID, Spec: raw})
	if !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("shrinking: %v", err)
	}
}

func TestDefaultRules(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	planID, acctID := store.NewID("plan"), store.NewID("acct")
	planRaw, _ := json.Marshal(planSpec{MaxWorkers: 3})
	acctRaw, _ := json.Marshal(accountSpec{Name: "shop", Plan: string(planID)})
	if _, err := e.submit(t, e.admin,
		changeset.Change{Op: changeset.OpCreate, Kind: "hosting.plan", ID: planID, Scope: store.ServerScope(), Spec: planRaw},
		changeset.Change{Op: changeset.OpCreate, Kind: "hosting.account", ID: acctID, Scope: store.ServerScope(), Spec: acctRaw},
	); err != nil {
		t.Fatal(err)
	}
	ruleID := e.rule(t, "core.policy", store.ServerScope(), hooks.PolicySpec{
		Name: "Security headers and plan limits", Type: hooks.PolicyDefault, Enabled: true, Kinds: []string{"web.site"},
		Defaults: []hooks.FieldDefault{
			{Field: "headers.xFrameOptions", Expression: `"DENY"`},
			{Field: "workers", Expression: `plan == null ? 2 : int(plan.spec.maxWorkers)`, Override: true},
		},
	})
	acct := store.AccountScope(acctID)
	cs, err := e.submit(t, e.admin, site(acct, siteSpec{Domains: []string{"shop.example"}, Headers: map[string]string{"xFrameOptions": "SAMEORIGIN"}, Workers: 10}))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := siteKind.Get(ctx, e.store, cs.Changes[0].ID)
	if got.Spec.Headers["xFrameOptions"] != "SAMEORIGIN" || got.Spec.Workers != 3 {
		t.Errorf("site = %+v", got.Spec)
	}
	if len(cs.Contributions) != 1 || cs.Contributions[0].Source != (changeset.Source{Type: "rule", Name: string(ruleID), Hook: "web.site.create.plan"}) {
		t.Errorf("contributions = %+v", cs.Contributions)
	}
	cs, err = e.submit(t, e.admin, site(acct, siteSpec{Domains: []string{"blog.example"}}))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = siteKind.Get(ctx, e.store, cs.Changes[0].ID)
	if got.Spec.Headers["xFrameOptions"] != "DENY" {
		t.Errorf("a missing field is filled in: %+v", got.Spec)
	}
}

func TestScopes(t *testing.T) {
	e := newEnv(t)
	reseller := e.owner(t, resellerKind)
	mine, theirs, other := e.owner(t, accountKind), e.owner(t, accountKind), e.owner(t, accountKind)
	e.owners[mine] = reseller
	e.owners[theirs] = reseller
	deny := func(name string) hooks.PolicySpec {
		return hooks.PolicySpec{Name: name, Type: hooks.PolicyValidate, Enabled: true, Kinds: []string{"web.site"}, Expression: "true", Message: name + " says no."}
	}
	e.rule(t, "core.account_policy", store.AccountScope(mine), deny("Account rule"))
	if _, err := e.submit(t, e.admin, site(store.AccountScope(theirs), siteSpec{Domains: []string{"a.example"}})); err != nil {
		t.Errorf("an account's rule doesn't reach another account: %v", err)
	}
	if _, err := e.submit(t, e.admin, site(store.AccountScope(mine), siteSpec{Domains: []string{"b.example"}})); !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("an account's rule applies to its account: %v", err)
	}
	e.rule(t, "core.reseller_policy", store.ResellerScope(reseller), deny("Reseller rule"))
	if _, err := e.submit(t, e.admin, site(store.AccountScope(theirs), siteSpec{Domains: []string{"c.example"}})); !errors.Is(err, hooks.ErrDenied) {
		t.Errorf("a reseller's rule applies to its customers: %v", err)
	}
	if _, err := e.submit(t, e.admin, site(store.AccountScope(other), siteSpec{Domains: []string{"d.example"}})); err != nil {
		t.Errorf("a reseller's rule doesn't reach other accounts: %v", err)
	}
}

func TestCostLimit(t *testing.T) {
	e := newEnv(t)
	e.rules.opts.CostLimit = 50
	e.rule(t, "core.policy", store.ServerScope(), hooks.PolicySpec{
		Name: "Expensive", Type: hooks.PolicyValidate, Enabled: true, Kinds: []string{"web.site"},
		Expression: `resource.spec.domains.all(d, resource.spec.domains.all(e, d.size() + e.size() > 0)) && false`,
		Message:    "never",
	})
	var domains []string
	for i := 0; i < 30; i++ {
		domains = append(domains, "a"+strings.Repeat("b", i)+".example")
	}
	// The evaluation stops at the limit and counts as a failure of the
	// rule's hook, which lets the change through.
	if _, err := e.submit(t, e.admin, site(store.AccountScope(e.owner(t, accountKind)), siteSpec{Domains: domains})); err != nil {
		t.Fatalf("a rule over its cost limit fails open: %v", err)
	}
	res, err := e.rules.Test(context.Background(), e.admin, TestRequest{
		Rule: hooks.PolicySpec{Name: "Expensive", Type: hooks.PolicyValidate, Kinds: []string{"web.site"},
			Expression: `resource.spec.domains.all(d, resource.spec.domains.all(e, d.size() + e.size() > 0))`, Message: "never"},
		Owner: store.ServerScope(), Operation: "create", Kind: "web.site", Scope: store.AccountScope(store.NewID("acct")),
		Spec: mustJSON(siteSpec{Domains: domains}),
	})
	if err != nil || !strings.Contains(res.Error, "cost") {
		t.Errorf("tester = %+v, %v", res, err)
	}
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func TestTester(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	acct := store.AccountScope(store.NewID("acct"))
	res, err := e.rules.Test(ctx, e.admin, TestRequest{
		Rule:  hooks.PolicySpec{Name: "No .invalid", Type: hooks.PolicyValidate, Expression: `resource.spec.domains.exists(d, d.endsWith(".invalid"))`, Message: "Not here."},
		Owner: store.ServerScope(), Operation: "create", Kind: "web.site", Scope: acct, Spec: mustJSON(siteSpec{Domains: []string{"x.invalid"}}),
	})
	if err != nil || !res.Applies || !res.Refused || res.Message != "Not here." || res.Cost == 0 || len(res.Points) != 1 || res.Points[0] != "*.*.*.validate" {
		t.Errorf("validate = %+v, %v", res, err)
	}
	res, err = e.rules.Test(ctx, e.admin, TestRequest{
		Rule: hooks.PolicySpec{Name: "Quota", Type: hooks.PolicyDefault, Kinds: []string{"mail.mailbox"},
			Defaults: []hooks.FieldDefault{{Field: "quotaBytes", Expression: "5 * 1024 * 1024 * 1024"}}},
		Owner: store.ServerScope(), Operation: "create", Kind: "mail.mailbox", Scope: acct, Spec: mustJSON(map[string]any{"address": "ada@example.com"}),
	})
	if err != nil || !res.Applies || len(res.Fields) != 1 || !res.Fields[0].Set || !strings.Contains(string(res.Spec), `"quotaBytes":5368709120`) {
		t.Errorf("default = %+v (%s), %v", res, res.Spec, err)
	}
	res, err = e.rules.Test(ctx, e.admin, TestRequest{
		Rule:  hooks.PolicySpec{Name: "Sites only", Type: hooks.PolicyValidate, Kinds: []string{"web.site"}, Expression: "true", Message: "no"},
		Owner: store.AccountScope(store.NewID("acct")), Operation: "create", Kind: "web.site", Scope: acct,
	})
	if err != nil || res.Applies {
		t.Errorf("another account's rule doesn't apply: %+v, %v", res, err)
	}
	res, err = e.rules.Test(ctx, e.admin, TestRequest{
		Rule:  hooks.PolicySpec{Name: "Missing field", Type: hooks.PolicyValidate, Expression: "resource.spec.nothing == 1", Message: "no"},
		Owner: store.ServerScope(), Operation: "create", Kind: "web.site", Scope: acct,
	})
	if err != nil || res.Error == "" {
		t.Errorf("an evaluation error is reported: %+v, %v", res, err)
	}
	if _, err := e.rules.Test(ctx, e.admin, TestRequest{
		Rule:  hooks.PolicySpec{Name: "Broken", Type: hooks.PolicyValidate, Expression: "(", Message: "no"},
		Owner: store.ServerScope(), Operation: "create", Kind: "web.site", Scope: acct,
	}); !errors.Is(err, i18n.ErrInvalid) || i18n.From(err).Fields[0].Path != "body.rule.expression" {
		t.Errorf("a rule that doesn't compile: %v", err)
	}
}

func TestLoadAttachesStoredRules(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.rule(t, "core.policy", store.ServerScope(), hooks.PolicySpec{Name: "On", Type: hooks.PolicyValidate, Enabled: true, Expression: "false", Message: "no"})
	e.rule(t, "core.policy", store.ServerScope(), hooks.PolicySpec{Name: "Off", Type: hooks.PolicyValidate, Expression: "false", Message: "no"})
	fresh := hooks.NewRegistry()
	for _, k := range []store.Kind{siteKind, Policy} {
		fresh.DeclareKind(k)
	}
	engine := NewEngine(Options{Registry: fresh})
	if err := engine.Load(ctx, e.store); err != nil {
		t.Fatal(err)
	}
	if owners := fresh.Owners(); len(owners) != 1 {
		t.Errorf("owners = %v", owners)
	}
}
