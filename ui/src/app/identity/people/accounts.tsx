import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Building2, Plus } from "lucide-react";
import { type FormEvent, useState } from "react";
import { useIntl } from "react-intl";
import { DataTable } from "@/design/components/data-table/data-table";
import type { Column } from "@/design/components/data-table/model";
import { Button } from "@/design/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/design/components/ui/dialog";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import { EmptyState, ErrorState, type Status, StatusDot } from "@/design/components/ui/states";
import type { ApiClient, Schemas } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { usePermission } from "../../modules";
import {
  type Account,
  listAccounts,
  type OpenAccountInput,
  type OpenedAccount,
  openAccount,
} from "../api";
import { useAskedToAdd } from "../asked";
import { ListSkeleton, PageHeader, TimeText } from "../components";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { PreviewDialog } from "../preview";
import { useSession } from "../session";
import { parseSize, sizeText } from "../util";
import { SignInLink } from "./users";

type Limits = Schemas["AccountLimits"];

/** Every account the person can see, page after page. */
export async function listAllAccounts(api: ApiClient): Promise<Account[]> {
  const out: Account[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < 50; page++) {
    const { items, next } = await listAccounts(api, { limit: 200, ...(cursor ? { cursor } : {}) });
    out.push(...items);
    if (!next) {
      break;
    }
    cursor = next;
  }
  return out;
}

/** An account's phase as a status dot. */
export function AccountPhase({ account }: { account: Account }) {
  const intl = useIntl();
  const phase = (account.status as { phase?: string } | undefined)?.phase ?? "pending";
  const status: Record<string, Status> = {
    ready: "ok",
    failed: "error",
    degraded: "warning",
    pending: "pending",
    deleting: "pending",
  };
  return (
    <StatusDot
      status={status[phase] ?? "unknown"}
      label={intl.formatMessage(t.accountPhase, { phase })}
    />
  );
}

/** The limits of an account as the form edits them: sizes and numbers as typed. */
export type LimitTexts = Record<keyof Limits, string>;

export function limitTexts(limits: Limits | undefined): LimitTexts {
  return {
    memoryMax: sizeText(limits?.memoryMax),
    memoryHigh: sizeText(limits?.memoryHigh),
    cpu: limits?.cpu ? String(limits.cpu) : "",
    cpuWeight: limits?.cpuWeight ? String(limits.cpuWeight) : "",
    ioWeight: limits?.ioWeight ? String(limits.ioWeight) : "",
    tasks: limits?.tasks ? String(limits.tasks) : "",
  };
}

/** The limits the texts say, and which of them don't read as a size or a number. */
export function limitsOf(texts: LimitTexts): { limits: Limits; bad: (keyof Limits)[] } {
  const limits: Limits = {};
  const bad: (keyof Limits)[] = [];
  for (const key of ["memoryMax", "memoryHigh"] as const) {
    if (texts[key].trim() === "") {
      continue;
    }
    const n = parseSize(texts[key]);
    if (n === null) {
      bad.push(key);
    } else {
      limits[key] = n;
    }
  }
  for (const key of ["cpu", "cpuWeight", "ioWeight", "tasks"] as const) {
    if (texts[key].trim() === "") {
      continue;
    }
    const n = Number(texts[key]);
    if (!Number.isInteger(n) || n < 0) {
      bad.push(key);
    } else {
      limits[key] = n;
    }
  }
  return { limits, bad };
}

/** The fields of an account's limits. */
export function LimitsFields({
  value,
  onChange,
  bad,
}: {
  value: LimitTexts;
  onChange: (value: LimitTexts) => void;
  bad: readonly (keyof Limits)[];
}) {
  const intl = useIntl();
  const field = (key: keyof Limits, label: string, help: string, size: boolean) => (
    <Field
      label={label}
      description={help}
      error={
        bad.includes(key) ? intl.formatMessage(size ? t.limitBadSize : t.limitBadNumber) : undefined
      }
    >
      <Input
        value={value[key]}
        onChange={(e) => onChange({ ...value, [key]: e.target.value })}
        inputMode={size ? "text" : "numeric"}
        placeholder={intl.formatMessage(t.limitNone)}
        spellCheck={false}
      />
    </Field>
  );
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      {field(
        "memoryMax",
        intl.formatMessage(t.limitMemoryMax),
        intl.formatMessage(t.limitMemoryMaxHelp),
        true,
      )}
      {field(
        "memoryHigh",
        intl.formatMessage(t.limitMemoryHigh),
        intl.formatMessage(t.limitMemoryHighHelp),
        true,
      )}
      {field("cpu", intl.formatMessage(t.limitCpu), intl.formatMessage(t.limitCpuHelp), false)}
      {field(
        "tasks",
        intl.formatMessage(t.limitTasks),
        intl.formatMessage(t.limitTasksHelp),
        false,
      )}
      {field(
        "cpuWeight",
        intl.formatMessage(t.limitCpuWeight),
        intl.formatMessage(t.limitWeightHelp),
        false,
      )}
      {field(
        "ioWeight",
        intl.formatMessage(t.limitIoWeight),
        intl.formatMessage(t.limitWeightHelp),
        false,
      )}
    </div>
  );
}

