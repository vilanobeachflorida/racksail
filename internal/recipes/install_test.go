package recipes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"golang.org/x/crypto/blake2b"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore/sigstoretest"
)

// site answers the health checks of the installs below.
func healthServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	return srv
}

func checkedRecipe(srv *httptest.Server) string {
	return wordpress + "health:\n  - {type: resource, target: site}\n  - {type: resource, target: wordpress}\n  - {type: http, target: \"" + srv.URL + "/?for=${inputs.domain}\", expect: \"200\"}\n" +
		"upgrade: {strategy: in-place}\nuninstall: {keepData: true}\n"
}

func TestInstallUpgradeUninstall(t *testing.T) {
	ctx := context.Background()
	srv := healthServer(t)
	c := newCatalogServer(t)
	c.add(checkedRecipe(srv), nil)
	p := newPanel(t, nil)
	cat := p.addCatalog(t, c, c.srv.URL)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	acct := p.newAccount(t)
	in := InstallInput{Source: Source{Catalog: cat, Item: "wordpress"}, Scope: acct, Inputs: map[string]any{"domain": "blog.example.com", "dbPassword": "correct horse"}}
	req, err := p.recipes.InstallRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// The installation comes first, so its upstream files are checked before
	// anything else applies, and its health checks last.
	if len(req.Changes) != 4 || req.Changes[0].Kind != InstallationKind.Name() || req.Changes[0].Op != changeset.OpCreate || req.Title != "Install WordPress 6.8.3" {
		t.Fatalf("request = %s", mustJSON(req))
	}
	if last := req.Changes[3]; last.Op != changeset.OpAction || last.Action != "check" || last.ID != req.Changes[0].ID || string(last.Input) != `{"wait":true}` {
		t.Errorf("the install ends with its health checks: %s", mustJSON(last))
	}
	var spec InstallationSpec
	json.Unmarshal(req.Changes[0].Spec, &spec)
	if spec.Catalog != cat || spec.RecipeSHA256 == "" || spec.IndexSHA256 == "" || spec.Inputs["dbPassword"] != nil || spec.Inputs["domain"] != "blog.example.com" || len(spec.Resources) != 2 {
		t.Errorf("provenance = %+v", spec)
	}
	if req.Changes[1].Labels["racksail.com/installation"] != string(req.Changes[0].ID) {
		t.Error("resources are labelled with their installation")
	}
	cs := p.submit(t, req)
	if cs.State != changeset.StateApplied {
		t.Fatalf("install: %s %s", cs.State, mustJSON(cs.Error))
	}
	id := req.Changes[0].ID
	inst, err := InstallationKind.Get(ctx, p.store, id)
	if err != nil || inst.Status.Applied != "6.8.3" || inst.Status.Phase != store.PhaseReady {
		t.Fatalf("installation = %+v, %v", inst, err)
	}
	out, err := p.recipes.Check(ctx, id)
	if err != nil || !out.Healthy || len(out.Health) != 3 {
		t.Fatalf("check = %+v, %v", out, err)
	}
	db := spec.Resources[0].ID
	dbBefore, _ := p.store.Get(ctx, db)

	// Version 6.9.0 adds a cache in front of the site.
	v2 := strings.Replace(checkedRecipe(srv), `version: "6.8.3"`, `version: "6.9.0"`, 1) +
		""
	v2 = strings.Replace(v2, "resources:\n", "resources:\n  - name: cache\n    kind: web.site\n    dependsOn: [db]\n    spec: {domains: [\"cache.${inputs.domain}\"], php: \"8.4\", workers: 1, database: \"${resources.db.id}\"}\n", 1)
	c.add(v2, nil)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	p.recipes.findUpgrade(ctx, InstallationKind, id)
	if inst, _ := InstallationKind.Get(ctx, p.store, id); inst.Status.Upgrade != "6.9.0" {
		t.Errorf("the catalog's newer version is noticed: %+v", inst.Status)
	}
	up, err := p.recipes.UpgradeRequest(ctx, id, UpgradeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if up.Changes[0].Kind != InstallationKind.Name() || up.Changes[0].Op != changeset.OpUpdate {
		t.Errorf("the installation comes first: %s", mustJSON(up.Changes[0]))
	}
	var ops []string
	for _, ch := range up.Changes[1:] {
		ops = append(ops, string(ch.Op)+" "+ch.Kind)
		if ch.ID == db && len(ch.Secrets) > 0 {
			t.Error("a password given at install isn't asked for again, and isn't replaced")
		}
	}
	if strings.Join(ops, ",") != "update databases.database,create web.site,update web.site,action core.installation" {
		t.Errorf("upgrade changes = %v", ops)
	}
	if cs := p.submit(t, up); cs.State != changeset.StateApplied {
		t.Fatalf("upgrade: %s %s", cs.State, mustJSON(cs.Error))
	}
	dbAfter, _ := p.store.Get(ctx, db)
	var before, after databaseSpec
	json.Unmarshal(dbBefore.Spec, &before)
	json.Unmarshal(dbAfter.Spec, &after)
	if after.Owner.Password == "" || after.Owner.Password != before.Owner.Password {
		t.Errorf("the database keeps its password reference: %q then %q", before.Owner.Password, after.Owner.Password)
	}
	inst, _ = InstallationKind.Get(ctx, p.store, id)
	if inst.Spec.Version != "6.9.0" || inst.Status.Applied != "6.9.0" || len(inst.Spec.Resources) != 3 {
		t.Errorf("after the upgrade = %+v %+v", inst.Spec, inst.Status)
	}
	if _, err := p.recipes.UpgradeRequest(ctx, id, UpgradeInput{}); !errors.Is(err, ErrNoUpgrade) {
		t.Errorf("nothing newer: %v", err)
	}

	// Uninstalling keeps the database, as the recipe says, detached.
	un, err := p.recipes.UninstallRequest(ctx, id, UninstallInput{})
	if err != nil {
		t.Fatal(err)
	}
	if cs := p.submit(t, un); cs.State != changeset.StateApplied {
		t.Fatalf("uninstall: %s %s", cs.State, mustJSON(cs.Error))
	}
	kept, err := p.store.Get(ctx, db)
	if err != nil || kept.Labels["racksail.com/kept-from"] != "wordpress" {
		t.Errorf("the database is kept: %+v, %v", kept, err)
	}
	for _, res := range inst.Spec.Resources {
		if res.Kind == "web.site" {
			if r, err := p.store.Get(ctx, res.ID); err == nil && !r.Deleted() {
				t.Errorf("site %s is gone", res.Name)
			}
		}
	}
	if r, err := p.store.Get(ctx, id); err == nil && !r.Deleted() {
		t.Error("the installation is gone")
	}
}

// An upstream file that isn't what the recipe pins refuses the install
// before anything of it is applied, and one that is gets recorded.
func TestUpstreamFilesAreCheckedFirst(t *testing.T) {
	ctx := context.Background()
	payload := []byte("wordpress 6.8.3 release")
	sum := sha256.Sum256(payload)
	var mu sync.Mutex
	served := payload
	serve := func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		served = data
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/wordpress.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Write(served)
	}))
	defer upstream.Close()
	srv := healthServer(t)
	recipe := checkedRecipe(srv) + "downloads:\n  - {url: \"" + upstream.URL + "/wordpress.tar.gz\", sha256: " + hex.EncodeToString(sum[:]) + ", size: " + strconv.Itoa(len(payload)) + "}\n"
	c := newCatalogServer(t)
	c.add(recipe, nil)
	p := newPanel(t, func(o *EngineOptions) { o.Verifier = &Verifier{HTTP: upstream.Client(), Dir: t.TempDir()} })
	cat := p.addCatalog(t, c, c.srv.URL)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	install := func() (*changeset.ChangeSet, changeset.Request) {
		t.Helper()
		req, err := p.recipes.InstallRequest(ctx, InstallInput{
			Source: Source{Catalog: cat, Item: "wordpress"}, Scope: p.newAccount(t),
			Inputs: map[string]any{"domain": "blog.example.com", "dbPassword": "correct horse"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return p.submit(t, req), req
	}

	// The preview says which files are checked.
	req, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "wordpress"}, Scope: p.newAccount(t), Inputs: map[string]any{"domain": "a.example", "dbPassword": "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	req.Actor, req.DryRun = p.admin, true
	preview, err := p.engine.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(preview.Effects)), `"action":"verify"`) || preview.Estimate.DownloadBytes != int64(len(payload)) {
		t.Errorf("the preview lists the files checked: %s", mustJSON(preview.Effects))
	}

	// Upstream changed the file: nothing is applied.
	serve([]byte("something else entirely"))
	cs, req := install()
	if cs.State != changeset.StateFailed || !strings.Contains(cs.Error, "Nothing of WordPress 6.8.3 was installed") {
		t.Fatalf("a changed file: %s %s", cs.State, cs.Error)
	}
	for _, ch := range cs.Changes[1:] {
		if ch.State == changeset.ChangeDone {
			t.Errorf("%s %s was applied although its upstream file changed", ch.Op, ch.Kind)
		}
		if ch.Op == changeset.OpCreate {
			if r, err := p.store.Get(ctx, ch.ID); err == nil {
				var st store.Status
				json.Unmarshal(r.Status, &st)
				if st.Phase == store.PhaseReady {
					t.Errorf("%s is in place", ch.Kind)
				}
			}
		}
	}
	inst, _ := InstallationKind.Get(ctx, p.store, req.Changes[0].ID)
	if inst.Status.Phase != store.PhaseFailed || inst.Status.Applied != "" {
		t.Errorf("the installation says so: %+v", inst.Status)
	}

	// The file it pins installs, and is recorded.
	serve(payload)
	cs, req = install()
	if cs.State != changeset.StateApplied {
		t.Fatalf("install: %s %s", cs.State, cs.Error)
	}
	inst, _ = InstallationKind.Get(ctx, p.store, req.Changes[0].ID)
	if len(inst.Status.Downloads) != 1 || inst.Status.Downloads[0].SHA256 != hex.EncodeToString(sum[:]) || inst.Status.Applied != "6.8.3" || len(inst.Status.Health) != 3 {
		t.Errorf("installation = %+v", inst.Status)
	}
}

