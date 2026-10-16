import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, KeyRound, Laptop, Link2, Pencil, Puzzle, Smartphone, Trash2 } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { type FormResult, SchemaForm } from "@/design/components/schema-form";
import { Button } from "@/design/components/ui/button";
import { ConfirmDialog } from "@/design/components/ui/confirm-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { Alert, Badge } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { setAt, type UIHints } from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import {
  disableMfa,
  forgetMyDevice,
  forgetMyDevices,
  getMe,
  getMyAlerts,
  getSignInOptions,
  linkMyIdentity,
  listMyDevices,
  listMyLinks,
  listMyMfa,
  listMyPasskeys,
  type Me,
  type MFAOption,
  newRecoveryCodes,
  type PasskeyView,
  removePasskey,
  removeTotp,
  renamePasskey,
  type SignInOption,
  setMyAlerts,
  startLinkingMyIdentity,
  testMyAlerts,
  unlinkMyIdentity,
} from "../api";
import {
  Item,
  ItemList,
  ListSkeleton,
  PageHeader,
  RecoveryCodes,
  Section,
  ShownOnce,
  TimeText,
} from "../components";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { page } from "../page";
import { useSession } from "../session";
import { useStepUp } from "../step-up";
import { AddonSetup, AddPasskey, TotpSetup } from "../two-factor";
import { failureMessage } from "../util";

/**
 * Runs one of the page's actions: it asks the person to confirm it's them
 * when the panel wants that, reads the account again afterwards, and says
 * how it went in a toast.
 */
function useAction() {
  const intl = useIntl();
  const client = useQueryClient();
  const { guard } = useStepUp();
  const { refresh } = useSession();
  return async (run: () => Promise<unknown>, done?: string): Promise<boolean> => {
    try {
      await guard(run);
      await Promise.all([client.invalidateQueries({ queryKey: ["me"] }), refresh()]);
      if (done) {
        toast.success(done);
      }
      return true;
    } catch (error) {
      toast.error(failureMessage(intl, error));
      return false;
    }
  };
}

function SetupDialog({
  open,
  title,
  description,
  onOpenChange,
  children,
}: {
  open: boolean;
  title: ReactNode;
  description: ReactNode;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {open ? children : null}
      </DialogContent>
    </Dialog>
  );
}

function StateBadge({ on }: { on: boolean }) {
  const intl = useIntl();
  return (
    <Badge variant={on ? "success" : "outline"}>
      {intl.formatMessage(on ? t.stateOn : t.stateOff)}
    </Badge>
  );
}

function Heading({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <span className="flex flex-wrap items-center gap-2 [&>svg]:size-4 [&>svg]:text-muted-foreground">
      {icon}
      {children}
    </span>
  );
}

function Authenticator({ me }: { me: Me }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const act = useAction();
  const [setting, setSetting] = useState(false);
  const on = me.security.totp;
  return (
    <>
      <Section
        title={
          <Heading icon={<Smartphone aria-hidden="true" />}>
            {intl.formatMessage(t.totpTitle)}
            <StateBadge on={on} />
          </Heading>
        }
        description={intl.formatMessage(t.totpDescription)}
        actions={
          on ? (
            <ConfirmDialog
              trigger={<Button variant="outline">{intl.formatMessage(t.totpRemove)}</Button>}
              title={intl.formatMessage(t.totpRemoveTitle)}
              description={intl.formatMessage(t.totpRemoveDescription)}
              confirmLabel={intl.formatMessage(t.totpRemove)}
              onConfirm={async () => {
                await act(() => removeTotp(api), intl.formatMessage(t.totpRemoved));
              }}
            />
          ) : (
            <Button onClick={() => setSetting(true)}>{intl.formatMessage(t.totpSetUp)}</Button>
          )
        }
      />
      <SetupDialog
        open={setting}
        onOpenChange={setSetting}
        title={intl.formatMessage(t.totpSetUpTitle)}
        description={intl.formatMessage(t.totpDescription)}
      >
        <TotpSetup
          onDone={() => {
            setSetting(false);
            void client.invalidateQueries({ queryKey: ["me"] });
          }}
          onCancel={() => setSetting(false)}
        />
      </SetupDialog>
    </>
  );
}

