import { createIntl } from "react-intl";
import { describe, expect, it } from "vitest";
import { errorMessage, problemMessage } from "../errors";
import { shellMessages } from "../i18n";
import { ApiError, createApiClient, isProblem, toProblem, unwrap } from "./client";

const json = (body: unknown, status = 200, type = "application/json") =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": type } });

function client(answer: () => Response | Promise<Response>) {
  return createApiClient({ baseUrl: "http://panel.test", fetch: async () => answer() });
}

const intl = createIntl({ locale: "en", messages: shellMessages });

describe("unwrap", () => {
  it("returns the data of a successful answer", async () => {
    const version = {
      version: "1.2.3",
      channel: "stable",
      goVersion: "go1.27",
      os: "linux",
      arch: "amd64",
    };
    const api = client(() => json(version));
    await expect(unwrap(api.GET("/api/v1/version"))).resolves.toEqual(version);
  });

  it("throws the problem of an error answer", async () => {
    const problem = {
      type: "about:blank",
      status: 401,
      code: "core.unauthenticated",
      detail: "Sign in to continue.",
    };
    const api = client(() => json(problem, 401, "application/problem+json"));
    const error = await unwrap(api.GET("/api/v1/version")).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 401,
      code: "core.unauthenticated",
      message: "Sign in to continue.",
    });
  });

  it("turns answers from something other than the panel into problems", async () => {
    const api = client(() => new Response("<html>Bad gateway</html>", { status: 502 }));
    const error = await unwrap(api.GET("/api/v1/version")).catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 502, code: "core.unavailable" });
  });
});

describe("problems", () => {
  it("are recognized by their code", () => {
    expect(isProblem({ code: "core.conflict" })).toBe(true);
    expect(isProblem({ code: "" })).toBe(false);
    expect(isProblem("oops")).toBe(false);
    expect(isProblem(null)).toBe(false);
  });

  it("get a code for their status when the body has none", () => {
    expect(toProblem(500, "panic").code).toBe("core.internal");
    expect(toProblem(504, null).code).toBe("core.unavailable");
    expect(toProblem(400, "missing body")).toMatchObject({
      code: "core.bad_request",
      params: { reason: "missing body" },
    });
  });

  it("are shown with the translation of their code", () => {
    expect(
      problemMessage(intl, {
        type: "about:blank",
        code: "core.not_found",
        params: { what: "The site example.com" },
      }),
    ).toBe("The site example.com doesn't exist or was deleted.");
    expect(
      problemMessage(intl, {
        type: "about:blank",
        code: "web.unknown_thing",
        detail: "English from the server.",
      }),
    ).toBe("English from the server.");
    expect(problemMessage(intl, { type: "about:blank", code: "web.unknown_thing" })).toBe(
      shellMessages["error.core.internal"],
    );
  });

  it("explain any error", () => {
    expect(
      errorMessage(intl, new ApiError(401, { type: "about:blank", code: "core.unauthenticated" })),
    ).toBe("Sign in to continue.");
    expect(errorMessage(intl, new Error("chunk failed to load"))).toBe("chunk failed to load");
    expect(errorMessage(intl, 42)).toBe(shellMessages["error.core.internal"]);
  });
});
