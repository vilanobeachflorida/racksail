package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/inbox"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// The sample kind the API tests run against: a static site whose page the
// reconciler writes on the simulated server.
type siteSpec struct {
	Domains []string `json:"domains" minItems:"1" doc:"The site's domain names"`
	Content string   `json:"content" doc:"The page"`
	Note    string   `json:"note,omitempty" maxLength:"40"`
}

type siteStatus struct {
	store.Status
	Path string `json:"path,omitempty"`
}

type deployInput struct {
	Message string `json:"message" minLength:"1" doc:"Why"`
}

type pingOutput struct {
	Pong   bool   `json:"pong"`
	Domain string `json:"domain"`
}

var errDomainTaken = i18n.Define("apitest.domain_taken", http.StatusConflict, "{value} is already used by another site.")

var siteKind = store.Define[siteSpec, siteStatus](store.KindInfo{
	Name: "test.site", Prefix: "tsite", Scope: store.ScopeAccount, Title: "Site", Plural: "sites",
	Description: "A static site, for the API tests.",
	Indexes:     []store.Index{{Name: "domain", Path: "domains", Unique: true, Fold: true, Conflict: errDomainTaken}},
	Actions: []store.Action{
		{Name: "deploy", Title: "Deploy", Summary: "Deploy the site.", Description: "Writes a release marker.", Risk: store.RiskMedium, Input: deployInput{}},
		{Name: "ping", Summary: "Ping the site.", Risk: store.RiskLow, Output: pingOutput{}},
		{Name: "rebuild", Summary: "Rebuild the site.", Risk: store.RiskHigh, Long: true},
	},
	UI: store.UIHints{TitleField: "domains"},
})

const sitesDir = "/srv/racksail/sites"

func sitePath(id store.ID) string { return sitesDir + "/" + string(id) + ".html" }

type siteReconciler struct{}

func (siteReconciler) Plan(ctx context.Context, obj *store.Object[siteSpec, siteStatus], req reconcile.Request) (reconcile.Plan, error) {
	if obj.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: sitePath(obj.ID)})}}, nil
	}
	return reconcile.Plan{Steps: []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: sitesDir, Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: sitePath(obj.ID), Content: []byte(obj.Spec.Content), Mode: 0o644}),
	}}, nil
}

func (siteReconciler) Apply(ctx context.Context, obj *store.Object[siteSpec, siteStatus], plan reconcile.Plan, ex reconcile.Executor) (siteStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	if err == nil {
		st.Path = sitePath(obj.ID)
	}
	return st, err
}

func (siteReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	if req.Action != "deploy" {
		return reconcile.ActionPlan{}, nil
	}
	var in deployInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return reconcile.ActionPlan{}, err
	}
	return reconcile.ActionPlan{Plan: reconcile.Plan{Steps: []agent.Step{
		agent.WriteFile.Step(agent.WriteFileInput{Path: sitePath(req.Resource.ID) + ".release", Content: []byte(in.Message), Mode: 0o644}),
	}}}, nil
}

func (siteReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if req.Action != "ping" {
		return nil, nil
	}
	obj, err := siteKind.Decode(req.Resource)
	if err != nil {
		return nil, err
	}
	return pingOutput{Pong: true, Domain: obj.Spec.Domains[0]}, nil
}

// testAuth signs requests in by the X-Test-Actor header: admin, owner
// (of the account the stack makes), or stranger (owner of another one).
type testAuth struct {
	actors map[string]*auth.Actor
	// identity signs in everyone else, with real sessions and tokens.
	identity *identity.Service
}

func (a testAuth) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	actor, ok := a.actors[r.Header.Get("X-Test-Actor")]
	if !ok {
		if a.identity != nil {
			return a.identity.Authenticate(ctx, r)
		}
		return nil, nil
	}
	return actor.Clone(), nil
}

// stack is a panel built the way cmd/racksail builds one, around a SQLite
// database and a simulated server, with in-process test hooks.
type stack struct {
	t        *testing.T
	server   *Server
	http     *httptest.Server
	store    store.Store
	log      *events.Log
	jobs     *jobs.Manager
	machine  *sim.Machine
	kinds    *store.KindRegistry
	hooks    *hooks.Registry
	authz    *auth.RoleAuthorizer
	inbox    *inbox.Service
	identity *identity.Service
	owners   *account.Ownership
	account  store.ID
	// reseller owns the account the stack makes.
	reseller store.ID
	actors   map[string]*auth.Actor
}

