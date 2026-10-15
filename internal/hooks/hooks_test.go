package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
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
	if err := (&Runner{Registry: r}).Modify(ctx, "web.site.create.plan", cs); err != nil {
		t.Fatal(err)
	}
	if len(cs.Contributions) != 2 || cs.Contributions[0].Source.Name != "cdn-a" || len(cs.Effects) != 2 || cs.Effects[1].Contributor != "addon:cdn-b" {
		t.Errorf("contributions = %+v, effects = %+v", cs.Contributions, cs.Effects)
	}
	if len(cs.Conflicts) != 1 || cs.Conflicts[0].Path != "cdn" || len(cs.Conflicts[0].Sources) != 2 {
		t.Errorf("conflicts = %+v", cs.Conflicts)
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
		if err := runner.Modify(ctx, "web.site.create.plan", testChangeSet()); err != nil {
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
	if err := r.Register(Registration{Point: "web.site.create.applied", Kind: Observe, Owner: "addon:chat",
		OnObserve: func(ctx context.Context, cs *changeset.ChangeSet) { wg.Done() }}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Registration{Point: "system.caddy.config", Kind: System, Owner: "addon:cdn",
		OnConfig: func(ctx context.Context, p *ConfigPatch) error { p.Config["cdn"] = true; return nil }}); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Registry: r}
	runner.Applied(ctx, "web.site.create.applied", testChangeSet())
	wg.Wait()
	cfg, err := runner.PatchConfig(ctx, "caddy", "config", map[string]any{"listen": 443})
	if err != nil || cfg["cdn"] != true || cfg["listen"] != 443 {
		t.Errorf("patched config = %v, %v", cfg, err)
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

func TestPolicySpec(t *testing.T) {
	ok := PolicySpec{Name: "Quota", Type: PolicyDefault, Kinds: []string{"mail.mailbox"}, Defaults: []FieldDefault{{Field: "quotaBytes", Expression: "5 * 1024 * 1024 * 1024"}}}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	if err := (PolicySpec{Name: "Broken", Type: PolicyValidate}).Validate(); err == nil {
		t.Error("validate rules need an expression and a message")
	}
}
