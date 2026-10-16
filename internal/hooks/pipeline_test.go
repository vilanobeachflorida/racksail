package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func TestPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, point Point
		match          bool
	}{
		{"web.site.*.validate", "web.site.create.validate", true},
		{"web.site.*.validate", "web.site.deploy.validate", true},
		{"web.site.*.validate", "web.site.create.plan", false},
		{"*.*.create.plan", "dns.zone.create.plan", true},
		{"*.*.create.plan", "core.boot", false},
		{"web.*.create.validate", "web.site.create.validate", true},
		{"web.*.create.validate", "mail.mailbox.create.validate", false},
		// A wildcard stays within its segment.
		{"web.*.validate", "web.site.create.validate", false},
		{"web.site.re*.validate", "web.site.restart.validate", true},
		{"web.site.create.validate", "web.site.create.validate", true},
	} {
		if got := MatchPoint(tc.pattern, tc.point); got != tc.match {
			t.Errorf("MatchPoint(%s, %s) = %v", tc.pattern, tc.point, got)
		}
	}
	if KindPattern("", "create", "plan") != "*.*.create.plan" || KindPattern("web.site", "", "validate") != "web.site.*.validate" {
		t.Error("kind patterns")
	}
	if k, op, st, ok := ParseKindPoint("core.module.turn_on.applied"); !ok || k != "core.module" || op != "turn_on" || st != "applied" {
		t.Errorf("parse = %s %s %s %v", k, op, st, ok)
	}
	if _, _, _, ok := ParseKindPoint("system.caddy.config"); ok {
		t.Error("a configuration point isn't a point of the change pipeline")
	}

	r := newRegistry()
	var order []string
	add := func(owner string, p Point, prio int) {
		if err := r.Register(Registration{Point: p, Kind: Validate, Owner: owner, Priority: prio,
			OnValidate: func(context.Context, *Context) error { order = append(order, owner); return nil }}); err != nil {
			t.Fatal(err)
		}
	}
	add("rule:everything", "*.*.*.validate", 5)
	add("addon:site-guard", "web.site.create.validate", 1)
	add("addon:deploys", "web.site.deploy.validate", 1)
	cs := testChangeSet()
	if err := (&Runner{Registry: r}).Validate(context.Background(), "web.site.create.validate", cs, &cs.Changes[0]); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "addon:site-guard,rule:everything" {
		t.Errorf("order = %v", order)
	}
	for _, bad := range []Registration{
		{Point: "mail.*.*.validate", Kind: Validate, Owner: "addon:x", OnValidate: func(context.Context, *Context) error { return nil }},
		{Point: "system.*.config", Kind: System, Owner: "addon:x", OnConfig: func(context.Context, *ConfigPatch) error { return nil }},
		{Point: "*", Kind: Observe, Owner: "addon:x", OnObserve: func(context.Context, *Context) {}},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("registration should fail: %s %s", bad.Kind, bad.Point)
		}
	}
	if got := r.Matching("web.site.*.applied"); len(got) != 4 {
		t.Errorf("matching = %v", got)
	}
	r.Unregister("rule:everything")
	if len(r.For("web.site.create.validate", Validate)) != 1 {
		t.Error("unregistering removes pattern hooks too")
	}
}

func TestOwnersAndOverride(t *testing.T) {
	r := newRegistry()
	ok := func(context.Context, *Context) error { return nil }
	if err := r.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "core:modules", OnValidate: ok}); err != nil {
		t.Errorf("the core registers hooks of its own: %v", err)
	}
	if err := r.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "rule:pol_x", Override: true, OnValidate: ok}); err == nil {
		t.Error("rules can't hold core.override")
	}
	if err := r.Register(Registration{Point: "web.site.create.validate", Kind: Validate, Owner: "core:", OnValidate: ok}); err == nil {
		t.Error("owners need a name")
	}
	if got := r.Owners(); len(got) != 1 || got[0] != "core:modules" {
		t.Errorf("owners = %v", got)
	}
}

