// Package jobs runs long work in the background with progress, live logs,
// cancellation, retries and a history.
//
// Work is registered as a job Type with a name and a handler, so a queue can
// pick it up again after a restart: a job is a type name plus JSON
// arguments, never a closure. Applying a change set, running a backup and
// installing software are all jobs. The API answers 202 with the job's ID
// and clients follow its progress and logs over Server-Sent Events.
//
// Manager is the queue. NewSQL keeps it in the panel database, where queued
// jobs wait across restarts: when the panel starts again, jobs whose type
// says they are safe to run again (Type.Resume) start over, and other jobs
// that were running are reported as interrupted. A type can ask for
// automatic retries with a growing wait between them; each retry is a job of
// its own, linked to the one before. Finished jobs and their logs are kept
// within a Retention. NewMemory keeps a queue in memory, for tests and tools.
//
// A handler whose context ends learns why from Stopped: someone canceled the
// job, it ran out of time, or the panel is stopping. A type's Ended hears how
// each of its jobs ended, even one whose handler never ran, and Retryable can
// refuse a retry, so work that keeps records of its own keeps them in line
// with its jobs. Stop stops the queue and waits for the jobs running here.
package jobs
