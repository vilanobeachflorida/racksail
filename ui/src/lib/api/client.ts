import createClient, { type Client, type ClientOptions } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Schemas = components["schemas"];
export type Problem = Schemas["Problem"];
export type ApiClient = Client<paths>;

interface Transport {
  baseUrl: string;
  fetch: (request: Request) => Promise<Response>;
}

// What each client was created with, so requests the generated types don't
// cover yet go the same way, to the same server, as typed ones.
const transports = new WeakMap<ApiClient, Transport>();

/**
 * Creates a client for the panel API. The panel serves the UI, so by default
 * the client talks to the page's own origin.
 */
export function createApiClient(options: ClientOptions = {}): ApiClient {
  const baseUrl = options.baseUrl ?? window.location.origin;
  const client = createClient<paths>({ ...options, baseUrl });
  transports.set(client, {
    baseUrl,
    fetch:
      (options.fetch as Transport["fetch"] | undefined) ??
      ((request: Request) => globalThis.fetch(request)),
  });
  return client;
}

/**
 * Calls an API operation by method and path, for operations the generated
 * types don't describe yet. The answer is decoded from JSON, and errors
 * become ApiErrors like those of typed calls.
 */
export async function requestJSON<T>(
  client: ApiClient,
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE",
  path: string,
  body?: unknown,
): Promise<T> {
  const transport = transports.get(client);
  if (!transport) {
    throw new Error("the API client wasn't made by createApiClient");
  }
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const request = new Request(new URL(path, transport.baseUrl), {
    method,
    credentials: "same-origin",
    headers,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const response = await transport.fetch(request);
  const text = await response.text();
  let parsed: unknown = text;
  try {
    parsed = text === "" ? undefined : JSON.parse(text);
  } catch {
    // Not JSON: a proxy's error page, which toProblem explains.
  }
  if (!response.ok) {
    throw new ApiError(response.status, toProblem(response.status, parsed));
  }
  return parsed as T;
}

/** An error answer from the API, with its RFC 9457 problem details. */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem;

  constructor(status: number, problem: Problem) {
    super(problem.detail ?? problem.title ?? `The panel answered with status ${status}.`);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
  }

  /** The stable error code, such as "core.not_found". */
  get code(): string {
    return this.problem.code;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Whether a response body is problem details from the panel. */
export function isProblem(body: unknown): body is Problem {
  return isRecord(body) && typeof body.code === "string" && body.code !== "";
}

/**
 * Turns an error response into problem details. Something other than the
 * panel, such as a proxy in front of it, can answer with HTML or plain text;
 * that becomes a problem with the code for its status.
 */
export function toProblem(status: number, body: unknown): Problem {
  if (isProblem(body)) {
    return body;
  }
  if (status === 503 || status === 502 || status === 504) {
    return { type: "about:blank", status, code: "core.unavailable", params: { what: "The panel" } };
  }
  if (status >= 500) {
    return { type: "about:blank", status, code: "core.internal" };
  }
  const reason = typeof body === "string" && body.trim() !== "" ? body.trim() : `status ${status}`;
  return { type: "about:blank", status, code: "core.bad_request", params: { reason } };
}

interface Answer<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/**
 * Returns the data of a successful answer and throws an ApiError for an
 * error, so callers like TanStack Query see failures as exceptions.
 *
 *   const version = await unwrap(api.GET("/api/v1/version"));
 */
export async function unwrap<T>(request: Promise<Answer<T>>): Promise<T> {
  const { data, error, response } = await request;
  if (!response.ok || error !== undefined) {
    throw new ApiError(response.status, toProblem(response.status, error));
  }
  return data as T;
}
