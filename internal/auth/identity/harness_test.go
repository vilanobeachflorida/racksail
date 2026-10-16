package identity

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// clock is a time the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// resellerSpec is what the tests' resellers keep.
type resellerSpec struct {
	Name string `json:"name"`
}

// resellerKind stands for the kind the module that keeps resellers defines,
// for the users who belong to a reseller: resources only belong to
// resellers that exist.
var resellerKind = store.Define[resellerSpec, store.Status](store.KindInfo{
	Name: "test.reseller", Prefix: "rsl", Scope: store.ScopeReseller, Title: "Reseller", Plural: "resellers",
	Description: "A reseller, for the tests of users who belong to one.",
})

// harness is the identity service as the panel runs it: users are
// core.user resources changed through the change pipeline, whose apply
// jobs run their reconciler, and credentials are in a store of their own.
type harness struct {
	t         *testing.T
	ctx       context.Context
	svc       *Service
	store     *Memory
	resources store.Store
	pipeline  *changeset.Engine
	runner    *reconcile.Runner
	jobs      *jobs.Manager
	log       *events.Log
	hooks     *hooks.Registry
	owners    *account.Ownership
	clock     *clock
	vault     secrets.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	discard := slog.New(slog.DiscardHandler)
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{account.Kind, PolicyKind, ProviderKind, UserKind, resellerKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	resources := store.NewMemory(kinds)
	owners := account.NewOwnership(resources, nil)
	authz := auth.NewRoleAuthorizer(owners)
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	vault := secrets.NewMemory(box)
	log := events.NewMemory(1000)
	reg := hooks.NewRegistry()
	for _, k := range kinds.All() {
		reg.DeclareKind(k)
	}
	runHooks := &hooks.Runner{Registry: reg, Events: log}
	q := jobs.NewMemory(ctx, jobs.ManagerOptions{Log: discard})
	machine := sim.New(sim.Options{})
	recs := reconcile.NewRegistry()
	runner := &reconcile.Runner{Store: resources, Agent: agent.NewLocal(machine.Backend(), nil), Registry: recs, Secrets: vault, Events: log, Log: discard}
	engine, err := changeset.NewEngine(changeset.Options{
		Store: resources, Kinds: kinds, Runner: runner, Jobs: q, Events: log, Audit: log, Secrets: vault,
		Hooks: runHooks, Authorizer: authz, Log: discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The clock starts now, as the stores' own records of when they first
	// kept something for a user do.
	c := &clock{t: time.Now().UTC().Truncate(time.Second)}
	st := NewMemory()
	svc := New(Options{
		Store: st, Resources: resources, Pipeline: engine, Refresh: runner.Enqueue, Secrets: vault, Events: log, Audit: log,
		Hooks: runHooks, Authorizer: authz, Hostname: "panel.example.test", Log: discard, Now: c.now,
	})
	for _, r := range []reconcile.Reconciler{account.Reconciler(kinds), PolicyReconciler(), ProviderReconciler(), svc.UserReconciler()} {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	q.Start()
	return &harness{
		t: t, ctx: ctx, svc: svc, store: st, resources: resources, pipeline: engine, runner: runner, jobs: q, log: log,
		hooks: reg, owners: owners, clock: c, vault: vault,
	}
}

var client = auth.Client{IP: "198.51.100.7", UserAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"}

const password = "tangerine kettle orbit"

// root is who the tests add users as: the panel itself, with every grant
// the users the tests add get.
func root() *auth.Actor {
	a := auth.System("tests")
	a.Grants = []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope(), Permissions: []auth.Permission{auth.PermImpersonate, auth.PermSecretsRead}}}
	return a
}

// submit sends a request through the change pipeline and waits for its
// job, so what applying it does is done.
func (h *harness) submit(actor *auth.Actor, title string, changes ...changeset.Change) (*changeset.ChangeSet, error) {
	h.t.Helper()
	cs, err := h.pipeline.Submit(h.ctx, changeset.Request{Actor: actor, Title: title, Changes: changes})
	if err != nil {
		return cs, err
	}
	h.settle(cs)
	return cs, nil
}

// submitRequest sends a request the service made, as the actor.
func (h *harness) submitRequest(actor *auth.Actor, req changeset.Request) (*changeset.ChangeSet, error) {
	h.t.Helper()
	return h.submit(actor, req.Title, req.Changes...)
}

// settle waits for a change set's job to end.
func (h *harness) settle(cs *changeset.ChangeSet) {
	h.t.Helper()
	if cs == nil || cs.DryRun || cs.Job == "" {
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	if _, err := h.jobs.Wait(ctx, cs.Job); err != nil {
		h.t.Fatal(err)
	}
}

func specJSON(t *testing.T, spec UserSpec) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// create adds a user through the change pipeline as actor, after putting
// the account or reseller they belong to in the store when it isn't there.
func (h *harness) create(actor *auth.Actor, home store.Scope, spec UserSpec) (store.ID, error) {
	h.t.Helper()
	h.owner(home)
	id := store.NewID(PrefixUser)
	_, err := h.submit(actor, "Add "+spec.Username, changeset.Change{Op: changeset.OpCreate, Kind: KindUser, ID: id, Scope: home, Spec: specJSON(h.t, spec)})
	return id, err
}

// update changes a user's settings through the change pipeline as actor,
// from what they are now.
func (h *harness) update(actor *auth.Actor, id store.ID, change func(*UserSpec)) (*changeset.ChangeSet, error) {
	h.t.Helper()
	obj, err := UserKind.Get(h.ctx, h.resources, id)
	if err != nil {
		return nil, err
	}
	spec := obj.Spec
	change(&spec)
	return h.submit(actor, "Change "+spec.Username,
		changeset.Change{Op: changeset.OpUpdate, Kind: KindUser, ID: id, Spec: specJSON(h.t, spec), Revision: obj.Revision})
}

// remove deletes a user through the change pipeline as actor.
func (h *harness) remove(actor *auth.Actor, id store.ID) error {
	h.t.Helper()
	_, err := h.submit(actor, "Remove a user", changeset.Change{Op: changeset.OpDelete, Kind: KindUser, ID: id})
	return err
}

// user adds a user with the tests' password.
func (h *harness) user(name string, home store.Scope, grants ...auth.Grant) *User {
	h.t.Helper()
	id, err := h.create(root(), home, UserSpec{Username: name, Name: strings.ToUpper(name[:1]) + name[1:], Grants: grants})
	if err != nil {
		h.t.Fatalf("adding %s: %v", name, err)
	}
	hash, err := auth.HashPassword(h.ctx, password)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.SetPassword(h.ctx, id, hash, false, h.clock.now()); err != nil {
		h.t.Fatal(err)
	}
	return h.get(id)
}

func (h *harness) admin(name string) *User {
	return h.user(name, store.ServerScope(), auth.Grant{Role: auth.RoleAdmin, Scope: store.ServerScope(), Permissions: []auth.Permission{auth.PermImpersonate}})
}

// get reads a user as the service sees them.
func (h *harness) get(id store.ID) *User {
	h.t.Helper()
	u, err := h.svc.User(h.ctx, id)
	if err != nil {
		h.t.Fatalf("reading %s: %v", id, err)
	}
	return u
}

// status reconciles a user and returns their status.
func (h *harness) status(id store.ID) UserStatus {
	h.t.Helper()
	if err := h.runner.Reconcile(h.ctx, id, nil); err != nil {
		h.t.Fatal(err)
	}
	obj, err := UserKind.Get(h.ctx, h.resources, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return obj.Status
}

// owner puts the account or reseller a scope names straight in the resource
// store, unless it is there already.
func (h *harness) owner(scope store.Scope) {
	h.t.Helper()
	if scope.Level != store.ScopeAccount && scope.Level != store.ScopeReseller {
		return
	}
	if _, err := h.resources.Get(h.ctx, scope.ID); err == nil {
		return
	}
	kind, spec := resellerKind.Name(), json.RawMessage(`{"name":"Reseller"}`)
	if scope.Level == store.ScopeAccount {
		kind = account.Kind.Name()
		spec, _ = json.Marshal(account.AccountSpec{Slug: "a" + strings.ToLower(string(scope.ID[len(scope.ID)-8:])), Name: "Account"})
	}
	r := &store.Resource{Meta: store.Meta{ID: scope.ID, Kind: kind, Scope: scope}, Spec: spec}
	if err := h.resources.Write(h.ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(h.ctx, r) }); err != nil {
		h.t.Fatal(err)
	}
}

// createAccount puts an account straight in the resource store.
func (h *harness) createAccount(slug string, reseller store.ID) store.ID {
	h.t.Helper()
	id := store.NewID("acct")
	spec, _ := json.Marshal(account.AccountSpec{Slug: slug, Name: slug, Reseller: reseller})
	r := &store.Resource{Meta: store.Meta{ID: id, Kind: account.Kind.Name(), Scope: store.AccountScope(id)}, Spec: spec}
	if err := h.resources.Write(h.ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(h.ctx, r) }); err != nil {
		h.t.Fatal(err)
	}
	return id
}

// signIn signs in with the password and returns the session's actor.
func (h *harness) signIn(name string) (*auth.Actor, string) {
	h.t.Helper()
	res, err := h.svc.SignIn(h.ctx, SignInInput{Username: name, Password: password}, "", client)
	if err != nil {
		h.t.Fatalf("signing in as %s: %v", name, err)
	}
	if res.Issued == nil {
		h.t.Fatalf("signing in as %s needs a second step: %v", name, res.Methods)
	}
	return h.actor(res.Issued.Secret), res.Issued.Secret
}

// actor authenticates a request carrying the session cookie.
func (h *harness) actor(secret string) *auth.Actor {
	h.t.Helper()
	a, err := h.authenticate(secret)
	if err != nil || a == nil {
		h.t.Fatalf("authenticating = %v, %v", a, err)
	}
	return a
}

func (h *harness) authenticate(secret string) (*auth.Actor, error) {
	a, _, err := h.authenticateWith(secret)
	return a, err
}

// authenticateWith authenticates a request carrying the session cookie and
// returns the new secret the session was given, if it was.
func (h *harness) authenticateWith(secret string) (*auth.Actor, *Issued, error) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: secret})
	ctx, reissued := WithReissue(auth.WithClient(h.ctx, client))
	a, err := h.svc.Authenticate(ctx, r)
	return a, reissued.Issued, err
}

