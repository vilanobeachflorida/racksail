package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

type siteSpec struct {
	Domains []string `json:"domains"`
	CDN     string   `json:"cdn,omitempty"`
}

var siteKind = store.Define[siteSpec, store.Status](store.KindInfo{
	Name: "web.site", Prefix: "site", Scope: store.ScopeAccount, Title: "Site", Plural: "sites", Description: "A site.",
	Actions: []store.Action{{Name: "deploy", Summary: "Deploy.", Risk: store.RiskMedium}},
})

func newRegistry() *Registry {
	r := NewRegistry()
	r.DeclareKind(siteKind)
	r.Declare("system.caddy.config", "Caddy configuration.")
	return r
}

func TestPointsCoverEveryOperation(t *testing.T) {
	points := KindPoints(siteKind)
	if len(points) != 12 {
		t.Fatalf("points = %v", points)
	}
	r := newRegistry()
	for _, p := range []Point{"web.site.create.validate", "web.site.deploy.applied", "web.site.delete.plan", "core.boot", "system.caddy.config"} {
		if !r.Known(p) {
			t.Errorf("%s should be declared", p)
		}
	}
	if len(ModulePoints("web")) != 2 {
		t.Error("modules have on and off points")
	}
}

func TestRegistrationChecks(t *testing.T) {
	r := newRegistry()
	ok := Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "module:security", OnValidate: func(context.Context, *Context) error { return nil }}
	if err := r.Register(ok); err != nil {
		t.Fatal(err)
	}
	bad := []Registration{
		{Point: "web.site.create.validate", Kind: Validate, Owner: "nobody", OnValidate: ok.OnValidate},
		{Point: "web.site.create.validate", Kind: Validate, Owner: "module:x"},
		{Point: "web.site.create.validate", Kind: Validate, Owner: "module:x", Deadline: time.Minute, OnValidate: ok.OnValidate},
		{Point: "web.site.explode.validate", Kind: Validate, Owner: "module:x", OnValidate: ok.OnValidate},
		{Point: "web.site.create.plan", Kind: System, Owner: "module:x", OnConfig: func(context.Context, *ConfigPatch) error { return nil }},
	}
	for _, reg := range bad {
		if err := r.Register(reg); err == nil {
			t.Errorf("registration should fail: %+v", reg)
		}
	}
	sec := Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "module:security", Security: true, OnValidate: ok.OnValidate}
	if err := sec.check(); err != nil || sec.Failure != Block || sec.Deadline != DefaultDeadline {
		t.Errorf("security hooks block by default: %+v, %v", sec, err)
	}
}

func testChangeSet() *changeset.ChangeSet {
	return &changeset.ChangeSet{
		ID: store.NewID("chg"),
		Changes: []changeset.ResourceChange{{
			Op: changeset.OpCreate, Kind: "web.site", ID: store.NewID("site"),
			After: json.RawMessage(`{"domains":["a.test"]}`), Source: changeset.RequestSource,
		}},
		Effects: []system.Effect{},
	}
}

func TestValidateOrderDenialAndFailureModes(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	var order []string
	add := func(owner string, prio int, fn func() error) {
		if err := r.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: owner, Priority: prio, Deadline: 100 * time.Millisecond,
			OnValidate: func(ctx context.Context, c *Context) error { order = append(order, owner); return fn() }}); err != nil {
			t.Fatal(err)
		}
	}
	add("addon:b", 10, func() error { return nil })
	add("addon:a", 10, func() error { return nil })
	add("module:first", 1, func() error { return nil })
	runner := &Runner{Registry: r}
	cs := testChangeSet()
	if err := runner.Validate(ctx, "web.site.create.validate", cs, &cs.Changes[0]); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "module:first,addon:a,addon:b" {
		t.Errorf("order = %v", order)
	}

	r2 := newRegistry()
	deny := i18n.Define("hookstest.no_invalid", 403, "Domains ending in .invalid aren't allowed.")
	if err := r2.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "rule:no-invalid",
		OnValidate: func(context.Context, *Context) error { return deny.New() }}); err != nil {
		t.Fatal(err)
	}
	err := (&Runner{Registry: r2}).Validate(ctx, "web.site.create.validate", cs, &cs.Changes[0])
	if !errors.Is(err, ErrDenied) || !strings.Contains(i18n.From(err).Message, ".invalid") {
		t.Errorf("denial: %v", err)
	}

	r3 := newRegistry()
	for _, reg := range []Registration{
		{Point: "web.site.create.validate", Kind: Validate, Owner: "addon:slow-allow", Deadline: 20 * time.Millisecond,
			OnValidate: func(ctx context.Context, c *Context) error { <-ctx.Done(); return ctx.Err() }},
		{Point: "web.site.create.validate", Kind: Validate, Owner: "addon:crash", Failure: Allow,
			OnValidate: func(context.Context, *Context) error { panic("boom") }},
	} {
		if err := r3.Register(reg); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&Runner{Registry: r3}).Validate(ctx, "web.site.create.validate", cs, &cs.Changes[0]); err != nil {
		t.Errorf("failing hooks in allow mode let the change through: %v", err)
	}
	r4 := newRegistry()
	if err := r4.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "addon:slow-block", Security: true, Deadline: 20 * time.Millisecond,
		OnValidate: func(ctx context.Context, c *Context) error { <-ctx.Done(); return ctx.Err() }}); err != nil {
		t.Fatal(err)
	}
	if err := (&Runner{Registry: r4}).Validate(ctx, "web.site.create.validate", cs, &cs.Changes[0]); !errors.Is(err, ErrHookFailed) {
		t.Errorf("a security hook that times out blocks: %v", err)
	}
}

