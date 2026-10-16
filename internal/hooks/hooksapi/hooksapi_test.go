package hooksapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/hooks/rules"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type module struct{ modules.Base }

func newEnv(t *testing.T) (*modulestest.Env, http.Handler) {
	t.Helper()
	net := &module{modules.NewBase([]byte(`hardware: {arch: [amd64, arm64]}
id: net
name: Net
description: A module for tests.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
required: true
dependencies: {}
cost: {idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10}
upstream: []
`))}
	e := modulestest.New(t, modulestest.Options{}, net)
	e.Services.Rules = rules.NewEngine(rules.Options{Registry: e.Hooks.Registry})
	tokens := addons.NewMemoryTokens()
	e.Services.Addons = addons.NewRuntime(addons.Options{
		Registry: e.Hooks.Registry, Store: e.Store, Agent: e.Agent, Events: e.Events, Audit: e.Events, Tokens: tokens,
		SafeMode: addons.OpenSafeMode(t.TempDir(), nil), TokenDir: t.TempDir(), SocketDir: t.TempDir(), Log: slog.New(slog.DiscardHandler),
	})
	srv := api.New(api.Options{Dev: true, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)})
	return e, srv.Handler()
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

func TestHooksAndBreaker(t *testing.T) {
	e, h := newEnv(t)
	code, body := do(t, h, http.MethodGet, "/api/v1/hooks/points?prefix=core.os_upgrade.", nil)
	if code != http.StatusOK || !strings.Contains(string(body), "core.os_upgrade.check") || strings.Contains(string(body), "core.boot") {
		t.Fatalf("points = %d %s", code, body)
	}
	// A hook that keeps failing is switched off, then switched back on.
	err := e.Hooks.Registry.Register(hooks.Registration{Point: hooks.CoreBoot, Kind: hooks.Observe, Owner: "module:net", OnObserve: func(ctx context.Context, c *hooks.Context) {
		panic("broken")
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Observers run side by side, so each failure is waited for.
	for i := range 5 {
		e.Hooks.Notify(context.Background(), hooks.CoreBoot, auth.System("test"), nil)
		deadline := time.Now().Add(5 * time.Second)
		for e.Hooks.Registry.Failures("module:net", hooks.CoreBoot) < i+1 && len(e.Hooks.Registry.Disabled()) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	code, body = do(t, h, http.MethodGet, "/api/v1/hooks", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"disabled"`) {
		t.Fatalf("hooks = %d %s", code, body)
	}
	if len(e.Hooks.Registry.Disabled()) != 1 {
		t.Fatalf("the breaker didn't trip: %+v", e.Hooks.Registry.Disabled())
	}
	code, body = do(t, h, http.MethodPost, "/api/v1/hooks/enable", map[string]any{"owner": "module:net", "point": hooks.CoreBoot})
	if code != http.StatusNoContent || len(e.Hooks.Registry.Disabled()) != 0 {
		t.Errorf("enable = %d %s", code, body)
	}
	if code, body := do(t, h, http.MethodPost, "/api/v1/hooks/enable", map[string]any{"owner": "module:net", "point": hooks.CoreBoot}); code != http.StatusConflict || !strings.Contains(string(body), "hooks.not_disabled") {
		t.Errorf("enable again = %d %s", code, body)
	}
}

func TestRuleTester(t *testing.T) {
	_, h := newEnv(t)
	req := rules.TestRequest{
		Rule: hooks.PolicySpec{Name: "No test domains", Type: hooks.PolicyValidate, Kinds: []string{"web.site"}, Operations: []string{"create"},
			Expression: `resource.spec.domain.endsWith(".test")`, Message: "Test domains aren't allowed.", Enabled: true},
		Owner: store.ServerScope(), Operation: "create", Kind: "web.site", Scope: store.ServerScope(), Spec: json.RawMessage(`{"domain":"shop.test"}`),
	}
	code, body := do(t, h, http.MethodPost, "/api/v1/rules/test", req)
	var res rules.TestResult
	if err := json.Unmarshal(body, &res); code != http.StatusOK || err != nil || !res.Applies || !res.Refused || res.Message != "Test domains aren't allowed." {
		t.Fatalf("test = %d %s", code, body)
	}
	req.Rule.Expression = "this isn't CEL"
	if code, body := do(t, h, http.MethodPost, "/api/v1/rules/test", req); code != http.StatusUnprocessableEntity {
		t.Errorf("a rule that doesn't compile = %d %s", code, body)
	}
}

func TestSafeMode(t *testing.T) {
	_, h := newEnv(t)
	code, body := do(t, h, http.MethodGet, "/api/v1/safe-mode", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"active":false`) {
		t.Fatalf("status = %d %s", code, body)
	}
	code, body = do(t, h, http.MethodPost, "/api/v1/safe-mode", map[string]string{"reason": "An add-on misbehaves."})
	if code != http.StatusOK || !strings.Contains(string(body), `"active":true`) || !strings.Contains(string(body), "An add-on misbehaves.") {
		t.Fatalf("enter = %d %s", code, body)
	}
	code, body = do(t, h, http.MethodDelete, "/api/v1/safe-mode", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"active":false`) {
		t.Errorf("leave = %d %s", code, body)
	}
}

func TestAddonEntriesAndRoutes(t *testing.T) {
	_, h := newEnv(t)
	code, body := do(t, h, http.MethodGet, "/api/v1/addons/entries", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"entries":[]`) {
		t.Errorf("entries = %d %s", code, body)
	}
	// An add-on that doesn't run is nowhere to be found.
	if code, body := do(t, h, http.MethodGet, "/api/v1/addons/site-guard/reports", nil); code != http.StatusNotFound {
		t.Errorf("routes of an add-on that doesn't run = %d %s", code, body)
	}
	if code, _ := do(t, h, http.MethodGet, "/api/v1/addons/site-guard/ui/index.html", nil); code != http.StatusNotFound {
		t.Errorf("screens of an add-on that doesn't run = %d", code)
	}
	for pattern, matches := range map[string]map[string]bool{
		"/reports":        {"/reports": true, "/reports/": true, "/reports/1": false},
		"/reports/{id}":   {"/reports/1": true, "/reports": false, "/reports/1/x": false},
		"/sites/{id}/log": {"/sites/s1/log": true, "/sites//log": false},
	} {
		for path, want := range matches {
			if got := routeMatches(pattern, path); got != want {
				t.Errorf("routeMatches(%s, %s) = %v", pattern, path, got)
			}
		}
	}
}

func TestProblems(t *testing.T) {
	rec := httptest.NewRecorder()
	problem(rec, errors.New("secret detail"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret detail") {
		t.Errorf("an error without a code becomes core.internal and hides its text: %d %s", rec.Code, rec.Body)
	}
}