func TestBreakerPersistsAndNotifies(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"memory", "sql"} {
		t.Run(kind, func(t *testing.T) {
			var st BreakerStore = NewMemoryBreakers()
			if kind == "sql" {
				db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				if st, err = NewSQLBreakers(ctx, db); err != nil {
					t.Fatal(err)
				}
			}
			r := newRegistry()
			if err := r.UseBreakerStore(ctx, st, nil); err != nil {
				t.Fatal(err)
			}
			if err := r.Register(Registration{Point: "web.site.*.plan", Kind: Modify, Owner: "addon:flaky",
				OnModify: func(context.Context, *Context) error { return errors.New("the backend is down") }}); err != nil {
				t.Fatal(err)
			}
			bus := events.NewMemory(10)
			sub := bus.Subscribe(events.Filter{Types: []string{"core.hook.disabled"}}, 2)
			defer sub.Close()
			runner := &Runner{Registry: r, Events: bus, Audit: bus}
			for range BreakerThreshold {
				if err := runner.Modify(ctx, "web.site.create.plan", testChangeSet(), []int{0}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case e := <-sub.C:
				var data HookDisabledData
				if err := json.Unmarshal(e.Data, &data); err != nil || data.Owner != "addon:flaky" || data.Point != "web.site.*.plan" || data.Failures != BreakerThreshold || data.Reason != "the backend is down" {
					t.Errorf("event data = %s, %v", e.Data, err)
				}
			case <-time.After(time.Second):
				t.Fatal("no event about the switched-off hook")
			}
			if audit, _, _ := bus.QueryAudit(ctx, events.Query{Types: []string{"core.hook.disable"}}); len(audit) != 1 {
				t.Errorf("audit = %+v", audit)
			}

			// A registry that starts over with the same store keeps the hook
			// off, and so does registering the hook again.
			again := newRegistry()
			if err := again.UseBreakerStore(ctx, st, nil); err != nil {
				t.Fatal(err)
			}
			if err := again.Register(Registration{Point: "web.site.*.plan", Kind: Modify, Owner: "addon:flaky", OnModify: func(context.Context, *Context) error { return nil }}); err != nil {
				t.Fatal(err)
			}
			if len(again.For("web.site.create.plan", Modify)) != 0 || len(again.Disabled()) != 1 {
				t.Error("a hook the breaker switched off stays off after a restart")
			}
			if err := again.Enable(ctx, "addon:flaky", "web.site.*.plan"); err != nil {
				t.Fatal(err)
			}
			if err := again.Enable(ctx, "addon:flaky", "web.site.*.plan"); !errors.Is(err, ErrNotDisabled) {
				t.Errorf("enabling twice: %v", err)
			}
			if len(again.For("web.site.create.plan", Modify)) != 1 {
				t.Error("an admin switches it back on")
			}
			if left, _ := st.Disabled(ctx); len(left) != 0 {
				t.Errorf("the store still has %+v", left)
			}
		})
	}
}