func TestModifyContributionsAndConflicts(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	setCDN := func(value string) func(context.Context, *Context) error {
		return func(ctx context.Context, c *Context) error {
			c.ChangeSet.Changes[0].After = json.RawMessage(`{"domains":["a.test"],"cdn":"` + value + `"}`)
			c.ChangeSet.Effects = append(c.ChangeSet.Effects, system.Effect{Type: system.EffectFile, Action: "write", Target: "/etc/racksail/generated/" + value})
			return nil
		}
	}
	for _, owner := range []string{"addon:cdn-a", "addon:cdn-b"} {
		if err := r.Register(Registration{Point: "web.site.create.plan", Kind: Modify, Owner: owner, OnModify: setCDN(strings.TrimPrefix(owner, "addon:"))}); err != nil {
			t.Fatal(err)
		}
	}
	cs := testChangeSet()
	if err := (&Runner{Registry: r}).Modify(ctx, "web.site.create.plan", cs, []int{0}); err != nil {
		t.Fatal(err)
	}
	if len(cs.Contributions) != 2 || cs.Contributions[0].Source.Name != "cdn-a" || len(cs.Effects) != 2 || cs.Effects[1].Contributor != "addon:cdn-b" {
		t.Errorf("contributions = %+v, effects = %+v", cs.Contributions, cs.Effects)
	}
	if len(cs.Conflicts) != 1 || cs.Conflicts[0].Path != "cdn" || len(cs.Conflicts[0].Sources) != 2 {
		t.Errorf("conflicts = %+v", cs.Conflicts)
	}
}

