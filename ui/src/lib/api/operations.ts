import type { Job, JobList, LogLine } from "../changes";
import type { TimelineEventList } from "../events";
import { type ApiClient, unwrap } from "./client";
import type { operations, paths } from "./schema";
import { normalizeSession, type PanelSession, type SessionPreferences } from "./session";

/*
 * The calls the shell and several screens share: the job queue, the event
 * timeline and the session. They go through the client generated from
 * spec/openapi.json, so a change to an operation that doesn't suit them is
 * a type error here.
 */

/** Filters of the job list. */
export type JobQuery = NonNullable<operations["listJobs"]["parameters"]["query"]>;

/** Filters of the event timeline. */
export type EventQuery = NonNullable<operations["listEvents"]["parameters"]["query"]>;

/**
 * Where the timeline and each job's progress and log stream from: paths of
 * the document too, though EventSource takes them as text.
 */
export const streams = {
  events: "/api/v1/events/stream" satisfies keyof paths,
  job: (id: string) =>
    ("/api/v1/jobs/{id}/stream" satisfies keyof paths).replace("{id}", encodeURIComponent(id)),
};

/** The jobs the panel runs and ran recently, newest first. */
export function listJobs(api: ApiClient, query?: JobQuery): Promise<JobList> {
  return unwrap(api.GET("/api/v1/jobs", { params: { query } }));
}

/** A job's log lines. */
export function getJobLogs(api: ApiClient, id: string): Promise<{ lines: LogLine[] | null }> {
  return unwrap(api.GET("/api/v1/jobs/{id}/logs", { params: { path: { id } } }));
}

/** Cancels a queued or running job; the answer is the job. */
export function cancelJob(api: ApiClient, id: string): Promise<Job> {
  return unwrap(api.POST("/api/v1/jobs/{id}/cancel", { params: { path: { id } } }));
}

/** Runs a failed or canceled job again; the answer is the new job. */
export function retryJob(api: ApiClient, id: string): Promise<Job> {
  return unwrap(api.POST("/api/v1/jobs/{id}/retry", { params: { path: { id } } }));
}

/** Events from the timeline, newest first. */
export function listEvents(api: ApiClient, query?: EventQuery): Promise<TimelineEventList> {
  return unwrap(api.GET("/api/v1/events", { params: { query } }));
}

/** Who is signed in and how the panel looks and behaves for them. */
export async function getSession(api: ApiClient): Promise<PanelSession> {
  return normalizeSession(await unwrap(api.GET("/api/v1/session")));
}

/** Keeps the person's display preferences with their account. */
export async function savePreferences(
  api: ApiClient,
  preferences: SessionPreferences,
): Promise<void> {
  await unwrap(api.PUT("/api/v1/session/preferences", { body: preferences }));
}