// caddyConfig declares a configuration point whose check refuses documents
// without a listen address and whose invariants protect the sandbox.
func caddyConfig(t *testing.T, r *Registry, checks *atomic.Int32) {
	t.Helper()
	if err := r.DeclareConfig(ConfigService{
		Point: "system.caddy.config", Description: "Caddy's configuration.",
		Check: func(ctx context.Context, doc map[string]any) error {
			checks.Add(1)
			if _, ok := doc["listen"]; !ok {
				return errors.New("caddy: no listen address")
			}
			return nil
		},
		Invariants: []Invariant{
			KeepTrue("sandbox.noNewPrivileges", "Processes can't gain privileges."),
			AtMost("limits.memoryMB", "The memory limit."),
			AtLeast("tls.minVersion", "The oldest TLS version."),
			KeepItems("sandbox.deny", "Addresses the service may not reach."),
			KeepValue("admin", "The admin endpoint."),
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigChecksInvariantsAndOverride(t *testing.T) {
	ctx := context.Background()
	base := func() map[string]any {
		return map[string]any{
			"listen":  ":443",
			"admin":   "off",
			"sandbox": map[string]any{"noNewPrivileges": true, "deny": []any{"10.0.0.0/8", "127.0.0.1"}},
			"limits":  map[string]any{"memoryMB": 256},
			"tls":     map[string]any{"minVersion": 1.2},
		}
	}
	patch := func(owner string, override bool, fn func(doc map[string]any)) Registration {
		return Registration{Point: "system.caddy.config", Kind: System, Owner: owner, Override: override,
			OnConfig: func(ctx context.Context, p *ConfigPatch) error { fn(p.Config); return nil }}
	}
	for name, tc := range map[string]struct {
		reg     Registration
		applied bool
		code    i18n.Code
	}{
		"stricter is fine": {reg: patch("addon:strict", false, func(d map[string]any) {
			d["limits"].(map[string]any)["memoryMB"] = 128
			d["tls"].(map[string]any)["minVersion"] = 1.3
			d["sandbox"].(map[string]any)["deny"] = []any{"10.0.0.0/8", "127.0.0.1", "192.168.0.0/16"}
		}), applied: true},
		"turning a protection off": {reg: patch("addon:loose", false, func(d map[string]any) {
			d["sandbox"].(map[string]any)["noNewPrivileges"] = "no"
		}), code: ErrInvariant.Code},
		"raising a limit": {reg: patch("addon:loose", false, func(d map[string]any) {
			d["limits"].(map[string]any)["memoryMB"] = 4096
		}), code: ErrInvariant.Code},
		"taking a limit away": {reg: patch("addon:loose", false, func(d map[string]any) {
			delete(d, "limits")
		}), code: ErrInvariant.Code},
		"lowering a minimum": {reg: patch("addon:loose", false, func(d map[string]any) {
			d["tls"].(map[string]any)["minVersion"] = 1.0
		}), code: ErrInvariant.Code},
		"dropping a denied address": {reg: patch("addon:loose", false, func(d map[string]any) {
			d["sandbox"].(map[string]any)["deny"] = []any{"10.0.0.0/8"}
		}), code: ErrInvariant.Code},
		"changing a protected value": {reg: patch("addon:loose", false, func(d map[string]any) {
			d["admin"] = "localhost:2019"
		}), code: ErrInvariant.Code},
		"a document the service refuses": {reg: patch("addon:broken", false, func(d map[string]any) {
			delete(d, "listen")
		}), code: ErrConfigCheck.Code},
		"loosening with core.override": {reg: patch("addon:trusted", true, func(d map[string]any) {
			d["limits"].(map[string]any)["memoryMB"] = 1024
		}), applied: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRegistry()
			var checks atomic.Int32
			caddyConfig(t, r, &checks)
			if err := r.Register(tc.reg); err != nil {
				t.Fatal(err)
			}
			audit := events.NewMemory(10)
			res, err := (&Runner{Registry: r, Audit: audit}).PatchConfig(ctx, "caddy", "config", base())
			if err != nil {
				t.Fatal(err)
			}
			if tc.applied {
				if len(res.Applied) != 1 || len(res.Refused) != 0 {
					t.Fatalf("result = %+v", res)
				}
				if checks.Load() != 1 {
					t.Error("every patched document is checked")
				}
				records, _, _ := audit.QueryAudit(ctx, events.Query{Types: []string{"core.override"}})
				if tc.reg.Override && (len(res.Applied[0].Loosened) != 1 || len(records) != 1 || records[0].Actor.Name != "trusted") {
					t.Errorf("an override is shown and recorded: %+v, %+v", res.Applied, records)
				}
				if !tc.reg.Override && len(records) != 0 {
					t.Errorf("records = %+v", records)
				}
				return
			}
			if len(res.Applied) != 0 || len(res.Refused) != 1 || res.Refused[0].Code != tc.code {
				t.Fatalf("result = %+v", res)
			}
			if fmt.Sprint(res.Config) != fmt.Sprint(base()) {
				t.Errorf("a refused patch is rolled back: %v", res.Config)
			}
			if r.Failures(tc.reg.Owner, "system.caddy.config") != 1 {
				t.Error("a refused patch counts as a failure of its hook")
			}
		})
	}

	// A blocking hook whose patch is refused stops the change.
	r := newRegistry()
	var checks atomic.Int32
	caddyConfig(t, r, &checks)
	blocking := patch("addon:strict", false, func(d map[string]any) { delete(d, "listen") })
	blocking.Failure = Block
	if err := r.Register(blocking); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Runner{Registry: r}).PatchConfig(ctx, "caddy", "config", base()); !errors.Is(err, ErrHookFailed) || !errors.Is(err, ErrConfigCheck) {
		t.Errorf("a blocking hook: %v", err)
	}

	// Effects label the file with every contributor.
	res := &PatchResult{Applied: []PatchContribution{{Owner: "addon:cdn", Changed: []string{"encode"}}}}
	if effs := res.Effects("/etc/racksail/generated/caddy.json"); len(effs) != 1 || effs[0].Contributor != "addon:cdn" || effs[0].Target != "/etc/racksail/generated/caddy.json" {
		t.Errorf("effects = %+v", effs)
	}
}

type failingChecker struct{ err error }

func (f failingChecker) Check(ctx context.Context, server store.ID, hc reconcile.HealthCheck) error {
	return f.err
}

type simExecutor struct{ client agent.Client }

func (e simExecutor) Run(ctx context.Context, steps []agent.Step) ([]agent.StepResult, error) {
	var out []agent.StepResult
	for _, s := range steps {
		raw, err := json.Marshal(s.Input)
		if err != nil {
			return out, err
		}
		if _, err := e.client.Call(ctx, s.Op, raw, agent.CallOptions{}); err != nil {
			return out, err
		}
	}
	return out, nil
}

func TestRolloutPutsThePreviousFileBack(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{})
	ex := simExecutor{client: agent.NewLocal(m.Backend(), nil)}
	path := "/etc/racksail/generated/caddy.json"
	write := func(content string) agent.Step {
		return agent.WriteFile.Step(agent.WriteFileInput{Path: path, Content: []byte(content), Mode: 0o644})
	}
	if _, err := ex.Run(ctx, []agent.Step{agent.EnsureDir.Step(agent.EnsureDirInput{Path: "/etc/racksail/generated", Mode: 0o755}), write("old")}); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		data, _ := m.Backend().Files.ReadFile(ctx, path, 0)
		return string(data)
	}
	ro := Rollout{Service: "Caddy", Write: write("new"), Restore: write("old"), Checks: []reconcile.HealthCheck{{Name: "caddy", Type: "unit", Target: "racksail-edge.service"}}}
	if err := ro.Apply(ctx, ex, failingChecker{}, ""); err != nil || read() != "new" {
		t.Fatalf("a rollout that works keeps the new file: %v, %q", err, read())
	}
	ro.Write = write("broken")
	ro.Restore = write("new")
	err := ro.Apply(ctx, ex, failingChecker{err: errors.New("the edge doesn't answer")}, "")
	if !errors.Is(err, ErrRolledBack) || read() != "new" {
		t.Errorf("a failed check puts the previous file back: %v, %q", err, read())
	}
	if len(ro.Steps()) != 1 {
		t.Errorf("steps = %+v", ro.Steps())
	}
}

