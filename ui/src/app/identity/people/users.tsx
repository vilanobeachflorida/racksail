import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Plus, Users } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import { type IntlShape, useIntl } from "react-intl";
import { DataTable } from "@/design/components/data-table/data-table";
import type { Column } from "@/design/components/data-table/model";
import { Button } from "@/design/components/ui/button";
import { Checkbox, RadioGroup, RadioGroupItem } from "@/design/components/ui/choice";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import { Field, Label } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { EmptyState, ErrorState, type Status, StatusDot } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import type { ApiClient } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { usePermission } from "../../modules";
import {
  type ChangeSet,
  createUser,
  type Grant,
  type IssuedLink,
  issueSignInLink,
  listAccounts,
  listUsers,
  type Scope,
  setUserPassword,
  type UserSpec,
  type UserView,
  userView,
} from "../api";
import {
  CopyField,
  ListSkeleton,
  PageHeader,
  PasswordInput,
  ShownOnce,
  TimeText,
} from "../components";
import { GrantEditor, grantText, useAccountNames } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { PreviewDialog } from "../preview";
import { useSession } from "../session";
import { useStepUp } from "../step-up";
import { failureMessage, ownerLabel } from "../util";

/** Every user the person can see, page after page; scope narrows them to those of one owner. */
export async function listAllUsers(
  api: ApiClient,
  query: { scope?: string } = {},
): Promise<UserView[]> {
  const out: UserView[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < 50; page++) {
    const { users, next } = await listUsers(api, {
      ...query,
      limit: 200,
      sort: "username",
      ...(cursor ? { cursor } : {}),
    });
    out.push(...users.map(userView));
    if (!next) {
      break;
    }
    cursor = next;
  }
  return out;
}

/** Whether a user is turned off, locked out for now, or fine. */
export function userState(
  user: UserView,
  now: number = Date.now(),
): "disabled" | "locked" | "active" {
  if (user.disabled) {
    return "disabled";
  }
  if (user.lockedUntil && new Date(user.lockedUntil).getTime() > now) {
    return "locked";
  }
  return "active";
}

export function UserStatus({ user }: { user: UserView }) {
  const intl = useIntl();
  const state = userState(user);
  const status: Record<typeof state, Status> = { disabled: "off", locked: "warning", active: "ok" };
  const label = { disabled: t.userDisabled, locked: t.userLocked, active: t.userActive }[state];
  return <StatusDot status={status[state]} label={intl.formatMessage(label)} />;
}

function linkURL(link: IssuedLink): string {
  return link.url ?? `${window.location.origin}${link.path}`;
}

/** A sign-in link someone gets to choose their password, shown once to pass on. */
export function SignInLink({
  link,
  user,
  onClose,
}: {
  link: IssuedLink | null;
  user: string;
  onClose: () => void;
}) {
  const intl = useIntl();
  return (
    <ShownOnce
      open={link !== null}
      title={intl.formatMessage(t.linkTitle, { user })}
      description={intl.formatMessage(t.linkDescription, {
        expires: link
          ? intl.formatDate(new Date(link.expiresAt), { dateStyle: "medium", timeStyle: "short" })
          : "",
      })}
      onClose={onClose}
    >
      <CopyField label={intl.formatMessage(t.linkLabel)} value={link ? linkURL(link) : ""} />
    </ShownOnce>
  );
}

type Access = "link" | "password";

function where(intl: IntlShape, scope: Scope, names: ReadonlyMap<string, string>) {
  return ownerLabel(intl, scope, names);
}

/** Who a new user is, and how they get in once they're added. */
interface Draft {
  scope: Scope;
  spec: UserSpec;
  access: Access;
  password: string;
  mustChange: boolean;
}

