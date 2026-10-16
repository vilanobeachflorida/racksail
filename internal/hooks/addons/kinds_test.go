package addons

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// kindsManifest is the site guard as an add-on that adds a kind of its
// own: rules, whose resources it plans and applies.
var kindsManifest = strings.Replace(strings.Replace(guardManifest,
	"hooks: [validate, modify, observe]", "hooks: [validate, modify, observe, system]", 1),
	"routes:", `kinds:
  - name: guard.rule
    prefix: grule
    scope: server
    title: Guard rule
    plural: rules
    description: A pattern the site guard watches for.
    schema:
      type: object
      additionalProperties: false
      required: [pattern]
      properties:
        pattern: {type: string, minLength: 2, description: What to watch for}
    status:
      type: object
      properties:
        watching: {type: string}
    actions:
      - name: purge
        summary: Forget what the rule caught
        risk: low
        input:
          type: object
          additionalProperties: false
          properties:
            older: {type: string, pattern: "^[0-9]+h$"}
routes:`, 1)

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

// waitFor waits for something the background work brings about.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// withKinds is a server admin's own site guard that adds rules.
func withKinds(t *testing.T, manifest string) AddonSpec {
	t.Helper()
	s := guard(true)
	man, err := hooks.ParseAddonManifest([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	s.Manifest, s.Approved, s.Catalog, s.Item = manifest, man.Permissions, "", ""
	return s
}

func TestKindManifests(t *testing.T) {
	if _, err := hooks.ParseAddonManifest([]byte(kindsManifest)); err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]string{
		"without the system level":  strings.Replace(kindsManifest, "hooks: [validate, modify, observe, system]", "hooks: [validate, modify, observe]", 1),
		"in the core's namespace":   strings.Replace(kindsManifest, "name: guard.rule", "name: core.rule", 1),
		"in two namespaces":         strings.Replace(kindsManifest, "routes:", "  - {name: other.thing, prefix: oth, scope: server, title: Thing, plural: things, description: A thing., schema: {type: object}}\nroutes:", 1),
		"with an action of no risk": strings.Replace(kindsManifest, "risk: low", "risk: none", 1),
		"for an account to install": kindsManifest,
	} {
		man, err := hooks.ParseAddonManifest([]byte(manifest))
		if name == "for an account to install" {
			if err != nil {
				t.Fatal(err)
			}
			err = man.AccountInstalled()
		}
		if err == nil {
			t.Errorf("a manifest whose kinds are %s is accepted", name)
		}
	}
	// A schema may only refer to itself.
	_, err := NewKind("site-guard", "Site guard", hooks.AddonKind{
		Name: "guard.rule", Prefix: "grule", Scope: "server", Title: "Guard rule", Plural: "rules", Description: "A rule.",
		Schema: map[string]any{"$ref": "file:///etc/shadow"},
	})
	if !errors.Is(err, ErrKindSchema) {
		t.Errorf("a schema that reaches for a file: %v", err)
	}
}

// An add-on that adds a kind: its resources go through the change
// pipeline, with hook points, validation against the kind's schema and a
// preview the add-on plans, and the add-on applies them, within what it may
// do on the server.
func TestAddonKinds(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "core.addon", Scope: store.ServerScope(), Spec: spec(t, withKinds(t, kindsManifest))})
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("installing: %v %+v", err, cs)
	}
	addonID := cs.Changes[0].ID
	k, ok := e.kinds.Get("guard.rule")
	if !ok {
		t.Fatal("the kind isn't registered")
	}
	if _, isSchema := k.(store.SchemaKind); !isSchema {
		t.Fatal("the kind isn't described by its schema")
	}
	for _, p := range []hooks.Point{"guard.rule.create.validate", "guard.rule.purge.applied", "guard.rule.delete.plan"} {
		if !e.registry.Known(p) {
			t.Errorf("%s isn't a hook point", p)
		}
	}

	// Its settings are checked against its schema.
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"x"}`)}); !errors.Is(err, i18n.ErrInvalid) || !strings.Contains(string(mustJSON(i18n.From(err))), "spec.pattern") {
		t.Errorf("a pattern too short: %v", err)
	}
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"wp-admin","extra":1}`)}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("a field the schema doesn't have: %v", err)
	}

	// The preview shows what the add-on planned, as its own.
	rule := changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"wp-admin"}`)}
	preview, err := e.engine.Submit(ctx, changeset.Request{Actor: e.admin, DryRun: true, Changes: []changeset.Change{rule}})
	if err != nil {
		t.Fatal(err)
	}
	var watched, wrote bool
	for _, eff := range preview.Effects {
		if eff.Action == "watch" && eff.Target == "wp-admin" && eff.Contributor == "addon:site-guard" {
			watched = true
		}
		if eff.Type == system.EffectFile && strings.HasPrefix(eff.Target, KindDir("site-guard")+"/") {
			wrote = true
		}
	}
	if !watched || !wrote {
		t.Errorf("the preview = %+v", preview.Effects)
	}
	cs, err = e.submit(e.admin, rule)
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("a rule: %v %+v", err, cs)
	}
	ruleID := cs.Changes[0].ID
	file := KindDir("site-guard") + "/" + string(ruleID) + ".conf"
	if got, err := e.machine.Backend().Files.ReadFile(ctx, file, 0); err != nil || string(got) != "wp-admin\n" {
		t.Errorf("the rule's file = %q, %v", got, err)
	}
	res, _ := e.store.Get(ctx, ruleID)
	var status map[string]any
	json.Unmarshal(res.Status, &status)
	if status["watching"] != "wp-admin" || status["phase"] != string(store.PhaseReady) {
		t.Errorf("the status = %s", res.Status)
	}

	// Its actions are checked against their input's schema, and answer
	// with what the add-on returns.
	purge := changeset.Change{Op: changeset.OpAction, Kind: "guard.rule", ID: ruleID, Action: "purge", Input: json.RawMessage(`{"older":"soon"}`)}
	if _, err := e.submit(e.admin, purge); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("an input its schema refuses: %v", err)
	}
	purge.Input = json.RawMessage(`{"older":"24h"}`)
	if cs, err := e.submit(e.admin, purge); err != nil || cs.State != changeset.StateApplied || string(cs.Changes[0].Output) != `{"purged":3}` {
		t.Errorf("a purge = %v %+v", err, cs)
	}

	// The add-on's failures fail the change, with what it said.
	cs, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"fail"}`)})
	if err != nil || cs.State != changeset.StateFailed || !strings.Contains(cs.Error, "refused the rule") {
		t.Errorf("a rule the add-on can't apply = %v %+v", err, cs)
	}
	e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "guard.rule", ID: cs.Changes[0].ID})

	// Its plans only touch its own directory.
	e.fake.mu.Lock()
	e.fake.stray = true
	e.fake.mu.Unlock()
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"etc"}`)}); !ErrKindStep.Is(err) {
		t.Errorf("a plan that writes in /etc/racksail: %v", err)
	}
	e.fake.mu.Lock()
	e.fake.stray = false
	e.fake.mu.Unlock()

	// Nobody else takes its namespace or its prefix.
	other := withKinds(t, strings.NewReplacer("id: site-guard", "id: copycat", "name: Site guard", "name: Copycat").Replace(kindsManifest))
	other.ID = "copycat"
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "core.addon", Scope: store.ServerScope(), Spec: spec(t, other)}); !ErrKindTaken.Is(err) {
		t.Errorf("another add-on adding guard.rule: %v", err)
	}
	web := withKinds(t, strings.NewReplacer("id: site-guard", "id: copycat", "name: guard.rule", "name: web.rule").Replace(kindsManifest))
	web.ID = "copycat"
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "core.addon", Scope: store.ServerScope(), Spec: spec(t, web)}); !ErrKindTaken.Is(err) {
		t.Errorf("an add-on adding kinds in the web module's namespace: %v", err)
	}

	// While it doesn't run, its kind's resources can't change; and it
	// can't go, or drop the kind, while they exist.
	off := withKinds(t, kindsManifest)
	off.Enabled = false
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: addonID, Spec: spec(t, off)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"while off"}`)}); !ErrKindOff.Is(err) {
		t.Errorf("a rule while the add-on is off: %v", err)
	}
	if _, ok := e.kinds.Get("guard.rule"); !ok {
		t.Error("an add-on that is off still has its kind, so its resources stay readable")
	}
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: addonID, Spec: spec(t, withKinds(t, kindsManifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "core.addon", ID: addonID}); !ErrKindInUse.Is(err) {
		t.Errorf("removing the add-on while a rule exists: %v", err)
	}
	dropped := withKinds(t, guardManifest)
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: addonID, Spec: spec(t, dropped)}); !ErrKindInUse.Is(err) {
		t.Errorf("a version without the kind while a rule exists: %v", err)
	}

	// Deleting the rule has the add-on undo it, and then the add-on can go,
	// with its kind.
	if cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "guard.rule", ID: ruleID}); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("deleting the rule: %v %+v", err, cs)
	}
	waitFor(t, "the rule is gone", func() bool {
		_, err := e.store.Get(ctx, ruleID)
		return store.ErrNotFound.Is(err)
	})
	if _, err := e.machine.Backend().Files.ReadFile(ctx, file, 0); err == nil {
		t.Error("the rule's file is still there")
	}
	e.fake.mu.Lock()
	var undone bool
	for _, c := range e.fake.kindCalls {
		undone = undone || (c.what == "apply" && c.call.Resource.ID == ruleID && c.call.Resource.Deleted)
	}
	e.fake.mu.Unlock()
	if !undone {
		t.Error("the add-on wasn't asked to undo the rule")
	}
	// Nor while a rule it failed to undo is still being deleted, which only
	// it can finish.
	cs, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"sticky"}`)})
	if err != nil {
		t.Fatal(err)
	}
	sticky := cs.Changes[0].ID
	e.fake.mu.Lock()
	e.fake.failDeletes = true
	e.fake.mu.Unlock()
	if cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "guard.rule", ID: sticky}); err != nil || cs.State != changeset.StateFailed {
		t.Fatalf("a delete the add-on can't undo = %v %+v", err, cs)
	}
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "core.addon", ID: addonID}); !ErrKindInUse.Is(err) {
		t.Errorf("removing the add-on while a rule is still being deleted: %v", err)
	}
	e.fake.mu.Lock()
	e.fake.failDeletes = false
	e.fake.mu.Unlock()
	if err := e.runner.Reconcile(ctx, sticky, nil); err != nil {
		t.Fatal(err)
	}
	// Deleting rules and the add-on in one change set works, rules first.
	cs, err = e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "guard.rule", Scope: store.ServerScope(), Spec: json.RawMessage(`{"pattern":"last"}`)})
	if err != nil {
		t.Fatal(err)
	}
	last := cs.Changes[0].ID
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "core.addon", ID: addonID}, changeset.Change{Op: changeset.OpDelete, Kind: "guard.rule", ID: last}); !ErrKindInUse.Is(err) {
		t.Errorf("the add-on before its last rule: %v", err)
	}
	if cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpDelete, Kind: "guard.rule", ID: last}, changeset.Change{Op: changeset.OpDelete, Kind: "core.addon", ID: addonID}); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("removing the last rule and the add-on: %v %+v", err, cs)
	}
	waitFor(t, "the add-on is gone", func() bool {
		_, ok := e.kinds.Get("guard.rule")
		return !ok && !e.registry.Known("guard.rule.create.validate")
	})
}
