package spec

import (
	"strings"
	"testing"
)

func TestSchemasCompile(t *testing.T) {
	for _, name := range Names() {
		if _, err := compile(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := compile("nope"); err == nil {
		t.Error("an unknown schema should fail")
	}
}

func TestExamples(t *testing.T) {
	valid := map[string]string{
		Recipe: `
format: 1
kind: app
id: wordpress
name: WordPress
version: "6.8.3"
summary: The blogging platform and CMS.
license: GPL-2.0-or-later
licenseClass: open-source
maintainers: [{name: Racksail maintainers, github: vilanobeachflorida}]
requires: {modules: [web, databases], memoryMB: 256}
inputs:
  - {name: domain, type: domain, label: Domain, required: true}
resources:
  - name: db
    kind: databases.database
    spec: {name: "wp_${inputs.domain}"}
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}"]}
    dependsOn: [db]
downloads:
  - {url: "https://wordpress.org/wordpress-6.8.3.tar.gz", sha256: "` + strings.Repeat("a", 64) + `", size: 26000000}
health:
  - {type: http, target: "https://${inputs.domain}/", expect: "200"}
`,
		Addon: `
format: 1
id: backup-policy
name: Backup policy
version: 1.2.0
license: MIT
source: https://example.com/backup-policy
author: Example
description: Sends every backup to a second destination.
apiVersions: [v1]
hooks:
  - {point: backups.run.before, kind: modify, priority: 10, deadline: 2s, failureMode: allow}
permissions:
  hooks: [modify]
  read: [{kind: backups.policy}]
  network: [s3.example.com]
  resources: {cpu: 10%, memoryMB: 64}
`,
		Policy: `
name: No .invalid domains
type: validate
kinds: [web.site]
expression: 'resource.spec.domains.exists(d, d.endsWith(".invalid"))'
message: Sites can't use .invalid domains.
`,
		Answers: `
format: 1
mode: install
hostname: web1.example.test
network: {mode: static, address: 203.0.113.10/24, gateway: 203.0.113.1, dns: [9.9.9.9]}
disks: {layout: mirror, devices: [/dev/sda, /dev/sdb], confirmWipe: true}
account: {username: admin, sshKeys: ["ssh-ed25519 AAAAC3Nza admin@example"]}
reboot: true
`,
		Archive: `
format: 1
createdAt: 2026-10-15T12:00:00Z
source: {product: cpanel, version: "120"}
account: {slug: blog}
resources:
  - {kind: web.site, id: site_01h455vb4pex5vsknk084sn02q, spec: {domains: [blog.example]}}
contents:
  - {type: files, resource: site_01h455vb4pex5vsknk084sn02q, path: files/site.tar, format: tar, sha256: "` + strings.Repeat("b", 64) + `", size: 1024}
`,
	}
	for name, doc := range valid {
		if err := ValidateYAML(name, []byte(doc)); err != nil {
			t.Errorf("%s example: %v", name, err)
		}
	}

	invalid := map[string]string{
		Recipe:  "format: 1\nkind: app\nid: x\n",
		Policy:  "name: Missing expression\ntype: validate\n",
		Answers: "format: 1\nmode: install\nnetwork: {mode: static}\ndisks: {layout: single}\naccount: {username: admin}\n",
		Addon:   "format: 1\nid: x\nname: x\nversion: 1.0.0\nlicense: MIT\nsource: https://e.test\nauthor: a\ndescription: d\napiVersions: [v1]\npermissions: {}\nhooks: [{point: web.site.create.validate, kind: validate, deadline: 30s}]\n",
		Archive: "format: 1\ncreatedAt: 2026-10-15T12:00:00Z\nsource: {product: racksail}\naccount: {slug: blog}\nresources: []\ncontents: [{type: files, path: ../escape, sha256: x, size: 1}]\n",
	}
	for name, doc := range invalid {
		if err := ValidateYAML(name, []byte(doc)); err == nil {
			t.Errorf("%s: an invalid document passed", name)
		}
	}
}
