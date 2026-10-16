package modules

import "testing"

// Manifests are embedded in modules, and checked by the schema before the
// module registers; reading one never panics, whatever it holds.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(`id: web
name: Web
description: Sites.
version: 0.1.0
license: AGPL-3.0-or-later
category: hosting
dependencies: {requires: [network]}
profiles: [all-in-one]
hardware: {arch: [amd64, arm64]}
cost: {idleMemoryMB: 4, activeMemoryMB: 40, diskMB: 10}
upstream:
  - {id: webd, name: Webd, license: MIT, source: {type: debian, packages: [webd]}, install: With the first site.}
ports: [{port: 443, protocol: tcp, description: HTTPS}]
services: [{unit: webd.service, description: Serves the sites.}]
`))
	f.Add([]byte("id: [\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseManifest(data)
		if err != nil {
			return
		}
		_ = m.ValidateSettings(nil)
		_ = UninstallPlan(m)
		for _, u := range m.Upstream {
			_ = exactPackages(u)
			_ = sourceOf(u)
		}
	})
}
