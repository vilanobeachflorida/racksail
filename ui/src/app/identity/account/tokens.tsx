import { useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus } from "lucide-react";
import { type FormEvent, useState } from "react";
import { FormattedMessage, type IntlShape, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Checkbox, RadioGroup, RadioGroupItem } from "@/design/components/ui/choice";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/design/components/ui/select";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import {
  type CreatedToken,
  createMyToken,
  listAccounts,
  listMyTokens,
  revokeMyToken,
  type TokenInput,
  type TokenView,
} from "../api";
import {
  CopyField,
  Item,
  ItemList,
  ListSkeleton,
  PageHeader,
  ShownOnce,
  TimeText,
} from "../components";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { useStepUp } from "../step-up";
import { failureMessage } from "../util";

/** What a token may do, in a few words. */
export function tokenReach(
  intl: IntlShape,
  token: Pick<TokenView, "permissions" | "accounts">,
): string {
  const permissions = token.permissions ?? [];
  const accounts = token.accounts ?? [];
  const what =
    permissions.length === 0
      ? intl.formatMessage(t.tokenEverything)
      : permissions.length === 1 && permissions[0] === "*.read"
        ? intl.formatMessage(t.tokenReadOnly)
        : permissions.join(", ");
  return accounts.length === 0
    ? what
    : intl.formatMessage(t.tokenInAccounts, { what, count: accounts.length });
}

function expired(token: TokenView): boolean {
  return token.expiresAt !== undefined && new Date(token.expiresAt).getTime() < Date.now();
}

/** The API tokens of a person, with a way to revoke each. */
export function TokenList({
  tokens,
  onRevoke,
}: {
  tokens: readonly TokenView[];
  onRevoke: (token: TokenView) => Promise<void>;
}) {
  const intl = useIntl();
  return (
    <ItemList label={intl.formatMessage(t.tokensTitle)}>
      {tokens.map((tok) => (
        <Item
          key={tok.id}
          icon={<KeyRound aria-hidden="true" />}
          title={tok.name}
          badge={
            expired(tok) ? (
              <Badge variant="danger">{intl.formatMessage(t.tokenExpired)}</Badge>
            ) : null
          }
          meta={
            <FormattedMessage
              {...t.tokenMeta}
              values={{
                hint: <span className="font-mono">{tok.hint}…</span>,
                reach: tokenReach(intl, tok),
                used: <TimeText value={tok.lastUsedAt} />,
                expires: (
                  <TimeText value={tok.expiresAt} empty={intl.formatMessage(t.tokenNoEnd)} />
                ),
              }}
            />
          }
          actions={
            <ConfirmDialog
              trigger={
                <Button variant="ghost" size="sm">
                  {intl.formatMessage(t.tokenRevoke)}
                </Button>
              }
              title={intl.formatMessage(t.tokenRevokeTitle, { name: tok.name })}
              description={intl.formatMessage(t.tokenRevokeDescription)}
              confirmLabel={intl.formatMessage(t.tokenRevoke)}
              onConfirm={() => onRevoke(tok)}
            />
          }
        />
      ))}
    </ItemList>
  );
}

type Reach = "everything" | "read" | "some";
type Expiry = "7" | "30" | "90" | "365" | "never" | "date";

