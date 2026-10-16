import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Trash2, UserRound } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Field } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import { ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import { usePermission } from "../../modules";
import { type Account, deleteAccount, getAccount, updateAccount } from "../api";
import { Item, ItemList, ListSkeleton, PageHeader, Section, TimeText } from "../components";
import { grantText, useAccountNames } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { PreviewDialog } from "../preview";
import { AccountPhase, LimitsFields, type LimitTexts, limitsOf, limitTexts } from "./accounts";
import { listAllUsers } from "./users";

interface AccountStatus {
  phase?: string;
  uid?: number;
  user?: string;
  home?: string;
  slice?: string;
  conditions?: { type: string; status: string; reason?: string; message?: string }[] | null;
}

function Server({ account }: { account: Account }) {
  const intl = useIntl();
  const status = (account.status ?? {}) as AccountStatus;
  const failing = (status.conditions ?? []).filter((c) => c.status !== "true" && c.message);
  return (
    <Section
      title={intl.formatMessage(t.accountServer)}
      description={intl.formatMessage(t.accountServerDescription)}
    >
      <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-[max-content_1fr]">
        <dt className="text-muted-foreground">{intl.formatMessage(t.accountState)}</dt>
        <dd>
          <AccountPhase account={account} />
        </dd>
        <dt className="text-muted-foreground">{intl.formatMessage(t.accountLinuxUser)}</dt>
        <dd className="font-mono">{status.user ? `${status.user} (${status.uid ?? "?"})` : "—"}</dd>
        <dt className="text-muted-foreground">{intl.formatMessage(t.accountHome)}</dt>
        <dd className="break-all font-mono">{status.home ?? "—"}</dd>
        <dt className="text-muted-foreground">{intl.formatMessage(t.accountSlice)}</dt>
        <dd className="break-all font-mono">{status.slice ?? "—"}</dd>
        <dt className="text-muted-foreground">{intl.formatMessage(t.created)}</dt>
        <dd>
          <TimeText value={account.createdAt} />
        </dd>
      </dl>
      {failing.map((c) => (
        <p key={c.type} className="text-danger text-sm">
          {c.message}
        </p>
      ))}
    </Section>
  );
}

function Settings({ account, editable }: { account: Account; editable: boolean }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const [name, setName] = useState(account.spec.name);
  const [note, setNote] = useState(account.spec.note ?? "");
  const [limits, setLimits] = useState<LimitTexts>(limitTexts(account.spec.limits));
  const [bad, setBad] = useState<(keyof LimitTexts)[]>([]);
  const [previewing, setPreviewing] = useState(false);
  useEffect(() => {
    setName(account.spec.name);
    setNote(account.spec.note ?? "");
    setLimits(limitTexts(account.spec.limits));
  }, [account]);
  const body = (dryRun: boolean) =>
    updateAccount(
      api,
      account.id,
      {
        revision: account.revision,
        ...(account.labels ? { labels: account.labels } : {}),
        spec: {
          ...account.spec,
          name: name.trim(),
          note: note.trim(),
          limits: limitsOf(limits).limits,
        },
      },
      dryRun,
    );
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { bad: wrong } = limitsOf(limits);
    setBad(wrong);
    if (wrong.length === 0) {
      setPreviewing(true);
    }
  };
  return (
    <Section
      title={intl.formatMessage(t.accountSettings)}
      description={intl.formatMessage(t.accountLimitsHelp)}
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field label={intl.formatMessage(t.fieldAccountName)}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
            required
            disabled={!editable}
            className="max-w-sm"
          />
        </Field>
        <fieldset disabled={!editable} className="flex flex-col gap-4">
          <LimitsFields value={limits} onChange={setLimits} bad={bad} />
        </fieldset>
        <Field label={intl.formatMessage(t.fieldNote)}>
          <Textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            maxLength={1000}
            disabled={!editable}
          />
        </Field>
        {editable ? (
          <Button type="submit" className="self-start">
            {intl.formatMessage(t.previewButton)}
          </Button>
        ) : null}
      </form>
      {previewing ? (
        <PreviewDialog
          open
          title={intl.formatMessage(t.accountChangeTitle, { name: account.spec.name })}
          preview={() => body(true)}
          apply={() => body(false)}
          onApplied={() => {
            setPreviewing(false);
            toast.success(intl.formatMessage(t.accountChanged, { name: account.spec.name }));
            void client.invalidateQueries({ queryKey: ["accounts"] });
          }}
          onClose={() => setPreviewing(false)}
        />
      ) : null}
    </Section>
  );
}

