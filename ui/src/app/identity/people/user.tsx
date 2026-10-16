import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Eye, LifeBuoy, Link2, Trash2 } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Checkbox, Switch } from "@/design/components/ui/choice";
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
import { Field, Label } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import { ErrorState } from "@/design/components/ui/states";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/design/components/ui/tabs";
import { toast } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import { usePermission } from "../../modules";
import { SessionList } from "../account/sessions";
import { KeyList } from "../account/ssh-keys";
import { TokenList } from "../account/tokens";
import {
  deleteUser,
  type Grant,
  getUser,
  type IssuedLink,
  impersonateUser,
  issueSignInLink,
  listUserSessions,
  listUserSSHKeys,
  listUserTokens,
  type ResetInput,
  removeUserSSHKey,
  resetUser,
  revokeUserToken,
  setUserPassword,
  type UserResource,
  type UserSpec,
  type UserView,
  updateUser,
  userView,
} from "../api";
import { ListSkeleton, PageHeader, PasswordInput, Section, TimeText } from "../components";
import { GrantEditor, GrantSummary, useAccountNames } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { page } from "../page";
import { PreviewDialog } from "../preview";
import { useSession } from "../session";
import { useStepUp } from "../step-up";
import { failureMessage, ownerLabel } from "../util";
import { SignInLink, UserStatus, userState } from "./users";

/**
 * Saves a change to a user's settings: a change set made from the
 * revision the page shows, so a change someone else made meanwhile isn't
 * lost but reported.
 */
function useSave(user: UserResource) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { guard } = useStepUp();
  return async (change: Partial<UserSpec>, done: string): Promise<boolean> => {
    try {
      await guard(() =>
        updateUser(api, user.id, user.revision, { ...user.spec, ...change }, false),
      );
      await client.invalidateQueries({ queryKey: ["users", user.id], exact: true });
      await client.invalidateQueries({ queryKey: ["users"], exact: true });
      toast.success(done);
      return true;
    } catch (error) {
      toast.error(failureMessage(intl, error));
      return false;
    }
  };
}

function Details({ user, editable }: { user: UserResource; editable: boolean }) {
  const intl = useIntl();
  const save = useSave(user);
  const names = useAccountNames();
  const spec = user.spec;
  const [name, setName] = useState(spec.name ?? "");
  const [email, setEmail] = useState(spec.email ?? "");
  const [locale, setLocale] = useState(spec.locale ?? "");
  const [allowFrom, setAllowFrom] = useState((spec.allowFrom ?? []).join("\n"));
  const [disabled, setDisabled] = useState(spec.disabled ?? false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setName(user.spec.name ?? "");
    setEmail(user.spec.email ?? "");
    setLocale(user.spec.locale ?? "");
    setAllowFrom((user.spec.allowFrom ?? []).join("\n"));
    setDisabled(user.spec.disabled ?? false);
  }, [user]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    await save(
      {
        name: name.trim(),
        email: email.trim(),
        locale: locale.trim(),
        allowFrom: allowFrom.split(/[\s,]+/).filter(Boolean),
        disabled,
      },
      intl.formatMessage(t.userSaved, { user: spec.username }),
    );
    setBusy(false);
  };
  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={intl.formatMessage(t.fieldName)}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
            disabled={!editable}
          />
        </Field>
        <Field label={intl.formatMessage(t.fieldEmail)}>
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            maxLength={254}
            disabled={!editable}
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldLocale)}
          description={intl.formatMessage(t.fieldLocaleHelp)}
        >
          <Input
            value={locale}
            onChange={(e) => setLocale(e.target.value)}
            maxLength={35}
            disabled={!editable}
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldHome)}
          description={intl.formatMessage(t.userHomeFixed)}
        >
          <Input value={ownerLabel(intl, user.scope, names)} readOnly />
        </Field>
      </div>
      <Field
        label={intl.formatMessage(t.fieldAllowFrom)}
        description={intl.formatMessage(t.fieldUserAllowFromHelp)}
      >
        <Textarea
          value={allowFrom}
          onChange={(e) => setAllowFrom(e.target.value)}
          spellCheck={false}
          disabled={!editable}
        />
      </Field>
      <div className="flex items-start gap-3">
        <Switch
          id="disabled"
          checked={disabled}
          onCheckedChange={setDisabled}
          disabled={!editable}
        />
        <div className="flex flex-col gap-0.5">
          <Label htmlFor="disabled">{intl.formatMessage(t.userTurnOff)}</Label>
          <p className="text-muted-foreground text-xs">{intl.formatMessage(t.userTurnOffHelp)}</p>
        </div>
      </div>
      {editable ? (
        <Button type="submit" busy={busy} className="self-start">
          {intl.formatMessage(t.save)}
        </Button>
      ) : null}
    </form>
  );
}