function lines(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function NewToken({
  onCreated,
  onCancel,
}: {
  onCreated: (token: CreatedToken) => void;
  onCancel: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  const accounts = useQuery({
    queryKey: ["accounts", "names-list"],
    queryFn: () => listAccounts(api, { limit: 500 }),
    retry: false,
  });
  const [name, setName] = useState("");
  const [reach, setReach] = useState<Reach>("everything");
  const [permissions, setPermissions] = useState("");
  const [only, setOnly] = useState<string[]>([]);
  const [allowFrom, setAllowFrom] = useState("");
  const [expiry, setExpiry] = useState<Expiry>("90");
  const [date, setDate] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const create = async (event: FormEvent) => {
    event.preventDefault();
    const input: TokenInput = { name: name.trim() };
    if (reach === "read") {
      input.permissions = ["*.read"];
    } else if (reach === "some") {
      input.permissions = lines(permissions);
    }
    if (only.length > 0) {
      input.accounts = only;
    }
    if (lines(allowFrom).length > 0) {
      input.allowFrom = lines(allowFrom);
    }
    if (expiry === "date" && date) {
      input.expiresAt = new Date(`${date}T23:59:59`).toISOString();
    } else if (expiry !== "never" && expiry !== "date") {
      input.expiresAt = new Date(Date.now() + Number(expiry) * 86_400_000).toISOString();
    }
    setBusy(true);
    setError(null);
    try {
      onCreated(await guard(() => createMyToken(api, input)));
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  const radio = (value: Reach, label: string, help: string) => (
    <div className="flex items-start gap-3">
      <RadioGroupItem id={`reach-${value}`} value={value} className="mt-0.5" />
      <div className="flex flex-col gap-0.5">
        <Label htmlFor={`reach-${value}`}>{label}</Label>
        <p className="text-muted-foreground text-xs">{help}</p>
      </div>
    </div>
  );
  return (
    <form onSubmit={create} className="flex flex-col gap-5">
      <Field
        label={intl.formatMessage(t.fieldTokenName)}
        description={intl.formatMessage(t.fieldTokenNameHelp)}
      >
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={64}
          required
          autoFocus
        />
      </Field>
      <fieldset className="flex flex-col gap-3">
        <legend className="mb-2 font-medium text-sm">{intl.formatMessage(t.tokenReach)}</legend>
        <RadioGroup value={reach} onValueChange={(v) => setReach(v as Reach)} className="gap-3">
          {radio(
            "everything",
            intl.formatMessage(t.tokenEverything),
            intl.formatMessage(t.tokenEverythingHelp),
          )}
          {radio(
            "read",
            intl.formatMessage(t.tokenReadOnly),
            intl.formatMessage(t.tokenReadOnlyHelp),
          )}
          {radio("some", intl.formatMessage(t.tokenSome), intl.formatMessage(t.tokenSomeHelp))}
        </RadioGroup>
        {reach === "some" ? (
          <Field
            label={intl.formatMessage(t.fieldPermissions)}
            description={intl.formatMessage(t.fieldPermissionsHelp)}
          >
            <Input
              value={permissions}
              onChange={(e) => setPermissions(e.target.value)}
              spellCheck={false}
              required
            />
          </Field>
        ) : null}
      </fieldset>
      {(accounts.data?.items ?? []).length > 0 ? (
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 font-medium text-sm">
            {intl.formatMessage(t.tokenAccounts)}
          </legend>
          <p className="text-muted-foreground text-xs">{intl.formatMessage(t.tokenAccountsHelp)}</p>
          <ul className="flex max-h-40 flex-col gap-2 overflow-y-auto">
            {(accounts.data?.items ?? []).map((a) => (
              <li key={a.id} className="flex items-center gap-2">
                <Checkbox
                  id={`account-${a.id}`}
                  checked={only.includes(a.id)}
                  onCheckedChange={(v) =>
                    setOnly(v === true ? [...only, a.id] : only.filter((x) => x !== a.id))
                  }
                />
                <Label htmlFor={`account-${a.id}`} className="font-normal">
                  {a.spec.name} ({a.spec.slug})
                </Label>
              </li>
            ))}
          </ul>
        </fieldset>
      ) : null}
      <Field
        label={intl.formatMessage(t.fieldAllowFrom)}
        description={intl.formatMessage(t.fieldAllowFromHelp)}
      >
        <Textarea
          value={allowFrom}
          onChange={(e) => setAllowFrom(e.target.value)}
          spellCheck={false}
        />
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={intl.formatMessage(t.fieldExpires)}>
          <Select value={expiry} onValueChange={(v) => setExpiry(v as Expiry)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["7", "30", "90", "365"] as const).map((days) => (
                <SelectItem key={days} value={days}>
                  {intl.formatMessage(t.expiresInDays, { days: Number(days) })}
                </SelectItem>
              ))}
              <SelectItem value="date">{intl.formatMessage(t.expiresOn)}</SelectItem>
              <SelectItem value="never">{intl.formatMessage(t.expiresNever)}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {expiry === "date" ? (
          <Field label={intl.formatMessage(t.fieldExpiresOn)}>
            <Input
              type="date"
              value={date}
              onChange={(e) => setDate(e.target.value)}
              required
              min={new Date().toISOString().slice(0, 10)}
            />
          </Field>
        ) : null}
      </div>
      {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
      <DialogFooter>
        <Button variant="outline" onClick={onCancel} disabled={busy}>
          {intl.formatMessage(t.cancel)}
        </Button>
        <Button type="submit" busy={busy}>
          {intl.formatMessage(t.tokenCreate)}
        </Button>
      </DialogFooter>
    </form>
  );
}

function Tokens() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const tokens = useQuery({ queryKey: ["me", "tokens"], queryFn: () => listMyTokens(api) });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const reload = () => client.invalidateQueries({ queryKey: ["me", "tokens"] });
  const revoke = async (tok: TokenView) => {
    try {
      await revokeMyToken(api, tok.id);
      toast.success(intl.formatMessage(t.tokenRevoked, { name: tok.name }));
      await reload();
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  const create = (
    <Button onClick={() => setCreating(true)}>
      <Plus aria-hidden="true" />
      {intl.formatMessage(t.tokenCreate)}
    </Button>
  );
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.tokensTitle)}
        description={intl.formatMessage(t.tokensDescription)}
        actions={tokens.data?.length ? create : null}
      />
      {tokens.isPending ? <ListSkeleton rows={3} /> : null}
      {tokens.isError ? (
        <ErrorState
          title={intl.formatMessage(t.tokensFailed)}
          error={tokens.error}
          onRetry={() => void tokens.refetch()}
        />
      ) : null}
      {tokens.data && tokens.data.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title={intl.formatMessage(t.tokensNone)}
          description={intl.formatMessage(t.tokensNoneHelp)}
          action={create}
        />
      ) : null}
      {tokens.data && tokens.data.length > 0 ? (
        <TokenList tokens={tokens.data} onRevoke={revoke} />
      ) : null}
      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{intl.formatMessage(t.tokenCreateTitle)}</DialogTitle>
            <DialogDescription>{intl.formatMessage(t.tokenCreateDescription)}</DialogDescription>
          </DialogHeader>
          {creating ? (
            <NewToken
              onCreated={(tok) => {
                setCreating(false);
                setCreated(tok);
                void reload();
              }}
              onCancel={() => setCreating(false)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
      <ShownOnce
        open={created !== null}
        title={intl.formatMessage(t.tokenCreatedTitle, { name: created?.name ?? "" })}
        description={intl.formatMessage(t.tokenCreatedDescription)}
        onClose={() => setCreated(null)}
      >
        <CopyField label={intl.formatMessage(t.tokenValue)} value={created?.token ?? ""} />
        <div className="flex flex-col gap-1.5">
          <p className="text-muted-foreground text-xs">{intl.formatMessage(t.tokenCliHelp)}</p>
          <pre className="overflow-x-auto rounded-md bg-surface-sunken p-3 font-mono text-xs">
            {`racksail login --server ${window.location.origin} --token-stdin`}
          </pre>
        </div>
      </ShownOnce>
    </div>
  );
}

/** Tokens for scripts, the racksail command and other programs. */
export default function TokensPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={3} />}>
      <Tokens />
    </IdentityMessages>
  );
}
