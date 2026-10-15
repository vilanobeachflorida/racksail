// Package reconcile is the framework that makes machines match the desired
// state in the store.
//
// Each kind has a Reconciler with two halves. Plan is pure: from the desired
// resource, other resources it may read and the machine's facts, it returns
// the agent operations (Steps) that make the machine match, plus effects the
// steps don't describe themselves (memory estimates, ports) and the health
// checks that verify the result. Apply runs the plan through an Executor and
// returns the resource's new status. Plans are idempotent, so running one
// twice changes nothing; ApplySteps is the standard Apply.
//
// Typed lets a module write its reconciler against its own spec and status
// types. The Runner loads a resource, plans, applies, verifies the result
// with the plan's health checks (through a Checker), writes the status and,
// for deleted resources, purges them once their system state is gone. It
// reconciles one resource at a time. Resources are reconciled when a change
// to them is applied, and all of them again at every Resync.
package reconcile