/** Makes a short name from what an account is called: Acme Inc. becomes acmeinc. */
export function slugFrom(name: string): string {
  const letters = name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]/g, "")
    .replace(/^[0-9]+/, "");
  return letters.slice(0, 16);
}

function OpenAccount({
  onOpened,
  onCancel,
}: {
  onOpened: (opened: OpenedAccount) => void;
  onCancel: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const { session } = useSession();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [reseller, setReseller] = useState("");
  const [note, setNote] = useState("");
  const [limits, setLimits] = useState<LimitTexts>(limitTexts(undefined));
  const [owner, setOwner] = useState({ username: "", name: "", email: "" });
  const [bad, setBad] = useState<(keyof Limits)[]>([]);
  const [previewing, setPreviewing] = useState(false);
  const shownSlug = slugTouched ? slug : slugFrom(name);
  const input = (): OpenAccountInput => {
    const { limits: parsed } = limitsOf(limits);
    return {
      name: name.trim(),
      slug: shownSlug,
      ...(reseller.trim() ? { reseller: reseller.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
      ...(Object.keys(parsed).length > 0 ? { limits: parsed } : {}),
      ...(owner.username.trim()
        ? {
            owner: {
              username: owner.username.trim(),
              ...(owner.name.trim() ? { name: owner.name.trim() } : {}),
              ...(owner.email.trim() ? { email: owner.email.trim() } : {}),
            },
          }
        : {}),
    };
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { bad: wrong } = limitsOf(limits);
    setBad(wrong);
    if (wrong.length === 0) {
      setPreviewing(true);
    }
  };
  return (
    <form onSubmit={submit} className="flex flex-col gap-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label={intl.formatMessage(t.fieldAccountName)}
          description={intl.formatMessage(t.fieldAccountNameHelp)}
        >
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
            required
            autoFocus
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldSlug)}
          description={intl.formatMessage(t.fieldSlugHelp)}
        >
          <Input
            value={shownSlug}
            onChange={(e) => {
              setSlugTouched(true);
              setSlug(e.target.value.toLowerCase());
            }}
            pattern="[a-z][a-z0-9]{2,15}"
            minLength={3}
            maxLength={16}
            required
            spellCheck={false}
            autoCapitalize="none"
            className="font-mono"
          />
        </Field>
        {session?.user.home?.level !== "reseller" ? (
          <Field
            label={intl.formatMessage(t.fieldReseller)}
            description={intl.formatMessage(t.fieldResellerHelp)}
          >
            <Input
              value={reseller}
              onChange={(e) => setReseller(e.target.value)}
              spellCheck={false}
            />
          </Field>
        ) : null}
      </div>
      <fieldset className="flex flex-col gap-3">
        <legend className="mb-1 font-medium text-sm">{intl.formatMessage(t.accountOwner)}</legend>
        <p className="text-muted-foreground text-xs">{intl.formatMessage(t.accountOwnerHelp)}</p>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={intl.formatMessage(t.fieldUsername)}>
            <Input
              value={owner.username}
              onChange={(e) => setOwner({ ...owner, username: e.target.value })}
              autoCapitalize="none"
              spellCheck={false}
            />
          </Field>
          <Field label={intl.formatMessage(t.fieldName)}>
            <Input
              value={owner.name}
              onChange={(e) => setOwner({ ...owner, name: e.target.value })}
            />
          </Field>
          <Field label={intl.formatMessage(t.fieldEmail)}>
            <Input
              type="email"
              value={owner.email}
              onChange={(e) => setOwner({ ...owner, email: e.target.value })}
            />
          </Field>
        </div>
      </fieldset>
      <Collapsible>
        <CollapsibleTrigger className="font-medium text-sm">
          {intl.formatMessage(t.accountLimits)}
        </CollapsibleTrigger>
        <CollapsibleContent className="flex flex-col gap-4 pt-3">
          <p className="text-muted-foreground text-xs">{intl.formatMessage(t.accountLimitsHelp)}</p>
          <LimitsFields value={limits} onChange={setLimits} bad={bad} />
          <Field label={intl.formatMessage(t.fieldNote)}>
            <Textarea value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} />
          </Field>
        </CollapsibleContent>
      </Collapsible>
      <DialogFooter>
        <Button variant="outline" onClick={onCancel}>
          {intl.formatMessage(t.cancel)}
        </Button>
        <Button type="submit">{intl.formatMessage(t.previewButton)}</Button>
      </DialogFooter>
      {previewing ? (
        <PreviewDialog
          open
          title={intl.formatMessage(t.accountOpenTitle)}
          preview={async () => (await openAccount(api, input(), true)).changeSet}
          apply={() => openAccount(api, input(), false)}
          onApplied={(opened) => {
            setPreviewing(false);
            onOpened(opened);
          }}
          onClose={() => setPreviewing(false)}
        />
      ) : null}
    </form>
  );
}