// A modify hook may rewrite the specs of the changes its point is about and
// add changes and effects. Anything else makes it fail, and its failure mode
// decides what happens; either way none of its work stays.
func TestModifyHooksCantOverstep(t *testing.T) {
	ctx := context.Background()
	twoChanges := func() *changeset.ChangeSet {
		cs := testChangeSet()
		cs.Changes = append(cs.Changes, changeset.ResourceChange{
			Op: changeset.OpUpdate, Kind: "dns.zone", ID: store.NewID("zone"), Before: json.RawMessage(`{"name":"a.test"}`),
			After: json.RawMessage(`{"name":"a.test"}`), Source: changeset.RequestSource,
		})
		cs.Effects = append(cs.Effects, system.Effect{Type: system.EffectFile, Action: "write", Target: "/etc/racksail/generated/a.conf"})
		cs.Risk = store.RiskLow
		return cs
	}
	for name, edit := range map[string]func(c *Context){
		"operation":      func(c *Context) { c.ChangeSet.Changes[0].Op = changeset.OpDelete },
		"resource":       func(c *Context) { c.ChangeSet.Changes[0].ID = store.NewID("site") },
		"scope":          func(c *Context) { c.ChangeSet.Changes[0].Scope = store.AccountScope(store.NewID("acct")) },
		"labels":         func(c *Context) { c.ChangeSet.Changes[0].Labels = map[string]string{"env": "prod"} },
		"previous spec":  func(c *Context) { c.ChangeSet.Changes[0].Before = json.RawMessage(`{"domains":[]}`) },
		"source":         func(c *Context) { c.ChangeSet.Changes[0].Source = changeset.Source{Type: "module", Name: "web"} },
		"other change":   func(c *Context) { c.ChangeSet.Changes[1].After = json.RawMessage(`{"name":"b.test"}`) },
		"removed change": func(c *Context) { c.ChangeSet.Changes = c.ChangeSet.Changes[:1] },
		"planned effect": func(c *Context) { c.ChangeSet.Effects[0].Target = "/etc/racksail/generated/b.conf" },
		"risk":           func(c *Context) { c.ChangeSet.Risk = store.RiskRead },
		"contribution": func(c *Context) {
			c.ChangeSet.Contributions = append(c.ChangeSet.Contributions, changeset.Contribution{Summary: "nothing to see"})
		},
		"title":          func(c *Context) { c.ChangeSet.Title = "Something else" },
		"spec, not JSON": func(c *Context) { c.ChangeSet.Changes[0].After = json.RawMessage(`{"domains":`) },
		"action steps": func(c *Context) {
			c.ChangeSet.Changes[0].Steps = []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: "/srv/racksail/accounts", Recursive: true})}
		},
		"previous labels": func(c *Context) { c.ChangeSet.Changes[0].LabelsBefore = map[string]string{"a": "b"} },
	} {
		for _, mode := range []FailureMode{Allow, Block} {
			r := newRegistry()
			if err := r.Register(Registration{Point: "web.site.create.plan", Kind: Modify, Owner: "addon:sneaky", Failure: mode,
				OnModify: func(ctx context.Context, c *Context) error {
					c.ChangeSet.Changes[0].After = json.RawMessage(`{"domains":["a.test"],"cdn":"on"}`)
					edit(c)
					return nil
				}}); err != nil {
				t.Fatal(err)
			}
			cs := twoChanges()
			want, _ := json.Marshal(cs)
			err := (&Runner{Registry: r}).Modify(ctx, "web.site.create.plan", cs, []int{0})
			if mode == Block && (!errors.Is(err, ErrHookFailed) || !errors.Is(err, ErrOverstep)) {
				t.Errorf("%s, blocking: %v", name, err)
			}
			if mode == Allow && err != nil {
				t.Errorf("%s, allowing: %v", name, err)
			}
			if got, _ := json.Marshal(cs); string(got) != string(want) {
				t.Errorf("%s, %s: the hook's work was kept: %s", name, mode, got)
			}
		}
	}
}

func TestModifyHooksAreLabelled(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	zone := store.NewID("zone")
	if err := r.Register(Registration{Point: "web.site.create.plan", Kind: Modify, Owner: "addon:dns-helper",
		OnModify: func(ctx context.Context, c *Context) error {
			c.ChangeSet.Changes = append(c.ChangeSet.Changes, changeset.ResourceChange{
				Op: changeset.OpCreate, Kind: "dns.zone", ID: zone, After: json.RawMessage(`{"name":"a.test"}`),
				// Claiming to be the request doesn't make it so.
				Source: changeset.RequestSource,
			})
			c.ChangeSet.Effects = append(c.ChangeSet.Effects, system.Effect{Type: system.EffectService, Action: "reload", Target: "knot.service", Contributor: "module:dns"})
			return nil
		}}); err != nil {
		t.Fatal(err)
	}
	cs := testChangeSet()
	if err := (&Runner{Registry: r}).Modify(ctx, "web.site.create.plan", cs, []int{0}); err != nil {
		t.Fatal(err)
	}
	if len(cs.Changes) != 2 || cs.Changes[1].Source != (changeset.Source{Type: "addon", Name: "dns-helper", Hook: "web.site.create.plan"}) {
		t.Errorf("added change = %+v", cs.Changes[1:])
	}
	if len(cs.Effects) != 1 || cs.Effects[0].Contributor != "addon:dns-helper" {
		t.Errorf("added effect = %+v", cs.Effects)
	}
	if len(cs.Contributions) != 1 || cs.Contributions[0].Changes[0] != 1 || cs.Contributions[0].Effects[0] != 0 {
		t.Errorf("contributions = %+v", cs.Contributions)
	}
}

