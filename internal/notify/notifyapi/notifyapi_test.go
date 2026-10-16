package notifyapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/notify/notifyapi"
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

func server(t *testing.T, actor *auth.Actor) (http.Handler, *notify.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{notify.ChannelKind, notify.RelayKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st := store.NewMemory(kinds)
	log := events.NewMemory(100)
	svc, err := notify.NewSQL(ctx, db, notify.Options{Store: st, Log: log, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	services := &modules.Services{Store: st, Kinds: kinds, Events: log, Audit: log, Authorizer: auth.NewRoleAuthorizer(nil), Notify: svc}
	srv := api.New(api.Options{Auth: signedIn{actor}, Services: services, Log: slog.New(slog.DiscardHandler)})
	return srv.Handler(), svc
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
	acct := store.NewID("acct")
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(acct)}}}
	h, svc := server(t, owner)
	code, body := do(t, h, http.MethodGet, "/api/v1/notifications/preferences", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"email":false`) {
		t.Fatalf("before = %d %s", code, body)
	}
	prefs := notify.NotificationPreferences{Email: true, Events: []string{"*.failed"}, QuietHours: &notify.QuietHours{Start: "22:00", End: "07:00", TimeZone: "Europe/Madrid"}}
	if code, body := do(t, h, http.MethodPut, "/api/v1/notifications/preferences", prefs); code != http.StatusOK {
		t.Fatalf("set = %d %s", code, body)
	}
	stored, err := svc.Preferences(context.Background(), owner.ID)
	if err != nil || !stored.Email || stored.QuietHours == nil || stored.UpdatedAt == nil {
		t.Errorf("stored = %+v, %v", stored, err)
	}
	prefs.QuietHours.TimeZone = "Mars/Olympus"
	if code, body := do(t, h, http.MethodPut, "/api/v1/notifications/preferences", prefs); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "quietHours.timeZone") {
		t.Errorf("a bad time zone = %d %s", code, body)
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
