import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Terminal } from "lucide-react";
import { type FormEvent, useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
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
import { Alert } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input, Textarea } from "@/design/components/ui/input";
import { EmptyState, ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import { addMySSHKey, listMySSHKeys, removeMySSHKey, type SSHKey } from "../api";
import { useAskedToAdd } from "../asked";
import { Item, ItemList, ListSkeleton, PageHeader } from "../components";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { useStepUp } from "../step-up";
import { failureMessage } from "../util";

/** SSH keys, with a way to remove each. */
export function KeyList({
  keys,
  onRemove,
}: {
  keys: readonly SSHKey[];
  onRemove: (key: SSHKey) => Promise<void>;
}) {
  const intl = useIntl();
  return (
    <ItemList label={intl.formatMessage(t.sshKeysTitle)}>
      {keys.map((k) => (
        <Item
          key={k.id}
          icon={<Terminal aria-hidden="true" />}
          title={k.name}
          meta={
            <FormattedMessage
              {...t.sshKeyMeta}
              values={{
                type: k.bits ? `${k.type} (${k.bits})` : k.type,
                fingerprint: <span className="break-all font-mono">{k.fingerprint}</span>,
              }}
            />
          }
          actions={
            <ConfirmDialog
              trigger={
                <Button variant="ghost" size="sm">
                  {intl.formatMessage(t.remove)}
                </Button>
              }
              title={intl.formatMessage(t.sshKeyRemoveTitle, { name: k.name })}
              description={intl.formatMessage(t.sshKeyRemoveDescription)}
              confirmLabel={intl.formatMessage(t.remove)}
              onConfirm={() => onRemove(k)}
            />
          }
        />
      ))}
    </ItemList>
  );
}

function NewKey({ onAdded, onCancel }: { onAdded: (name: string) => void; onCancel: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const add = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // The key goes in through a change set of the person's own user.
      const cs = await guard(() =>
        addMySSHKey(api, {
          publicKey: key.trim(),
          ...(name.trim() ? { name: name.trim() } : {}),
        }),
      );
      const added = cs.changes?.[0]?.input as { name?: string } | undefined;
      onAdded(added?.name ?? name.trim());
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <form onSubmit={add} className="flex flex-col gap-4">
      <Field
        label={intl.formatMessage(t.fieldPublicKey)}
        description={intl.formatMessage(t.fieldPublicKeyHelp)}
      >
        <Textarea
          value={key}
          onChange={(e) => setKey(e.target.value)}
          spellCheck={false}
          required
          autoFocus
          className="font-mono text-xs"
          placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5… you@laptop"
        />
      </Field>
      <Field
        label={intl.formatMessage(t.fieldKeyName)}
        description={intl.formatMessage(t.fieldKeyNameHelp)}
      >
        <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={64} />
      </Field>
      {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
      <DialogFooter>
        <Button variant="outline" onClick={onCancel} disabled={busy}>
          {intl.formatMessage(t.cancel)}
        </Button>
        <Button type="submit" busy={busy}>
          {intl.formatMessage(t.sshKeyAdd)}
        </Button>
      </DialogFooter>
    </form>
  );
}

function Keys() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const keys = useQuery({ queryKey: ["me", "ssh-keys"], queryFn: () => listMySSHKeys(api) });
  const [adding, setAdding] = useState(false);
  useAskedToAdd(() => setAdding(true));
  const reload = () => client.invalidateQueries({ queryKey: ["me", "ssh-keys"] });
  const { guard } = useStepUp();
  const remove = async (k: SSHKey) => {
    try {
      await guard(() => removeMySSHKey(api, k.id));
      toast.success(intl.formatMessage(t.sshKeyRemoved, { name: k.name }));
      await reload();
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  const add = (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden="true" />
      {intl.formatMessage(t.sshKeyAdd)}
    </Button>
  );
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.sshKeysTitle)}
        description={intl.formatMessage(t.sshKeysDescription)}
        actions={keys.data?.length ? add : null}
      />
      {keys.isPending ? <ListSkeleton rows={2} /> : null}
      {keys.isError ? (
        <ErrorState
          title={intl.formatMessage(t.sshKeysFailed)}
          error={keys.error}
          onRetry={() => void keys.refetch()}
        />
      ) : null}
      {keys.data && keys.data.length === 0 ? (
        <EmptyState
          icon={Terminal}
          title={intl.formatMessage(t.sshKeysNone)}
          description={intl.formatMessage(t.sshKeysNoneHelp)}
          action={add}
        />
      ) : null}
      {keys.data && keys.data.length > 0 ? <KeyList keys={keys.data} onRemove={remove} /> : null}
      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{intl.formatMessage(t.sshKeyAddTitle)}</DialogTitle>
            <DialogDescription>{intl.formatMessage(t.sshKeysDescription)}</DialogDescription>
          </DialogHeader>
          {adding ? (
            <NewKey
              onAdded={(name) => {
                setAdding(false);
                toast.success(intl.formatMessage(t.sshKeyAdded, { name }));
                void reload();
              }}
              onCancel={() => setAdding(false)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  );
}

/** The keys that sign you in over SSH and SFTP. */
export default function SSHKeysPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={2} />}>
      <Keys />
    </IdentityMessages>
  );
}
