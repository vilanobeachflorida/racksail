// Package changeset is the single path every change takes:
//
//	request → authenticate → authorize → validate → plan → preview → apply → verify → record
//
// A change set lists resource changes with field-level diffs, the system
// effects their reconcilers will cause (downloads, packages, ports,
// services, files, memory and disk), what hooks and add-ons contributed,
// labelled with their source, and any conflicts between them. A dry run
// returns the change set without applying it; that is the preview the UI,
// the CLI and the assistant show.
//
// Applying writes every resource change in one store transaction and then
// runs the reconcilers as a job with live progress. Undo is a new change set
// built from the recorded previous specs.
//
// Pipeline is the interface the API, recipes, automations, add-ons and the
// assistant call. Engine implements it. Hooks are reached through the Hooks
// interface, which the hooks package implements, so this package doesn't
// depend on it.
package changeset
