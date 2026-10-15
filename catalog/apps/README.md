# Apps

Software people install for a site or an account, such as WordPress, Nextcloud or Forgejo. An app's recipe usually creates a site (`web.site`) or a container app (`containers.app`), the databases it needs and anything else it uses, all in one change set.

Rules for apps, besides the catalog's:

- At least one health check that shows the app answering, usually an `http` check of its address.
- An install test with values for every required input, such as `domain: blog.example.test`.
- Downloads of the app's own release, pinned by SHA-256 checksum and size, and verified against the upstream signature where there is one.
- `upgrade.strategy` says how new versions are installed, and `uninstall.keepData` whether removing the app keeps its data by default.

```
apps/wordpress/
  recipe.yaml
  icon.svg
```

```yaml
format: 1
kind: app
id: wordpress
name: WordPress
version: "6.8.3"
summary: The blogging platform and content management system.
license: GPL-2.0-or-later
licenseClass: open-source
icon: icon.svg
categories: [blog, cms]
maintainers: [{name: Racksail maintainers, github: vilanobeachflorida}]
requires: {modules: [web, databases], memoryMB: 256, diskMB: 100}
inputs:
  - {name: domain, type: domain, label: Domain, required: true}
  - {name: adminPassword, type: password, label: Admin password, required: true}
resources:
  - name: db
    kind: databases.database
    spec: {name: wordpress}
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}"], database: "${resources.db.id}", admin: {password: "${inputs.adminPassword}"}}
    dependsOn: [db]
health:
  - {type: http, target: "https://${inputs.domain}/wp-login.php", expect: "200"}
tests:
  inputs: {domain: wordpress.example.test, adminPassword: correct-horse-battery}
  timeoutSeconds: 600
```