function NewUser({ onReady, onCancel }: { onReady: (draft: Draft) => void; onCancel: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const { session } = useSession();
  const names = useAccountNames();
  const accounts = useQuery({
    queryKey: ["accounts", "names-list"],
    queryFn: () => listAccounts(api, { limit: 500 }),
    retry: false,
  });
  const own = session?.user.home ?? { level: "server" as const };
  const homes: Scope[] = useMemo(() => {
    const list: Scope[] = [];
    if (own.level === "server") {
      list.push({ level: "server" });
    } else if (own.level === "reseller") {
      list.push({ level: "reseller", ...(own.id ? { id: own.id } : {}) });
    }
    for (const a of accounts.data?.items ?? []) {
      list.push({ level: "account", id: a.id });
    }
    if (list.length === 0) {
      list.push(own);
    }
    return list;
  }, [accounts.data, own]);
  const key = (s: Scope) => (s.id ? `${s.level}:${s.id}` : s.level);
  const [username, setUsername] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [home, setHome] = useState(key(own));
  const [grants, setGrants] = useState<Grant[]>([]);
  const [access, setAccess] = useState<Access>("link");
  const [password, setPassword] = useState("");
  const [change, setChange] = useState(true);
  const create = (event: FormEvent) => {
    event.preventDefault();
    onReady({
      scope: homes.find((h) => key(h) === home) ?? own,
      spec: {
        username: username.trim(),
        grants,
        ...(name.trim() ? { name: name.trim() } : {}),
        ...(email.trim() ? { email: email.trim() } : {}),
      },
      access,
      password,
      mustChange: change,
    });
  };
  return (
    <form onSubmit={create} className="flex flex-col gap-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label={intl.formatMessage(t.fieldUsername)}
          description={intl.formatMessage(t.fieldUsernameHelp)}
        >
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoCapitalize="none"
            spellCheck={false}
            required
            autoFocus
          />
        </Field>
        <Field label={intl.formatMessage(t.fieldName)}>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldEmail)}
          description={intl.formatMessage(t.fieldEmailHelp)}
        >
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            maxLength={254}
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldHome)}
          description={intl.formatMessage(t.fieldHomeHelp)}
        >
          <Select value={home} onValueChange={setHome}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {homes.map((h) => (
                <SelectItem key={key(h)} value={key(h)}>
                  {where(intl, h, names)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 font-medium text-sm">{intl.formatMessage(t.grantsTitle)}</legend>
        <p className="text-muted-foreground text-xs">{intl.formatMessage(t.grantsHelp)}</p>
        <GrantEditor value={grants} onChange={setGrants} />
      </fieldset>
      <fieldset className="flex flex-col gap-3">
        <legend className="mb-1 font-medium text-sm">{intl.formatMessage(t.userAccess)}</legend>
        <RadioGroup value={access} onValueChange={(v) => setAccess(v as Access)} className="gap-3">
          <div className="flex items-start gap-3">
            <RadioGroupItem id="access-link" value="link" className="mt-0.5" />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="access-link">{intl.formatMessage(t.userAccessLink)}</Label>
              <p className="text-muted-foreground text-xs">
                {intl.formatMessage(t.userAccessLinkHelp)}
              </p>
            </div>
          </div>
          <div className="flex items-start gap-3">
            <RadioGroupItem id="access-password" value="password" className="mt-0.5" />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="access-password">{intl.formatMessage(t.userAccessPassword)}</Label>
              <p className="text-muted-foreground text-xs">
                {intl.formatMessage(t.userAccessPasswordHelp)}
              </p>
            </div>
          </div>
        </RadioGroup>
        {access === "password" ? (
          <>
            <Field label={intl.formatMessage(t.fieldPassword)}>
              <PasswordInput
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                minLength={10}
                required
              />
            </Field>
            <div className="flex items-center gap-2">
              <Checkbox
                id="must-change"
                checked={change}
                onCheckedChange={(v) => setChange(v === true)}
              />
              <Label htmlFor="must-change" className="font-normal">
                {intl.formatMessage(t.userMustChange)}
              </Label>
            </div>
          </>
        ) : null}
      </fieldset>
      <DialogFooter>
        <Button variant="outline" onClick={onCancel}>
          {intl.formatMessage(t.cancel)}
        </Button>
        <Button type="submit">{intl.formatMessage(t.userAddPreview)}</Button>
      </DialogFooter>
    </form>
  );
}

/**
 * Adds the user a draft describes, after a preview of the change set: the
 * user is a resource like any other, so hooks and rules see it, and the
 * change can be undone. Their way in, a sign-in link or a password, is set
 * once they're there, since it's never part of a change set.
 */
function AddUser({
  draft,
  onAdded,
  onClose,
}: {
  draft: Draft | null;
  onAdded: (id: string, link: IssuedLink | null) => void;
  onClose: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  // The preview picks the user's ID, and applying it adds that same user.
  const [previewed, setPreviewed] = useState<string | undefined>();
  const preview = async (): Promise<ChangeSet> => {
    if (!draft) {
      throw new Error("nothing to add");
    }
    const cs = await createUser(api, { scope: draft.scope, spec: draft.spec }, true);
    setPreviewed(cs.changes?.[0]?.id);
    return cs;
  };
  const apply = async () => {
    if (!draft) {
      throw new Error("nothing to add");
    }
    const cs = await createUser(
      api,
      { scope: draft.scope, spec: draft.spec, ...(previewed ? { id: previewed } : {}) },
      false,
    );
    return cs.changes?.[0]?.id ?? previewed ?? "";
  };
  const welcome = async (id: string) => {
    try {
      if (draft?.access === "password") {
        await guard(() =>
          setUserPassword(api, id, { password: draft.password, mustChange: draft.mustChange }),
        );
        onAdded(id, null);
        return;
      }
      onAdded(id, await guard(() => issueSignInLink(api, id)));
    } catch (e) {
      // The user is there; only their way in is missing, which their page
      // offers again.
      toast.error(failureMessage(intl, e));
      onAdded(id, null);
    }
  };
  return (
    <PreviewDialog
      open={draft !== null}
      title={intl.formatMessage(t.userAddTitle)}
      preview={preview}
      apply={apply}
      onApplied={(id) => void welcome(id)}
      onClose={onClose}
    />
  );
}

function UsersList() {
  const intl = useIntl();
  const api = useApi();
  const navigate = useNavigate();
  const can = usePermission();
  const names = useAccountNames();
  const users = useQuery({ queryKey: ["users"], queryFn: () => listAllUsers(api) });
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [created, setCreated] = useState<{ id: string; user: string; link: IssuedLink } | null>(
    null,
  );
  const columns: Column<UserView>[] = [
    {
      id: "username",
      header: intl.formatMessage(t.fieldUsername),
      value: (u) => u.username,
      cell: (u) => (
        <Link
          to={`/users/${u.id}`}
          className="font-medium text-primary underline-offset-4 hover:underline"
        >
          {u.username}
        </Link>
      ),
      sortable: true,
      filter: "text",
    },
    {
      id: "name",
      header: intl.formatMessage(t.fieldName),
      value: (u) => u.name,
      sortable: true,
      hideable: true,
    },
    {
      id: "email",
      header: intl.formatMessage(t.fieldEmail),
      value: (u) => u.email ?? "",
      sortable: true,
      hideable: true,
    },
    {
      id: "home",
      header: intl.formatMessage(t.fieldHome),
      value: (u) => ownerLabel(intl, u.home, names),
      sortable: true,
      hideable: true,
    },
    {
      id: "roles",
      header: intl.formatMessage(t.profileRoles),
      value: (u) => (u.grants ?? []).map((g) => grantText(intl, g, names)).join("; "),
      hideable: true,
      width: "minmax(12rem, 2fr)",
    },
    {
      id: "twoFactor",
      header: intl.formatMessage(t.userTwoFactor),
      value: (u) => (u.twoFactor ? intl.formatMessage(t.stateOn) : intl.formatMessage(t.stateOff)),
      filter: {
        options: [
          { value: intl.formatMessage(t.stateOn), label: intl.formatMessage(t.stateOn) },
          { value: intl.formatMessage(t.stateOff), label: intl.formatMessage(t.stateOff) },
        ],
      },
      hideable: true,
    },
    {
      id: "lastSignIn",
      header: intl.formatMessage(t.userLastSignIn),
      value: (u) => (u.lastSignInAt ? new Date(u.lastSignInAt) : null),
      cell: (u) => <TimeText value={u.lastSignInAt} />,
      sortable: true,
      hideable: true,
    },
    {
      id: "state",
      header: intl.formatMessage(t.userState),
      value: (u) => userState(u),
      cell: (u) => <UserStatus user={u} />,
      filter: {
        options: [
          { value: "active", label: intl.formatMessage(t.userActive) },
          { value: "locked", label: intl.formatMessage(t.userLocked) },
          { value: "disabled", label: intl.formatMessage(t.userDisabled) },
        ],
      },
    },
  ];
  const add = can("core.user.create") ? (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden="true" />
      {intl.formatMessage(t.userAdd)}
    </Button>
  ) : null;
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.usersTitle)}
        description={intl.formatMessage(t.usersDescription)}
        actions={add}
      />
      {users.isError ? (
        <ErrorState
          title={intl.formatMessage(t.usersFailed)}
          error={users.error}
          onRetry={() => void users.refetch()}
        />
      ) : null}
      {!users.isError ? (
        <DataTable
          id="users"
          label={intl.formatMessage(t.usersTitle)}
          rows={users.data ?? []}
          rowKey={(u) => u.id}
          columns={columns}
          loading={users.isPending}
          empty={
            <EmptyState
              icon={Users}
              title={intl.formatMessage(t.usersNone)}
              description={intl.formatMessage(t.usersNoneHelp)}
              action={add}
            />
          }
        />
      ) : null}
      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{intl.formatMessage(t.userAddTitle)}</DialogTitle>
            <DialogDescription>{intl.formatMessage(t.userAddDescription)}</DialogDescription>
          </DialogHeader>
          {adding ? (
            <NewUser
              onReady={(d) => {
                setAdding(false);
                setDraft(d);
              }}
              onCancel={() => setAdding(false)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
      <AddUser
        draft={draft}
        onAdded={(id, link) => {
          const username = draft?.spec.username ?? "";
          setDraft(null);
          void users.refetch();
          toast.success(intl.formatMessage(t.userAdded, { user: username }));
          if (link) {
            setCreated({ id, user: username, link });
          } else {
            void navigate({ to: `/users/${id}` });
          }
        }}
        onClose={() => setDraft(null)}
      />
      <SignInLink
        link={created?.link ?? null}
        user={created?.user ?? ""}
        onClose={() => {
          const id = created?.id;
          setCreated(null);
          if (id) {
            void navigate({ to: `/users/${id}` });
          }
        }}
      />
    </div>
  );
}

/** The panel's users: who they are, their roles and whether they can sign in. */
export default function UsersPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={5} />}>
      <UsersList />
    </IdentityMessages>
  );
}
