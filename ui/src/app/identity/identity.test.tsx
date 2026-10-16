import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import type { PanelSession } from "@/lib/api/session";
import { fakeApi, json, renderApp, renderWithProviders } from "@/test/render";
import TokensPage from "./account/tokens";
import { auditExportURL } from "./api";
import { ImpersonationBanner } from "./banner";
import { page } from "./page";
import { auditQuery } from "./people/audit";
import { SessionProvider } from "./session";
import { readArrival } from "./sign-in";
import { StepUpProvider, useStepUp } from "./step-up";
import { initials } from "./user-menu";
import { parseScope, parseSize, sizeText } from "./util";

const problem = (status: number, code: string, detail = "") =>
  json({ type: "about:blank", status, code, detail }, status);

const shell = {
  "GET /api/v1/health": () => json({ status: "ok", checks: [] }),
  "GET /api/v1/version": () =>
    json({ version: "1.2.3", channel: "stable", goVersion: "go1.27", os: "linux", arch: "arm64" }),
  "GET /api/v1/jobs": () => json({ jobs: [] }),
  "GET /api/v1/events": () => json({ events: [] }),
};

const ada: PanelSession = {
  user: { id: "user_1", name: "Ada Lovelace", username: "ada", home: { level: "server" } },
  permissions: ["*"],
  modules: {},
  stepUpMethods: ["password", "totp"],
  accounts: [],
};

beforeEach(() => {
  window.history.replaceState(null, "", "/");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("signing in", () => {
  it("takes a password, then the second step, then opens the panel", async () => {
    let signedIn = false;
    const sent: Record<string, unknown> = {};
    renderApp(
      fakeApi({
        ...shell,
        "GET /api/v1/session": () => (signedIn ? json(ada) : problem(401, "core.unauthenticated")),
        "GET /api/v1/auth/options": () =>
          json({
            setupRequired: false,
            passkeys: false,
            providers: [{ id: "signprov_1", title: "Acme SSO", method: "redirect" }],
          }),
        "POST /api/v1/auth/sign-in": async (r) => {
          sent.first = await r.json();
          return json({
            signedIn: false,
            challenge: "chal_1",
            methods: ["totp", "recovery"],
            name: "Ada",
          });
        },
        "POST /api/v1/auth/sign-in/second-step": async (r) => {
          sent.second = await r.json();
          signedIn = true;
          return json({ signedIn: true });
        },
      }),
    );
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Sign in with Acme SSO" })).toHaveAttribute(
      "href",
      "/api/v1/auth/providers/signprov_1/start",
    );
    await userEvent.type(screen.getByLabelText("Username"), "ada");
    await userEvent.type(screen.getByLabelText("Password"), "correct horse battery");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("heading", { name: "One more step" })).toBeInTheDocument();
    expect(screen.getByText("Hello, Ada.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Code"), "123 456");
    await userEvent.click(screen.getByRole("checkbox", { name: /Trust this browser/ }));
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByRole("navigation", { name: "Main" })).toBeInTheDocument();
    expect(sent.first).toEqual({
      username: "ada",
      password: "correct horse battery",
      remember: false,
    });
    expect(sent.second).toEqual({
      challenge: "chal_1",
      method: "totp",
      code: "123456",
      trust: true,
    });
  });

  it("says what went wrong, and offers the other second steps", async () => {
    renderApp(
      fakeApi({
        ...shell,
        "GET /api/v1/session": () => problem(401, "core.unauthenticated"),
        "GET /api/v1/auth/options": () =>
          json({ setupRequired: false, passkeys: false, providers: [] }),
        "POST /api/v1/auth/sign-in": () =>
          json({ signedIn: false, challenge: "chal_1", methods: ["totp", "recovery"] }),
        "POST /api/v1/auth/sign-in/second-step": () => problem(401, "auth.bad_code"),
      }),
    );
    await userEvent.type(await screen.findByLabelText("Username"), "ada");
    await userEvent.type(screen.getByLabelText("Password"), "correct horse battery");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await userEvent.click(await screen.findByRole("button", { name: "A recovery code" }));
    await userEvent.type(screen.getByLabelText("Recovery code"), "abcde-fghij");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "That code isn't right, or it was used already.",
    );
  });

  it("makes the first admin with the setup code", async () => {
    let made = false;
    let body: unknown;
    renderApp(
      fakeApi({
        ...shell,
        "GET /api/v1/session": () => (made ? json(ada) : problem(401, "core.unauthenticated")),
        "GET /api/v1/auth/options": () =>
          json({ setupRequired: true, passkeys: false, providers: [] }),
        "POST /api/v1/auth/setup": async (r) => {
          body = await r.json();
          made = true;
          return json({ signedIn: true });
        },
      }),
    );
    expect(await screen.findByRole("heading", { name: "Welcome to Racksail" })).toBeInTheDocument();
    await userEvent.type(await screen.findByLabelText("Setup code"), " 1234-5678 ");
    await userEvent.type(screen.getByLabelText("Username"), "ada");
    await userEvent.type(screen.getByLabelText("New password"), "correct horse battery");
    await userEvent.click(screen.getByRole("button", { name: "Make the admin and sign in" }));
    expect(await screen.findByRole("navigation", { name: "Main" })).toBeInTheDocument();
    expect(body).toEqual({ code: "1234-5678", username: "ada", password: "correct horse battery" });
  });

  it("chooses a password with a sign-in link", async () => {
    window.history.replaceState(null, "", "/activate#rsi_code");
    let used = false;
    let body: unknown;
    renderApp(
      fakeApi({
        ...shell,
        "GET /api/v1/session": () => (used ? json(ada) : problem(401, "core.unauthenticated")),
        "POST /api/v1/auth/links/lookup": () =>
          json({ username: "ada", purpose: "activate", expiresAt: "2030-01-01T10:00:00Z" }),
        "POST /api/v1/auth/links/use": async (r) => {
          body = await r.json();
          used = true;
          return json({ signedIn: true });
        },
      }),
      "/activate",
    );
    expect(
      await screen.findByRole("heading", { name: "Choose your password" }),
    ).toBeInTheDocument();
    await userEvent.type(await screen.findByLabelText("New password"), "correct horse battery");
    await userEvent.type(screen.getByLabelText("New password again"), "correct horse batterz");
    expect(screen.getByText("The two passwords aren't the same.")).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("New password again"));
    await userEvent.type(screen.getByLabelText("New password again"), "correct horse battery");
    await userEvent.click(screen.getByRole("button", { name: "Save the password and sign in" }));
    await waitFor(() =>
      expect(body).toEqual({ code: "rsi_code", password: "correct horse battery" }),
    );
    await waitFor(() => expect(window.location.pathname).toBe("/"));
  });

  it("reads what the address brings", () => {
    expect(readArrival({ pathname: "/activate", hash: "#rsi_abc" })).toEqual({
      kind: "activate",
      code: "rsi_abc",
    });
    expect(
      readArrival({ pathname: "/sign-in", hash: "#challenge=c1&methods=totp,passkey" }),
    ).toEqual({
      kind: "second-step",
      challenge: "c1",
      methods: ["totp", "passkey"],
    });
    expect(readArrival({ pathname: "/sign-in", hash: "#error=auth.identity_unlinked" })).toEqual({
      kind: "error",
      code: "auth.identity_unlinked",
    });
    expect(readArrival({ pathname: "/sites", hash: "" })).toEqual({ kind: "sign-in" });
  });

  it("asks someone who must first set up a second step to do it", async () => {
    renderApp(
      fakeApi({
        ...shell,
        "GET /api/v1/session": () => json({ ...ada, pending: "two-factor" }),
        "GET /api/v1/me/mfa": () => json({ methods: [] }),
      }),
    );
    expect(
      await screen.findByRole("heading", { name: "Add a second sign-in step" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Authenticator app/ })).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Main" })).not.toBeInTheDocument();
  });
});

