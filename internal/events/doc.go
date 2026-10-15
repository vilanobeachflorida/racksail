// Package events carries what happened: typed events on a bus, the timeline
// that keeps them, and the audit log.
//
// Event types are named <kind>.<past-tense verb>, such as web.site.created or
// backups.run.failed. Every kind gets created, updated and deleted events plus
// one per action; modules declare the others. Subscribers include the UI's
// live updates over SSE, outgoing webhooks, automations and observe hooks.
//
// The audit log records every change and every denied attempt with the
// actor, the person they acted for and the surface they used.
//
// Memory implements the bus, the timeline and the audit log in memory with
// bounded retention; the panel's durable implementations satisfy the same
// interfaces.
package events