function RenamePasskey({ passkey, onClose }: { passkey: PasskeyView; onClose: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const [name, setName] = useState(passkey.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    try {
      await renamePasskey(api, passkey.id, name.trim());
      await client.invalidateQueries({ queryKey: ["me", "passkeys"] });
      onClose();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent>
        <form onSubmit={save} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{intl.formatMessage(t.passkeyRenameTitle)}</DialogTitle>
            <DialogDescription>{intl.formatMessage(t.fieldPasskeyNameHelp)}</DialogDescription>
          </DialogHeader>
          <Field label={intl.formatMessage(t.fieldPasskeyName)}>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={64}
              required
              autoFocus
            />
          </Field>
          {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
          <DialogFooter>
            <Button variant="outline" onClick={onClose} disabled={busy}>
              {intl.formatMessage(t.cancel)}
            </Button>
            <Button type="submit" busy={busy}>
              {intl.formatMessage(t.save)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Passkeys() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const act = useAction();
  const passkeys = useQuery({ queryKey: ["me", "passkeys"], queryFn: () => listMyPasskeys(api) });
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<PasskeyView | null>(null);
  const reload = () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ["me", "passkeys"] }),
      client.invalidateQueries({ queryKey: ["me"], exact: true }),
    ]);
  return (
    <Section
      title={
        <Heading icon={<KeyRound aria-hidden="true" />}>
          {intl.formatMessage(t.passkeysTitle)}
        </Heading>
      }
      description={intl.formatMessage(t.passkeysDescription)}
      actions={<Button onClick={() => setAdding(true)}>{intl.formatMessage(t.passkeyAdd)}</Button>}
    >
      {passkeys.isPending ? <ListSkeleton rows={2} /> : null}
      {passkeys.isError ? (
        <ErrorState
          title={intl.formatMessage(t.passkeysFailed)}
          error={passkeys.error}
          onRetry={() => void passkeys.refetch()}
        />
      ) : null}
      {passkeys.data && passkeys.data.length === 0 ? (
        <p className="text-muted-foreground">{intl.formatMessage(t.passkeysNone)}</p>
      ) : null}
      {passkeys.data && passkeys.data.length > 0 ? (
        <ItemList label={intl.formatMessage(t.passkeysTitle)}>
          {passkeys.data.map((p) => (
            <Item
              key={p.id}
              icon={<KeyRound aria-hidden="true" />}
              title={p.name}
              badge={
                p.synced ? (
                  <Badge variant="outline">{intl.formatMessage(t.passkeySynced)}</Badge>
                ) : null
              }
              meta={
                <FormattedMessage
                  {...t.passkeyMeta}
                  values={{
                    added: <TimeText value={p.createdAt} />,
                    used: <TimeText value={p.lastUsedAt} />,
                  }}
                />
              }
              actions={
                <>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={intl.formatMessage(t.passkeyRename, { name: p.name })}
                    onClick={() => setRenaming(p)}
                  >
                    <Pencil aria-hidden="true" />
                  </Button>
                  <ConfirmDialog
                    trigger={
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={intl.formatMessage(t.passkeyRemove, { name: p.name })}
                      >
                        <Trash2 aria-hidden="true" />
                      </Button>
                    }
                    title={intl.formatMessage(t.passkeyRemoveTitle, { name: p.name })}
                    description={intl.formatMessage(t.passkeyRemoveDescription)}
                    confirmLabel={intl.formatMessage(t.remove)}
                    onConfirm={async () => {
                      if (await act(() => removePasskey(api, p.id))) {
                        await reload();
                      }
                    }}
                  />
                </>
              }
            />
          ))}
        </ItemList>
      ) : null}
      <SetupDialog
        open={adding}
        onOpenChange={setAdding}
        title={intl.formatMessage(t.passkeyAddTitle)}
        description={intl.formatMessage(t.passkeysDescription)}
      >
        <AddPasskey
          onDone={() => {
            setAdding(false);
            void reload();
            toast.success(intl.formatMessage(t.passkeyAdded));
          }}
          onCancel={() => setAdding(false)}
        />
      </SetupDialog>
      {renaming ? <RenamePasskey passkey={renaming} onClose={() => setRenaming(null)} /> : null}
    </Section>
  );
}

function Recovery({ me }: { me: Me }) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  const client = useQueryClient();
  const [codes, setCodes] = useState<string[] | null>(null);
  if (!me.twoFactor) {
    return null;
  }
  const left = me.security.recoveryCodes;
  return (
    <>
      <Section
        title={intl.formatMessage(t.recoveryTitle)}
        description={intl.formatMessage(t.recoveryDescription, { count: left })}
        actions={
          <ConfirmDialog
            trigger={<Button variant="outline">{intl.formatMessage(t.recoveryNew)}</Button>}
            title={intl.formatMessage(t.recoveryNewTitle)}
            description={intl.formatMessage(t.recoveryNewDescription)}
            confirmLabel={intl.formatMessage(t.recoveryNew)}
            destructive={false}
            onConfirm={async () => {
              try {
                setCodes(await guard(() => newRecoveryCodes(api)));
                await client.invalidateQueries({ queryKey: ["me"] });
              } catch (error) {
                toast.error(failureMessage(intl, error));
              }
            }}
          />
        }
      >
        {left <= 2 ? (
          <Alert tone="warning">{intl.formatMessage(t.recoveryLow, { count: left })}</Alert>
        ) : null}
      </Section>
      <ShownOnce
        open={codes !== null}
        title={intl.formatMessage(t.recoveryNewTitle)}
        description={intl.formatMessage(t.recoveryCodesKeep)}
        onClose={() => setCodes(null)}
      >
        <RecoveryCodes codes={codes ?? []} />
      </ShownOnce>
    </>
  );
}

