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
// events and generic screens are all derived from that one description.
//
// The Store interface is the single place resources live. Writes happen in
// transactions through Store.Write; every spec version is kept in the
// resource's history. NewMemory returns an in-memory Store for development
// and tests; the panel's SQLite store implements the same interface.
package store
