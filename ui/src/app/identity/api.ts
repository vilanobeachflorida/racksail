import { type ApiClient, type Schemas, unwrap } from "@/lib/api/client";
import type { paths } from "@/lib/api/schema";

/*
 * The identity operations: signing in, the person's own account and
 * security, users, roles, accounts, the audit log and the sign-in
 * settings. Lists the panel sends as null when they're empty come back as
 * empty lists.
 */

export type SignInOptions = Schemas["SignInOptions"];
export type SignInOption = Schemas["SignInOption"];
export type SignInAnswer = Schemas["SignInAnswer"];
export type LinkInfo = Schemas["LinkInfo"];
export type Me = Schemas["Me"];
export type PasskeyView = Schemas["PasskeyView"];
export type SessionView = Schemas["SessionView"];
export type DeviceView = Schemas["DeviceView"];
export type TokenView = Schemas["TokenView"];
export type TokenInput = Schemas["TokenInput"];
export type CreatedToken = Schemas["CreatedToken"];
export type SSHKey = Schemas["SSHKey"];
export type LinkView = Schemas["LinkView"];
export type MFAOption = Schemas["MFAOption"];
export type AlertChannelType = Schemas["AlertChannelTypeView"];
export type UserView = Schemas["UserView"];
export type UserResource = Schemas["CoreUser"];
export type UserSpec = Schemas["UserSpec"];
export type PasswordSetting = Schemas["PasswordSetting"];
export type Grant = Schemas["Grant"];
export type Role = Grant["role"];
export type Scope = Schemas["Scope"];
export type ResetInput = Schemas["ResetInput"];
export type ResetResult = Schemas["ResetResult"];
export type IssuedLink = Schemas["IssuedLink"];
export type RoleInfo = Schemas["RoleInfo"];
export type MatrixEntry = Schemas["MatrixEntry"];
export type AuditRecord = Schemas["AuditRecord"];
export type Account = Schemas["CoreAccount"];
export type OpenAccountInput = Schemas["OpenAccountInput"];
export type OpenedAccount = Schemas["OpenedAccount"];
export type ChangeSet = Schemas["ChangeSet"];
export type PolicySpec = Schemas["SignInPolicySpec"];
export type SigninPolicy = Schemas["CoreSigninPolicy"];
export type SigninProvider = Schemas["CoreSigninProvider"];
export type ProviderSpec = Schemas["ProviderSpec"];

const list = <T>(items: T[] | null | undefined): T[] => items ?? [];

// Signing in.

export const getSignInOptions = (api: ApiClient) => unwrap(api.GET("/api/v1/auth/options"));

export const setUpFirstAdmin = (api: ApiClient, body: Schemas["SetupInput"]) =>
  unwrap(api.POST("/api/v1/auth/setup", { body }));

export const signIn = (api: ApiClient, body: Schemas["SignInInput"]) =>
  unwrap(api.POST("/api/v1/auth/sign-in", { body }));

export const signInSecondStep = (api: ApiClient, body: Schemas["SecondStepInput"]) =>
  unwrap(api.POST("/api/v1/auth/sign-in/second-step", { body }));

export const beginSecondStepPasskey = (api: ApiClient, challenge: string) =>
  unwrap(api.POST("/api/v1/auth/sign-in/second-step/passkey", { body: { challenge } }));

export const beginSecondStepMfa = (api: ApiClient, challenge: string, method: string) =>
  unwrap(api.POST("/api/v1/auth/sign-in/second-step/mfa", { body: { challenge, method } }));

export const beginPasskeySignIn = (api: ApiClient) => unwrap(api.POST("/api/v1/auth/passkey"));

export const finishPasskeySignIn = (
  api: ApiClient,
  body: { ceremony: string; credential: unknown; remember?: boolean },
) => unwrap(api.POST("/api/v1/auth/passkey/finish", { body }));

export const lookUpSignInLink = (api: ApiClient, code: string) =>
  unwrap(api.POST("/api/v1/auth/links/lookup", { body: { code } }));

export const redeemSignInLink = (api: ApiClient, code: string, password: string) =>
  unwrap(api.POST("/api/v1/auth/links/use", { body: { code, password } }));

export async function signOut(api: ApiClient): Promise<void> {
  await unwrap(api.POST("/api/v1/auth/sign-out"));
}

/** Where the browser goes to sign in through an outside provider. */
export function providerStartURL(id: string, remember: boolean): string {
  const path = `/api/v1/auth/providers/${encodeURIComponent(id)}/start`;
  return remember ? `${path}?remember=true` : path;
}

// Confirming it's the person, and impersonation.

export const stepUp = (api: ApiClient, body: Schemas["StepUpInput"]) =>
  unwrap(api.POST("/api/v1/session/step-up", { body }));