function People({ account }: { account: Account }) {
  const intl = useIntl();
  const api = useApi();
  const names = useAccountNames();
  const people = useQuery({
    queryKey: ["users", "home", account.id],
    queryFn: () => listAllUsers(api, { home: `account:${account.id}` }),
  });
  return (
    <Section
      title={intl.formatMessage(t.accountPeople)}
      description={intl.formatMessage(t.accountPeopleDescription)}
    >
      {people.isPending ? <ListSkeleton rows={2} /> : null}
      {people.isError ? (
        <ErrorState title={intl.formatMessage(t.usersFailed)} error={people.error} />
      ) : null}
      {people.data?.length === 0 ? (
        <p className="text-muted-foreground">{intl.formatMessage(t.accountPeopleNone)}</p>
      ) : null}
      {people.data && people.data.length > 0 ? (
        <ItemList label={intl.formatMessage(t.accountPeople)}>
          {people.data.map((u) => (
            <Item
              key={u.id}
              icon={<UserRound aria-hidden="true" />}
              title={
                <Link
                  to={`/users/${u.id}`}
                  className="text-primary underline-offset-4 hover:underline"
                >
                  {u.name || u.username}
                </Link>
              }
              meta={(u.grants ?? []).map((g) => grantText(intl, g, names)).join("; ")}
            />
          ))}
        </ItemList>
      ) : null}
    </Section>
  );
}

function AccountView({ id }: { id: string }) {
  const intl = useIntl();
  const api = useApi();
  const navigate = useNavigate();
  const client = useQueryClient();
  const can = usePermission();
  const account = useQuery({ queryKey: ["accounts", id], queryFn: () => getAccount(api, id) });
  const [deleting, setDeleting] = useState(false);
  if (account.isPending) {
    return <ListSkeleton rows={5} />;
  }
  if (account.isError) {
    return (
      <ErrorState
        title={intl.formatMessage(t.accountFailed)}
        error={account.error}
        onRetry={() => void account.refetch()}
      />
    );
  }
  const a = account.data;
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={a.spec.name}
        description={<span className="font-mono">{a.spec.slug}</span>}
        actions={
          can("core.account.delete") ? (
            <Button variant="outline" onClick={() => setDeleting(true)}>
              <Trash2 aria-hidden="true" />
              {intl.formatMessage(t.accountDelete)}
            </Button>
          ) : null
        }
      />
      <Server account={a} />
      <Settings account={a} editable={can("core.account.update")} />
      <People account={a} />
      {deleting ? (
        <PreviewDialog
          open
          title={intl.formatMessage(t.accountDeleteTitle, { name: a.spec.name })}
          description={intl.formatMessage(t.accountDeleteDescription)}
          preview={() => deleteAccount(api, a.id, a.revision, true)}
          apply={() => deleteAccount(api, a.id, a.revision, false)}
          onApplied={() => {
            setDeleting(false);
            toast.success(intl.formatMessage(t.accountDeleted, { name: a.spec.name }));
            void client.invalidateQueries({ queryKey: ["accounts"] });
            void navigate({ to: "/accounts" });
          }}
          onClose={() => setDeleting(false)}
        />
      ) : null}
    </div>
  );
}

/** One account: what it is on the server, its limits, and its people. */
export default function AccountPage() {
  const params = useParams({ strict: false }) as { accountId?: string };
  return (
    <IdentityMessages fallback={<ListSkeleton rows={5} />}>
      <AccountView id={params.accountId ?? ""} />
    </IdentityMessages>
  );
}
