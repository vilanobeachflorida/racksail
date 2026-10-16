package account

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
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// A thing that lives in an account, so deleting a full account can be
// refused.
type noteSpec struct {
	Text string `json:"text"`
}

var noteKind = store.Define[noteSpec, store.Status](store.KindInfo{
	Name: "test.note", Prefix: "note", Scope: store.ScopeAccount, Title: "Note", Plural: "notes",
	Description: "A note kept in an account, for the account tests.",
})

type harness struct {
	engine  *changeset.Engine
	store   store.Store
	machine *sim.Machine
	jobs    jobs.Queue
	owners  *Ownership
	admin   *auth.Actor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{Kind, noteKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st := store.NewMemory(kinds)
	bus := events.NewMemory(100)
	q := jobs.NewMemory(ctx, jobs.MemoryOptions{Workers: 2})
	m := sim.New(sim.Options{})
	recs := reconcile.NewRegistry()
	if err := recs.Register(Reconciler(kinds)); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	owners := NewOwnership(st, nil)
	az := auth.NewRoleAuthorizer(owners)
	engine, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Jobs: q, Events: bus, Audit: bus, Secrets: secrets.NewMemory(box),
		Runner:     &reconcile.Runner{Store: st, Agent: agent.NewLocal(m.Backend(), nil), Registry: recs},
		Authorizer: az,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	t.Cleanup(func() { q.Stop(context.Background()) })
	admin := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "admin", Source: auth.SourceUI,
		Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	return &harness{engine: engine, store: st, machine: m, jobs: q, owners: owners, admin: admin}
}

func (h *harness) submit(t *testing.T, actor *auth.Actor, dryRun bool, changes ...changeset.Change) (*changeset.ChangeSet, error) {
	t.Helper()
	cs, err := h.engine.Submit(context.Background(), changeset.Request{Actor: actor, Changes: changes, DryRun: dryRun})
	if err != nil || dryRun {
		return cs, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.jobs.Wait(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	return h.engine.Get(ctx, cs.ID)
}

func createAccount(spec AccountSpec) changeset.Change {
	id := store.NewID("acct")
	raw, _ := json.Marshal(spec)
	return changeset.Change{Op: changeset.OpCreate, Kind: Kind.Name(), ID: id, Scope: store.AccountScope(id), Spec: raw}
}

func TestAccountLifecycle(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	change := createAccount(AccountSpec{Slug: "acme", Name: "Acme Inc.", Limits: AccountLimits{MemoryMax: 1 << 30, CPU: 150, Tasks: 512}})

	preview, err := h.submit(t, h.admin, true, change)
	if err != nil {
		t.Fatal(err)
	}
	var effects []string
	for _, e := range preview.Effects {
		effects = append(effects, e.Action+" "+e.Target)
	}
	joined := strings.Join(effects, "\n")
	if !strings.Contains(joined, "create acme") || !strings.Contains(joined, system.AccountSlice("acme")) {
		t.Errorf("the preview should show the user and the slice:\n%s", joined)
	}
	if _, err := h.machine.Backend().Users.LookupUser(ctx, "acme"); err == nil {
		t.Fatal("a preview changed the machine")
	}

	cs, err := h.submit(t, h.admin, false, change)
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("create = %+v, %v", cs, err)
	}
	obj, err := Kind.Get(ctx, h.store, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := obj.Status
	if st.Phase != store.PhaseReady || st.UID < system.AccountUIDMin || st.UID > system.AccountUIDMax || st.User != "acme" || st.Slice != system.AccountSlice("acme") {
		t.Fatalf("status = %+v", st)
	}
	user, err := h.machine.Backend().Users.LookupUser(ctx, "acme")
	if err != nil || user.UID != st.UID || user.GID != st.UID || user.Home != system.AccountDir("acme") || user.SubIDs == nil {
		t.Fatalf("user = %+v, %v", user, err)
	}
	if !strings.Contains(user.Comment, "Acme Inc.") {
		t.Errorf("comment = %q", user.Comment)
	}
	info, err := h.machine.Backend().Files.Stat(ctx, system.AccountDir("acme"))
	if err != nil || info.Perm() != homeMode {
		t.Errorf("home = %+v, %v", info, err)
	}
	unit, err := h.machine.Backend().Files.ReadFile(ctx, system.UnitDir+"/"+system.AccountSlice("acme"), 0)
	if err != nil || !strings.Contains(string(unit), "MemoryMax=1073741824") || !strings.Contains(string(unit), "CPUQuota=150%") {
		t.Errorf("slice = %s, %v", unit, err)
	}

	// Changing the limits rewrites the slice and keeps the IDs.
	obj.Spec.Limits = AccountLimits{MemoryMax: 2 << 30}
	raw, _ := json.Marshal(obj.Spec)
	if cs, err := h.submit(t, h.admin, false, changeset.Change{Op: changeset.OpUpdate, Kind: Kind.Name(), ID: obj.ID, Spec: raw}); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("update = %+v, %v", cs, err)
	}
	unit, _ = h.machine.Backend().Files.ReadFile(ctx, system.UnitDir+"/"+system.AccountSlice("acme"), 0)
	again, _ := Kind.Get(ctx, h.store, obj.ID)
	if !strings.Contains(string(unit), "MemoryMax=2147483648") || again.Status.UID != st.UID {
		t.Errorf("after update: %s, uid %d", unit, again.Status.UID)
	}

	// The account's owner sees it but can't change it.
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(obj.ID)}}}
	obj.Spec.Limits = AccountLimits{}
	raw, _ = json.Marshal(obj.Spec)
	if _, err := h.submit(t, owner, true, changeset.Change{Op: changeset.OpUpdate, Kind: Kind.Name(), ID: obj.ID, Spec: raw}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("an owner raised their own limits: %v", err)
	}

	// Something left in the account keeps it from being removed.
	note, _ := json.Marshal(noteSpec{Text: "keep"})
	noteChange := changeset.Change{Op: changeset.OpCreate, Kind: "test.note", Scope: store.AccountScope(obj.ID), Spec: note}
	if _, err := h.submit(t, owner, false, noteChange); err != nil {
		t.Fatal(err)
	}
	del := changeset.Change{Op: changeset.OpDelete, Kind: Kind.Name(), ID: obj.ID}
	if _, err := h.submit(t, h.admin, true, del); !ErrNotEmpty.Is(err) || !strings.Contains(err.Error(), "1 note") {
		t.Errorf("deleting a full account = %v", err)
	}
	notes, _, _ := noteKind.List(ctx, h.store, store.Query{})
	if _, err := h.submit(t, h.admin, false, changeset.Change{Op: changeset.OpDelete, Kind: "test.note", ID: notes[0].ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.purge(ctx, notes[0].ID); err != nil {
		t.Fatal(err)
	}
	cs, err = h.submit(t, h.admin, false, del)
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("delete = %+v, %v", cs, err)
	}
	if _, err := h.machine.Backend().Users.LookupUser(ctx, "acme"); err == nil {
		t.Error("the account's user should be gone")
	}
	if _, err := h.machine.Backend().Files.Stat(ctx, system.AccountDir("acme")); err == nil {
		t.Error("the account's home should be gone")
	}
}