export const beginStepUpPasskey = (api: ApiClient) =>
  unwrap(api.POST("/api/v1/session/step-up/passkey"));

export async function stopImpersonating(api: ApiClient): Promise<void> {
  await unwrap(api.DELETE("/api/v1/session/impersonation"));
}

export async function impersonateUser(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.POST("/api/v1/core/users/{id}/impersonate", { params: { path: { id } } }));
}

// The person's own account.

export const getMe = (api: ApiClient) => unwrap(api.GET("/api/v1/me"));

/** Changes the person's own profile, through a change set like any change to a user. */
export const updateMe = (api: ApiClient, body: Schemas["ProfileInput"], dryRun = false) =>
  unwrap(api.PUT("/api/v1/me", { params: { query: { dryRun } }, body }));

export async function changeMyPassword(
  api: ApiClient,
  body: Schemas["PasswordInput"],
): Promise<void> {
  await unwrap(api.PUT("/api/v1/me/password", { body }));
}

export const beginTotp = (api: ApiClient) => unwrap(api.POST("/api/v1/me/totp"));

export const confirmTotp = async (api: ApiClient, code: string) =>
  list((await unwrap(api.POST("/api/v1/me/totp/confirm", { body: { code } }))).codes);

export async function removeTotp(api: ApiClient): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/totp"));
}

export const newRecoveryCodes = async (api: ApiClient) =>
  list((await unwrap(api.POST("/api/v1/me/recovery-codes"))).codes);

export const listMyPasskeys = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/passkeys"))).passkeys);

export const beginPasskeyRegistration = (api: ApiClient) => unwrap(api.POST("/api/v1/me/passkeys"));

export const finishPasskeyRegistration = (
  api: ApiClient,
  body: { ceremony: string; name?: string; credential: unknown },
) => unwrap(api.POST("/api/v1/me/passkeys/finish", { body }));

export const renamePasskey = (api: ApiClient, id: string, name: string) =>
  unwrap(api.PATCH("/api/v1/me/passkeys/{id}", { params: { path: { id } }, body: { name } }));

export async function removePasskey(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/passkeys/{id}", { params: { path: { id } } }));
}

export const listMySessions = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/sessions"))).sessions);

export async function endMySession(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/sessions/{id}", { params: { path: { id } } }));
}

export const endMyOtherSessions = async (api: ApiClient) =>
  (await unwrap(api.POST("/api/v1/me/sessions/end-others"))).ended;

export const listMyDevices = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/devices"))).devices);

export async function forgetMyDevice(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/devices/{id}", { params: { path: { id } } }));
}

export async function forgetMyDevices(api: ApiClient): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/devices"));
}

export const listMyTokens = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/tokens"))).tokens);

export const createMyToken = (api: ApiClient, body: TokenInput) =>
  unwrap(api.POST("/api/v1/me/tokens", { body }));

export async function revokeMyToken(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/tokens/{id}", { params: { path: { id } } }));
}

export const listMySSHKeys = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/ssh-keys"))).keys);

export const addMySSHKey = (api: ApiClient, body: Schemas["SSHKeyInput"], dryRun = false) =>
  unwrap(api.POST("/api/v1/me/ssh-keys", { params: { query: { dryRun } }, body }));

export const removeMySSHKey = (api: ApiClient, id: string, dryRun = false) =>
  unwrap(api.DELETE("/api/v1/me/ssh-keys/{id}", { params: { path: { id }, query: { dryRun } } }));

export const listMyLinks = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/links"))).links);

/** Links the person's account at a provider that checks passwords, such as a directory. */
export const linkMyIdentity = (api: ApiClient, provider: string, body: Schemas["LinkInput"]) =>
  unwrap(api.POST("/api/v1/me/links/{provider}", { params: { path: { provider } }, body }));

/** Starts linking at a provider with its own page; the browser goes to the location it answers with. */
export const startLinkingMyIdentity = async (api: ApiClient, provider: string) =>
  (await unwrap(api.POST("/api/v1/me/links/{provider}/start", { params: { path: { provider } } })))
    .location;

export async function unlinkMyIdentity(api: ApiClient, provider: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/links/{provider}", { params: { path: { provider } } }));
}

export const listMyMfa = async (api: ApiClient) =>
  list((await unwrap(api.GET("/api/v1/me/mfa"))).methods);

export const beginMfa = (api: ApiClient, id: string) =>
  unwrap(api.POST("/api/v1/me/mfa/{id}", { params: { path: { id } } }));

export async function finishMfa(
  api: ApiClient,
  id: string,
  ceremony: string,
  response: string,
): Promise<void> {
  await unwrap(
    api.POST("/api/v1/me/mfa/{id}/finish", {
      params: { path: { id } },
      body: { ceremony, response },
    }),
  );
}

