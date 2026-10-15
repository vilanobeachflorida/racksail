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
resources:
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}", "www.${inputs.domain}"], php: "${inputs.php}", workers: "${inputs.workers}", database: "${resources.db.id}"}
    dependsOn: [db]
  - name: db
    kind: databases.database
    spec: {name: wordpress}
`

func prefixes(kind string) (string, bool) {
	p, ok := map[string]string{"web.site": "site", "databases.database": "db"}[kind]
	return p, ok
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
	values, err := r.Values(map[string]any{"domain": "blog.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if values["workers"] != 4 || values["php"] != "8.4" {
		t.Errorf("defaults = %v", values)
	}
	acct := store.AccountScope(store.NewID("acct"))
	reqs, err := r.Requests(values, acct, prefixes)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].Kind != "databases.database" || reqs[1].Labels["racksail.dev/recipe"] != "wordpress" {
		t.Fatalf("requests = %+v", reqs)
	}
	var site map[string]any
	if err := json.Unmarshal(reqs[1].Spec, &site); err != nil {
		t.Fatal(err)
	}
	domains := site["domains"].([]any)
	if domains[1] != "www.blog.example.com" || site["database"] != string(reqs[0].ID) || site["workers"] != float64(4) {
		t.Errorf("site spec = %v", site)
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
		"unknown input":    strings.Replace(wordpress, "${inputs.php}", "${inputs.ruby}", 1),
		"unknown resource": strings.Replace(wordpress, "${resources.db.id}", "${resources.cache.id}", 1),
		"bad dependency":   strings.Replace(wordpress, "dependsOn: [db]", "dependsOn: [cache]", 1),
		"cycle":            strings.Replace(wordpress, "spec: {name: wordpress}", "spec: {name: wordpress}\n    dependsOn: [site]", 1),
		"schema":           strings.Replace(wordpress, "licenseClass: open-source", "licenseClass: maybe", 1),
	}
	for name, doc := range broken {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: should fail", name)
		}
	}
	unknown, _ := Parse([]byte(wordpress))
	if _, err := unknown.Requests(map[string]any{"domain": "a.example"}, store.ServerScope(), func(string) (string, bool) { return "", false }); err == nil {
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
}