// The case that matters: a modify hook can't turn an update the actor may
// make into a delete they may not.
func TestModifyHooksCantChangeWhatIsAuthorized(t *testing.T) {
	ctx := context.Background()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(siteKind); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(kinds)
	acct := store.NewID("acct")
	site := &store.Resource{Meta: store.Meta{Kind: "web.site", Scope: store.AccountScope(acct)}, Spec: json.RawMessage(`{"domains":["a.test"]}`)}
	if err := st.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, site) }); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []FailureMode{Allow, Block} {
		r := newRegistry()
		if err := r.Register(Registration{Point: "web.site.update.plan", Kind: Modify, Owner: "addon:wrecker", Failure: mode,
			OnModify: func(ctx context.Context, c *Context) error {
				c.ChangeSet.Changes[0].Op = changeset.OpDelete
				return nil
			}}); err != nil {
			t.Fatal(err)
		}
		queue := jobs.NewMemory(ctx, jobs.MemoryOptions{})
		engine, err := changeset.NewEngine(changeset.Options{Store: st, Kinds: kinds, Jobs: queue, Hooks: &Runner{Registry: r}, Authorizer: auth.NewRoleAuthorizer(nil)})
		if err != nil {
			t.Fatal(err)
		}
		editor := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{
			Role: auth.RoleCollaborator, Scope: store.AccountScope(acct), Permissions: []auth.Permission{"web.site.update", "web.site.read"},
		}}}
		cs, err := engine.Submit(ctx, changeset.Request{Actor: editor, Changes: []changeset.Change{{
			Op: changeset.OpUpdate, Kind: "web.site", ID: site.ID, Spec: json.RawMessage(`{"domains":["a.test","b.test"]}`),
		}}})
		if mode == Block {
			if !errors.Is(err, ErrHookFailed) {
				t.Errorf("a blocking hook that oversteps stops the change: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queue.Wait(ctx, cs.Job); err != nil {
			t.Fatal(err)
		}
		if cs.Changes[0].Op != changeset.OpUpdate || len(cs.Contributions) != 0 {
			t.Errorf("the change set is still the update that was asked for: %+v", cs.Changes[0])
		}
		if got, err := st.Get(ctx, site.ID); err != nil || got.Deleted() || !strings.Contains(string(got.Spec), "b.test") {
			t.Errorf("the site was updated, not deleted: %+v, %v", got, err)
		}
	}
}