function Addons() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const act = useAction();
  const options = useQuery({ queryKey: ["me", "mfa"], queryFn: () => listMyMfa(api) });
  const [setting, setSetting] = useState<MFAOption | null>(null);
  if (!options.data || options.data.length === 0) {
    return null;
  }
  const reload = () => client.invalidateQueries({ queryKey: ["me"] });
  return (
    <Section
      title={
        <Heading icon={<Puzzle aria-hidden="true" />}>{intl.formatMessage(t.addonsTitle)}</Heading>
      }
      description={intl.formatMessage(t.addonsDescription)}
    >
      <ItemList label={intl.formatMessage(t.addonsTitle)}>
        {options.data.map((o) => (
          <Item
            key={o.id}
            title={o.title}
            badge={<StateBadge on={o.enabled} />}
            actions={
              o.enabled ? (
                <ConfirmDialog
                  trigger={
                    <Button variant="outline" size="sm">
                      {intl.formatMessage(t.turnOff)}
                    </Button>
                  }
                  title={intl.formatMessage(t.addonOffTitle, { name: o.title })}
                  description={intl.formatMessage(t.addonOffDescription)}
                  confirmLabel={intl.formatMessage(t.turnOff)}
                  onConfirm={async () => {
                    if (await act(() => disableMfa(api, o.id))) {
                      await reload();
                    }
                  }}
                />
              ) : (
                <Button size="sm" onClick={() => setSetting(o)}>
                  {intl.formatMessage(t.turnOn)}
                </Button>
              )
            }
          />
        ))}
      </ItemList>
      <SetupDialog
        open={setting !== null}
        onOpenChange={(open) => (open ? undefined : setSetting(null))}
        title={setting?.title ?? ""}
        description={intl.formatMessage(t.addonsDescription)}
      >
        {setting ? (
          <AddonSetup
            option={setting}
            onDone={() => {
              setSetting(null);
              void reload();
            }}
            onCancel={() => setSetting(null)}
          />
        ) : null}
      </SetupDialog>
    </Section>
  );
}

/** What the browser came back with from linking at a provider's own page. */
export function readLinkReturn(hash: string): {
  linked?: string;
  error?: string;
  provider?: string;
} {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const out: { linked?: string; error?: string; provider?: string } = {};
  for (const key of ["linked", "error", "provider"] as const) {
    const value = params.get(key);
    if (value) {
      out[key] = value;
    }
  }
  return out;
}