// An install whose health checks don't pass fails, once they had their
// time.
func TestInstallsEndWithTheirHealthChecks(t *testing.T) {
	ctx := context.Background()
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer sick.Close()
	recipe := strings.Replace(checkedRecipe(sick), `expect: "200"}`, `expect: "200", timeoutSeconds: 1}`, 1)
	c := newCatalogServer(t)
	c.add(recipe, nil)
	p := newPanel(t, nil)
	cat := p.addCatalog(t, c, c.srv.URL)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	req, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "wordpress"}, Scope: p.newAccount(t), Inputs: map[string]any{"domain": "a.example", "dbPassword": "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	cs := p.submit(t, req)
	if cs.State != changeset.StateFailed || !strings.Contains(cs.Error, "WordPress check 3") {
		t.Fatalf("an install that doesn't work: %s %s", cs.State, cs.Error)
	}
	inst, _ := InstallationKind.Get(ctx, p.store, req.Changes[0].ID)
	if inst.Status.Phase != store.PhaseDegraded || len(inst.Status.Health) != 3 || inst.Status.Health[2].OK {
		t.Errorf("the installation records its health: %+v", inst.Status)
	}
}

func TestUninstallKeepsDataByDefault(t *testing.T) {
	for _, c := range []struct {
		uninstall string
		want      bool
	}{
		{"", true},
		{"uninstall: {keepData: true}\n", true},
		{"uninstall: {keepData: false, notes: Nothing in it is worth keeping.}\n", false},
	} {
		r, err := Parse([]byte(wordpress + "health:\n  - {type: resource, target: site}\n" + c.uninstall))
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Uninstall.Keeps(); got != c.want {
			t.Errorf("%q keeps data: %v, want %v", c.uninstall, got, c.want)
		}
	}
}