func TestValidateHooksGetCopies(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	if err := r.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "addon:meddler",
		OnValidate: func(ctx context.Context, c *Context) error {
			c.Change.After = json.RawMessage(`{"domains":["evil.test"]}`)
			c.ChangeSet.Risk = store.RiskRead
			c.ChangeSet.Effects = append(c.ChangeSet.Effects, system.Effect{Type: system.EffectFile})
			c.Actor.Grants = append(c.Actor.Grants, auth.Grant{Role: auth.RoleAdmin, Scope: store.ServerScope()})
			return nil
		}}); err != nil {
		t.Fatal(err)
	}
	cs := testChangeSet()
	cs.Risk = store.RiskHigh
	cs.Actor = &auth.Actor{Type: auth.ActorUser}
	want, _ := json.Marshal(cs)
	if err := (&Runner{Registry: r}).Validate(ctx, "web.site.create.validate", cs, &cs.Changes[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(cs); string(got) != string(want) || len(cs.Actor.Grants) != 0 {
		t.Errorf("a validate hook changed the change set: %s", got)
	}
}

func TestCircuitBreaker(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	bus := events.NewMemory(10)
	sub := bus.Subscribe(events.Filter{Types: []string{"core.hook.*"}}, 4)
	defer sub.Close()
	if err := r.Register(Registration{Point: "web.site.create.plan", Kind: Modify, Owner: "addon:flaky",
		OnModify: func(context.Context, *Context) error { return errors.New("down") }}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Registry: r, Events: bus}
	for i := 0; i < BreakerThreshold; i++ {
		if err := runner.Modify(ctx, "web.site.create.plan", testChangeSet(), []int{0}); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.For("web.site.create.plan", Modify)) != 0 {
		t.Error("the hook should be switched off")
	}
	select {
	case e := <-sub.C:
		if e.Type != "core.hook.disabled" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Error("no event about the switched-off hook")
	}
	r.Reset("addon:flaky", "web.site.create.plan")
	if len(r.For("web.site.create.plan", Modify)) != 1 {
		t.Error("reset should switch the hook back on")
	}
}

func TestAppliedAndConfigPatches(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	var wg sync.WaitGroup
	wg.Add(1)
	var seen []int
	if err := r.Register(Registration{Point: "web.site.create.applied", Kind: Observe, Owner: "addon:chat",
		OnObserve: func(ctx context.Context, c *Context) { seen = c.Changes; wg.Done() }}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Registration{Point: "system.caddy.config", Kind: System, Owner: "addon:cdn",
		OnConfig: func(ctx context.Context, p *ConfigPatch) error { p.Config["cdn"] = true; return nil }}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Registry: r}
	runner.Applied(ctx, "web.site.create.applied", testChangeSet(), []int{0})
	wg.Wait()
	if len(seen) != 1 || seen[0] != 0 {
		t.Errorf("observers learn which changes the point is about: %v", seen)
	}
	res, err := runner.PatchConfig(ctx, "caddy", "config", map[string]any{"listen": 443})
	if err != nil || res.Config["cdn"] != true || res.Config["listen"] != 443 {
		t.Errorf("patched config = %v, %v", res, err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Owner != "addon:cdn" || res.Applied[0].Changed[0] != "cdn" {
		t.Errorf("contributions = %+v", res.Applied)
	}

	// A hook that runs out of time leaves nothing behind, even though it
	// goes on changing its copy.
	slow := newRegistry()
	release := make(chan struct{})
	finished := make(chan struct{})
	if err := slow.Register(Registration{Point: "system.caddy.config", Kind: System, Owner: "addon:slow", Deadline: 20 * time.Millisecond,
		OnConfig: func(ctx context.Context, p *ConfigPatch) error {
			defer close(finished)
			<-release
			p.Config["late"] = true
			p.Config["routes"].([]any)[0] = "changed"
			return nil
		}}); err != nil {
		t.Fatal(err)
	}
	original := map[string]any{"routes": []any{"site"}}
	res, err = (&Runner{Registry: slow}).PatchConfig(ctx, "caddy", "config", original)
	close(release)
	<-finished
	if err != nil || res.Config["late"] != nil || res.Config["routes"].([]any)[0] != "site" || original["routes"].([]any)[0] != "site" {
		t.Errorf("after a hook timed out = %v, %v (original %v)", res, err, original)
	}
	if len(res.Refused) != 1 || res.Refused[0].Owner != "addon:slow" {
		t.Errorf("refused = %+v", res.Refused)
	}
}

func TestCheckAndNotify(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	type login struct{ User string }
	if err := r.Register(Registration{Point: AuthLoginValidate, Kind: Validate, Owner: "rule:office-hours",
		OnValidate: func(ctx context.Context, c *Context) error {
			if c.Data.(login).User == "night-owl" {
				return i18n.ErrForbidden.New("action", "sign in at night")
			}
			return nil
		}}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Registry: r}
	if err := runner.Check(ctx, AuthLoginValidate, nil, login{User: "early-bird"}); err != nil {
		t.Errorf("allowed sign-in: %v", err)
	}
	if err := runner.Check(ctx, AuthLoginValidate, nil, login{User: "night-owl"}); !errors.Is(err, ErrDenied) {
		t.Errorf("refused sign-in: %v", err)
	}

	got := make(chan any, 1)
	if err := r.Register(Registration{Point: CoreBoot, Kind: Observe, Owner: "module:monitoring",
		OnObserve: func(ctx context.Context, c *Context) { got <- c.Data }}); err != nil {
		t.Fatal(err)
	}
	runner.Notify(ctx, CoreBoot, nil, "started")
	select {
	case d := <-got:
		if d != "started" {
			t.Errorf("data = %v", d)
		}
	case <-time.After(time.Second):
		t.Error("the observer wasn't notified")
	}
}

type greeter interface{ Greet() string }

type english struct{ name string }

func (e english) Greet() string { return "Hello, " + e.name }

func TestProviders(t *testing.T) {
	ctx := context.Background()
	r := newRegistry()
	r.Declare("provide.greeting", "Greetings.")
	hello := &Provider{Name: "english", Title: "English", Settings: struct{ Name string }{},
		Open: func(ctx context.Context, settings json.RawMessage) (any, error) {
			var s struct{ Name string }
			if err := json.Unmarshal(settings, &s); err != nil {
				return nil, err
			}
			return english{s.Name}, nil
		}}
	if err := r.Register(Registration{Point: "provide.greeting", Kind: Provide, Owner: "addon:greetings", Provider: hello}); err != nil {
		t.Fatal(err)
	}
	g, err := Open[greeter](ctx, r, "provide.greeting", "english", json.RawMessage(`{"Name":"Ada"}`))
	if err != nil || g.Greet() != "Hello, Ada" {
		t.Errorf("open = %v, %v", g, err)
	}
	if _, err := Open[greeter](ctx, r, "provide.greeting", "klingon", nil); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown provider: %v", err)
	}
	if _, err := Open[interface{ Shout() }](ctx, r, "provide.greeting", "english", json.RawMessage(`{}`)); err == nil {
		t.Error("a provider that doesn't implement the capability is an error")
	}
	for _, bad := range []Registration{
		{Point: "provide.greeting", Kind: Provide, Owner: "module:other", Provider: hello},
		{Point: "web.site.create.validate", Kind: Provide, Owner: "module:x", Provider: &Provider{Name: "x", Title: "X", Open: hello.Open}},
		{Point: "provide.greeting", Kind: Provide, Owner: "module:x", Provider: &Provider{Name: "Bad Name", Title: "X", Open: hello.Open}},
		{Point: "provide.greeting", Kind: Provide, Owner: "module:x"},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("registration should fail: %+v", bad)
		}
	}
	if len(r.Providers(ProvideDNS)) != 0 || !IsCapability(AuthMFA) || IsCapability(CoreBoot) || Capabilities["auth-provider"] != AuthProvider {
		t.Error("capability points")
	}
	r.Unregister("addon:greetings")
	if _, ok := r.Provider("provide.greeting", "english"); ok {
		t.Error("removing an add-on removes its providers")
	}
}

func TestExtensions(t *testing.T) {
	r := newRegistry()
	tab := Registration{Point: ExtendTab, Kind: Extend, Owner: "addon:security", Extension: &Extension{UI: &UIExtension{Type: "tab", Label: "Security", Kind: "web.site", Path: "/tab"}}}
	if err := r.Register(tab); err != nil {
		t.Fatal(err)
	}
	routes := Registration{Point: ExtendRoutes, Kind: Extend, Owner: "addon:security", Extension: &Extension{
		Routes:  []AddonRoute{{Method: "GET", Path: "/scans", Summary: "List scans"}},
		Handler: http.NotFoundHandler(),
	}}
	if err := r.Register(routes); err != nil {
		t.Fatal(err)
	}
	if len(r.Extensions(ExtendTab)) != 1 || len(r.Extensions(ExtendRoutes)) != 1 {
		t.Error("extensions are listed by point")
	}
	for _, bad := range []Registration{
		{Point: ExtendTab, Kind: Extend, Owner: "module:web", Extension: tab.Extension},
		{Point: ExtendNav, Kind: Extend, Owner: "addon:x", Extension: tab.Extension},
		{Point: ExtendRoutes, Kind: Extend, Owner: "addon:x", Extension: &Extension{Routes: routes.Extension.Routes}},
		{Point: ExtendCard, Kind: Extend, Owner: "addon:x", Extension: &Extension{UI: &UIExtension{Type: "card"}}},
		{Point: ExtendCard, Kind: Extend, Owner: "addon:x"},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("registration should fail: %+v", bad)
		}
	}
	if p, ok := ExtendPoint("column"); !ok || p != ExtendColumn {
		t.Errorf("column point = %s", p)
	}
}

