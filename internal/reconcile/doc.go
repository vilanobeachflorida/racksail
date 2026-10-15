// Package reconcile is the framework that makes machines match the desired
// state in the store.
//
// Each kind has a Reconciler with two halves. Plan is pure: from the desired
// resource, other resources it may read and the machine's facts, it returns
// the agent operations (Steps) that make the machine match, plus effects the
// steps don't describe themselves (memory estimates, ports) and the health
// checks that verify the result. Apply runs the plan through an Executor and
// returns the resource's new status. Plans are idempotent, so running one
// twice changes nothing; ApplySteps is the standard Apply. Reconcilers of
// kinds with actions also plan them (ActionPlanner), and actions that do work
// in the panel itself apply it (ActionApplier).
//
// Typed lets a module write its reconciler against its own spec and status
// types. The Runner loads a resource, plans, applies, verifies the result
// with the plan's health checks (through a Checker), writes the status and,
// for deleted resources, purges them once their system state is gone. It
// reconciles one resource at a time, and no more resources at once than its
// limits allow, in all and for each kind (Limiter).
//
// Runner.Run works a queue: resources whose latest generation was never
// carried out go first, failed reconciles are retried with a growing wait,
// resources reported as drifted (ReportDrift) are put back, and everything
// is reconciled again at every resync. Stats and Check are the queue's
// metrics and health check. A reconcile of a resource
// that was ready which still changes something has found drift: the Drifted
// condition and a <kind>.drifted event say what was put back.
package reconcile
