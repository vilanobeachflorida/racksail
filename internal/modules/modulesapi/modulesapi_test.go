package modulesapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulesapi"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
)

func manifest(id, extra string) []byte {
	return []byte(`hardware: {arch: [amd64, arm64]}
id: ` + id + `
name: ` + strings.ToUpper(id[:1]) + id[1:] + `
description: A module for tests.
version: 0.1.0
license: AGPL-3.0-or-later
category: operations
profiles: [all-in-one]
cost: {idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10}
upstream: []
` + extra)
}

type module struct{ modules.Base }

func do(t *testing.T, h http.Handler, method, path string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, body
}

func TestModulesAPI(t *testing.T) {
	net := &module{modules.NewBase(manifest("net", "required: true\ndependencies: {}\n"))}
	notes := &module{modules.NewBase(manifest("notes", "dependencies: {requires: [net]}\n"))}
	e := modulestest.New(t, modulestest.Options{}, net, notes)
	srv := api.New(api.Options{Dev: true, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)})
	h := srv.Handler()

	code, body := do(t, h, http.MethodGet, "/api/v1/modules")
	var list struct {
		Modules []modules.ModuleInfo `json:"modules"`
	}
	if err := json.Unmarshal(body, &list); code != http.StatusOK || err != nil || len(list.Modules) != 2 {
		t.Fatalf("list = %d %s", code, body)
	}
	states := map[string]modules.State{}
	for _, m := range list.Modules {
		states[m.Manifest.ID] = m.State
	}
	if states["net"] != modules.StateOn || states["notes"] != modules.StateOff {
		t.Errorf("states = %v", states)
	}
	if code, body := do(t, h, http.MethodGet, "/api/v1/modules/notes"); code != http.StatusOK || !strings.Contains(string(body), `"resource":"mod_`) {
		t.Errorf("get = %d %s", code, body)
	}
	if code, body := do(t, h, http.MethodGet, "/api/v1/modules/nope"); code != http.StatusNotFound || !strings.Contains(string(body), "modules.unknown") {
		t.Errorf("unknown module = %d %s", code, body)
	}
	code, body = do(t, h, http.MethodGet, "/api/v1/modules/profiles")
	var profiles struct {
		Profiles []modulesapi.Profile `json:"profiles"`
	}
	json.Unmarshal(body, &profiles)
	if code != http.StatusOK || len(profiles.Profiles) == 0 || profiles.Profiles[0].ID != modules.AllInOne || strings.Join(profiles.Profiles[0].Modules, ",") != "net,notes" {
		t.Errorf("profiles = %d %s", code, body)
	}

	// A preview changes nothing; applying turns the profile's modules on.
	code, body = do(t, h, http.MethodPost, "/api/v1/modules/profiles/all-in-one?dryRun=true")
	if code != http.StatusOK || !strings.Contains(string(body), `"dryRun":true`) {
		t.Fatalf("preview = %d %s", code, body)
	}
	if e.Manager.State("notes") != modules.StateOff {
		t.Error("a preview turned a module on")
	}
	code, body = do(t, h, http.MethodPost, "/api/v1/modules/profiles/all-in-one")
	if code != http.StatusAccepted {
		t.Fatalf("apply = %d %s", code, body)
	}
	e.Settle(t)
	if e.Manager.State("notes") != modules.StateOn {
		t.Errorf("notes is %s", e.Manager.State("notes"))
	}
	if code, body := do(t, h, http.MethodPost, "/api/v1/modules/profiles/all-in-one"); code != http.StatusConflict || !strings.Contains(string(body), "modules.nothing_to_turn_on") {
		t.Errorf("again = %d %s", code, body)
	}
}