func TestAddonManifest(t *testing.T) {
	manifest := []byte(`
format: 1
id: site-guard
name: Site guard
version: 0.3.0
license: Apache-2.0
source: https://example.com/site-guard
author: Example
description: Blocks deploys that contain known web shells.
apiVersions: [v1]
hooks:
  - {point: web.site.deploy.validate, kind: validate, security: true, deadline: 5s}
permissions:
  hooks: [validate]
  read: [{kind: web.site, fields: [domains]}]
`)
	m, err := ParseAddonManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "site-guard" || len(m.Hooks) != 1 {
		t.Errorf("manifest = %+v", m)
	}
	if d, _ := m.Hooks[0].DeadlineDuration(); d != 5*time.Second {
		t.Errorf("deadline = %s", d)
	}
	if err := m.AccountInstalled(); err != nil {
		t.Errorf("a validate-only add-on can be installed by an account: %v", err)
	}
	noPerm := strings.Replace(string(manifest), "hooks: [validate]", "hooks: [observe]", 1)
	if _, err := ParseAddonManifest([]byte(noPerm)); err == nil {
		t.Error("hooks need the matching permission")
	}
	oldAPI := strings.Replace(string(manifest), "apiVersions: [v1]", "apiVersions: [v9]", 1)
	if _, err := ParseAddonManifest([]byte(oldAPI)); err == nil {
		t.Error("unsupported API versions must be refused")
	}
	m.Permissions.Hooks = append(m.Permissions.Hooks, System)
	if err := m.AccountInstalled(); err == nil {
		t.Error("account add-ons can't hold system hooks")
	}
}

