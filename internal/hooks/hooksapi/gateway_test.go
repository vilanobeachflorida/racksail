package hooksapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/modules/modulestest"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

type boxSpec struct {
	Name  string `json:"name" doc:"The box's name"`
	Notes string `json:"notes,omitempty" doc:"What the box is for"`
}

// boxKind is the kind of the net module here, which an add-on may read
// some of.
var boxKind = store.Define[boxSpec, store.Status](store.KindInfo{
	Name: "net.box", Prefix: "box", Scope: store.ScopeServer, Title: "Box", Plural: "boxes", Description: "A box, for tests.",
})

// boxes is the net module with its boxes.
type boxes struct{ modules.Base }

func (boxes) Kinds() []store.Kind { return []store.Kind{boxKind} }

const readerManifest = `
format: 1
id: box-reader
name: Box reader
version: 0.1.0
license: Apache-2.0
source: https://example.com/box-reader
author: Example
description: Reads the names of boxes.
apiVersions: [v1]
permissions:
  hooks: []
  read: [{kind: net.box, fields: [name]}]
`

// Through the panel's own API, an add-on allowed the name of boxes reads
// their names and phases and nothing else, however it asks: whole, cut
// down to some fields, or by ID alone; and asking for the rest is refused,
// since whether an answer has it would say what it holds.
func TestTheGatewayKeepsToWhatAddonsMayRead(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "box-reader.sock"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(hooks.AddonHealth{Status: "ok", Version: "0.1.0", APIVersion: "v1"})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	var rt *addons.Runtime
	e := modulestest.New(t, modulestest.Options{
		Kinds: []store.Kind{addons.Addon, addons.AccountAddon},
		Reconcilers: func(svc *modules.Services) []reconcile.Reconciler {
			rt = addons.NewRuntime(addons.Options{
				Registry: svc.Hooks.Registry, Store: svc.Store, Kinds: svc.Kinds, Agent: svc.Agent, Events: svc.Events, Audit: svc.Audit,
				Tokens: addons.NewMemoryTokens(), SafeMode: addons.OpenSafeMode(filepath.Join(dir, "state"), nil),
				TokenDir: filepath.Join(dir, "tokens"), SocketDir: dir, Log: slog.New(slog.DiscardHandler),
			})
			svc.Addons = rt
			return rt.Reconcilers()
		},
	}, &boxes{modules.NewBase([]byte(`hardware: {arch: [amd64, arm64]}
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
`))})
	man, err := hooks.ParseAddonManifest([]byte(readerManifest))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(addons.AddonSpec{
		ID: "box-reader", Manifest: readerManifest, Enabled: true, Approved: man.Permissions,
		Package: addons.Package{URL: "https://example.com/box-reader-0.1.0.tar.gz", SHA256: strings.Repeat("ab", 32), Size: 1 << 20, Format: "tar.gz"},
	})
	e.MustApply(t, changeset.Request{Actor: e.Admin, Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: addons.Addon.Name(), Scope: store.ServerScope(), Spec: raw}}})
	cs := e.MustApply(t, changeset.Request{Actor: e.Admin, Changes: []changeset.Change{{
		Op: changeset.OpCreate, Kind: boxKind.Name(), Scope: store.ServerScope(), Labels: map[string]string{"team": "red"},
		Spec: json.RawMessage(`{"name":"b1","notes":"private"}`),
	}}})
	e.Settle(t)
	if _, _, _, _, ok := rt.Running("box-reader"); !ok {
		t.Fatalf("the add-on isn't running: %v", rt.Instances())
	}
	box := cs.Changes[0].ID
	if err := e.Store.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
		return tx.UpdateStatus(ctx, box, json.RawMessage(`{"phase":"ready","message":"Serving from /srv/private"}`))
	}); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(dir, "tokens", "box-reader.token"))
	if err != nil {
		t.Fatal(err)
	}
	g := &addons.Gateway{
		Runtime: rt, Store: e.Store,
		API: api.New(api.Options{Auth: addons.GatewayAuthenticator{}, Modules: e.Registry, Services: e.Services, Log: slog.New(slog.DiscardHandler)}).Handler(),
	}
	get := func(path string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, string(body)
	}

	for _, path := range []string{
		"/api/v1/net/boxes", "/api/v1/net/boxes/" + string(box), "/api/v1/resources/" + string(box),
		"/api/v1/net/boxes?fields=status", "/api/v1/net/boxes/" + string(box) + "?fields=status,spec.name",
		"/api/v1/resources/" + string(box) + "?fields=status.phase", "/api/v1/net/boxes?fields=id,kind",
	} {
		code, body := get(path)
		if code != http.StatusOK || !strings.Contains(body, string(box)) {
			t.Errorf("%s = %d %s", path, code, body)
			continue
		}
		for _, leak := range []string{"private", "team", "red"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s shows %s: %s", path, leak, body)
			}
		}
		if strings.Contains(path, "status") && !strings.Contains(body, `"phase":"ready"`) {
			t.Errorf("%s leaves out the phase: %s", path, body)
		}
	}
	for _, path := range []string{
		"/api/v1/net/boxes?fields=labels,status", "/api/v1/net/boxes/" + string(box) + "?fields=labels,status",
		"/api/v1/resources/" + string(box) + "?fields=labels,status", "/api/v1/net/boxes?fields=spec.notes",
		"/api/v1/resources/" + string(box) + "?fields=status.message",
	} {
		if code, body := get(path); code != http.StatusForbidden || !strings.Contains(body, "addons.fields_denied") {
			t.Errorf("%s = %d %s", path, code, body)
		}
	}
}
