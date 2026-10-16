// Package changeset is the single path every change takes:
//
//	request → authenticate → authorize → validate → plan → preview → apply → verify → record
//
// A Request holds one or more changes. They are planned in order against an
// overlay of the store, so each change sees the resources the earlier ones
// create, update or delete, and together they become one change set: one
// preview, one store transaction, one apply job and one undo. Recipes,
// migrations, account creation and assistant proposals all rely on that.
//
// A change set lists resource changes with field-level diffs, the system
// effects their reconcilers will cause (downloads, packages, ports,
// services, files, memory and disk), what hooks and add-ons contributed,
// labelled with their source, and any conflicts between them. A dry run
// returns the change set without applying it; that is the preview the UI,
// the CLI and the assistant show.
//
// Modify hooks may rewrite the specs of the changes they're given and add
// changes and effects. Whatever they rewrote or added is authorized for the
// actor, validated, run past the validate hooks and planned again, so the
// preview lists what the specs they wrote do to the machine, and they can't
// change anything else.
//
// Applying a change set that removes data first takes read-only snapshots of
// what it removes. It then writes every resource change, and in the panel
// database the change set, its job, the request's idempotency key and the
// audit records too, in one transaction, and runs the reconcilers as a job
// with live progress, which verifies the result with the plans' health
// checks. Undo is a new change set built from the recorded previous specs,
// labels and placement, planned and applied like any other.
//
// The audit log gets every change as a success, every change the actor
// wasn't allowed or a validate hook stopped as denied, and every change
// that was allowed but couldn't be made as failed. A change to a resource
// the actor may not even read answers as if it didn't exist.
//
// The job records each change as started before it runs and as done once it
// is in place. However the job ends, canceled, out of time, or cut short by
// the panel stopping, the change set ends failed with the reason, and the
// resync carries out the settings it stored. Retrying the job carries on
// from the change that didn't finish; it doesn't run an action that may
// already have run a second time, since a retry doesn't go through
// authorization and validate hooks again, and it never touches a change set
// that was undone.
//
// A resource that was purged keeps its history, and the secrets its change
// sets stored stay until those are no longer kept, so its ID is never given
// to a new resource: only undoing the change set that deleted it brings it
// back under that ID.
//
// Secret values never enter specs. A kind's sensitive fields hold references
// to the resource's own secrets; values arrive in Change.Secrets and are
// stored, under a new name for each change set, only when it is applied.
//
// A preview can be applied by its ID (Engine.Apply): its request is planned
// again for the person applying it, and applied only if the plan still does
// what the preview showed; otherwise the new plan is kept as a preview of its
// own for them to look at first. Actions whose work happens in the panel
// rather than on the machine return an output, kept with the change set.
//
// Pipeline is the interface the API, recipes, automations, add-ons and the
// assistant call. Engine implements it and keeps change sets, the requests
// previews came from and idempotency keys in a Storage, within the bounds of
// its Retention: SQLStorage in the panel database, or MemoryStorage. Hooks
// are reached through the Hooks interface, which the hooks package
// implements, so this package doesn't depend on it.
package changeset