export async function disableMfa(api: ApiClient, id: string): Promise<void> {
  await unwrap(api.DELETE("/api/v1/me/mfa/{id}", { params: { path: { id } } }));
}

export async function getMyAlerts(api: ApiClient) {
  const answer = await unwrap(api.GET("/api/v1/me/alerts"));
  return { channel: answer.channel ?? "", types: list(answer.types) };
}

export async function setMyAlerts(api: ApiClient, type: string, settings?: unknown): Promise<void> {
  await unwrap(
    api.PUT("/api/v1/me/alerts", {
      body: settings === undefined ? { type } : { type, settings },
    }),
  );
}

export async function testMyAlerts(api: ApiClient): Promise<void> {
  await unwrap(api.POST("/api/v1/me/alerts/test"));
}

// Users and roles. Users are core.user resources: who they are and what
// they may do. How they sign in is theirs alone, and the panel shows only
// what it last saw of it, in the user's status.

export type UserQuery = NonNullable<paths["/api/v1/core/users"]["get"]["parameters"]["query"]>;

/** A user as the screens show them, from their resource. */
export function userView(r: UserResource): UserView {
  const spec = r.spec;
  const status = r.status;
  return {
    id: r.id,
    username: spec.username,
    name: spec.name ?? "",
    email: spec.email ?? "",
    locale: spec.locale ?? "",
    home: r.scope,
    grants: list(spec.grants),
    allowFrom: list(spec.allowFrom),
    disabled: spec.disabled ?? false,
    passwordSet: status?.passwordSet ?? false,
    mustChangePassword: status?.mustChangePassword ?? false,
    twoFactor: status?.twoFactor ?? false,
    ...(status?.lockedUntil ? { lockedUntil: status.lockedUntil } : {}),
    ...(status?.lastSignInAt ? { lastSignInAt: status.lastSignInAt } : {}),
    ...(status?.lastSignInIp ? { lastSignInIp: status.lastSignInIp } : {}),
    revision: r.revision,
    createdAt: r.createdAt,
    updatedAt: r.updatedAt,
  };
}

export async function listUsers(api: ApiClient, query: UserQuery = {}) {
  const answer = await unwrap(api.GET("/api/v1/core/users", { params: { query } }));
  return { users: list(answer.items), next: answer.next ?? "" };
}

export const getUser = (api: ApiClient, id: string) =>
  unwrap(api.GET("/api/v1/core/users/{id}", { params: { path: { id } } }));

/** Adds a user; id, when given, is the one a preview showed, so applying it adds the same user. */
export const createUser = (
  api: ApiClient,
  body: { scope: Scope; spec: UserSpec; id?: string },
  dryRun: boolean,
) => unwrap(api.POST("/api/v1/core/users", { params: { query: { dryRun } }, body }));

export const updateUser = (
  api: ApiClient,
  id: string,
  revision: number,
  spec: UserSpec,
  dryRun: boolean,
) =>
  unwrap(
    api.PUT("/api/v1/core/users/{id}", {
      params: { path: { id }, query: { dryRun } },
      body: { spec, revision },
    }),
  );

export const deleteUser = (api: ApiClient, id: string, dryRun: boolean) =>
  unwrap(api.DELETE("/api/v1/core/users/{id}", { params: { path: { id }, query: { dryRun } } }));

/** Sets a user's password, or only whether they choose a new one when they next sign in. */
export async function setUserPassword(
  api: ApiClient,
  id: string,
  body: PasswordSetting,
): Promise<void> {
  await unwrap(api.PUT("/api/v1/core/users/{id}/password", { params: { path: { id } }, body }));
}

export const resetUser = (api: ApiClient, id: string, body: ResetInput) =>
  unwrap(api.POST("/api/v1/core/users/{id}/reset", { params: { path: { id } }, body }));

export const issueSignInLink = (api: ApiClient, id: string) =>
  unwrap(api.POST("/api/v1/core/users/{id}/link", { params: { path: { id } } }));

export const listUserSessions = async (api: ApiClient, id: string) =>
  list(
    (await unwrap(api.GET("/api/v1/core/users/{id}/sessions", { params: { path: { id } } })))
      .sessions,
  );

export const listUserTokens = async (api: ApiClient, id: string) =>
  list(
    (await unwrap(api.GET("/api/v1/core/users/{id}/tokens", { params: { path: { id } } }))).tokens,
  );

export async function revokeUserToken(api: ApiClient, id: string, token: string): Promise<void> {
  await unwrap(
    api.DELETE("/api/v1/core/users/{id}/tokens/{token}", { params: { path: { id, token } } }),
  );
}

export const listUserSSHKeys = async (api: ApiClient, id: string) =>
  list(
    (await unwrap(api.GET("/api/v1/core/users/{id}/ssh-keys", { params: { path: { id } } }))).keys,
  );