func TestInstallRefusals(t *testing.T) {
	ctx := context.Background()
	c := newCatalogServer(t)
	c.add(strings.NewReplacer("id: wordpress", "id: paid", "licenseClass: open-source", "licenseClass: proprietary", "license: GPL-2.0-or-later", "license: LicenseRef-Paid").Replace(wordpress), nil)
	c.add(strings.NewReplacer("id: wordpress", "id: big", "requires: {modules: [web, databases]}", "requires: {modules: [web, databases], memoryMB: 999999999}").Replace(wordpress), nil)
	c.add(strings.NewReplacer("id: wordpress", "id: mail", "requires: {modules: [web, databases]}", "requires: {modules: [mail]}").Replace(wordpress), nil)
	c.add(strings.Replace(wordpress, "id: wordpress", "id: flaky", 1), func(it *IndexItem) { it.Broken = true })
	p := newPanel(t, func(o *EngineOptions) {
		o.Facts = nil
	})
	cat := p.addCatalog(t, c, c.srv.URL)
	if _, err := p.catalogs.Sync(ctx, cat); err != nil {
		t.Fatal(err)
	}
	acct := p.newAccount(t)
	inputs := map[string]any{"domain": "a.example", "dbPassword": "pw"}
	if _, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "paid"}, Scope: acct, Inputs: inputs}); !errors.Is(err, ErrLicenseNotice) {
		t.Errorf("proprietary software needs its license accepted: %v", err)
	}
	if _, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "paid"}, Scope: acct, Inputs: inputs, AcceptLicense: true}); err != nil {
		t.Errorf("accepted: %v", err)
	}
	if _, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "mail"}, Scope: acct, Inputs: inputs}); !errors.Is(err, ErrModuleOff) {
		t.Errorf("a module that is off: %v", err)
	}
	if _, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "flaky"}, Scope: acct, Inputs: inputs}); !errors.Is(err, ErrBroken) {
		t.Errorf("a broken item: %v", err)
	}
	if _, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Catalog: cat, Item: "wordpress"}, Scope: store.Scope{Level: store.ScopeReseller, ID: store.NewID("rsl")}, Inputs: inputs}); !errors.Is(err, ErrScope) {
		t.Errorf("into a reseller: %v", err)
	}
	if _, err := p.recipes.InstallRequest(ctx, InstallInput{Source: Source{Item: "wordpress"}, Scope: acct}); !errors.Is(err, ErrNoRecipeSource) {
		t.Errorf("no source: %v", err)
	}
}

