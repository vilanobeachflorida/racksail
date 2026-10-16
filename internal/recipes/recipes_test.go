package recipes

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

const wordpress = `
format: 1
kind: app
id: wordpress
name: WordPress
version: "6.8.3"
summary: The blogging platform and CMS.
license: GPL-2.0-or-later
licenseClass: open-source
maintainers: [{name: Racksail maintainers}]
requires: {modules: [web, databases]}
inputs:
  - {name: domain, type: domain, label: Domain, required: true}
  - {name: workers, type: integer, label: PHP workers, default: 4, min: 1, max: 32}
  - {name: php, type: select, label: PHP version, default: "8.4", options: [{value: "8.3", label: PHP 8.3}, {value: "8.4", label: PHP 8.4}]}
  - {name: dbPassword, type: password, label: Database password, required: true}
resources:
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}", "www.${inputs.domain}"], php: "${inputs.php}", workers: "${inputs.workers}", database: "${resources.db.id}"}
    dependsOn: [db]
  - name: db
    kind: databases.database
    spec: {name: wordpress, owner: {password: "${inputs.dbPassword}"}}
`

type siteSpec struct {
	Domains  []string `json:"domains"`
	PHP      string   `json:"php"`
	Workers  int      `json:"workers"`
	Database string   `json:"database"`
}

type databaseSpec struct {
	Name  string `json:"name"`
	Owner struct {
		Password string `json:"password,omitempty"`
	} `json:"owner"`
}

func testKinds(t *testing.T) *store.KindRegistry {
	t.Helper()
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{
		store.Define[siteSpec, store.Status](store.KindInfo{Name: "web.site", Prefix: "site", Scope: store.ScopeAccount, Title: "Site", Plural: "sites", Description: "A site."}),
		store.Define[databaseSpec, store.Status](store.KindInfo{
			Name: "databases.database", Prefix: "db", Scope: store.ScopeAccount, Title: "Database", Plural: "databases", Description: "A database.",
			UI: store.UIHints{Fields: map[string]store.FieldHint{"owner.password": {Sensitive: true}}},
		}),
	} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	return kinds
}