func TestAppliedRecordsContributions(t *testing.T) {
	ctx := context.Background()
	audit := events.NewMemory(10)
	r := newRegistry()
	runner := &Runner{Registry: r, Audit: audit}
	cs := testChangeSet()
	cs.Title = "Create site"
	cs.Actor = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI}
	zone := store.NewID("zone")
	cs.Changes = append(cs.Changes, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "dns.zone", ID: zone,
		Source: changeset.Source{Type: "addon", Name: "dns-helper", Hook: "web.site.create.plan"}})
	cs.Effects = append(cs.Effects,
		system.Effect{Type: system.EffectService, Action: "reload", Target: "knot.service", Contributor: "addon:dns-helper"},
		system.Effect{Type: system.EffectFile, Action: "patch", Target: "/etc/racksail/generated/caddy.json", Contributor: "addon:cdn"})
	cs.Contributions = []changeset.Contribution{{
		Source: changeset.Source{Type: "addon", Name: "dns-helper", Hook: "web.site.create.plan"}, Summary: "addon:dns-helper changed the plan",
		Changes: []int{1}, Effects: []int{0},
	}}
	runner.Applied(ctx, "web.site.create.applied", cs, []int{0})
	runner.Applied(ctx, "dns.zone.create.applied", cs, []int{1})
	records, _, err := audit.QueryAudit(ctx, events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	byActor := map[string]events.AuditRecord{}
	for _, rec := range records {
		byActor[rec.Actor.Name] = rec
	}
	helper := byActor["dns-helper"]
	if helper.Actor.Type != auth.ActorAddon || helper.Actor.User != cs.Actor.ID || helper.Subject != zone || helper.ChangeSet != cs.ID || helper.Action != "web.site.create.plan" {
		t.Errorf("the add-on's contribution = %+v", helper)
	}
	if cdn := byActor["cdn"]; cdn.Action != "core.hook.contribute" || !strings.Contains(string(cdn.Details), "caddy.json") {
		t.Errorf("the configuration patch = %+v", cdn)
	}
}

func TestDescribe(t *testing.T) {
	r := newRegistry()
	var checks atomic.Int32
	caddyConfig(t, r, &checks)
	if err := r.Register(Registration{Point: "web.site.*.validate", Kind: Validate, Owner: "rule:pol_1", Label: "No .invalid domains",
		OnValidate: func(context.Context, *Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	points := r.Describe("web.site.create")
	if len(points) != 3 {
		t.Fatalf("points = %+v", points)
	}
	for _, p := range points {
		if p.Kind != "web.site" || p.Operation != "create" {
			t.Errorf("point = %+v", p)
		}
		if p.Stage == "validate" && (len(p.Hooks) != 1 || p.Hooks[0].Label != "No .invalid domains" || p.Hooks[0].Point != "web.site.*.validate") {
			t.Errorf("hooks at %s = %+v", p.Point, p.Hooks)
		}
	}
	caddy := r.Describe("system.caddy")
	if len(caddy) != 1 || !caddy[0].Config || !caddy[0].Checked || len(caddy[0].Invariants) != 5 {
		t.Errorf("configuration point = %+v", caddy)
	}
	if hooks := r.Hooks(); len(hooks) != 1 || hooks[0].Owner != "rule:pol_1" {
		t.Errorf("hooks = %+v", hooks)
	}
}
