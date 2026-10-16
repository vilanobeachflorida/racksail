package inbox

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

type noteSpec struct {
	Name string `json:"name"`
}

type noteStatus struct {
	store.Status
}

var (
	noteKind = store.Define[noteSpec, noteStatus](store.KindInfo{
		Name: "inboxtest.note", Prefix: "itnote", Scope: store.ScopeAccount, Title: "Note", Plural: "notes",
		Description: "A note, for the inbox tests.", UI: store.UIHints{TitleField: "name"},
	})
	settingKind = store.Define[noteSpec, noteStatus](store.KindInfo{
		Name: "core.inboxtest", Prefix: "itset", Scope: store.ScopeServer, Title: "Setting", Plural: "settings",
		Description: "A setting of the core, which backups don't suggest themselves for.",
	})
)

func kindsForTest(t *testing.T) *store.KindRegistry {
	t.Helper()
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{noteKind, settingKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	return kinds
}

// putNote stores a note with the given phase in its status.
func putNote(t *testing.T, st store.Store, scope store.Scope, name string, phase store.Phase, message string) store.ID {
	t.Helper()
	ctx := t.Context()
	r := &store.Resource{Meta: store.Meta{Kind: noteKind.Name(), Scope: scope}, Spec: json.RawMessage(`{"name":"` + name + `"}`)}
	if err := st.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	status := store.Status{Phase: phase, Message: message, ObservedGeneration: 1}
	status.SetCondition(store.Condition{Type: "Ready", Status: store.ConditionFalse}, time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
	if err := store.SetStatus(ctx, st, r.ID, status); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestFailedJobs(t *testing.T) {
	ctx := t.Context()
	q := jobs.NewMemory(ctx, jobs.ManagerOptions{Log: quiet})
	fail := true
	if err := q.Handle(jobs.Type{Name: "inboxtest.work", Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		if fail {
			return nil, i18n.ErrUnavailable.New("what", "The mirror")
		}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	q.Start()
	acct := store.AccountScope(store.NewID("acct"))
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "owner", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: acct}}}
	run := func(opts jobs.Options) *jobs.Job {
		t.Helper()
		j, err := q.Enqueue(ctx, "inboxtest.work", nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		j, err = q.Wait(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	mine := run(jobs.Options{Title: "Install PHP", Actor: owner, Scope: acct})
	theirs := run(jobs.Options{Title: "Install Node", Actor: alice, Scope: store.AccountScope(store.NewID("acct"))})
	undone := run(jobs.Options{Title: "Change a site", Actor: owner, Scope: acct, ChangeSet: store.NewID("chg")})
	stillFailed := run(jobs.Options{Title: "Create a site", Actor: owner, Scope: acct, ChangeSet: store.NewID("chg")})
	changeSets := map[store.ID]*changeset.ChangeSet{
		undone.ChangeSet:      {ID: undone.ChangeSet, State: changeset.StateUndone},
		stillFailed.ChangeSet: {ID: stillFailed.ChangeSet, State: changeset.StateFailed, Changes: []changeset.ResourceChange{{Op: changeset.OpCreate}}},
	}
	src := FailedJobs{Queue: q, ChangeSets: func(ctx context.Context, id store.ID) (*changeset.ChangeSet, error) {
		if cs, ok := changeSets[id]; ok {
			return cs, nil
		}
		return nil, i18n.ErrNotFound.New("what", string(id))
	}}
	now := time.Now()
	items, err := src.Items(ctx, Query{Actor: owner, Scopes: []store.Scope{acct}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(items), []string{"job:" + string(stillFailed.ID), "job:" + string(mine.ID)}; !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	it := items[0]
	if it.Code != "job_failed" || it.Params["title"] != "Create a site" || it.Message != "The mirror isn't available right now. Try again in a moment." ||
		it.Access.Job == nil || it.Version != string(stillFailed.ID) {
		t.Errorf("item = %+v", it)
	}
	var types []ActionType
	for _, a := range it.Actions {
		types = append(types, a.Type)
	}
	if !slices.Equal(types, []ActionType{ActionRetryJob, ActionUndoChangeSet, ActionOpenJob}) {
		t.Errorf("actions = %v", types)
	}
	all, _ := src.Items(ctx, Query{All: true, Now: now})
	if !slices.Contains(ids(all), "job:"+string(theirs.ID)) {
		t.Errorf("everything = %v", ids(all))
	}
	// A job that ran again, or failed long ago, needs nothing more.
	fail = false
	if _, err := q.Retry(ctx, mine.ID, jobs.RetryOptions{Actor: owner}); err != nil {
		t.Fatal(err)
	}
	items, _ = src.Items(ctx, Query{All: true, Now: now})
	if slices.Contains(ids(items), "job:"+string(mine.ID)) {
		t.Errorf("a retried job still shows: %v", ids(items))
	}
	if items, _ := src.Items(ctx, Query{All: true, Now: now.Add(8 * 24 * time.Hour)}); len(items) != 0 {
		t.Errorf("old failures = %v", ids(items))
	}
}

func TestResources(t *testing.T) {
	kinds := kindsForTest(t)
	st := store.NewMemory(kinds)
	acct, other := store.AccountScope(store.NewID("acct")), store.AccountScope(store.NewID("acct"))
	failed := putNote(t, st, acct, "broken", store.PhaseFailed, "The disk is full.")
	degraded := putNote(t, st, acct, "slow", store.PhaseDegraded, "One of two workers runs.")
	putNote(t, st, acct, "fine", store.PhaseReady, "")
	elsewhere := putNote(t, st, other, "theirs", store.PhaseFailed, "Gone.")
	src := Resources{Store: st, Kinds: kinds}
	items, err := src.Items(t.Context(), Query{Scopes: []store.Scope{acct}, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if len(items) != 2 {
		t.Fatalf("items = %v", ids(items))
	}
	f := byID["resource:"+string(failed)]
	if f.Severity != events.SeverityError || f.Code != "resource_failed" || f.Title != "Note broken isn't working" || f.Message != "The disk is full." ||
		f.Access.Resource == nil || f.Access.Resource.ID != failed || !f.Time.Equal(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("failed = %+v", f)
	}
	if d := byID["resource:"+string(degraded)]; d.Severity != events.SeverityWarning || d.Code != "resource_degraded" || d.Actions[0].Type != ActionOpenResource {
		t.Errorf("degraded = %+v", d)
	}
	all, _ := src.Items(t.Context(), Query{All: true})
	if !slices.Contains(ids(all), "resource:"+string(elsewhere)) || len(all) != 3 {
		t.Errorf("everything = %v", ids(all))
	}
	// Someone granted single resources sees only those.
	collaborator := &auth.Actor{Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: other, Object: elsewhere}}}
	if items, _ := src.Items(t.Context(), Query{Actor: collaborator}); !slices.Equal(ids(items), []string{"resource:" + string(elsewhere)}) {
		t.Errorf("collaborator = %v", ids(items))
	}
	if items, _ := src.Items(t.Context(), Query{Actor: bob}); len(items) != 0 {
		t.Errorf("without grants = %v", ids(items))
	}
	// The version changes with what is wrong.
	before := f.Version
	if err := store.SetStatus(t.Context(), st, failed, store.Status{Phase: store.PhaseFailed, Message: "The disk is still full.", ObservedGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	items, _ = src.Items(t.Context(), Query{All: true})
	for _, it := range items {
		if it.Subject == failed && it.Version == before {
			t.Error("a new reason kept the version")
		}
	}
}

func TestAlerts(t *testing.T) {
	log := events.NewMemory(100)
	ctx := t.Context()
	acct := store.AccountScope(store.NewID("acct"))
	site := store.NewID("site")
	publish := func(e events.Event) {
		t.Helper()
		if err := log.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	corrected, _ := json.Marshal(events.Drift{Source: "resync", Corrected: true})
	open, _ := json.Marshal(events.Drift{Source: "agent", Corrected: false})
	publish(events.Event{Type: "web.site.deploy_failed", Severity: events.SeverityWarning, Subject: site, Kind: "web.site", Scope: acct, Message: "The deploy failed."})
	publish(events.Event{Type: "web.site.deploy_failed", Severity: events.SeverityError, Subject: site, Kind: "web.site", Scope: acct, Message: "The deploy failed again."})
	publish(events.Event{Type: "web.site.failed", Severity: events.SeverityError, Subject: site, Kind: "web.site", Scope: acct, Message: "Applying failed."})
	publish(events.Event{Type: "web.site.drifted", Severity: events.SeverityWarning, Subject: site, Kind: "web.site", Scope: acct, Message: "Put back.", Data: corrected})
	publish(events.Event{Type: "web.site.drifted", Severity: events.SeverityWarning, Subject: store.NewID("site"), Kind: "web.site", Scope: acct, Message: "Changed.", Data: open})
	publish(events.Event{Type: events.ChangeSetFailed.Name, Severity: events.SeverityError, Scope: acct, Message: "Couldn't apply."})
	publish(events.Event{Type: events.HookDisabled.Name, Severity: events.SeverityWarning, Scope: store.ServerScope(), Message: "A hook was switched off."})
	publish(events.Event{Type: "web.site.created", Severity: events.SeverityInfo, Scope: acct, Message: "Created."})

	src := Alerts{Events: log}
	items, err := src.Items(ctx, Query{All: true, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: the switched-off hook, the drift nothing put back yet,
	// then the deploys.
	if len(items) != 3 || items[0].ID != "alert:"+events.HookDisabled.Name || items[2].ID != "alert:web.site.deploy_failed:"+string(site) {
		t.Fatalf("items = %v", ids(items))
	}
	deploy := items[2]
	if deploy.Count != 2 || deploy.Severity != events.SeverityError || deploy.Title != "The deploy failed again." || deploy.Event == nil ||
		deploy.Version != string(deploy.Event.ID) || deploy.Access.Permission != PermEventRead || deploy.Access.Scope != acct {
		t.Errorf("deploy = %+v", deploy)
	}
	if items, _ := src.Items(ctx, Query{Scopes: []store.Scope{acct}, Now: time.Now()}); len(items) != 2 {
		t.Errorf("an account's = %v", ids(items))
	}
	if items, _ := src.Items(ctx, Query{All: true, Now: time.Now().Add(96 * time.Hour)}); len(items) != 0 {
		t.Errorf("old alerts = %v", ids(items))
	}
}

func TestUpdates(t *testing.T) {
	m := sim.New(sim.Options{})
	client := agent.NewLocal(m.Backend(), quiet)
	src := Updates{Agent: client}
	ctx := t.Context()
	items, err := src.Items(ctx, Query{All: true, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("a fresh server = %+v", items)
	}
	m.OfferUpgrade("sudo", "1.9.17-1")
	m.OfferUpgrade("linux-image-amd64", "6.12.57-1")
	items, err = src.Items(ctx, Query{All: true, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(items); !slices.Equal(got, []string{"updates:packages", "updates:kernel"}) {
		t.Fatalf("items = %v", got)
	}
	pkgs, kernel := items[0], items[1]
	if pkgs.Params["count"] != "1" || pkgs.Title != "A package update is available" || pkgs.Access.Permission != PermServerRead || pkgs.Actions[0].Target != "install/updates" {
		t.Errorf("packages = %+v", pkgs)
	}
	if kernel.Params["version"] != "6.12.57-1" || kernel.Code != "kernel_available" {
		t.Errorf("kernel = %+v", kernel)
	}
	before := pkgs.Version
	m.OfferUpgrade("sudo", "1.9.17-2")
	items, _ = src.Items(ctx, Query{All: true, Now: time.Now()})
	if items[0].Version == before {
		t.Error("a newer package kept the version")
	}
	// Installing the kernel leaves a restart to do.
	if _, err := agent.UpgradePackages.Call(ctx, client, agent.UpgradeInput{Full: true}, agent.CallOptions{}); err != nil {
		t.Fatal(err)
	}
	items, err = src.Items(ctx, Query{All: true, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(items), []string{"updates:restart"}) || items[0].Code != "restart_needed" || items[0].Severity != events.SeverityWarning {
		t.Errorf("after upgrading = %+v", items)
	}
}

func selfSigned(t *testing.T, name string, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestCertificates(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	kinds := kindsForTest(t)
	st := store.NewMemory(kinds)
	acct := store.AccountScope(store.NewID("acct"))
	site := putNote(t, st, acct, "example.com", store.PhaseReady, "")
	m := sim.New(sim.Options{})
	client := agent.NewLocal(m.Backend(), quiet)
	chain, key := selfSigned(t, "mail.example.com", now.Add(3*24*time.Hour))
	if _, err := agent.InstallCertificate.Call(ctx, client, agent.CertificateInput{Name: "mail", Chain: chain, Key: key}, agent.CallOptions{}); err != nil {
		t.Fatal(err)
	}
	chain, key = selfSigned(t, "db.example.com", now.Add(60*24*time.Hour))
	if _, err := agent.InstallCertificate.Call(ctx, client, agent.CertificateInput{Name: "db", Chain: chain, Key: key}, agent.CallOptions{}); err != nil {
		t.Fatal(err)
	}
	src := Certificates{
		Edge: func(ctx context.Context) ([]edge.Certificate, error) {
			return []edge.Certificate{
				{Subject: "example.com", Source: "acme", Status: edge.CertFailed, Error: "the CA couldn't reach the server", Owners: []store.ID{site}},
				{Subject: "soon.example.com", Source: "acme", Status: edge.CertValid, NotAfter: now.Add(5 * 24 * time.Hour)},
				{Subject: "old.example.com", Source: "uploaded", Status: edge.CertExpired, NotAfter: now.Add(-time.Hour)},
				{Subject: "fine.example.com", Source: "acme", Status: edge.CertValid, NotAfter: now.Add(60 * 24 * time.Hour)},
				{Subject: "dev.localhost", Source: "local", Status: edge.CertValid, NotAfter: now.Add(24 * time.Hour)},
				{Subject: "panel.example.com", Source: "acme", Status: edge.CertFailed, Owners: []store.ID{edge.PanelOwner}},
			}, nil
		},
		Agent: client, Store: st,
	}
	items, err := src.Items(ctx, Query{All: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	want := []string{"certificate:example.com", "certificate:soon.example.com", "certificate:old.example.com", "certificate:panel.example.com", "certificate:installed:mail"}
	if got := ids(items); !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	failed := byID["certificate:example.com"]
	if failed.Code != "certificate_failed" || failed.Message != "the CA couldn't reach the server" || failed.Access.Resource == nil ||
		failed.Access.Resource.ID != site || failed.Scope != acct || failed.Actions[0].Target != string(site) {
		t.Errorf("failed = %+v", failed)
	}
	if soon := byID["certificate:soon.example.com"]; soon.Code != "certificate_expiring" || soon.Params["days"] != "5" ||
		soon.Access.Permission != PermServerRead {
		t.Errorf("soon = %+v", soon)
	}
	if old := byID["certificate:old.example.com"]; old.Code != "certificate_expired" || old.Severity != events.SeverityError {
		t.Errorf("expired = %+v", old)
	}
	if panel := byID["certificate:panel.example.com"]; panel.Access.Permission != PermServerRead || panel.Subject != "" {
		t.Errorf("the panel's = %+v", panel)
	}
	if mail := byID["certificate:installed:mail"]; mail.Code != "certificate_expiring" || mail.Params["name"] != "mail" {
		t.Errorf("installed = %+v", mail)
	}
}

func TestSuggestions(t *testing.T) {
	ctx := t.Context()
	kinds := kindsForTest(t)
	st := store.NewMemory(kinds)
	m := sim.New(sim.Options{})
	states := map[string]string{"network": "on", "backups": "off", "storage": "off", "monitoring": "off"}
	src := Suggestions{
		Modules: func() []ModuleState {
			var out []ModuleState
			for _, id := range []string{"network", "backups", "storage", "monitoring", "web"} {
				out = append(out, ModuleState{ID: id, Name: id, State: states[id]})
			}
			return out
		},
		Store: st, Kinds: kinds, UnusedDisks: UnusedDisks(agent.NewLocal(m.Backend(), quiet), time.Minute),
	}
	items, err := src.Items(ctx, Query{All: true, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// The simulated server has three empty disks, and nothing kept yet.
	if !slices.Equal(ids(items), []string{"suggestion:storage"}) || items[0].Params["count"] != "3" || items[0].Title != "3 disks aren't used yet" ||
		items[0].Access.Permission != PermModuleRead || items[0].Actions[0].Target != "modules/storage" {
		t.Errorf("a fresh server = %+v", items)
	}
	// Core settings aren't what backups are for; a module's resources are.
	ctxw := context.Background()
	core := &store.Resource{Meta: store.Meta{Kind: settingKind.Name(), Scope: store.ServerScope()}, Spec: json.RawMessage(`{"name":"x"}`)}
	st.Write(ctxw, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctxw, core) })
	if items, _ := src.Items(ctx, Query{All: true, Now: time.Now()}); slices.Contains(ids(items), "suggestion:backups") {
		t.Error("backups are suggested for core settings")
	}
	putNote(t, st, store.AccountScope(store.NewID("acct")), "kept", store.PhaseReady, "")
	states["web"] = "on"
	items, _ = src.Items(ctx, Query{All: true, Now: time.Now()})
	if got := ids(items); !slices.Equal(got, []string{"suggestion:backups", "suggestion:storage", "suggestion:monitoring"}) {
		t.Errorf("with data and a module on = %v", got)
	}
	states["backups"], states["storage"], states["monitoring"] = "on", "active", "on"
	if items, _ := src.Items(ctx, Query{All: true, Now: time.Now()}); len(items) != 0 {
		t.Errorf("with the modules on = %v", ids(items))
	}
}
