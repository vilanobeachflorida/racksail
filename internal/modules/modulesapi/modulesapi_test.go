package modulesapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulesapi"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
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

type noteSpec struct {
	Text string `json:"text"`
	Key  string `json:"key,omitempty"`
}

var noteKind = store.Define[noteSpec, store.Status](store.KindInfo{
	Name: "notes.note", Prefix: "note", Scope: store.ScopeServer, Title: "Note", Plural: "notes", Description: "A note.",
	UI: store.UIHints{Fields: map[string]store.FieldHint{"key": {Sensitive: true}}},
})

type notesModule struct{ modules.Base }

func (notesModule) Kinds() []store.Kind { return []store.Kind{noteKind} }

func init() {
	sim.RegisterPackage("notesd", sim.PackageInfo{Version: "1.2-1", DownloadBytes: 3 << 20, InstalledBytes: 9 << 20})
}

func send(t *testing.T, h http.Handler, method, path string, body any) (int, http.Header, []byte) {
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
	return rec.Code, rec.Result().Header, out
}

// Turning a module on with what it needs, turning it off, saying what it
// installs, exporting its resources and removing it, each through the API.
func TestModuleTransitionsAPI(t *testing.T) {
	ctx := context.Background()
	net := &module{modules.NewBase(manifest("net", "required: true\ndependencies: {}\n"))}
	notes := &notesModule{modules.NewBase([]byte(strings.Replace(string(manifest("notes", "dependencies: {requires: [net]}\n")), "upstream: []\n",
		"upstream:\n  - {id: notesd, name: Notesd, license: MIT, source: {type: debian, packages: [notesd]}, install: With the first note.}\n", 1)))}
	shop := &module{modules.NewBase(manifest("shop", "dependencies: {requires: [notes]}\n"))}
	e := modulestest.New(t, modulestest.Options{}, net, notes, shop)
	h := api.New(api.Options{Dev: true, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)}).Handler()

	// Shop needs notes, which is turned on with it, first.
	code, _, body := send(t, h, http.MethodPost, "/api/v1/modules/shop/actions/turn_on?dryRun=true", nil)
	var preview changeset.ChangeSet
	if err := json.Unmarshal(body, &preview); code != http.StatusOK || err != nil || len(preview.Changes) != 2 || preview.Title != "Turn on Shop and Notes" {
		t.Fatalf("preview = %d %s", code, body)
	}
	if !strings.Contains(string(body), `"action":"download_on_first_use","target":"Notesd"`) || preview.Estimate.DownloadBytes != 3<<20 {
		t.Errorf("the preview lists what notes installs once used: %s", body)
	}
	if code, _, body := send(t, h, http.MethodPost, "/api/v1/modules/shop/actions/turn_on", nil); code != http.StatusAccepted {
		t.Fatalf("turn on = %d %s", code, body)
	}
	e.Settle(t)
	if !e.Manager.State("notes").IsOn() || !e.Manager.State("shop").IsOn() {
		t.Fatalf("states = %v", e.Manager.States())
	}

	code, _, body = send(t, h, http.MethodGet, "/api/v1/modules/notes/software", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"downloadBytes":3145728`) || !strings.Contains(string(body), `"installed":false`) {
		t.Errorf("software = %d %s", code, body)
	}

	e.MustApply(t, changeset.Request{Changes: []changeset.Change{{
		Op: changeset.OpCreate, Kind: "notes.note", Scope: store.ServerScope(), Spec: modulestest.JSON(t, noteSpec{Text: "remember"}),
		Secrets: map[string]string{"key": "s3cret"},
	}}})
	code, header, body := send(t, h, http.MethodGet, "/api/v1/modules/notes/export", nil)
	var export modules.Export
	if err := json.Unmarshal(body, &export); code != http.StatusOK || err != nil || len(export.Resources) != 1 {
		t.Fatalf("export = %d %s", code, body)
	}
	if r := export.Resources[0]; !strings.Contains(string(r.Spec), "remember") || strings.Contains(string(body), "s3cret") || strings.Contains(string(r.Spec), "key") || len(r.Withheld) != 1 {
		t.Errorf("the export keeps the settings and leaves secrets out: %s", body)
	}
	if !strings.HasPrefix(header.Get("Content-Disposition"), `attachment; filename="racksail-notes-`) {
		t.Errorf("disposition = %q", header.Get("Content-Disposition"))
	}

	// Removing needs the name typed, and what needs the module off first.
	if code, _, body := send(t, h, http.MethodPost, "/api/v1/modules/notes/actions/remove", modules.RemoveInput{Confirm: "nots"}); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "modules.confirm") {
		t.Errorf("a typo = %d %s", code, body)
	}
	if code, _, body := send(t, h, http.MethodPost, "/api/v1/modules/notes/actions/remove?dryRun=true", modules.RemoveInput{Confirm: "Notes"}); code != http.StatusConflict || !strings.Contains(string(body), "modules.in_use") {
		t.Errorf("while shop needs it = %d %s", code, body)
	}
	if code, _, body := send(t, h, http.MethodPost, "/api/v1/modules/shop/actions/turn_off", nil); code != http.StatusAccepted {
		t.Fatalf("turn off = %d %s", code, body)
	}
	e.Settle(t)
	code, _, body = send(t, h, http.MethodPost, "/api/v1/modules/notes/actions/remove?dryRun=true", modules.RemoveInput{Confirm: "Notes"})
	var removal changeset.ChangeSet
	if err := json.Unmarshal(body, &removal); code != http.StatusOK || err != nil || len(removal.Changes) != 2 ||
		removal.Changes[0].Op != changeset.OpDelete || removal.Changes[0].Kind != "notes.note" || removal.Changes[1].Action != modules.ActionRemove {
		t.Fatalf("the removal deletes the note and removes the module = %d %s", code, body)
	}
	if code, _, body := send(t, h, http.MethodPost, "/api/v1/modules/notes/actions/remove", modules.RemoveInput{Confirm: "Notes"}); code != http.StatusAccepted {
		t.Fatalf("remove = %d %s", code, body)
	}
	e.Settle(t)
	if objs, _, _ := noteKind.List(ctx, e.Store, store.Query{}); len(objs) != 0 || e.Manager.State("notes") != modules.StateOff {
		t.Errorf("after removing: %d notes, notes is %s", len(objs), e.Manager.State("notes"))
	}
	if code, _, body := send(t, h, http.MethodGet, "/api/v1/modules/notes", nil); code != http.StatusOK || !strings.Contains(string(body), `"removed":true`) {
		t.Errorf("notes = %d %s", code, body)
	}
}