/** A user's roles, changed after a preview of what the change set does. */
function Roles({ user, editable }: { user: UserResource; editable: boolean }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const current = user.spec.grants ?? [];
  const [grants, setGrants] = useState<Grant[]>(current);
  const [previewing, setPreviewing] = useState(false);
  useEffect(() => setGrants(user.spec.grants ?? []), [user]);
  const changed = JSON.stringify(grants) !== JSON.stringify(current);
  const spec = { ...user.spec, grants };
  return (
    <div className="flex flex-col gap-4">
      <p className="text-muted-foreground text-sm">{intl.formatMessage(t.grantsHelp)}</p>
      {editable ? (
        <GrantEditor value={grants} onChange={setGrants} />
      ) : (
        <GrantSummary grants={current} />
      )}
      {editable && changed ? (
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => setPreviewing(true)}>{intl.formatMessage(t.save)}</Button>
          <Button variant="outline" onClick={() => setGrants(current)}>
            {intl.formatMessage(t.cancel)}
          </Button>
        </div>
      ) : null}
      <PreviewDialog
        open={previewing}
        title={intl.formatMessage(t.userRolesPreview, { user: user.spec.username })}
        preview={() => updateUser(api, user.id, user.revision, spec, true)}
        apply={() => updateUser(api, user.id, user.revision, spec, false)}
        onApplied={async () => {
          setPreviewing(false);
          toast.success(intl.formatMessage(t.userRolesSaved, { user: user.spec.username }));
          await client.invalidateQueries({ queryKey: ["users", user.id], exact: true });
          await client.invalidateQueries({ queryKey: ["users"], exact: true });
        }}
        onClose={() => setPreviewing(false)}
      />
    </div>
  );
}

