// Package jobs runs long work in the background with progress, live logs,
// cancellation and retries.
//
// Work is registered as a job Type with a name and a handler, so a durable
// queue can pick it up again after a restart: a job is a type name plus JSON
// arguments, never a closure. Applying a change set, running a backup and
// installing software are all jobs. The API answers 202 with the job's ID
// and clients follow its progress and logs over Server-Sent Events.
//
// Memory is a queue that keeps jobs and their logs in memory, dropping
// finished ones once they outlive its Retention.
package jobs