// sendKeyed sends a request with an Idempotency-Key and decodes the change
// set it answers with, when it answers with one.
func sendKeyed(t *testing.T, h http.Handler, path, key string, body any) (int, *changeset.ChangeSet, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(http.MethodPost, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	var cs changeset.ChangeSet
	if json.Unmarshal(out, &cs) != nil || cs.ID == "" {
		return rec.Code, nil, out
	}
	return rec.Code, &cs, out
}

// A retry of turning a module on or off, removing it or turning a profile
// on, with the first request's Idempotency-Key, answers with the first
// change set even once it was applied, when the module's state would refuse
// the request or ask for something else.
func TestRetriesGetTheFirstChangeSet(t *testing.T) {
	net := &module{modules.NewBase(manifest("net", "required: true\ndependencies: {}\n"))}
	notes := &module{modules.NewBase(manifest("notes", "dependencies: {requires: [net]}\n"))}
	vms := &module{modules.NewBase([]byte(strings.Replace(string(manifest("vms", "dependencies: {}\n")),
		"hardware: {arch: [amd64, arm64]}", "hardware: {arch: [amd64, arm64], features: [virtualization]}", 1)))}
	e := modulestest.New(t, modulestest.Options{Machine: sim.New(sim.Options{NoVirtualization: true})}, net, notes, vms)
	h := api.New(api.Options{Dev: true, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)}).Handler()

	steps := []struct {
		name, path, key string
		body            any
		on              bool
		refused         string
	}{
		{"turning on", "/api/v1/modules/notes/actions/turn_on", "on-1", nil, true, "modules.already_on"},
		{"turning off", "/api/v1/modules/notes/actions/turn_off", "off-1", nil, false, "modules.already_off"},
		{"turning the profile on", "/api/v1/modules/profiles/all-in-one", "profile-1", nil, true, "modules.nothing_to_turn_on"},
		{"turning off after the profile", "/api/v1/modules/notes/actions/turn_off", "off-2", nil, false, "modules.already_off"},
		{"removing", "/api/v1/modules/notes/actions/remove", "remove-1", modules.RemoveInput{Confirm: "Notes"}, false, ""},
	}
	for _, step := range steps {
		code, first, body := sendKeyed(t, h, step.path, step.key, step.body)
		if step.path == "/api/v1/modules/profiles/all-in-one" {
			first = profileChange(t, body)
		}
		if code != http.StatusAccepted || first == nil {
			t.Fatalf("%s = %d %s", step.name, code, body)
		}
		e.Settle(t)
		if e.Manager.State("notes").IsOn() != step.on {
			t.Fatalf("after %s notes is %s", step.name, e.Manager.State("notes"))
		}
		code, again, body := sendKeyed(t, h, step.path, step.key, step.body)
		if step.path == "/api/v1/modules/profiles/all-in-one" {
			again = profileChange(t, body)
			// The answer is the first one whole: the modules the machine
			// can't run are still named.
			if !strings.Contains(string(body), `"module":"vms"`) {
				t.Errorf("a retried profile names what it left out: %s", body)
			}
		}
		if code != http.StatusAccepted || again == nil || again.ID != first.ID {
			t.Errorf("%s again with the same key = %d %s, want the first change set %s", step.name, code, body, first.ID)
		}
		if step.refused == "" {
			continue
		}
		// The key answers retries only: another request is checked as ever,
		// and a dry run takes no key.
		if code, _, body := sendKeyed(t, h, step.path, step.key+"-other", step.body); code != http.StatusConflict || !strings.Contains(string(body), step.refused) {
			t.Errorf("%s with another key = %d %s", step.name, code, body)
		}
		if code, _, body := sendKeyed(t, h, step.path+"?dryRun=true", step.key, step.body); code != http.StatusConflict || !strings.Contains(string(body), step.refused) {
			t.Errorf("a preview of %s with the key = %d %s", step.name, code, body)
		}
	}
	if e.Manager.State("notes") != modules.StateOff {
		t.Errorf("notes is %s", e.Manager.State("notes"))
	}
}

// profileChange reads the change set out of the answer of turning a profile
// on.
func profileChange(t *testing.T, body []byte) *changeset.ChangeSet {
	t.Helper()
	var out modulesapi.ProfileChange
	if json.Unmarshal(body, &out) != nil || out.ChangeSet == nil || out.ChangeSet.ID == "" {
		return nil
	}
	return out.ChangeSet
}
