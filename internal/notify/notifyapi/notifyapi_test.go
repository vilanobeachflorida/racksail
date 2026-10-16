package notifyapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/notify/notifyapi"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// signedIn signs every request in as one person.
type signedIn struct{ actor *auth.Actor }

func (s signedIn) Authenticate(ctx context.Context, r *http.Request) (*auth.Actor, error) {
	return s.actor.Clone(), nil
}

func do(t *testing.T, h http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, out
}

// panel is the API with notifications and the change pipeline.
type panel struct {
	h        http.Handler
	svc      *notify.Service
	pipeline *changeset.Engine
	jobs     *jobs.Manager
}

func server(t *testing.T, actor *auth.Actor) (http.Handler, *notify.Service) {
	p := newPanel(t, actor)
	return p.h, p.svc
}

func newPanel(t *testing.T, actor *auth.Actor) *panel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{notify.ChannelKind, notify.RelayKind, notify.PreferencesKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st := store.NewMemory(kinds)
	log := events.NewMemory(100)
	discard := slog.New(slog.DiscardHandler)
	svc, err := notify.NewSQL(ctx, db, notify.Options{Store: st, Log: log, Logger: discard})
	if err != nil {
		t.Fatal(err)
	}
	reg := hooks.NewRegistry()
	for _, k := range kinds.All() {
		reg.DeclareKind(k)
	}
	for _, h := range svc.Hooks() {
		if err := reg.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	q := jobs.NewMemory(ctx, jobs.MemoryOptions{Log: discard})
	t.Cleanup(func() { q.Stop(context.Background()) })
	authz := auth.NewRoleAuthorizer(nil)
	pipeline, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: &reconcile.Runner{Store: st, Registry: reconcile.NewRegistry(), Events: log, Log: discard},
		Jobs: q, Events: log, Audit: log, Hooks: &hooks.Runner{Registry: reg, Log: discard}, Authorizer: authz, Log: discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	services := &modules.Services{Store: st, Kinds: kinds, Events: log, Audit: log, Authorizer: authz, Notify: svc, Pipeline: pipeline, Jobs: q}
	srv := api.New(api.Options{Auth: signedIn{actor}, Services: services, Log: discard})
	return &panel{h: srv.Handler(), svc: svc, pipeline: pipeline, jobs: q}
}

// wait waits for a change set's job and returns how it ended.
func (p *panel) wait(t *testing.T, body []byte) *changeset.ChangeSet {
	t.Helper()
	var cs changeset.ChangeSet
	if err := json.Unmarshal(body, &cs); err != nil {
		t.Fatalf("not a change set: %s", body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.jobs.Wait(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final, err := p.pipeline.Get(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func TestChannelTypes(t *testing.T) {
	admin := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	h, _ := server(t, admin)
	code, body := do(t, h, http.MethodGet, "/api/v1/notifications/types", nil)
	var out struct {
		Types []notifyapi.ChannelType `json:"types"`
	}
	if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil {
		t.Fatalf("types = %d %s", code, body)
	}
	byName := map[string]notifyapi.ChannelType{}
	for _, ct := range out.Types {
		byName[ct.Name] = ct
	}
	tg, ok := byName["telegram"]
	if !ok || len(byName) < 7 || !strings.Contains(string(tg.Schema), `"chatId"`) || strings.Contains(string(tg.Schema), "$ref") {
		t.Errorf("telegram = %+v; every type comes with its settings' schema, inlined", tg)
	}
	if code, body := do(t, h, http.MethodGet, "/api/v1/notifications", nil); code != http.StatusOK || !strings.Contains(string(body), `"notifications":[]`) {
		t.Errorf("log = %d %s", code, body)
	}
}

func TestPreferences(t *testing.T) {
	ctx := context.Background()
	acct := store.NewID("acct")
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(acct)}}}
	p := newPanel(t, owner)
	h, svc := p.h, p.svc
	code, body := do(t, h, http.MethodGet, "/api/v1/notifications/preferences", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"email":false`) {
		t.Fatalf("before = %d %s", code, body)
	}

	// Preferences change through the change pipeline, so they can be
	// previewed first, and are audited and undone like every change.
	prefs := notify.NotificationPreferences{Email: true, Events: []string{"*.failed"}, QuietHours: &notify.QuietHours{Start: "22:00", End: "07:00", TimeZone: "Europe/Madrid"}}
	code, body = do(t, h, http.MethodPut, "/api/v1/notifications/preferences?dryRun=true", prefs)
	if code != http.StatusOK || !strings.Contains(string(body), `"dryRun":true`) || !strings.Contains(string(body), `"kind":"core.notification_preferences"`) {
		t.Fatalf("preview = %d %s", code, body)
	}
	if stored, _ := svc.Preferences(ctx, owner.ID); stored.Email {
		t.Error("a preview changed the preferences")
	}
	code, body = do(t, h, http.MethodPut, "/api/v1/notifications/preferences", prefs)
	if code != http.StatusAccepted {
		t.Fatalf("set = %d %s", code, body)
	}
	if cs := p.wait(t, body); cs.State != changeset.StateApplied || cs.Actor.ID != owner.ID {
		t.Fatalf("set = %s %s by %v", cs.State, cs.Error, cs.Actor)
	}
	stored, err := svc.Preferences(ctx, owner.ID)
	if err != nil || !stored.Email || stored.QuietHours == nil || stored.UpdatedAt == nil {
		t.Errorf("stored = %+v, %v", stored, err)
	}
	// Changing them again updates the same resource.
	prefs.Muted = []string{"dns.*"}
	code, body = do(t, h, http.MethodPut, "/api/v1/notifications/preferences", prefs)
	if cs := p.wait(t, body); code != http.StatusAccepted || cs.State != changeset.StateApplied || cs.Changes[0].Op != changeset.OpUpdate {
		t.Fatalf("again = %d %s", code, body)
	}
	prefs.QuietHours.TimeZone = "Mars/Olympus"
	if code, body := do(t, h, http.MethodPut, "/api/v1/notifications/preferences", prefs); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "quietHours.timeZone") {
		t.Errorf("a bad time zone = %d %s", code, body)
	}

	// Nobody else changes them, whatever grants they carry.
	other := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI}
	req, err := svc.PreferencesRequest(ctx, owner.ID, notify.NotificationPreferences{})
	if err != nil {
		t.Fatal(err)
	}
	req.Actor = notify.PreferencesActor(other)
	if _, err := p.pipeline.Submit(ctx, req); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "belong to their person") {
		t.Errorf("someone else changing them: %v", err)
	}
	req.Actor = other
	if _, err := p.pipeline.Submit(ctx, req); !errors.Is(err, i18n.ErrForbidden) && !store.ErrNotFound.Is(err) {
		t.Errorf("someone else without the grant: %v", err)
	}
	// An account owner doesn't see the server's channels or their log.
	if code, _ := do(t, h, http.MethodGet, "/api/v1/notifications/types", nil); code != http.StatusForbidden {
		t.Errorf("types for an account owner = %d", code)
	}
	if code, _ := do(t, h, http.MethodGet, "/api/v1/notifications", nil); code != http.StatusForbidden {
		t.Errorf("log for an account owner = %d", code)
	}
	// Their own test notification is theirs to see.
	code, body = do(t, h, http.MethodPost, "/api/v1/notifications/preferences/test", nil)
	var n notify.Notification
	if err := json.Unmarshal(body, &n); code != http.StatusOK || err != nil || n.Person != owner.ID {
		t.Fatalf("test = %d %s", code, body)
	}
	if code, body := do(t, h, http.MethodGet, "/api/v1/notifications/"+string(n.ID), nil); code != http.StatusOK || !strings.Contains(string(body), "Test notification") {
		t.Errorf("own notification = %d %s", code, body)
	}

	// A token without a person has no preferences.
	token := &auth.Actor{Type: auth.ActorToken, ID: store.NewID("tok"), Source: auth.SourceAPI, Grants: owner.Grants}
	th, _ := server(t, token)
	if code, body := do(t, th, http.MethodGet, "/api/v1/notifications/preferences", nil); code != http.StatusForbidden || !strings.Contains(string(body), "notify.not_a_person") {
		t.Errorf("a token = %d %s", code, body)
	}
}