function Accounts() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const navigate = useNavigate();
  const can = usePermission();
  const accounts = useQuery({ queryKey: ["accounts", "all"], queryFn: () => listAllAccounts(api) });
  const [opening, setOpening] = useState(false);
  useAskedToAdd(() => setOpening(can("core.account.create")));
  const [opened, setOpened] = useState<OpenedAccount | null>(null);
  const columns: Column<Account>[] = [
    {
      id: "name",
      header: intl.formatMessage(t.fieldAccountName),
      value: (a) => a.spec.name,
      cell: (a) => (
        <Link
          to={`/accounts/${a.id}`}
          className="font-medium text-primary underline-offset-4 hover:underline"
        >
          {a.spec.name}
        </Link>
      ),
      sortable: true,
      filter: "text",
    },
    {
      id: "slug",
      header: intl.formatMessage(t.fieldSlug),
      value: (a) => a.spec.slug,
      cell: (a) => <span className="font-mono">{a.spec.slug}</span>,
      sortable: true,
    },
    {
      id: "reseller",
      header: intl.formatMessage(t.fieldReseller),
      value: (a) => a.spec.reseller ?? "",
      sortable: true,
      hideable: true,
    },
    {
      id: "phase",
      header: intl.formatMessage(t.accountState),
      value: (a) => (a.status as { phase?: string } | undefined)?.phase ?? "",
      cell: (a) => <AccountPhase account={a} />,
      sortable: true,
    },
    {
      id: "created",
      header: intl.formatMessage(t.created),
      value: (a) => new Date(a.createdAt),
      cell: (a) => <TimeText value={a.createdAt} />,
      sortable: true,
      hideable: true,
    },
  ];
  const open = can("core.account.create") ? (
    <Button onClick={() => setOpening(true)}>
      <Plus aria-hidden="true" />
      {intl.formatMessage(t.accountOpen)}
    </Button>
  ) : null;
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.accountsTitle)}
        description={intl.formatMessage(t.accountsDescription)}
        actions={open}
      />
      {accounts.isError ? (
        <ErrorState
          title={intl.formatMessage(t.accountsFailed)}
          error={accounts.error}
          onRetry={() => void accounts.refetch()}
        />
      ) : null}
      {!accounts.isError ? (
        <DataTable
          id="accounts"
          label={intl.formatMessage(t.accountsTitle)}
          rows={accounts.data ?? []}
          rowKey={(a) => a.id}
          columns={columns}
          loading={accounts.isPending}
          empty={
            <EmptyState
              icon={Building2}
              title={intl.formatMessage(t.accountsNone)}
              description={intl.formatMessage(t.accountsNoneHelp)}
              action={open}
            />
          }
        />
      ) : null}
      <Dialog open={opening} onOpenChange={setOpening}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{intl.formatMessage(t.accountOpenTitle)}</DialogTitle>
            <DialogDescription>{intl.formatMessage(t.accountOpenDescription)}</DialogDescription>
          </DialogHeader>
          {opening ? (
            <OpenAccount
              onOpened={(result) => {
                setOpening(false);
                void client.invalidateQueries({ queryKey: ["accounts"] });
                if (result.link) {
                  setOpened(result);
                } else {
                  const id = result.changeSet.changes?.[0]?.id;
                  if (id) {
                    void navigate({ to: `/accounts/${id}` });
                  }
                }
              }}
              onCancel={() => setOpening(false)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
      <SignInLink
        link={opened?.link ?? null}
        user={opened?.owner?.username ?? ""}
        onClose={() => {
          const id = opened?.changeSet.changes?.[0]?.id;
          setOpened(null);
          if (id) {
            void navigate({ to: `/accounts/${id}` });
          }
        }}
      />
    </div>
  );
}

/** The accounts: the server's customers and tenants, and opening one. */
export default function AccountsPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={5} />}>
      <Accounts />
    </IdentityMessages>
  );
}
