// Package hooks lets modules, rules and add-ons take part in every change.
//
// Hook points are named <kind>.<operation>.<stage>, such as
// web.site.create.validate, for every operation (create, update, delete and
// each declared action) and every stage (validate, plan and applied) of every
// kind, so coverage is guaranteed by construction. Other points cover
// configuration patches (system.<service>.config), lifecycle events
// (core.boot, modules.<id>.on and the like), identity, capabilities and
// add-on extensions.
//
// A Registration attaches one handler to a point: validate, modify and
// observe hooks take part in changes; system hooks patch generated
// configuration; provide hooks register a Provider, one implementation of a
// capability such as provide.dns or auth.provider, which the code that needs
// it connects with Open; extend hooks register what an add-on adds to the
// UI and the API. Hooks run in a deterministic order: priority, then owner.
//
// Runner implements changeset.Hooks: it gives every hook its own copy of
// what it works on, enforces deadlines and failure modes, holds modify hooks
// to rewriting the specs of their changes and adding changes and effects,
// labels what they contribute, reports two hooks changing the same field as
// a conflict, and switches off hooks that keep failing. Its Check and Notify
// methods run validate and observe hooks at points outside the change
// pipeline.
//
// The add-on manifest, its permissions and the spec of CEL rules
// (core.policy) are defined here too; the add-on runtime and the rule engine
// build on them.
package hooks