/** Removes one of a user's SSH keys, through the change set of the user's remove_ssh_key action. */
export const removeUserSSHKey = (api: ApiClient, id: string, key: string, dryRun: boolean) =>
  unwrap(
    api.POST("/api/v1/core/users/{id}/actions/remove_ssh_key", {
      params: { path: { id }, query: { dryRun } },
      body: { id: key },
    }),
  );

export async function listRoles(api: ApiClient) {
  const answer = await unwrap(api.GET("/api/v1/roles"));
  return { roles: list(answer.roles), matrix: list(answer.matrix) };
}

// Accounts.

export type AccountQuery = NonNullable<
  paths["/api/v1/core/accounts"]["get"]["parameters"]["query"]
>;

export async function listAccounts(api: ApiClient, query: AccountQuery = {}) {
  const answer = await unwrap(api.GET("/api/v1/core/accounts", { params: { query } }));
  return { items: list(answer.items), next: answer.next ?? "" };
}

export const getAccount = (api: ApiClient, id: string) =>
  unwrap(api.GET("/api/v1/core/accounts/{id}", { params: { path: { id } } }));

export const openAccount = (api: ApiClient, body: OpenAccountInput, dryRun: boolean) =>
  unwrap(api.POST("/api/v1/core/accounts/open", { params: { query: { dryRun } }, body }));

export const updateAccount = (
  api: ApiClient,
  id: string,
  body: Schemas["CoreAccountUpdateRequest"],
  dryRun: boolean,
) =>
  unwrap(
    api.PUT("/api/v1/core/accounts/{id}", {
      params: { path: { id }, query: { dryRun } },
      body,
    }),
  );

export const deleteAccount = (api: ApiClient, id: string, revision: number, dryRun: boolean) =>
  unwrap(
    api.DELETE("/api/v1/core/accounts/{id}", {
      params: { path: { id }, query: { revision, dryRun } },
    }),
  );

// The audit log.

export type AuditQuery = NonNullable<paths["/api/v1/audit"]["get"]["parameters"]["query"]>;

export async function listAudit(api: ApiClient, query: AuditQuery = {}) {
  const answer = await unwrap(api.GET("/api/v1/audit", { params: { query } }));
  return { records: list(answer.records), next: answer.next ?? "" };
}

/** Where the audit log downloads from, with the same filters as the list. */
export function auditExportURL(query: AuditQuery, format: "csv" | "json"): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "" || key === "cursor") {
      continue;
    }
    for (const item of Array.isArray(value) ? value : [value]) {
      params.append(key, String(item));
    }
  }
  params.set("format", format);
  return `/api/v1/audit/export?${params.toString()}`;
}

// Sign-in settings.

export async function listSigninPolicies(api: ApiClient) {
  return list((await unwrap(api.GET("/api/v1/core/signin-policies"))).items);
}

export const createSigninPolicy = (api: ApiClient, spec: PolicySpec, dryRun: boolean) =>
  unwrap(
    api.POST("/api/v1/core/signin-policies", { params: { query: { dryRun } }, body: { spec } }),
  );

export const updateSigninPolicy = (
  api: ApiClient,
  id: string,
  revision: number,
  spec: PolicySpec,
  dryRun: boolean,
) =>
  unwrap(
    api.PUT("/api/v1/core/signin-policies/{id}", {
      params: { path: { id }, query: { dryRun } },
      body: { spec, revision },
    }),
  );

export async function listSigninProviders(api: ApiClient) {
  return list((await unwrap(api.GET("/api/v1/core/signin-providers"))).items);
}

export const createSigninProvider = (
  api: ApiClient,
  spec: ProviderSpec,
  secrets: Record<string, string>,
  dryRun: boolean,
) =>
  unwrap(
    api.POST("/api/v1/core/signin-providers", {
      params: { query: { dryRun } },
      body: { spec, secrets },
    }),
  );

export const updateSigninProvider = (
  api: ApiClient,
  id: string,
  revision: number,
  spec: ProviderSpec,
  secrets: Record<string, string>,
  dryRun: boolean,
) =>
  unwrap(
    api.PUT("/api/v1/core/signin-providers/{id}", {
      params: { path: { id }, query: { dryRun } },
      body: { spec, secrets, revision },
    }),
  );

export const deleteSigninProvider = (
  api: ApiClient,
  id: string,
  revision: number,
  dryRun: boolean,
) =>
  unwrap(
    api.DELETE("/api/v1/core/signin-providers/{id}", {
      params: { path: { id }, query: { revision, dryRun } },
    }),
  );

/** The JSON Schema and form hints of a kind, for forms that build themselves. */
export const getKind = (api: ApiClient, name: string) =>
  unwrap(api.GET("/api/v1/kinds/{name}", { params: { path: { name } } }));
