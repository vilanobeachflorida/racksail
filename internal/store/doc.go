// Package store holds the resource model and the generic resource store.
//
// Everything Racksail manages is a resource of a registered kind, such as
// web.site or storage.pool. A resource has a TypeID, a kind, a scope (the
// server, a reseller or an account), a typed spec with the desired state, a
// typed status written by reconcilers, generations, labels, timestamps and a
// soft-delete marker.
//
// Modules describe their kinds with Define, giving the Go types of the spec
// and the status, the TypeID prefix, the scope, actions with risk levels,
// validators, indexes and UI hints. The API, CLI, assistant tools, hook points,
// events and generic screens are all derived from that one description, and
// specs are validated against the JSON Schema generated from the spec type.
//
// The Store interface is the single place resources live. Writes happen in
// transactions through Store.Write; every spec version is kept in the
// resource's history, and watchers learn about every committed write. Lists
// filter by owner, labels, indexes, phase and search text, sort by metadata or
// indexes, and page with cursors.
//
// The panel keeps its state in one SQLite database in WAL mode (DB), whose
// parts each bring their own migrations (DB.Migrate). Their writes can share
// one transaction (DB.Atomic), with what must wait for the commit waiting
// for it (DB.AfterCommit). SQLite is the Store kept there; Memory is an
// in-memory Store for tests that behaves the same way, and the same
// conformance tests run against both.
package store
