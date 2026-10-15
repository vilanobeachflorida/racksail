// Package web manages websites: sites and their domains, runtimes (static,
// PHP, Node, Python, Ruby, Go, Bun, Deno, containers and VMs), deploys from
// Git or uploads with releases and rollback, staging and preview deployments,
// per-site rules, scheduled jobs and temporary URLs. Sites run as sandboxed
// systemd services in their account's slice and sleep when idle.
//
// Its screens live in ui/src/modules/web and its documentation in
// docs/site/src/content/docs/modules/web.md.
package web