// Tabs and columns only show on the screens of the kind they name, and the
// panel asks a column's values of the add-on's own route; an entry that
// can't show is refused when the add-on is installed, not left out quietly.
func TestAddonManifestUIEntries(t *testing.T) {
	base := `
format: 1
id: site-guard
name: Site guard
version: 0.3.0
license: Apache-2.0
source: https://example.com/site-guard
author: Example
description: Blocks deploys that contain known web shells.
apiVersions: [v1]
permissions:
  read: [{kind: web.site, fields: [domains]}]
routes:
  - {method: GET, path: /blocked, summary: Visitors blocked per site}
  - {method: POST, path: /unblock, summary: Let a visitor in again}
`
	for _, tc := range []struct {
		name, ui string
		refused  string
	}{
		{"pages", "  - {type: nav, label: Guard, path: /overview}\n  - {type: card, label: Blocked, path: card}", ""},
		{"a tab of a kind", "  - {type: tab, label: Guard, kind: web.site, path: /tab}", ""},
		{"a tab of no kind", "  - {type: tab, label: Guard, path: /tab}", `the tab "Guard" needs the kind`},
		{"a column from a GET route", "  - {type: column, label: Blocked, kind: web.site, path: blocked}", ""},
		{"a column of no kind", "  - {type: column, label: Blocked, path: /blocked}", `the column "Blocked" needs the kind`},
		{"a column without a route", "  - {type: column, label: Blocked, kind: web.site, path: /elsewhere}", `the column "Blocked" needs the path`},
		{"a column from a POST route", "  - {type: column, label: Unblock, kind: web.site, path: /unblock}", `the column "Unblock" needs the path`},
		{"a column without a path", "  - {type: column, label: Blocked, kind: web.site}", `the column "Blocked" needs the path`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseAddonManifest([]byte(base + "ui:\n" + tc.ui + "\n"))
			switch {
			case tc.refused == "" && err != nil:
				t.Fatal(err)
			case tc.refused != "" && err == nil:
				t.Fatalf("accepted %+v", m.UI)
			case tc.refused != "" && !strings.Contains(err.Error(), tc.refused):
				t.Fatalf("refused with %q, want %q", err, tc.refused)
			}
		})
	}
	// The panel serves the add-on's screens, and the frames it shows them
	// in, under its paths' /ui/ and /frames/, so routes stay out of them.
	for _, path := range []string{"/ui/report", "'/frames/{token}'", "/ui"} {
		manifest := strings.Replace(base, "path: /unblock", "path: "+path, 1)
		if _, err := ParseAddonManifest([]byte(manifest)); err == nil || !strings.Contains(err.Error(), "where the panel serves the add-on's screens") {
			t.Errorf("a route at %s: %v", path, err)
		}
	}
	if _, err := ParseAddonManifest([]byte(strings.Replace(base, "path: /unblock", "path: /uis/report", 1))); err != nil {
		t.Errorf("a route whose path only starts like one of them: %v", err)
	}
}

func TestPolicySpec(t *testing.T) {
	ok := PolicySpec{Name: "Quota", Type: PolicyDefault, Kinds: []string{"mail.mailbox"}, Defaults: []FieldDefault{{Field: "quotaBytes", Expression: "5 * 1024 * 1024 * 1024"}}}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	if err := (PolicySpec{Name: "Broken", Type: PolicyValidate}).Validate(); err == nil {
		t.Error("validate rules need an expression and a message")
	}
}

func TestProtocolDocument(t *testing.T) {
	raw, err := ProtocolDocument()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
		Comps   struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Paths["/health"]["get"] == nil || doc.Paths["/hooks/{point}"]["post"] == nil {
		t.Errorf("paths = %v", doc.Paths)
	}
	for _, name := range []string{"HookCall", "HookReply", "ChangeSet", "ConfigPatch", "Problem"} {
		if doc.Comps.Schemas[name] == nil {
			t.Errorf("schema %s is missing", name)
		}
	}
}
