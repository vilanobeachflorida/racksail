// Package events carries what happened: typed events on a bus, the timeline
// that keeps them, and the audit log.
//
// Event types are named <kind>.<past-tense verb>, such as web.site.created or
// backups.run.failed. Every kind gets created, updated, deleted, failed and
// drifted events plus one per action; the core registers its own types with
// RegisterType and modules declare theirs. Catalog lists them all, for
// webhooks, automations and the docs. Subscribers include the UI's live
// updates over SSE, outgoing webhooks, automations and observe hooks.
//
// The audit log records every change and every denied attempt with the
// actor, the person they acted for, the surface they used and the API
// request it came through.
//
// Log is the bus, the timeline and the audit log in one. It stores every
// event before subscribers see it, numbering them with a sequence that is
// never reused, so a follower that falls behind, or comes back after a
// restart, catches up from the timeline in the same order. NewSQL keeps the
// log in the panel database and NewMemory in memory, both within a
// Retention. Outgoing webhooks live in the webhook package below this one.
package events
