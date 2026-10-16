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
// The audit log records who did what, with the actor, the person they acted
// for, the surface they used and the API request it came through. Every
// change is there as a success. Every attempt that was refused is there as
// denied: one the authorizer refused, a read or a job's cancel or retry as
// much as a change, and a change a validate hook stopped. A change that was
// allowed but couldn't be made, such as one whose settings aren't valid, is
// there as failed; previews that fail are left out, since they change
// nothing. So that a caller who keeps being refused can't push everyone
// else's records out, a refusal the same as one of the actor's recorded
// within the hour only counts on that record (Repeats and LastTime), and an
// actor's different refusals past a hundred in the hour count together on
// one record of the action RefusalsAction (see Denials).
//
// Log is the bus, the timeline and the audit log in one. It stores every
// event before subscribers see it, numbering them with a sequence that is
// never reused, so a follower that falls behind, or comes back after a
// restart, catches up from the timeline in the same order. NewSQL keeps the
// log in the panel database and NewMemory in memory, both within a
// Retention. Outgoing webhooks live in the webhook package below this one.
package events
