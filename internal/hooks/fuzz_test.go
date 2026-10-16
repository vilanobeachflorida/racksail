package hooks

import (
	"slices"
	"testing"
)

const fuzzManifest = `
format: 1
id: site-guard
name: Site guard
version: 0.3.0
license: Apache-2.0
source: https://example.com/site-guard
author: Example
description: Refuses sites on evil domains.
apiVersions: [v1]
hooks:
  - {point: web.site.create.validate, kind: validate, security: true, deadline: 2s}
  - {point: web.site.*.plan, kind: modify}
provides: [notifier]
permissions:
  hooks: [validate, modify]
  read: [{kind: web.site, fields: [domains]}]
  write: [{kind: web.site, fields: [cdn]}]
  network: [api.example.com]
`

// Add-on manifests come with packages from catalogs. Whatever one holds,
// reading it never panics, and what reads holds its hooks to the
// permissions it asks for and to deadlines the runner takes.
func FuzzParseAddonManifest(f *testing.F) {
	f.Add([]byte(fuzzManifest))
	f.Add([]byte("format: 1\nid: x\nhooks: [{point: core.boot, kind: system}]\n"))
	f.Add([]byte("id: [\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseAddonManifest(data)
		if err != nil {
			return
		}
		for _, h := range m.Hooks {
			if !slices.Contains(m.Permissions.Hooks, h.Kind) {
				t.Fatalf("%s hook on %s without the permission", h.Kind, h.Point)
			}
			if d, err := h.DeadlineDuration(); err != nil || d <= 0 || d > MaxDeadline {
				t.Fatalf("deadline %q = %v, %v", h.Deadline, d, err)
			}
		}
		// An account's add-on only ever takes part in changes.
		if m.AccountInstalled() == nil {
			for _, h := range m.Hooks {
				if !AccountPoint(Point(h.Point)) {
					t.Fatalf("an account may install an add-on at %s", h.Point)
				}
			}
			if len(m.Provides) > 0 || m.Permissions.Override || slices.Contains(m.Permissions.Hooks, System) {
				t.Fatalf("an account may install %+v", m)
			}
		}
	})
}

// Patterns of points come from add-ons' manifests and rules.
func FuzzMatchPoint(f *testing.F) {
	f.Add("web.site.*.validate", "web.site.create.validate")
	f.Add("*.*.create.plan", "dns.zone.create.plan")
	f.Add("a.[b", "a.b")
	f.Fuzz(func(t *testing.T, pattern, point string) {
		matched := MatchPoint(Point(pattern), Point(point))
		if !IsPattern(Point(pattern)) && matched != (pattern == point) {
			t.Fatalf("%q matched %q", pattern, point)
		}
		_, _, _, _ = ParseKindPoint(Point(point))
		_ = AccountPoint(Point(pattern))
	})
}
