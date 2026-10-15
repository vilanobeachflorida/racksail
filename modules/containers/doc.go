// Package containers runs containers: apps from catalog recipes, any compose
// file or imported Unraid templates, with ports, volumes on shares and
// storage, environment, limits, health checks, networks including their own
// LAN address, GPU access, web UIs published through the edge, update checks
// with snapshots and rollback, logs and shells. Accounts get rootless Podman.
//
// Its screens live in ui/src/modules/containers and its documentation in
// docs/site/src/content/docs/modules/containers.md.
package containers