// purge removes a deleted resource the way the runner does once its plan
// ran.
func (h *harness) purge(ctx context.Context, id store.ID) error {
	r, err := h.store.Get(ctx, id)
	if store.ErrNotFound.Is(err) {
		return nil
	}
	if err != nil || !r.Deleted() {
		return err
	}
	return h.store.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Purge(ctx, id) })
}

func TestAccountValidation(t *testing.T) {
	h := newHarness(t)
	for name, spec := range map[string]AccountSpec{
		"short slug":    {Slug: "ab", Name: "x"},
		"upper slug":    {Slug: "Acme", Name: "x"},
		"reserved slug": {Slug: "root", Name: "x"},
		"no name":       {Slug: "acme"},
		"memory":        {Slug: "acme", Name: "x", Limits: AccountLimits{MemoryHigh: 2 << 30, MemoryMax: 1 << 30}},
		"tiny memory":   {Slug: "acme", Name: "x", Limits: AccountLimits{MemoryMax: 1 << 20}},
		"few tasks":     {Slug: "acme", Name: "x", Limits: AccountLimits{Tasks: 3}},

		// The agent refuses an account the name of a privileged group or of
		// a package's user, so the panel does too.
		"privileged group slug": {Slug: "kvm", Name: "x"},
		"package user slug":     {Slug: "postdrop", Name: "x"},
	} {
		if _, err := h.submit(t, h.admin, true, createAccount(spec)); !i18n.ErrInvalid.Is(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An account belongs to itself.
	raw, _ := json.Marshal(AccountSpec{Slug: "acme", Name: "x"})
	other := changeset.Change{Op: changeset.OpCreate, Kind: Kind.Name(), ID: store.NewID("acct"), Scope: store.AccountScope(store.NewID("acct")), Spec: raw}
	if _, err := h.submit(t, h.admin, true, other); !ErrOwnScope.Is(err) {
		t.Errorf("another account's scope = %v", err)
	}
	created := createAccount(AccountSpec{Slug: "acme", Name: "Acme"})
	if _, err := h.submit(t, h.admin, false, created); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(t, h.admin, true, createAccount(AccountSpec{Slug: "ACME", Name: "Again"})); err == nil {
		t.Error("short names are unique, whatever their case")
	}
	renamed, _ := json.Marshal(AccountSpec{Slug: "acme2", Name: "Acme"})
	if _, err := h.submit(t, h.admin, true, changeset.Change{Op: changeset.OpUpdate, Kind: Kind.Name(), ID: created.ID, Spec: renamed}); !i18n.ErrInvalid.Is(err) {
		t.Errorf("renaming = %v", err)
	}
}

func TestResellersCreateTheirOwnAccounts(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	reseller, rival := store.NewID("rsl"), store.NewID("rsl")
	res := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleReseller, Scope: store.ResellerScope(reseller)}}}

	change := createAccount(AccountSpec{Slug: "shop", Name: "Shop", Reseller: reseller})
	if _, err := h.submit(t, res, true, change); !errors.Is(err, i18n.ErrForbidden) {
		t.Fatalf("without being expected = %v", err)
	}
	h.owners.Expect(change.ID, reseller, time.Minute)
	cs, err := h.submit(t, res, false, change)
	if err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("expected = %+v, %v", cs, err)
	}
	if r, err := h.owners.ResellerOf(ctx, change.ID); err != nil || r != reseller {
		t.Errorf("owner = %s, %v", r, err)
	}
	accounts, err := h.owners.AccountsOf(ctx, reseller)
	if err != nil || len(accounts) != 1 || accounts[0] != change.ID {
		t.Errorf("accounts = %v, %v", accounts, err)
	}

	// Naming another reseller is refused, even when expected.
	stolen := createAccount(AccountSpec{Slug: "stolen", Name: "Stolen", Reseller: rival})
	h.owners.Expect(stolen.ID, reseller, time.Minute)
	if _, err := h.submit(t, res, true, stolen); !i18n.ErrInvalid.Is(err) {
		t.Errorf("another reseller's account = %v", err)
	}
	// So is a direct account of the server.
	direct := createAccount(AccountSpec{Slug: "direct", Name: "Direct"})
	h.owners.Expect(direct.ID, reseller, time.Minute)
	if _, err := h.submit(t, res, true, direct); !i18n.ErrInvalid.Is(err) {
		t.Errorf("a server account = %v", err)
	}

	// Expectations run out.
	late := store.NewID("acct")
	h.owners.Expect(late, reseller, time.Minute)
	h.owners.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if r, _ := h.owners.ResellerOf(ctx, late); r != "" {
		t.Errorf("an expectation outlived its time: %s", r)
	}
}

func TestSteps(t *testing.T) {
	steps, err := Steps(AccountSpec{Slug: "acme", Name: "Acme\nInc:"}, 20001)
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, s := range steps {
		ops = append(ops, s.Op)
	}
	if strings.Join(ops, " ") != "EnsureGroup EnsureUser EnsureDir EnsureUnit" {
		t.Errorf("ops = %v", ops)
	}
	user := steps[1].Input.(agent.EnsureUserInput)
	if user.Comment != "Racksail account Acme Inc " || user.SubIDs.Start != system.SubIDStart+system.SubIDCount || !user.Locked {
		t.Errorf("user = %+v", user)
	}
	for _, s := range steps {
		if in, ok := s.Input.(interface{ Validate() error }); ok {
			if err := in.Validate(); err != nil {
				t.Errorf("%s: %v", s.Op, err)
			}
		}
	}
	for _, s := range RemoveSteps(AccountSpec{Slug: "acme"}) {
		if err := s.Input.(interface{ Validate() error }).Validate(); err != nil {
			t.Errorf("%s: %v", s.Op, err)
		}
	}
}
