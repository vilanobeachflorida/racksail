package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// A ticket's comments are sent from the panel itself, so its action applies
// in the panel and returns an output.
type ticketSpec struct {
	Subject string `json:"subject" minLength:"1"`
}

type commentInput struct {
	Text string `json:"text" minLength:"1"`
}

type commentOutput struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

var ticketKind = store.Define[ticketSpec, pageStatus](store.KindInfo{
	Name: "test.ticket", Prefix: "ticket", Scope: store.ScopeAccount, Title: "Ticket", Plural: "tickets",
	Description: "A support ticket, for actions that run in the panel.",
	Actions:     []store.Action{{Name: "comment", Title: "Comment", Summary: "Add a comment.", Risk: store.RiskLow, Input: commentInput{}, Output: commentOutput{}}},
})

type ticketReconciler struct{}

var (
	commentsMu sync.Mutex
	comments   []string
)

func (ticketReconciler) Plan(ctx context.Context, obj *store.Object[ticketSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (ticketReconciler) Apply(ctx context.Context, obj *store.Object[ticketSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

func (ticketReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (ticketReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	var in commentInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return nil, err
	}
	commentsMu.Lock()
	defer commentsMu.Unlock()
	comments = append(comments, in.Text)
	return commentOutput{Number: len(comments), Text: in.Text}, nil
}

// Labels have no reconciler: they only keep settings.
type labelSpec struct {
	Name string `json:"name"`
}

var labelKind = store.Define[labelSpec, store.Status](store.KindInfo{
	Name: "test.label", Prefix: "label", Scope: store.ScopeAccount, Title: "Label", Plural: "labels",
	Description: "A label, for kinds without a reconciler.",
})

// Resellers belong to themselves, as accounts do, the way a module that
// keeps resellers defines them; offers are a reseller's.
var (
	resellerKind = store.Define[labelSpec, store.Status](store.KindInfo{
		Name: "test.reseller", Prefix: "rsl", Scope: store.ScopeReseller, Title: "Reseller", Plural: "resellers",
		Description: "A reseller, for the checks of who resources belong to.",
	})
	offerKind = store.Define[labelSpec, store.Status](store.KindInfo{
		Name: "test.offer", Prefix: "offer", Scope: store.ScopeReseller, Title: "Offer", Plural: "offers",
		Description: "Something a reseller offers, for the checks of who resources belong to.",
	})
)

func (e *env) create(kind string, spec any) Change {
	raw, _ := json.Marshal(spec)
	return Change{Op: OpCreate, Kind: kind, Scope: store.AccountScope(e.acct), Spec: raw}
}

func TestApplyingAPreview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	preview := e.submit(t, true, e.createPage(pageSpec{Path: "about.html", Content: "About"}))
	applied, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, IdempotencyKey: "apply-1"})
	if err != nil {
		t.Fatal(err)
	}
	if applied.ID != preview.ID || applied.DryRun || applied.Job == "" {
		t.Fatalf("the preview becomes the applied change set: %+v", applied)
	}
	if again, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, IdempotencyKey: "apply-1"}); err != nil || again.ID != applied.ID {
		t.Errorf("a retry with the same key returns the same change set: %+v, %v", again, err)
	}
	final := e.wait(t, applied)
	if final.State != StateApplied || final.Changes[0].ID != preview.Changes[0].ID {
		t.Errorf("applied = %+v", final)
	}
	if got := e.file(pagesDir + "/about.html"); got != "About" {
		t.Errorf("page = %q", got)
	}
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID}); !errors.Is(err, ErrNotPreview) {
		t.Errorf("applying it a second time: %v", err)
	}
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: store.NewID("chg")}); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("a preview that doesn't exist: %v", err)
	}
}

