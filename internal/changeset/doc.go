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
// actor, validated and run past the validate hooks again, and they can't
// change anything else.
//
// Applying a change set that removes data first takes read-only snapshots of
// what it removes. It then writes every resource change in one store
// transaction and runs the reconcilers as a job with live progress, which
// verifies the result with the plans' health checks. Undo is a new change
// set built from the recorded previous specs, labels and placement, planned
// and applied like any other.
//
// Secret values never enter specs. A kind's sensitive fields hold references
// to the resource's own secrets; values arrive in Change.Secrets and are
// stored, under a new name for each change set, only when it is applied.
//
// Pipeline is the interface the API, recipes, automations, add-ons and the
// assistant call. Engine implements it and keeps change sets in memory,
// within the bounds of its Retention. Hooks are reached through the Hooks
// interface, which the hooks package implements, so this package doesn't
// depend on it.
package changeset