function Guarded() {
  const api = useApi();
  const { guard } = useStepUp();
  const [result, setResult] = useState("");
  return (
    <>
      <button
        type="button"
        onClick={() =>
          guard(async () => {
            const { response } = await api.POST("/api/v1/me/tokens", { body: { name: "ci" } });
            if (!response.ok) {
              throw new ApiError(response.status, {
                type: "about:blank",
                status: response.status,
                code: "auth.step_up_required",
              });
            }
            return "made";
          }).then(setResult, () => setResult("refused"))
        }
      >
        Make
      </button>
      <output>{result}</output>
    </>
  );
}

describe("confirming it's you", () => {
  it("asks before a sensitive action and runs it again once confirmed", async () => {
    let confirmed = false;
    let password = "";
    const api = fakeApi({
      "POST /api/v1/me/tokens": () =>
        confirmed ? json({ id: "tok_1" }, 201) : problem(403, "auth.step_up_required"),
      "POST /api/v1/session/step-up": async (r) => {
        password = ((await r.json()) as { password: string }).password;
        confirmed = true;
        return json({ steppedUpUntil: "2030-01-01T10:00:00Z" });
      },
    });
    renderWithProviders(
      <SessionProvider value={{ session: ada, refresh: async () => {} }}>
        <StepUpProvider>
          <Guarded />
        </StepUpProvider>
      </SessionProvider>,
      { api },
    );
    await userEvent.click(screen.getByRole("button", { name: "Make" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm it's you" });
    await userEvent.type(
      within(dialog).getByLabelText("Password", { selector: "input" }),
      "my password",
    );
    await userEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("made"));
    expect(password).toBe("my password");
  });

  it("gives up when the person doesn't confirm", async () => {
    const api = fakeApi({ "POST /api/v1/me/tokens": () => problem(403, "auth.step_up_required") });
    renderWithProviders(
      <SessionProvider value={{ session: ada, refresh: async () => {} }}>
        <StepUpProvider>
          <Guarded />
        </StepUpProvider>
      </SessionProvider>,
      { api },
    );
    await userEvent.click(screen.getByRole("button", { name: "Make" }));
    const dialog = await screen.findByRole("dialog", { name: "Confirm it's you" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("refused"));
  });
});

describe("API tokens", () => {
  it("makes a token and shows it once", async () => {
    let made: unknown;
    const tokens: unknown[] = [];
    const api = fakeApi({
      "GET /api/v1/me/tokens": () => json({ tokens }),
      "GET /api/v1/core/accounts": () => json({ items: [], next: "" }),
      "POST /api/v1/me/tokens": async (r) => {
        made = await r.json();
        const token = {
          id: "tok_1",
          user: "user_1",
          name: "Deploys",
          hint: "rsk_abcd",
          permissions: ["*.read"],
          accounts: null,
          allowFrom: null,
          createdAt: "2026-10-15T10:00:00Z",
        };
        tokens.push(token);
        return json({ ...token, token: "rsk_abcdsecret" }, 201);
      },
    });
    renderWithProviders(<TokensPage />, { api });
    expect(await screen.findByText("No API tokens yet")).toBeInTheDocument();
    await userEvent.click(
      screen.getAllByRole("button", { name: "Make a token" })[0] as HTMLElement,
    );
    const dialog = await screen.findByRole("dialog", { name: "Make an API token" });
    await userEvent.type(within(dialog).getByLabelText("Name"), "Deploys");
    await userEvent.click(within(dialog).getByRole("radio", { name: "Only look" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Make a token" }));
    const shown = await screen.findByRole("dialog", { name: "Your token Deploys" });
    expect(within(shown).getByDisplayValue("rsk_abcdsecret")).toBeInTheDocument();
    expect(made).toMatchObject({ name: "Deploys", permissions: ["*.read"] });
    expect((made as { expiresAt: string }).expiresAt).toMatch(/^\d{4}-/);
    await userEvent.click(within(shown).getByRole("button", { name: "I've kept it" }));
    expect(await screen.findByText("Deploys")).toBeInTheDocument();
    expect(screen.queryByDisplayValue("rsk_abcdsecret")).not.toBeInTheDocument();
  });
});

describe("viewing the panel as someone", () => {
  it("shows the banner the whole time and stops on request", async () => {
    const load = vi.spyOn(page, "load").mockImplementation(() => {});
    let stopped = false;
    const api = fakeApi({
      "DELETE /api/v1/session/impersonation": () => {
        stopped = true;
        return new Response(null, { status: 204 });
      },
    });
    const viewed: PanelSession = {
      ...ada,
      user: { id: "user_2", name: "Bea" },
      impersonation: {
        by: "user_1",
        name: "Ada",
        startedAt: "2026-10-15T10:00:00Z",
        endsAt: "2026-10-15T11:00:00Z",
      },
    };
    renderWithProviders(
      <SessionProvider value={{ session: viewed, refresh: async () => {} }}>
        <ImpersonationBanner />
      </SessionProvider>,
      { api },
    );
    const banner = screen.getByRole("region", { name: "Viewing the panel as someone else" });
    expect(banner).toHaveTextContent("Ada, you're viewing the panel as Bea");
    await userEvent.click(within(banner).getByRole("button", { name: "Stop viewing as Bea" }));
    await waitFor(() => expect(load).toHaveBeenCalledWith("/"));
    expect(stopped).toBe(true);
  });
});

describe("helpers", () => {
  it("read owners, sizes and initials as people write them", () => {
    expect(parseScope("server")).toEqual({ level: "server" });
    expect(parseScope("acct_1")).toEqual({ level: "account", id: "acct_1" });
    expect(parseScope("reseller:user_9")).toEqual({ level: "reseller", id: "user_9" });
    expect(parseScope("nobody")).toBeNull();
    expect(parseSize("2G")).toBe(2 * 1024 ** 3);
    expect(parseSize("512m")).toBe(512 * 1024 ** 2);
    expect(parseSize("1.5GiB")).toBe(1.5 * 1024 ** 3);
    expect(parseSize("lots")).toBeNull();
    expect(sizeText(2 * 1024 ** 3)).toBe("2G");
    expect(sizeText(1536 * 1024 ** 2)).toBe("1536M");
    expect(initials("Ada Lovelace")).toBe("AL");
    expect(initials("ada")).toBe("A");
    expect(initials("Ana María de la Cruz")).toBe("AC");
  });

  it("filter and export the audit log the same way", () => {
    const query = auditQuery({
      outcome: "denied",
      actor: "user_1",
      action: "core.user.*, *.delete",
      subject: "",
      kind: "core.account",
      since: "2026-10-01",
      until: "",
    });
    expect(query).toMatchObject({
      outcome: "denied",
      actor: "user_1",
      action: ["core.user.*", "*.delete"],
      kind: "core.account",
    });
    expect(query.since).toMatch(/^2026-(09-30|10-01)T/);
    const url = new URL(auditExportURL({ ...query, cursor: "c2" }, "csv"), "http://panel.test");
    expect(url.pathname).toBe("/api/v1/audit/export");
    expect(url.searchParams.getAll("action")).toEqual(["core.user.*", "*.delete"]);
    expect(url.searchParams.get("format")).toBe("csv");
    expect(url.searchParams.has("cursor")).toBe(false);
  });
});