// A retry with the first request's idempotency key gets the first answer,
// although by then the first request changed what it was about: the change
// set it undid is undone, the resource it deleted is gone.
func TestRetriesGetTheFirstAnswer(t *testing.T) {
	ctx := context.Background()
	for name, e := range map[string]*env{"memory": newEnv(t, Retention{}, nil), "sqlite": buildEnv(t, envConfig{db: openDB(t)})} {
		t.Run(name, func(t *testing.T) {
			id := e.submit(t, false, e.createPage(pageSpec{Path: "keyed.html", Content: "v1"})).Changes[0].ID
			raw, _ := json.Marshal(pageSpec{Path: "keyed.html", Content: "v2"})
			update := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw})

			undo := UndoRequest{Actor: e.owner, ChangeSet: update.ID, IdempotencyKey: "undo-1"}
			first, err := e.engine.Undo(ctx, undo)
			if err != nil {
				t.Fatal(err)
			}
			e.wait(t, first)
			if again, err := e.engine.Undo(ctx, undo); err != nil || again.ID != first.ID {
				t.Errorf("undoing again with the key = %+v, %v", again, err)
			}
			if _, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: update.ID}); !errors.Is(err, ErrNotApplied) {
				t.Errorf("undoing again without it = %v", err)
			}
			if replayed, err := e.engine.Replay(ctx, e.owner, CallUndo, "undo-1"); err != nil || replayed == nil || replayed.ID != first.ID {
				t.Errorf("replaying the undo = %+v, %v", replayed, err)
			}
			stranger := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user")}
			for who, replay := range map[string]func() (*ChangeSet, error){
				"another actor": func() (*ChangeSet, error) { return e.engine.Replay(ctx, stranger, CallUndo, "undo-1") },
				"another call":  func() (*ChangeSet, error) { return e.engine.Replay(ctx, e.owner, CallSubmit, "undo-1") },
				"no key":        func() (*ChangeSet, error) { return e.engine.Replay(ctx, e.owner, CallUndo, "") },
			} {
				if cs, err := replay(); err != nil || cs != nil {
					t.Errorf("%s: %+v, %v", who, cs, err)
				}
			}

			del := Request{Actor: e.owner, Changes: []Change{{Op: OpDelete, Kind: "test.page", ID: id}}, IdempotencyKey: "del-1"}
			gone, err := e.engine.Submit(ctx, del)
			if err != nil {
				t.Fatal(err)
			}
			e.wait(t, gone)
			if _, err := e.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("the page should be purged: %v", err)
			}
			if again, err := e.engine.Submit(ctx, del); err != nil || again.ID != gone.ID {
				t.Errorf("deleting again with the key once the page is gone = %+v, %v", again, err)
			}

			// While the first request is still going, a retry is told so.
			if _, _, err := e.engine.storage.ReserveKey(ctx, idempotencyKey(CallApply, e.owner, "busy"), time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := e.engine.Replay(ctx, e.owner, CallApply, "busy"); !errors.Is(err, ErrDuplicateRequest) {
				t.Errorf("replaying a request still going = %v", err)
			}
		})
	}
}

// A dry run of applying a preview plans it again and applies nothing: the
// answer is the preview while applying it would still do what it shows.
func TestCheckingAPreview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	preview := e.submit(t, true, e.createPage(pageSpec{Path: "checked.html", Content: "x"}))
	checked, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, DryRun: true, IdempotencyKey: "check-1"})
	if err != nil || checked.ID != preview.ID || !checked.DryRun || checked.Job != "" {
		t.Fatalf("checking the preview = %+v, %v", checked, err)
	}
	if kept, _ := e.engine.Get(ctx, preview.ID); !kept.DryRun {
		t.Error("checking a preview applies nothing")
	}
	if page, _ := e.store.List(ctx, store.Query{Kind: "test.page"}); len(page.Items) != 0 {
		t.Error("nothing is written")
	}
	// A dry run keeps no key, so applying with the same one goes ahead.
	applied, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, IdempotencyKey: "check-1"})
	if err != nil || applied.DryRun || applied.Job == "" {
		t.Fatalf("applying after the check = %+v, %v", applied, err)
	}
	id := e.wait(t, applied).Changes[0].ID

	// Once something else changed the page, the check says the preview is
	// stale, and names the new one.
	raw, _ := json.Marshal(pageSpec{Path: "checked.html", Content: "v2"})
	stale := e.submit(t, true, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw})
	current, _ := e.store.Get(ctx, id)
	e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: current.Spec, Labels: map[string]string{"team": "web"}})
	_, err = e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: stale.ID, DryRun: true})
	if !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("checking a stale preview = %v", err)
	}
	if fresh, err := e.engine.Get(ctx, store.ID(i18n.From(err).Params["preview"].(string))); err != nil || !fresh.DryRun {
		t.Errorf("the new preview = %+v, %v", fresh, err)
	}
	if got, _ := e.store.Get(ctx, id); strings.Contains(string(got.Spec), "v2") {
		t.Error("checking applied the stale preview")
	}
}