/** Linking an account at a provider that checks passwords, such as a company directory. */
function LinkDialog({ provider, onClose }: { provider: SignInOption | null; onClose: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { guard } = useStepUp();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!provider) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await guard(() => linkMyIdentity(api, provider.id, { username: username.trim(), password }));
      toast.success(intl.formatMessage(t.linkDone, { provider: provider.title }));
      setPassword("");
      await client.invalidateQueries({ queryKey: ["me"] });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open={provider !== null} onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>
              {intl.formatMessage(t.linkTitleProvider, { provider: provider?.title ?? "" })}
            </DialogTitle>
            <DialogDescription>{intl.formatMessage(t.linkPasswordHelp)}</DialogDescription>
          </DialogHeader>
          <Field label={intl.formatMessage(t.fieldUsername)}>
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoCapitalize="none"
              spellCheck={false}
              autoComplete="off"
              required
              autoFocus
            />
          </Field>
          <Field label={intl.formatMessage(t.fieldPassword)}>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="off"
              required
            />
          </Field>
          {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
          <DialogFooter>
            <Button variant="outline" onClick={onClose} disabled={busy}>
              {intl.formatMessage(t.cancel)}
            </Button>
            <Button type="submit" busy={busy}>
              {intl.formatMessage(t.link)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Links() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const act = useAction();
  const { guard } = useStepUp();
  const links = useQuery({ queryKey: ["me", "links"], queryFn: () => listMyLinks(api) });
  const options = useQuery({
    queryKey: ["auth", "options"],
    queryFn: () => getSignInOptions(api),
    retry: false,
  });
  const [linking, setLinking] = useState<SignInOption | null>(null);
  const providers = options.data?.providers ?? [];
  const title = (id: string) => providers.find((p) => p.id === id)?.title ?? id;
  // Coming back from a provider's page says how linking went, once.
  const [arrival] = useState(() => readLinkReturn(window.location.hash));
  useEffect(() => {
    if (arrival.linked || arrival.error) {
      window.history.replaceState(null, "", window.location.pathname);
    }
    if (arrival.linked) {
      void client.invalidateQueries({ queryKey: ["me"] });
    }
  }, [arrival, client]);
  const returned = arrival.error
    ? new ApiError(400, {
        type: "about:blank",
        status: 400,
        code: arrival.error,
        params: { provider: title(arrival.provider ?? ""), reason: "" },
      })
    : null;
  const unlinked = providers.filter((p) => !(links.data ?? []).some((l) => l.provider === p.id));
  const start = async (p: SignInOption) => {
    if (p.method === "password") {
      setLinking(p);
      return;
    }
    try {
      page.load(await guard(() => startLinkingMyIdentity(api, p.id)));
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  if ((links.data ?? []).length === 0 && unlinked.length === 0 && !returned) {
    return null;
  }
  return (
    <Section
      title={
        <Heading icon={<Link2 aria-hidden="true" />}>{intl.formatMessage(t.linksTitle)}</Heading>
      }
      description={intl.formatMessage(t.linksDescription)}
    >
      {arrival.linked ? (
        <Alert tone="success">
          {intl.formatMessage(t.linkDone, { provider: title(arrival.linked) })}
        </Alert>
      ) : null}
      {returned ? <Alert tone="danger">{failureMessage(intl, returned)}</Alert> : null}
      {(links.data ?? []).length > 0 ? (
        <ItemList label={intl.formatMessage(t.linksTitle)}>
          {(links.data ?? []).map((l) => (
            <Item
              key={l.provider}
              title={l.title}
              meta={
                <FormattedMessage
                  {...t.linkMeta}
                  values={{
                    username: l.username || "none",
                    linked: <TimeText value={l.linkedAt} />,
                  }}
                />
              }
              actions={
                <ConfirmDialog
                  trigger={
                    <Button variant="outline" size="sm">
                      {intl.formatMessage(t.unlink)}
                    </Button>
                  }
                  title={intl.formatMessage(t.unlinkTitle, { provider: l.title })}
                  description={intl.formatMessage(t.unlinkDescription)}
                  confirmLabel={intl.formatMessage(t.unlink)}
                  onConfirm={async () => {
                    if (await act(() => unlinkMyIdentity(api, l.provider))) {
                      await client.invalidateQueries({ queryKey: ["me", "links"] });
                    }
                  }}
                />
              }
            />
          ))}
        </ItemList>
      ) : null}
      {unlinked.length > 0 ? (
        <ItemList label={intl.formatMessage(t.linksAvailable)}>
          {unlinked.map((p) => (
            <Item
              key={p.id}
              title={p.title}
              meta={intl.formatMessage(
                p.method === "password" ? t.linkMethodPassword : t.linkMethodRedirect,
              )}
              actions={
                <Button variant="outline" size="sm" onClick={() => void start(p)}>
                  {intl.formatMessage(t.link)}
                </Button>
              }
            />
          ))}
        </ItemList>
      ) : null}
      <LinkDialog provider={linking} onClose={() => setLinking(null)} />
    </Section>
  );
}

function Devices() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const act = useAction();
  const devices = useQuery({ queryKey: ["me", "devices"], queryFn: () => listMyDevices(api) });
  const reload = () => client.invalidateQueries({ queryKey: ["me", "devices"] });
  const list = devices.data ?? [];
  return (
    <Section
      title={
        <Heading icon={<Laptop aria-hidden="true" />}>{intl.formatMessage(t.devicesTitle)}</Heading>
      }
      description={intl.formatMessage(t.devicesDescription)}
      actions={
        list.length > 1 ? (
          <ConfirmDialog
            trigger={<Button variant="outline">{intl.formatMessage(t.devicesForgetAll)}</Button>}
            title={intl.formatMessage(t.devicesForgetAllTitle)}
            description={intl.formatMessage(t.devicesForgetAllDescription)}
            confirmLabel={intl.formatMessage(t.devicesForgetAll)}
            onConfirm={async () => {
              if (await act(() => forgetMyDevices(api))) {
                await reload();
              }
            }}
          />
        ) : null
      }
    >
      {devices.isPending ? <ListSkeleton rows={2} /> : null}
      {devices.isError ? (
        <ErrorState
          title={intl.formatMessage(t.devicesFailed)}
          error={devices.error}
          onRetry={() => void devices.refetch()}
        />
      ) : null}
      {devices.data && list.length === 0 ? (
        <p className="text-muted-foreground">{intl.formatMessage(t.devicesNone)}</p>
      ) : null}
      {list.length > 0 ? (
        <ItemList label={intl.formatMessage(t.devicesTitle)}>
          {list.map((d) => (
            <Item
              key={d.id}
              icon={<Laptop aria-hidden="true" />}
              title={d.name}
              badge={
                d.current ? (
                  <Badge variant="accent">{intl.formatMessage(t.thisBrowser)}</Badge>
                ) : null
              }
              meta={
                <FormattedMessage
                  {...t.deviceMeta}
                  values={{
                    used: <TimeText value={d.lastUsedAt} />,
                    expires: <TimeText value={d.expiresAt} />,
                    ip: d.ip || "none",
                  }}
                />
              }
              actions={
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={async () => {
                    if (await act(() => forgetMyDevice(api, d.id))) {
                      await reload();
                    }
                  }}
                >
                  {intl.formatMessage(t.deviceForget)}
                </Button>
              }
            />
          ))}
        </ItemList>
      ) : null}
    </Section>
  );
}

const noHints: UIHints = {};
const hintsOf = new WeakMap<readonly string[], UIHints>();

/**
 * Hides what people type into a channel's secret settings. The values stay
 * in the settings, which are kept encrypted whole. The same channel type
 * gets the same hints, so its form isn't built again on every render.
 */
export function secretHints(names: readonly string[] | null | undefined): UIHints {
  if (!names?.length) {
    return noHints;
  }
  let hints = hintsOf.get(names);
  if (!hints) {
    hints = { fields: Object.fromEntries(names.map((name) => [name, { widget: "password" }])) };
    hintsOf.set(names, hints);
  }
  return hints;
}

function Alerts() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const act = useAction();
  const alerts = useQuery({ queryKey: ["me", "alerts"], queryFn: () => getMyAlerts(api) });
  const [choice, setChoice] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [testing, setTesting] = useState(false);
  if (!alerts.data) {
    return alerts.isError ? (
      <ErrorState
        title={intl.formatMessage(t.alertsFailed)}
        error={alerts.error}
        onRetry={() => void alerts.refetch()}
      />
    ) : null;
  }
  const off = "off";
  const current = alerts.data.channel || off;
  const selected = choice ?? current;
  const type = alerts.data.types.find((ty) => ty.id === selected);
  const save = async (settings?: unknown) => {
    if (
      await act(
        () => setMyAlerts(api, selected === off ? "" : selected, settings),
        intl.formatMessage(t.alertsSaved),
      )
    ) {
      setChoice(null);
      setEditing(false);
      await client.invalidateQueries({ queryKey: ["me", "alerts"] });
    }
  };
  const submit = (result: FormResult) => {
    let settings = result.spec;
    for (const [path, value] of Object.entries(result.secrets)) {
      settings = setAt(settings, path, value);
    }
    void save(settings);
  };
  return (
    <Section
      title={
        <Heading icon={<Bell aria-hidden="true" />}>{intl.formatMessage(t.alertsTitle)}</Heading>
      }
      description={intl.formatMessage(t.alertsDescription)}
      actions={
        alerts.data.channel ? (
          <Button
            variant="outline"
            busy={testing}
            onClick={async () => {
              setTesting(true);
              await act(() => testMyAlerts(api), intl.formatMessage(t.alertsTested));
              setTesting(false);
            }}
          >
            {intl.formatMessage(t.alertsTest)}
          </Button>
        ) : null
      }
    >
      {alerts.data.types.length === 0 ? (
        <Alert tone="info">{intl.formatMessage(t.alertsNoChannels)}</Alert>
      ) : (
        <>
          <Field label={intl.formatMessage(t.alertsChannel)}>
            <Select value={selected} onValueChange={setChoice}>
              <SelectTrigger className="max-w-sm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={off}>{intl.formatMessage(t.alertsOff)}</SelectItem>
                {alerts.data.types.map((ty) => (
                  <SelectItem key={ty.id} value={ty.id}>
                    {ty.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {selected === current && selected !== off && !editing ? (
            <div className="flex flex-wrap items-center gap-3">
              <p className="text-muted-foreground text-sm">
                {intl.formatMessage(t.alertsSettingsHidden)}
              </p>
              {type?.settings ? (
                <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
                  {intl.formatMessage(t.alertsChangeSettings)}
                </Button>
              ) : null}
            </div>
          ) : null}
          {type?.settings && (selected !== current || editing) ? (
            <SchemaForm
              schema={type.settings as JSONSchema}
              hints={secretHints(type.secrets)}
              submitLabel={intl.formatMessage(t.save)}
              onSubmit={submit}
              onCancel={() => {
                setChoice(null);
                setEditing(false);
              }}
            />
          ) : selected !== current ? (
            <Button className="self-start" onClick={() => void save()}>
              {intl.formatMessage(t.save)}
            </Button>
          ) : null}
        </>
      )}
    </Section>
  );
}

function Security() {
  const intl = useIntl();
  const api = useApi();
  const me = useQuery({ queryKey: ["me"], queryFn: () => getMe(api) });
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.securityTitle)}
        description={intl.formatMessage(t.securityDescription)}
      />
      {me.isPending ? <ListSkeleton rows={4} /> : null}
      {me.isError ? (
        <ErrorState
          title={intl.formatMessage(t.profileFailed)}
          error={me.error}
          onRetry={() => void me.refetch()}
        />
      ) : null}
      {me.data ? (
        <>
          {!me.data.twoFactor ? (
            <Alert
              tone={me.data.security.required ? "warning" : "info"}
              title={intl.formatMessage(
                me.data.security.required ? t.twoFactorRequired : t.twoFactorSuggested,
              )}
            >
              {intl.formatMessage(t.twoFactorWhy)}
            </Alert>
          ) : null}
          <Authenticator me={me.data} />
          <Passkeys />
          <Recovery me={me.data} />
          <Addons />
          <Links />
          <Devices />
          <Alerts />
        </>
      ) : null}
    </div>
  );
}

/** How you sign in: second steps, passkeys, trusted browsers and security alerts. */
export default function SecurityPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={4} />}>
      <Security />
    </IdentityMessages>
  );
}