func (h *harness) bearer(secret string, ip string) (*auth.Actor, error) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("User-Agent", "racksail/1.0.0")
	ctx := auth.WithClient(h.ctx, auth.Client{IP: ip})
	return h.svc.Authenticate(ctx, r)
}

func (h *harness) audit(action string) []events.AuditRecord {
	h.t.Helper()
	records, _, err := h.log.QueryAudit(h.ctx, events.Query{Types: []string{action}, Limit: 1000})
	if err != nil {
		h.t.Fatal(err)
	}
	return records
}

func (h *harness) eventTypes() map[string]int {
	list, _, _ := h.log.QueryEvents(h.ctx, events.Query{Limit: 1000})
	out := map[string]int{}
	for _, e := range list {
		out[e.Type]++
	}
	return out
}

// provide registers a provider the way an add-on's would be.
func (h *harness) provide(point hooks.Point, name string, open func(settings json.RawMessage) (any, error)) {
	h.t.Helper()
	err := h.hooks.Register(hooks.Registration{
		Point: point, Kind: hooks.Provide, Owner: "addon:" + name,
		Provider: &hooks.Provider{Name: name, Title: name, Open: func(ctx context.Context, s json.RawMessage) (any, error) { return open(s) }},
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// connect connects a sign-in provider.
func (h *harness) connect(spec ProviderSpec) store.ID {
	h.t.Helper()
	raw, _ := json.Marshal(spec)
	r := &store.Resource{Meta: store.Meta{ID: store.NewID("signprov"), Kind: ProviderKind.Name(), Scope: store.ServerScope()}, Spec: raw}
	if err := h.resources.Write(h.ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(h.ctx, r) }); err != nil {
		h.t.Fatal(err)
	}
	return r.ID
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("waited too long")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