// A new resource belongs to an account or reseller that exists: one that is
// stored and not being removed, or one an earlier change of the same change
// set creates. Otherwise it would sit outside every account, and an account
// made later with that ID would inherit it.
func TestOwnersMustExist(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	admin := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	refused := func(err error, path string) bool {
		t.Helper()
		var coded *i18n.Error
		if !errors.As(err, &coded) || coded.Code != i18n.ErrInvalid.Code {
			return false
		}
		return slices.ContainsFunc(coded.Fields, func(f i18n.FieldError) bool { return f.Path == path && f.Code == i18n.ErrNotFound.Code })
	}
	nowhere := store.NewID("acct")
	page := e.createPage(pageSpec{Path: "lost.html", Content: "lost"})
	page.Scope = store.AccountScope(nowhere)
	for _, dry := range []bool{true, false} {
		if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: dry, Changes: []Change{page}}); !refused(err, "scope.id") {
			t.Errorf("an account that doesn't exist (dry run %v): %v", dry, err)
		}
	}
	if list, _ := e.store.List(ctx, store.Query{Kind: "test.page"}); len(list.Items) != 0 {
		t.Error("nothing is created for it")
	}

	// An account opened in the same change set, before its resources.
	acct := store.NewID("acct")
	spec, _ := json.Marshal(account.AccountSpec{Slug: "newcomer", Name: "Newcomer"})
	open := Change{Op: OpCreate, Kind: account.Kind.Name(), ID: acct, Scope: store.AccountScope(acct), Spec: spec}
	page.Scope = store.AccountScope(acct)
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{page, open}}); !refused(err, "changes[0].scope.id") {
		t.Errorf("an account opened after its page: %v", err)
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{open, page}}); err != nil {
		t.Errorf("an account opened with its page: %v", err)
	}
	opened, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: []Change{open}})
	if err != nil {
		t.Fatal(err)
	}
	if opened = e.wait(t, opened); opened.State != StateApplied {
		t.Fatalf("opening the account = %+v", opened)
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{page}}); err != nil {
		t.Errorf("a page of an account that exists: %v", err)
	}
	// An account being deleted takes nothing new.
	del := Change{Op: OpDelete, Kind: account.Kind.Name(), ID: acct}
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{del, page}}); !refused(err, "changes[1].scope.id") {
		t.Errorf("a page of an account deleted before it: %v", err)
	}
	// Nor can a resource of another kind name itself as its account.
	self := e.createPage(pageSpec{Path: "self.html", Content: "self"})
	self.ID = store.NewID("page")
	self.Scope = store.AccountScope(self.ID)
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{self}}); !refused(err, "scope.id") {
		t.Errorf("a page that names itself as its account: %v", err)
	}

	// A reseller is a resource of its own kind too.
	rsl := store.NewID("rsl")
	offer := Change{Op: OpCreate, Kind: "test.offer", Scope: store.ResellerScope(rsl), Spec: json.RawMessage(`{"name":"hosting"}`)}
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{offer}}); !refused(err, "scope.id") {
		t.Errorf("a reseller that doesn't exist: %v", err)
	}
	reseller := Change{Op: OpCreate, Kind: "test.reseller", ID: rsl, Scope: store.ResellerScope(rsl), Spec: json.RawMessage(`{"name":"Resold"}`)}
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{reseller, offer}}); err != nil {
		t.Errorf("a reseller and its offer: %v", err)
	}

	// Hooks can't add resources of an owner that doesn't exist either.
	e.hooks.rewrite = func(cs *ChangeSet, _ []int) {
		cs.Changes = append(cs.Changes, ResourceChange{Op: OpCreate, Kind: "test.label", Scope: store.AccountScope(nowhere), After: json.RawMessage(`{"name":"stray"}`),
			Source: Source{Type: "addon", Name: "stray"}})
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: admin, DryRun: true, Changes: []Change{e.createPage(pageSpec{Path: "home.html", Content: "home"})}}); !refused(err, "changes[1].scope.id") {
		t.Errorf("a hook's change in an account that doesn't exist: %v", err)
	}
	e.hooks.rewrite = nil

	// Undoing the delete of a resource whose account is gone since can't
	// bring it back into nothing.
	asAdmin := func(changes ...Change) *ChangeSet {
		t.Helper()
		cs, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: changes})
		if err != nil {
			t.Fatal(err)
		}
		return e.wait(t, cs)
	}
	gone := e.account(t)
	label := asAdmin(Change{Op: OpCreate, Kind: "test.label", Scope: store.AccountScope(gone), Spec: json.RawMessage(`{"name":"orphan"}`)})
	removed := asAdmin(Change{Op: OpDelete, Kind: "test.label", ID: label.Changes[0].ID})
	asAdmin(Change{Op: OpDelete, Kind: account.Kind.Name(), ID: gone})
	if _, err := e.engine.Undo(ctx, UndoRequest{Actor: admin, ChangeSet: removed.ID}); !refused(err, "scope.id") {
		t.Errorf("undoing a delete into an account that is gone: %v", err)
	}
}