func TestVerifyDownloads(t *testing.T) {
	ctx := context.Background()
	payload := []byte("upstream release tarball")
	sum := sha256.Sum256(payload)
	files := map[string][]byte{"/app.tar.gz": payload}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	d := Download{URL: srv.URL + "/app.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}
	v := &Verifier{HTTP: srv.Client(), Dir: t.TempDir()}
	if got, err := v.Verify(ctx, d); err != nil || got.SHA256 != d.SHA256 {
		t.Fatalf("plain checksum: %+v, %v", got, err)
	}
	changed := d
	changed.SHA256 = strings.Repeat("0", 64)
	if _, err := v.Verify(ctx, changed); err == nil || !strings.Contains(err.Error(), "upstream changed") {
		t.Errorf("a changed file: %v", err)
	}

	// minisign, prehashed and not.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyID := []byte("8bytesid")
	minisignKey := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID...), pub...))
	sign := func(alg string, msg []byte, comment string) []byte {
		sig := ed25519.Sign(priv, msg)
		global := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte(comment)...))
		return []byte("untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(append(append([]byte(alg), keyID...), sig...)) +
			"\ntrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
	}
	pre := blake2b.Sum512(payload)
	for alg, msg := range map[string][]byte{"ED": pre[:], "Ed": payload} {
		files["/app.tar.gz.minisig"] = sign(alg, msg, "timestamp:1 file:app.tar.gz")
		signed := d
		signed.Signature = &Signature{Type: SignatureMinisign, URL: srv.URL + "/app.tar.gz.minisig", Key: "untrusted comment: key\n" + minisignKey}
		if got, err := v.Verify(ctx, signed); err != nil || got.Signature != SignatureMinisign {
			t.Errorf("minisign %s: %+v, %v", alg, got, err)
		}
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	priv = other
	files["/app.tar.gz.minisig"] = sign("ED", pre[:], "x")
	signed := d
	signed.Signature = &Signature{Type: SignatureMinisign, URL: srv.URL + "/app.tar.gz.minisig", Key: minisignKey}
	if _, err := v.Verify(ctx, signed); err == nil {
		t.Error("a minisign signature by another key is refused")
	}

	// OpenPGP, with a vendor key the panel knows by name.
	entity, err := openpgp.NewEntity("Vendor", "", "release@vendor.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	var armored bytes.Buffer
	w, _ := armor.Encode(&armored, openpgp.PublicKeyType, nil)
	entity.Serialize(w)
	w.Close()
	if err := RegisterSigningKey("vendor.example", armored.Bytes()); err != nil {
		t.Fatal(err)
	}
	var sigBuf bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sigBuf, entity, bytes.NewReader(payload), nil); err != nil {
		t.Fatal(err)
	}
	files["/app.tar.gz.asc"] = sigBuf.Bytes()
	gpg := d
	gpg.Signature = &Signature{Type: SignatureGPG, URL: srv.URL + "/app.tar.gz.asc", Key: "vendor.example"}
	if got, err := v.Verify(ctx, gpg); err != nil || got.Signature != SignatureGPG {
		t.Errorf("gpg: %+v, %v", got, err)
	}
	gpg.Signature.Key = "unknown.example"
	if _, err := v.Verify(ctx, gpg); err == nil {
		t.Error("a key the panel doesn't know is refused")
	}

	// Sigstore, by an identity.
	s := sigstoretest.New()
	root, _ := sigstore.ParseTrustedRoot(s.TrustedRoot())
	v.TrustedRoot = func(context.Context) (*sigstore.TrustedRoot, error) { return root, nil }
	identity := "https://github.com/vendor/app/.github/workflows/release.yml@refs/tags/v1.0.0"
	files["/app.tar.gz.sigstore.json"] = s.Sign(payload, identity, sigstoretest.Issuer, sigstoretest.Options{})
	sg := d
	sg.Signature = &Signature{Type: SignatureSigstore, URL: srv.URL + "/app.tar.gz.sigstore.json", Key: identity}
	if got, err := v.Verify(ctx, sg); err != nil || got.Signature != SignatureSigstore {
		t.Errorf("sigstore: %+v, %v", got, err)
	}
	sg.Signature.Key = "https://github.com/someone/else/.github/workflows/release.yml@refs/tags/v1.0.0"
	if _, err := v.Verify(ctx, sg); err == nil {
		t.Error("another identity is refused")
	}
}