/** Setting someone's password, or asking them to choose a new one. */
function Password({ user }: { user: UserView }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { guard } = useStepUp();
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [mustChange, setMustChange] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const refresh = () => client.invalidateQueries({ queryKey: ["users", user.id], exact: true });
  const ask = async (on: boolean) => {
    try {
      await guard(() => setUserPassword(api, user.id, { mustChange: on }));
      toast.success(
        intl.formatMessage(on ? t.userMustChangeOn : t.userMustChangeOff, { user: user.username }),
      );
      await refresh();
    } catch (e) {
      toast.error(failureMessage(intl, e));
    }
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await guard(() => setUserPassword(api, user.id, { password, mustChange }));
      setOpen(false);
      setPassword("");
      toast.success(intl.formatMessage(t.userPasswordSet, { user: user.username }));
      await refresh();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-start gap-3">
        <Switch
          id="must-change"
          checked={user.mustChangePassword ?? false}
          onCheckedChange={(on) => void ask(on)}
          disabled={!user.passwordSet}
        />
        <div className="flex flex-col gap-0.5">
          <Label htmlFor="must-change">{intl.formatMessage(t.userMustChange)}</Label>
          <p className="text-muted-foreground text-xs">
            {intl.formatMessage(user.passwordSet ? t.userMustChangeHelp : t.userNoPassword)}
          </p>
        </div>
      </div>
      <Button variant="outline" className="self-start" onClick={() => setOpen(true)}>
        {intl.formatMessage(t.userSetPassword)}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <form onSubmit={submit} className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>
                {intl.formatMessage(t.userSetPasswordTitle, { user: user.username })}
              </DialogTitle>
              <DialogDescription>{intl.formatMessage(t.userSetPasswordHelp)}</DialogDescription>
            </DialogHeader>
            <Field label={intl.formatMessage(t.fieldPassword)}>
              <PasswordInput
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                minLength={10}
                required
                autoFocus
              />
            </Field>
            <div className="flex items-center gap-2">
              <Checkbox
                id="set-must-change"
                checked={mustChange}
                onCheckedChange={(v) => setMustChange(v === true)}
              />
              <Label htmlFor="set-must-change" className="font-normal">
                {intl.formatMessage(t.userMustChange)}
              </Label>
            </div>
            {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
            <DialogFooter>
              <Button variant="outline" onClick={() => setOpen(false)} disabled={busy}>
                {intl.formatMessage(t.cancel)}
              </Button>
              <Button type="submit" busy={busy}>
                {intl.formatMessage(t.userSetPassword)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function SignIns({ user, canReset, self }: { user: UserView; canReset: boolean; self: boolean }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { guard } = useStepUp();
  const sessions = useQuery({
    queryKey: ["users", user.id, "sessions"],
    queryFn: () => listUserSessions(api, user.id),
    enabled: canReset,
  });
  const tokens = useQuery({
    queryKey: ["users", user.id, "tokens"],
    queryFn: () => listUserTokens(api, user.id),
    enabled: canReset,
  });
  const keys = useQuery({
    queryKey: ["users", user.id, "ssh-keys"],
    queryFn: () => listUserSSHKeys(api, user.id),
  });
  const act = async (run: () => Promise<unknown>, key: string, done: string) => {
    try {
      await guard(run);
      toast.success(done);
      await client.invalidateQueries({ queryKey: ["users", user.id, key] });
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  return (
    <div className="flex flex-col gap-6">
      {canReset && !self ? (
        <Section
          title={intl.formatMessage(t.userPasswordTitle)}
          description={intl.formatMessage(t.userPasswordDescription)}
        >
          <Password user={user} />
        </Section>
      ) : null}
      {canReset ? (
        <Section
          title={intl.formatMessage(self ? t.sessionsTitle : t.userSessionsTitle)}
          description={intl.formatMessage(
            self ? t.userSessionsYoursDescription : t.userSessionsDescription,
          )}
        >
          {sessions.isPending ? <ListSkeleton rows={2} /> : null}
          {sessions.data?.length === 0 ? (
            <p className="text-muted-foreground">{intl.formatMessage(t.userSessionsNone)}</p>
          ) : null}
          {sessions.data && sessions.data.length > 0 ? (
            <SessionList sessions={sessions.data} />
          ) : null}
        </Section>
      ) : null}
      {canReset ? (
        <Section
          title={intl.formatMessage(t.tokensTitle)}
          description={intl.formatMessage(
            self ? t.userTokensYoursDescription : t.userTokensDescription,
          )}
        >
          {tokens.isPending ? <ListSkeleton rows={2} /> : null}
          {tokens.data?.length === 0 ? (
            <p className="text-muted-foreground">{intl.formatMessage(t.tokensNone)}</p>
          ) : null}
          {tokens.data && tokens.data.length > 0 ? (
            <TokenList
              tokens={tokens.data}
              onRevoke={(tok) =>
                act(
                  () => revokeUserToken(api, user.id, tok.id),
                  "tokens",
                  intl.formatMessage(t.tokenRevoked, { name: tok.name }),
                )
              }
            />
          ) : null}
        </Section>
      ) : null}
      <Section
        title={intl.formatMessage(t.sshKeysTitle)}
        description={intl.formatMessage(self ? t.userKeysYoursDescription : t.userKeysDescription)}
      >
        {keys.isPending ? <ListSkeleton rows={2} /> : null}
        {keys.isError ? (
          <ErrorState title={intl.formatMessage(t.sshKeysFailed)} error={keys.error} />
        ) : null}
        {keys.data?.length === 0 ? (
          <p className="text-muted-foreground">{intl.formatMessage(t.sshKeysNone)}</p>
        ) : null}
        {keys.data && keys.data.length > 0 ? (
          <KeyList
            keys={keys.data}
            onRemove={(k) =>
              act(
                () => removeUserSSHKey(api, user.id, k.id, false),
                "ssh-keys",
                intl.formatMessage(t.sshKeyRemoved, { name: k.name }),
              )
            }
          />
        ) : null}
      </Section>
    </div>
  );
}

/** Helping someone back in: a sign-in link, signing them out, their second steps and tokens, their lockout. */
function HelpBackIn({ user, onLink }: { user: UserView; onLink: (link: IssuedLink) => void }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { guard } = useStepUp();
  const [open, setOpen] = useState(false);
  const locked = userState(user) === "locked";
  const [input, setInput] = useState<ResetInput>({ link: true, endSessions: true, unlock: locked });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const toggle = (key: keyof ResetInput) => setInput({ ...input, [key]: !input[key] });
  const option = (key: keyof ResetInput, label: string, help: string) => (
    <div className="flex items-start gap-3">
      <Checkbox
        id={`reset-${key}`}
        checked={input[key] === true}
        onCheckedChange={() => toggle(key)}
        className="mt-0.5"
      />
      <div className="flex flex-col gap-0.5">
        <Label htmlFor={`reset-${key}`}>{label}</Label>
        <p className="text-muted-foreground text-xs">{help}</p>
      </div>
    </div>
  );
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await guard(() => resetUser(api, user.id, input));
      setOpen(false);
      setBusy(false);
      toast.success(
        intl.formatMessage(t.resetDone, {
          sessions: result.sessionsEnded,
          tokens: result.tokensRevoked,
        }),
      );
      await client.invalidateQueries({ queryKey: ["users", user.id] });
      if (result.link) {
        onLink(result.link);
      }
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        <LifeBuoy aria-hidden="true" />
        {intl.formatMessage(t.resetButton)}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <form onSubmit={submit} className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>
                {intl.formatMessage(t.resetTitleUser, { user: user.username })}
              </DialogTitle>
              <DialogDescription>{intl.formatMessage(t.resetHelp)}</DialogDescription>
            </DialogHeader>
            {option("link", intl.formatMessage(t.resetLink), intl.formatMessage(t.resetLinkHelp))}
            {option(
              "endSessions",
              intl.formatMessage(t.resetEndSessions),
              intl.formatMessage(t.resetEndSessionsHelp),
            )}
            {option(
              "removeTwoFactor",
              intl.formatMessage(t.resetTwoFactor),
              intl.formatMessage(t.resetTwoFactorHelp),
            )}
            {option(
              "revokeTokens",
              intl.formatMessage(t.resetTokens),
              intl.formatMessage(t.resetTokensHelp),
            )}
            {locked
              ? option(
                  "unlock",
                  intl.formatMessage(t.resetUnlock),
                  intl.formatMessage(t.resetUnlockHelp),
                )
              : null}
            {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
            <DialogFooter>
              <Button variant="outline" onClick={() => setOpen(false)} disabled={busy}>
                {intl.formatMessage(t.cancel)}
              </Button>
              <Button type="submit" busy={busy}>
                {intl.formatMessage(t.resetConfirm)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

function User({ id }: { id: string }) {
  const intl = useIntl();
  const api = useApi();
  const navigate = useNavigate();
  const can = usePermission();
  const { session } = useSession();
  const { guard } = useStepUp();
  const user = useQuery({ queryKey: ["users", id], queryFn: () => getUser(api, id) });
  const [link, setLink] = useState<IssuedLink | null>(null);
  const [removing, setRemoving] = useState(false);
  if (user.isPending) {
    return <ListSkeleton rows={5} />;
  }
  if (user.isError) {
    return (
      <ErrorState
        title={intl.formatMessage(t.userFailed)}
        error={user.error}
        onRetry={() => void user.refetch()}
      />
    );
  }
  const resource = user.data;
  const u = userView(resource);
  const self = session?.user.id === u.id;
  const canUpdate = can("core.user.update");
  const canReset = can("core.user.reset");
  const makeLink = async () => {
    try {
      setLink(await guard(() => issueSignInLink(api, u.id)));
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  const impersonate = async () => {
    try {
      await guard(() => impersonateUser(api, u.id));
      page.load("/");
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-3">
            {u.name || u.username}
            {self ? <Badge variant="accent">{intl.formatMessage(t.userYou)}</Badge> : null}
          </span>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <span className="font-mono">{u.username}</span>
            <UserStatus user={u} />
            <span>{intl.formatMessage(u.twoFactor ? t.userTwoFactorOn : t.userTwoFactorOff)}</span>
            <span>
              {intl.formatMessage(t.userLastSignIn)}: <TimeText value={u.lastSignInAt} />
            </span>
          </span>
        }
        actions={
          <>
            {canReset && !self ? <HelpBackIn user={u} onLink={setLink} /> : null}
            {canReset && !self ? (
              <Button variant="outline" onClick={() => void makeLink()}>
                <Link2 aria-hidden="true" />
                {intl.formatMessage(t.linkMake)}
              </Button>
            ) : null}
            {can("core.impersonate") && !self && !session?.impersonation ? (
              <ConfirmDialog
                trigger={
                  <Button variant="outline">
                    <Eye aria-hidden="true" />
                    {intl.formatMessage(t.impersonate)}
                  </Button>
                }
                title={intl.formatMessage(t.impersonateTitle, { user: u.username })}
                description={intl.formatMessage(t.impersonateDescription)}
                confirmLabel={intl.formatMessage(t.impersonate)}
                destructive={false}
                onConfirm={impersonate}
              />
            ) : null}
            {can("core.user.delete") && !self ? (
              <Button variant="outline" onClick={() => setRemoving(true)}>
                <Trash2 aria-hidden="true" />
                {intl.formatMessage(t.delete)}
              </Button>
            ) : null}
          </>
        }
      />
      <PreviewDialog
        open={removing}
        title={intl.formatMessage(t.userDeleteTitle, { user: u.username })}
        description={intl.formatMessage(t.userDeleteDescription)}
        preview={() => deleteUser(api, u.id, true)}
        apply={() => deleteUser(api, u.id, false)}
        onApplied={() => {
          setRemoving(false);
          toast.success(intl.formatMessage(t.userDeleted, { user: u.username }));
          void navigate({ to: "/users" });
        }}
        onClose={() => setRemoving(false)}
      />
      {self ? <Alert tone="info">{intl.formatMessage(t.userSelf)}</Alert> : null}
      <Tabs defaultValue="details">
        <TabsList aria-label={intl.formatMessage(t.userSections)}>
          <TabsTrigger value="details">{intl.formatMessage(t.userDetails)}</TabsTrigger>
          <TabsTrigger value="roles">{intl.formatMessage(t.profileRoles)}</TabsTrigger>
          <TabsTrigger value="access">{intl.formatMessage(t.userSignIns)}</TabsTrigger>
        </TabsList>
        <TabsContent value="details">
          <Details user={resource} editable={canUpdate && !self} />
        </TabsContent>
        <TabsContent value="roles">
          <Roles user={resource} editable={canUpdate && !self} />
        </TabsContent>
        <TabsContent value="access">
          <SignIns user={u} canReset={canReset} self={self} />
        </TabsContent>
      </Tabs>
      <SignInLink link={link} user={u.username} onClose={() => setLink(null)} />
    </div>
  );
}

/** One user: their details, roles and how they sign in, and helping them back in. */
export default function UserPage() {
  const params = useParams({ strict: false }) as { userId?: string };
  return (
    <IdentityMessages fallback={<ListSkeleton rows={5} />}>
      <User id={params.userId ?? ""} />
    </IdentityMessages>
  );
}