func TestStalePreviewsAreNotApplied(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	id := e.submit(t, false, e.createPage(pageSpec{Path: "news.html", Content: "v1"})).Changes[0].ID
	raw, _ := json.Marshal(pageSpec{Path: "news.html", Content: "v2"})
	preview := e.submit(t, true, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw})

	// Someone else changes the page's labels, which the preview would have
	// kept; applying it now would do something else than it showed.
	current, _ := e.store.Get(ctx, id)
	other := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: current.Spec, Labels: map[string]string{"team": "news"}})
	if other.State != StateApplied {
		t.Fatal(other)
	}
	_, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID})
	if !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("a stale preview: %v", err)
	}
	fresh := store.ID(i18n.From(err).Params["preview"].(string))
	newPreview, err := e.engine.Get(ctx, fresh)
	if err != nil || !newPreview.DryRun || newPreview.Changes[0].Labels["team"] != "news" {
		t.Fatalf("the new preview shows what applying does now: %+v, %v", newPreview, err)
	}
	if got, _ := e.store.Get(ctx, id); strings.Contains(string(got.Spec), "v2") {
		t.Error("nothing was applied")
	}
	applied, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: fresh})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, applied)
	if got := e.file(pagesDir + "/news.html"); got != "v2" {
		t.Errorf("page = %q", got)
	}
}

func TestPreviewsWithSecretsAreSentAgain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	ch := e.createPage(pageSpec{Path: "vault.html", Content: "x"})
	ch.Secrets = map[string]string{"secret": "hunter2"}
	preview := e.submit(t, true, ch)
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID}); !errors.Is(err, ErrPreviewHasSecrets) {
		t.Errorf("a preview whose secret values weren't kept: %v", err)
	}
}

func TestApplyingAnUndoPreview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	created := e.submit(t, false, e.createPage(pageSpec{Path: "gone.html", Content: "x"}))
	preview, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: created.ID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	undo, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	if undo = e.wait(t, undo); undo.State != StateApplied || undo.UndoOf != created.ID {
		t.Fatalf("undo = %+v", undo)
	}
	if orig, _ := e.engine.Get(ctx, created.ID); orig.State != StateUndone {
		t.Errorf("the original is marked as undone: %s", orig.State)
	}
	if _, err := e.store.Get(ctx, created.Changes[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the page is gone: %v", err)
	}
}

func TestListingChangeSets(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testListingChangeSets(t, newEnv(t, Retention{}, nil)) })
	t.Run("sqlite", func(t *testing.T) { testListingChangeSets(t, buildEnv(t, envConfig{db: openDB(t)})) })
}