// owners says the stack's account belongs to its reseller, and can list a
// reseller's accounts.
type owners map[store.ID]store.ID

func (o owners) ResellerOf(ctx context.Context, account store.ID) (store.ID, error) {
	return o[account], nil
}

func (o owners) AccountsOf(ctx context.Context, reseller store.ID) ([]store.ID, error) {
	var out []store.ID
	for account, r := range o {
		if r == reseller {
			out = append(out, account)
		}
	}
	return out, nil
}

func newStack(t *testing.T, limits RateLimit) *stack {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{siteKind, webhook.Kind, account.Kind, identity.PolicyKind, identity.ProviderKind, identity.UserKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.NewSQLite(ctx, db, kinds)
	if err != nil {
		t.Fatal(err)
	}
	log, err := events.NewSQL(ctx, db, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	q, err := jobs.NewSQL(ctx, db, jobs.ManagerOptions{Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	storage, err := changeset.NewSQLStorage(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	vault := secrets.NewMemory(box)
	machine := sim.New(sim.Options{})
	agentClient := agent.NewLocal(machine.Backend(), nil)
	// The receivers the tests run listen on the loopback address.
	loopback := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	dispatcher, err := webhook.NewSQL(ctx, db, webhook.Options{Store: st, Log: log, Secrets: vault, Logger: slog.New(slog.DiscardHandler), AllowNetworks: loopback})
	if err != nil {
		t.Fatal(err)
	}
	recs := reconcile.NewRegistry()
	for _, r := range []reconcile.Reconciler{
		reconcile.Typed(siteKind, siteReconciler{}), webhook.Reconciler(dispatcher), account.Reconciler(kinds),
		identity.PolicyReconciler(), identity.ProviderReconciler(),
	} {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	s := &stack{t: t, store: st, log: log, jobs: q, machine: machine, kinds: kinds, reseller: store.NewID("rsl")}
	s.account = s.newAccount(s.reseller)
	registry := hooks.NewRegistry()
	for _, k := range kinds.All() {
		registry.DeclareKind(k)
	}
	if err := registry.Register(hooks.Registration{
		Point: hooks.KindPoint("test.site", "create", changeset.StageValidate), Kind: hooks.Validate, Owner: "module:test",
		OnValidate: func(ctx context.Context, c *hooks.Context) error {
			var spec siteSpec
			if err := json.Unmarshal(c.Change.After, &spec); err != nil {
				return err
			}
			for _, d := range spec.Domains {
				if strings.HasSuffix(d, ".invalid") {
					return errDomainTaken.New("value", d)
				}
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	s.hooks = registry
	runner := &reconcile.Runner{Store: st, Agent: agentClient, Registry: recs, Secrets: vault, Events: log, Log: slog.New(slog.DiscardHandler)}
	s.owners = account.NewOwnership(st, owners{s.account: s.reseller})
	authz := auth.NewRoleAuthorizer(s.owners)
	s.authz = authz
	engine, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: q, Events: log, Audit: log, Secrets: vault, Storage: storage,
		Hooks: &hooks.Runner{Registry: registry, Events: log}, Authorizer: authz, Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	dismissals, err := inbox.NewSQL(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	s.inbox = inbox.New(ctx, inbox.Options{Sources: []inbox.Registration{
		{Name: "jobs", Source: inbox.FailedJobs{Queue: q, ChangeSets: engine.Get}},
		{Name: "resources", Source: inbox.Resources{Store: st, Kinds: kinds}},
		{Name: "alerts", Source: inbox.Alerts{Events: log}},
		{Name: "updates", Source: inbox.Updates{Agent: agentClient}, Keep: time.Hour},
	}, Dismissals: dismissals, Wait: 5 * time.Second, Log: slog.New(slog.DiscardHandler)})
	q.Start()
	go dispatcher.Run(ctx)
	s.actors = map[string]*auth.Actor{
		"admin": {Type: auth.ActorUser, ID: store.NewID("user"), Name: "admin", Source: auth.SourceUI,
			Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}},
		"owner": {Type: auth.ActorUser, ID: store.NewID("user"), Name: "owner", Source: auth.SourceCLI,
			Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(s.account)}}},
		"stranger": {Type: auth.ActorUser, ID: store.NewID("user"), Name: "stranger", Source: auth.SourceUI,
			Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(store.NewID("acct"))}}},
		"nobody": {Type: auth.ActorUser, ID: store.NewID("user"), Name: "nobody", Source: auth.SourceUI},
		"reseller": {Type: auth.ActorUser, ID: store.NewID("user"), Name: "reseller", Source: auth.SourceUI,
			Grants: []auth.Grant{{Role: auth.RoleReseller, Scope: store.ResellerScope(s.reseller)}}},
	}
	s.identity = identity.New(identity.Options{
		Store: identity.NewMemory(), Resources: st, Pipeline: engine, Refresh: runner.Enqueue, Secrets: vault, Events: log, Audit: log,
		Hooks: &hooks.Runner{Registry: registry, Events: log}, Authorizer: authz, Hostname: "localhost",
		Log: slog.New(slog.DiscardHandler),
	})
	if err := recs.Register(s.identity.UserReconciler()); err != nil {
		t.Fatal(err)
	}
	go runner.Run(ctx, time.Hour)
	s.server = New(Options{
		Auth: testAuth{actors: s.actors, identity: s.identity}, Modules: modules.NewRegistry(), Identity: s.identity, Accounts: s.owners,
		Services: &modules.Services{
			Store: st, Kinds: kinds, Pipeline: engine, Jobs: q, Events: log, Audit: log, Agent: agentClient, Secrets: vault, Authorizer: authz,
		},
		Timeline: log, Webhooks: dispatcher, Inbox: s.inbox, RateLimit: limits, Log: slog.New(slog.DiscardHandler),
	})
	s.http = httptest.NewServer(s.server.Handler())
	t.Cleanup(s.http.Close)
	return s
}

// newAccount puts an account straight in the store, a customer of reseller
// when it isn't empty, for resources to belong to, and returns its ID.
func (s *stack) newAccount(reseller store.ID) store.ID {
	s.t.Helper()
	id := store.NewID("acct")
	spec, _ := json.Marshal(account.AccountSpec{Slug: "a" + strings.ToLower(string(id[len(id)-8:])), Name: "Test", Reseller: reseller})
	ctx := context.Background()
	if err := s.store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: id, Kind: account.Kind.Name(), Scope: store.AccountScope(id)}, Spec: spec})
	}); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// call makes a request as an actor and decodes a JSON answer into out,
// when out isn't nil.
func (s *stack) call(actor, method, path string, body any, out any, header ...string) *http.Response {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		r = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, s.http.URL+path, r)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("X-Test-Actor", actor)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(strings.NewReader(string(raw)))
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatalf("%s %s: decoding %s: %v", method, path, raw, err)
		}
	}
	return resp
}

