// Package hooks lets modules, rules and add-ons take part in every change.
//
// Hook points are named <kind>.<operation>.<stage>, such as
// web.site.create.validate, for every operation (create, update, delete and
// each declared action) and every stage (validate, plan and applied) of every
// kind, so coverage is guaranteed by construction. Other points cover
// configuration patches (system.<service>.config), lifecycle events,
// identity and providers.
//
// A Registration attaches a handler of one hook kind (observe, validate,
// modify, system, provide or extend) to a point with a priority, a deadline
// and a failure mode. Hooks run in a deterministic order: priority, then
// owner. Runner implements changeset.Hooks: it enforces deadlines and
// failure modes, labels what modify hooks contribute, reports two hooks
// changing the same field as a conflict, and switches off hooks that keep
// failing.
//
// The add-on manifest, its permissions and the spec of CEL rules
// (core.policy) are defined here too; the add-on runtime and the rule engine
// build on them.
package hooks