func testListingChangeSets(t *testing.T, e *env) {
	ctx := context.Background()
	first := e.submit(t, false, e.createPage(pageSpec{Path: "a.html", Content: "a"}))
	id := first.Changes[0].ID
	raw, _ := json.Marshal(pageSpec{Path: "a.html", Content: "b"})
	second := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw})
	other := e.submit(t, false, e.createPage(pageSpec{Path: "c.html", Content: "c"}))
	preview := e.submit(t, true, e.createPage(pageSpec{Path: "d.html", Content: "d"}))
	ids := func(q ListQuery) []store.ID {
		t.Helper()
		list, _, err := e.engine.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.ID
		for _, cs := range list {
			out = append(out, cs.ID)
		}
		return out
	}
	if got := ids(ListQuery{}); !slices.Equal(got, []store.ID{other.ID, second.ID, first.ID}) {
		t.Errorf("applied change sets, newest first = %v", got)
	}
	if got := ids(ListQuery{Resource: id}); !slices.Equal(got, []store.ID{second.ID, first.ID}) {
		t.Errorf("by resource = %v", got)
	}
	if got := ids(ListQuery{DryRuns: true}); !slices.Equal(got, []store.ID{preview.ID}) {
		t.Errorf("previews = %v", got)
	}
	if got := ids(ListQuery{Actor: e.owner.ID, State: StateApplied, Limit: 1}); !slices.Equal(got, []store.ID{other.ID}) {
		t.Errorf("by actor and state = %v", got)
	}
	_, next, _ := e.engine.List(ctx, ListQuery{Limit: 2})
	if got := ids(ListQuery{Limit: 2, Cursor: next}); !slices.Equal(got, []store.ID{first.ID}) {
		t.Errorf("second page = %v", got)
	}

	// What someone may see: their own change sets, and those whose every
	// resource they reach.
	admin := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	label, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: []Change{e.create("test.label", labelSpec{Name: "x"})}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, label)
	otherAcct := store.AccountScope(e.account(t))
	elsewhere, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: []Change{{Op: OpCreate, Kind: "test.label", Scope: otherAcct, Spec: json.RawMessage(`{"name":"y"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, elsewhere)
	mine := store.AccountScope(e.acct)
	if got := ids(ListQuery{Visible: &Visibility{Person: e.owner.ID}}); !slices.Equal(got, []store.ID{other.ID, second.ID, first.ID}) {
		t.Errorf("their own = %v", got)
	}
	if got := ids(ListQuery{Visible: &Visibility{Scopes: []store.Scope{mine}}}); !slices.Equal(got, []store.ID{label.ID, other.ID, second.ID, first.ID}) {
		t.Errorf("in an account they reach = %v", got)
	}
	if got := ids(ListQuery{Visible: &Visibility{Objects: []store.ID{elsewhere.Changes[0].ID}}}); !slices.Equal(got, []store.ID{elsewhere.ID}) {
		t.Errorf("of a resource they hold a grant on = %v", got)
	}
	if got := ids(ListQuery{Visible: &Visibility{Person: store.NewID("user")}}); len(got) != 0 {
		t.Errorf("someone with no reach = %v", got)
	}
}

func TestActionsThatRunInThePanel(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"test.ticket.commented"}}, 4)
	defer sub.Close()
	ticket := e.submit(t, false, e.create("test.ticket", ticketSpec{Subject: "Printer on fire"}))
	id := ticket.Changes[0].ID
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpAction, Kind: "test.ticket", ID: id, Action: "comment", Input: json.RawMessage(`{"text":""}`)}}}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("inputs are checked against their schema: %v", err)
	}
	cs := e.submit(t, false, Change{Op: OpAction, Kind: "test.ticket", ID: id, Action: "comment", Input: json.RawMessage(`{"text":"Have you tried water?"}`)})
	var out commentOutput
	if err := json.Unmarshal(cs.Changes[0].Output, &out); err != nil || out.Text != "Have you tried water?" || out.Number == 0 {
		t.Errorf("the action's output is kept with the change set: %s, %v", cs.Changes[0].Output, err)
	}
	select {
	case ev := <-sub.C:
		if !strings.Contains(string(ev.Data), "Have you tried water?") {
			t.Errorf("the action's event carries its output: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("no event")
	}
	job, _ := e.jobs.Get(ctx, cs.Job)
	if !strings.Contains(string(job.Result), "outputs") {
		t.Errorf("the job's result has the output too: %s", job.Result)
	}
}

func TestKindsWithoutReconcilersAreReadyOnceStored(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	cs := e.submit(t, false, e.create("test.label", labelSpec{Name: "urgent"}))
	got, err := labelKind.Get(ctx, e.store, cs.Changes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != store.PhaseReady || got.Status.ObservedGeneration != 1 || !got.Status.IsTrue(reconcile.ConditionReady) {
		t.Errorf("status = %+v", got.Status)
	}
	del := e.submit(t, false, Change{Op: OpDelete, Kind: "test.label", ID: got.ID})
	if del.State != StateApplied {
		t.Fatal(del)
	}
	if _, err := e.store.Get(ctx, got.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a deleted label is purged at once: %v", err)
	}
}

// A change is written only at the revision it was planned against, so a
// change that lands meanwhile is never undone. A request that names no
// revision is planned again from what landed; one that names a revision
// fails, as it asked.
func TestChangesDontOverwriteWhatLandedMeanwhile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	cs := e.submit(t, false, e.create("test.label", labelSpec{Name: "draft"}))
	id := cs.Changes[0].ID
	// land has another write label the resource while the update is
	// checked, at the first attempts.
	land := func(attempts int) {
		var mu sync.Mutex
		landed := 0
		e.hooks.during = func(ctx context.Context, point string) {
			mu.Lock()
			defer mu.Unlock()
			if point != "test.label.update.validate" || landed == attempts {
				return
			}
			landed++
			err := e.store.Write(ctx, store.WriteMeta{Actor: "test"}, func(tx store.Tx) error {
				cur, err := tx.Get(ctx, id)
				if err != nil {
					return err
				}
				cur.Labels = map[string]string{"reviewed": strconv.Itoa(landed)}
				return tx.Update(ctx, cur)
			})
			if err != nil {
				t.Error(err)
			}
		}
	}
	rename := func(name string, revision int64) (*ChangeSet, error) {
		return e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{
			{Op: OpUpdate, Kind: "test.label", ID: id, Spec: json.RawMessage(`{"name":"` + name + `"}`), Revision: revision},
		}})
	}
	land(1)
	applied, err := rename("mine", 0)
	if err != nil {
		t.Fatalf("an update planned before another landed = %v", err)
	}
	e.wait(t, applied)
	got, err := labelKind.Get(ctx, e.store, id)
	if err != nil || got.Spec.Name != "mine" || got.Labels["reviewed"] != "1" {
		t.Errorf("the label keeps what landed and gets the update: %+v, %v", got, err)
	}

	land(1)
	if _, err := rename("theirs", got.Revision); !store.ErrConflict.Is(err) {
		t.Errorf("an update naming the revision it was made against = %v", err)
	}
	// A request that keeps losing to other writes gives up in the end.
	land(replans)
	if _, err := rename("yours", 0); !store.ErrConflict.Is(err) {
		t.Errorf("an update overtaken every time = %v", err)
	}
	e.hooks.during = nil
	if got, _ := labelKind.Get(ctx, e.store, id); got.Spec.Name != "mine" {
		t.Errorf("label = %+v", got.Spec)
	}

	// Several changes of one resource in a request build on each other.
	next := e.submit(t, false,
		Change{Op: OpUpdate, Kind: "test.label", ID: id, Spec: json.RawMessage(`{"name":"one"}`)},
		Change{Op: OpUpdate, Kind: "test.label", ID: id, Spec: json.RawMessage(`{"name":"two"}`)},
	)
	if next.State != StateApplied || next.Changes[0].Revision == 0 || next.Changes[1].Revision != 0 {
		t.Errorf("changes = %+v", next.Changes)
	}
	if got, _ := labelKind.Get(ctx, e.store, id); got.Spec.Name != "two" {
		t.Errorf("label = %+v", got.Spec)
	}
}

// What validation sees of other resources is the state the change set
// leaves: earlier changes of the same request included.
func TestValidationReadsThePlannedState(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	var seen []string
	e.hooks.during = func(ctx context.Context, point string) {
		r, ok := store.ReaderFrom(ctx)
		if !ok {
			t.Errorf("%s has no reader", point)
			return
		}
		page, err := r.List(ctx, store.Query{Kind: "test.label"})
		if err != nil {
			t.Error(err)
			return
		}
		for _, item := range page.Items {
			obj, _ := labelKind.Decode(item)
			seen = append(seen, obj.Spec.Name)
		}
		seen = append(seen, "|")
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, DryRun: true, Changes: []Change{
		e.create("test.label", labelSpec{Name: "first"}), e.create("test.label", labelSpec{Name: "second"}),
	}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, " "); got != "| first |" {
		t.Errorf("what the validate points saw: %q", got)
	}
}

func TestChangeSetEvents(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"core.changeset.*"}}, 8)
	defer sub.Close()
	cs := e.submit(t, false, e.createPage(pageSpec{Path: "e.html", Content: "e"}))
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: cs.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, undo)
	var got []string
	var undone events.ChangeSetData
	for len(got) < 3 {
		select {
		case ev := <-sub.C:
			got = append(got, ev.Type)
			if ev.Type == events.ChangeSetUndone.Name {
				if err := json.Unmarshal(ev.Data, &undone); err != nil {
					t.Fatal(err)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("events = %v", got)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"core.changeset.applied", "core.changeset.applied", "core.changeset.undone"}) {
		t.Errorf("events = %v", got)
	}
	if undone.UndoneBy != undo.ID || undone.Resources[0] != cs.Changes[0].ID {
		t.Errorf("undone = %+v", undone)
	}

	// A change that fails says so as a whole too.
	failing := e.createPage(pageSpec{Path: "broken.html", Content: "x"})
	e.sim.FailNext("files.write", pagesDir+"/broken.html", errors.New("disk full"))
	broken := e.submit(t, false, failing)
	if broken.State != StateFailed {
		t.Fatalf("change set = %+v", broken)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != events.ChangeSetFailed.Name || ev.Severity != events.SeverityError || ev.ChangeSet != broken.ID {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Error("no failure event")
	}
}

// orderedHooks records every hook call of the pipeline in order.
type orderedHooks struct {
	mu    sync.Mutex
	calls []string
}

func (o *orderedHooks) record(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, s)
}

func (o *orderedHooks) Validate(ctx context.Context, point string, cs *ChangeSet, change *ResourceChange) error {
	o.record("validate " + point + " " + string(change.Op))
	return nil
}

func (o *orderedHooks) Modify(ctx context.Context, point string, cs *ChangeSet, changes []int) error {
	var idx []string
	for _, i := range changes {
		idx = append(idx, cs.Changes[i].Kind)
	}
	o.record("modify " + point + " " + strings.Join(idx, ","))
	return nil
}

func (o *orderedHooks) Applied(ctx context.Context, point string, cs *ChangeSet, changes []int) {
	if cs.State != StateApplied {
		o.record("applied before the change set was applied")
	}
	o.record("applied " + point)
}

// The stages run in a fixed order: every change's validate hooks, as the
// changes are planned; then each plan point's modify hooks once, with its
// changes, in the order the points first appear; then, once the apply job
// has carried everything out, the applied hooks of each point.
func TestHookStagesRunInOrder(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	hooks := &orderedHooks{}
	e.engine.opts.Hooks = hooks
	pageID := store.NewID("page")
	page := e.createPage(pageSpec{Path: "home.html", Content: "home"})
	page.ID = pageID
	raw, _ := json.Marshal(menuSpec{Pages: []store.ID{pageID}})
	second := e.createPage(pageSpec{Path: "other.html", Content: "other"})
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{
		page, {Op: OpCreate, Kind: "test.menu", Scope: store.AccountScope(e.acct), Spec: raw}, second,
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, cs)
	deadline := time.Now().Add(2 * time.Second)
	for {
		hooks.mu.Lock()
		n := len(hooks.calls)
		hooks.mu.Unlock()
		if n >= 7 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	want := []string{
		"validate test.page.create.validate create",
		"validate test.menu.create.validate create",
		"validate test.page.create.validate create",
		"modify test.page.create.plan test.page,test.page",
		"modify test.menu.create.plan test.menu",
		"applied test.page.create.applied",
		"applied test.menu.create.applied",
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if !slices.Equal(hooks.calls, want) {
		t.Errorf("calls =\n%s\nwant\n%s", strings.Join(hooks.calls, "\n"), strings.Join(want, "\n"))
	}
}

// The panel keeps change sets, previews and idempotency keys in its
// database, so they outlive a restart: previews from before can be applied,
// keys still answer, and change sets from before can still be undone.
func TestChangeSetsOutliveARestart(t *testing.T) {
	db, err := store.OpenDB(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	machine := sim.New(sim.Options{})
	ctx1, stop := context.WithCancel(context.Background())
	first := buildEnv(t, envConfig{db: db, machine: machine, ctx: ctx1})
	created := first.submit(t, false, first.createPage(pageSpec{Path: "kept.html", Content: "v1"}))
	preview := first.submit(t, true, Change{Op: OpUpdate, Kind: "test.page", ID: created.Changes[0].ID, Spec: json.RawMessage(`{"path":"kept.html","content":"v2"}`)})
	keyed, err := first.engine.Submit(context.Background(), Request{Actor: first.owner, IdempotencyKey: "k-1", Changes: []Change{first.createPage(pageSpec{Path: "once.html", Content: "once"})}})
	if err != nil {
		t.Fatal(err)
	}
	first.wait(t, keyed)
	stop()

	ctx2, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := buildEnv(t, envConfig{db: db, machine: machine, ctx: ctx2, vault: first.secrets})
	second.owner, second.acct = first.owner, first.acct
	if again, err := second.engine.Submit(context.Background(), Request{Actor: first.owner, IdempotencyKey: "k-1", Changes: []Change{first.createPage(pageSpec{Path: "once.html", Content: "once"})}}); err != nil || again.ID != keyed.ID {
		t.Errorf("an idempotency key outlives the restart: %+v, %v", again, err)
	}
	applied, err := second.engine.Apply(context.Background(), ApplyRequest{Actor: first.owner, ChangeSet: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	second.wait(t, applied)
	if got := second.file(pagesDir + "/kept.html"); got != "v2" {
		t.Errorf("a preview from before the restart is applied: %q", got)
	}
	undo, err := second.engine.Undo(context.Background(), UndoRequest{Actor: first.owner, ChangeSet: applied.ID})
	if err != nil {
		t.Fatal(err)
	}
	second.wait(t, undo)
	if got := second.file(pagesDir + "/kept.html"); got != "v1" {
		t.Errorf("undo works across the restart: %q", got)
	}
	audit, _, _ := second.bus.QueryAudit(context.Background(), events.Query{Subject: created.Changes[0].ID})
	if len(audit) < 3 {
		t.Errorf("audit records = %d", len(audit))
	}
	actorEvents, _, _ := second.bus.QueryEvents(context.Background(), events.Query{Subject: created.Changes[0].ID})
	if len(actorEvents) < 3 || actorEvents[len(actorEvents)-1].Actor == nil || actorEvents[len(actorEvents)-1].Actor.Type != auth.ActorUser {
		t.Errorf("events = %+v", actorEvents)
	}
}

// Change sets kept before their resources' owners were recorded get them
// from what they changed.
func TestResourceScopesMigration(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	all, err := store.LoadMigrations(MigrationOwner, Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationOwner, fstest.MapFS{"0001_create_change_sets.sql": {Data: []byte(all[0].SQL)}}); err != nil {
		t.Fatal(err)
	}
	acct := store.AccountScope(store.NewID("acct"))
	cs := &ChangeSet{ID: store.NewID(store.PrefixChangeSet), Title: "Create page", State: StateApplied, CreatedAt: time.Now().UTC(),
		Changes: []ResourceChange{{Op: OpCreate, Kind: "test.page", ID: store.NewID("page"), Scope: acct}}}
	body, _ := json.Marshal(cs)
	if _, err := db.Exec(ctx, `INSERT INTO change_sets (id, title, state, dry_run, created_at, body) VALUES (?, ?, 'applied', 0, ?, ?)`,
		cs.ID, cs.Title, store.FormatTime(cs.CreatedAt), string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO change_set_resources (change_set, resource) VALUES (?, ?)`, cs.ID, cs.Changes[0].ID); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLStorage(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := s.List(ctx, ListQuery{Visible: &Visibility{Scopes: []store.Scope{acct}}})
	if err != nil || len(list) != 1 || list[0].ID != cs.ID {
		t.Errorf("found by its resource's owner = %v, %v", list, err)
	}
}