// bodyOf reads what call left of a response, and leaves it there to be read
// again.
func bodyOf(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(strings.NewReader(string(b)))
	return string(b)
}

// finish waits for a change set's job and returns the change set as it
// ended.
func (s *stack) finish(actor string, cs *changeset.ChangeSet) *changeset.ChangeSet {
	s.t.Helper()
	if cs.Job == "" {
		s.t.Fatalf("change set without a job: %+v", cs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.jobs.Wait(ctx, cs.Job); err != nil {
		s.t.Fatal(err)
	}
	var final changeset.ChangeSet
	if resp := s.call(actor, "GET", "/api/v1/changesets/"+string(cs.ID), nil, &final); resp.StatusCode != http.StatusOK {
		s.t.Fatalf("change set = %d %s", resp.StatusCode, bodyOf(resp))
	}
	return &final
}

func (s *stack) file(path string) string {
	data, err := s.machine.Backend().Files.ReadFile(context.Background(), path, 0)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *stack) problem(resp *http.Response) i18n.Code {
	var p struct {
		Code i18n.Code `json:"code"`
	}
	json.Unmarshal([]byte(bodyOf(resp)), &p)
	return p.Code
}

// readEvents reads Server-Sent Events from a stream until stop says so.
func readEvents(t *testing.T, resp *http.Response, stop func(event, id, data string) bool) {
	t.Helper()
	sc := bufio.NewScanner(resp.Body)
	var event, id, data string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && event != "":
				if stop(event, id, data) {
					return
				}
				event, id, data = "", "", ""
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream didn't deliver in time")
	}
}