func TestParseAndRequests(t *testing.T) {
	r, err := Parse([]byte(wordpress))
	if err != nil {
		t.Fatal(err)
	}
	ordered, _ := r.Order()
	if ordered[0].Name != "db" || ordered[1].Name != "site" {
		t.Errorf("order = %v", ordered)
	}
	values, err := r.Values(map[string]any{"domain": "blog.example.com", "dbPassword": "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	if values["workers"] != 4 || values["php"] != "8.4" {
		t.Errorf("defaults = %v", values)
	}
	acct := store.AccountScope(store.NewID("acct"))
	req, err := r.Request(values, acct, testKinds(t))
	if err != nil {
		t.Fatal(err)
	}
	changes := req.Changes
	if req.Title != "Install WordPress" || len(changes) != 2 || changes[0].Kind != "databases.database" || changes[1].Labels["racksail.dev/recipe"] != "wordpress" {
		t.Fatalf("request = %+v", req)
	}
	var site map[string]any
	if err := json.Unmarshal(changes[1].Spec, &site); err != nil {
		t.Fatal(err)
	}
	domains := site["domains"].([]any)
	if domains[1] != "www.blog.example.com" || site["database"] != string(changes[0].ID) || site["workers"] != float64(4) {
		t.Errorf("site spec = %v", site)
	}
	if changes[0].Secrets["owner.password"] != "correct horse" || strings.Contains(string(changes[0].Spec), "correct horse") {
		t.Errorf("the password belongs in the change's secrets: %s, %v", changes[0].Spec, changes[0].Secrets)
	}
}

func TestSecretInputsStayInSecretFields(t *testing.T) {
	leaky, err := Parse([]byte(strings.Replace(wordpress, "spec: {name: wordpress, owner", "spec: {name: \"${inputs.dbPassword}\", owner", 1)))
	if err != nil {
		t.Fatal(err)
	}
	values, _ := leaky.Values(map[string]any{"domain": "a.example", "dbPassword": "hunter22"})
	if _, err := leaky.Request(values, store.AccountScope(store.NewID("acct")), testKinds(t)); err == nil || !strings.Contains(err.Error(), "secret field") {
		t.Errorf("a password in a plain field must be refused: %v", err)
	}
}

func TestValuesAreChecked(t *testing.T) {
	r, _ := Parse([]byte(wordpress))
	cases := []map[string]any{
		{},
		{"domain": "not a domain"},
		{"domain": "a.example", "workers": 99},
		{"domain": "a.example", "php": "5.6"},
		{"domain": "a.example", "color": "blue"},
	}
	for _, given := range cases {
		if _, err := r.Values(given); err == nil {
			t.Errorf("Values(%v) should fail", given)
		}
	}
}

func TestBrokenRecipes(t *testing.T) {
	broken := map[string]string{
		"unknown input":                    strings.Replace(wordpress, "${inputs.php}", "${inputs.ruby}", 1),
		"unknown resource":                 strings.Replace(wordpress, "${resources.db.id}", "${resources.cache.id}", 1),
		"bad dependency":                   strings.Replace(wordpress, "dependsOn: [db]", "dependsOn: [cache]", 1),
		"cycle":                            strings.Replace(wordpress, "kind: databases.database\n", "kind: databases.database\n    dependsOn: [site]\n", 1),
		"schema":                           strings.Replace(wordpress, "licenseClass: open-source", "licenseClass: maybe", 1),
		"health check of an unknown input": wordpress + "health:\n  - {type: http, target: \"https://${inputs.host}/\"}\n",
		"resource check of nothing":        wordpress + "health:\n  - {type: resource, target: cache}\n",
		"resource check that expects":      wordpress + "health:\n  - {type: resource, target: site, expect: ready}\n",
		"health check without a target":    wordpress + "health:\n  - {type: unit, target: \"\"}\n",
	}
	for name, doc := range broken {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: should fail", name)
		}
	}
	// A resource check waits for one of the recipe's resources, or for the
	// whole item, named by the recipe's id.
	checked := wordpress + "health:\n  - {type: resource, target: site}\n  - {type: resource, target: wordpress}\n" +
		"  - {type: http, target: \"https://${inputs.domain}/\", expect: \"200\"}\n"
	if r, err := Parse([]byte(checked)); err != nil || len(r.Health) != 3 || r.Health[0].Type != HealthResource {
		t.Errorf("health checks = %+v, %v", r, err)
	}
	unknown, _ := Parse([]byte(wordpress))
	if _, err := unknown.Request(map[string]any{"domain": "a.example"}, store.ServerScope(), store.NewKindRegistry()); err == nil {
		t.Error("unknown kinds should fail")
	}
}

func TestVerifyIndex(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"format":1,"catalog":"official","built":"2026-10-15T00:00:00Z","items":[{"id":"wordpress","kind":"app","name":"WordPress","version":"6.8.3","summary":"CMS","licenseClass":"open-source","path":"apps/wordpress/recipe.yaml","sha256":"` + strings.Repeat("a", 64) + `"}]}`)
	sig := ed25519.Sign(priv, data)
	idx, err := VerifyIndex(data, sig, pub)
	if err != nil || len(idx.Items) != 1 {
		t.Fatalf("index = %+v, %v", idx, err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := VerifyIndex(data, sig, other); err == nil {
		t.Error("another key must not verify")
	}
	evil := []byte(strings.Replace(string(data), "apps/wordpress", "../../etc", 1))
	if _, err := VerifyIndex(evil, ed25519.Sign(priv, evil), pub); err == nil {
		t.Error("unsafe paths must be refused")
	}
	evilFile := []byte(strings.Replace(string(data), `"}]}`, `","files":[{"path":"/etc/passwd","sha256":"`+strings.Repeat("a", 64)+`","size":1}]}]}`, 1))
	if _, err := VerifyIndex(evilFile, ed25519.Sign(priv, evilFile), pub); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("unsafe file paths must be refused: %v", err)
	}
}
