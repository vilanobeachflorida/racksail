import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Laptop } from "lucide-react";
import { FormattedMessage, type IntlShape, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { ConfirmDialog } from "@/design/components/ui/confirm-dialog";
import { Badge } from "@/design/components/ui/display";
import { ErrorState } from "@/design/components/ui/states";
import { toast } from "@/design/components/ui/toaster";
import { useApi } from "@/lib/api/context";
import { endMyOtherSessions, endMySession, listMySessions, type SessionView } from "../api";
import { Item, ItemList, ListSkeleton, PageHeader, Section, TimeText } from "../components";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { useSession } from "../session";
import { failureMessage } from "../util";

/** How someone signed in, for people: "a password and an authenticator app". */
export function signedInWith(
  intl: IntlShape,
  session: Pick<SessionView, "method" | "secondFactor">,
): string {
  const first = (() => {
    switch (session.method) {
      case "password":
        return intl.formatMessage(t.methodPassword);
      case "passkey":
        return intl.formatMessage(t.methodPasskey);
      case "invite":
        return intl.formatMessage(t.methodLink);
      case "setup":
        return intl.formatMessage(t.methodSetup);
      case "impersonation":
        return intl.formatMessage(t.methodImpersonation);
    }
    return session.method.startsWith("provider:")
      ? intl.formatMessage(t.methodProvider, { provider: session.method.slice("provider:".length) })
      : session.method;
  })();
  const second = (() => {
    switch (session.secondFactor ?? "") {
      case "":
        return "";
      case "totp":
        return intl.formatMessage(t.factorTotp);
      case "recovery":
        return intl.formatMessage(t.factorRecovery);
      case "passkey":
        return intl.formatMessage(t.factorPasskey);
      case "device":
        return intl.formatMessage(t.factorDevice);
    }
    return intl.formatMessage(t.factorAddon);
  })();
  return second ? intl.formatMessage(t.methodAnd, { first, second }) : first;
}

/** The sessions a person has, with what can be done about them. */
export function SessionList({
  sessions,
  onEnd,
}: {
  sessions: readonly SessionView[];
  onEnd?: (session: SessionView) => void;
}) {
  const intl = useIntl();
  return (
    <ItemList label={intl.formatMessage(t.sessionsTitle)}>
      {sessions.map((s) => (
        <Item
          key={s.id}
          icon={<Laptop aria-hidden="true" />}
          title={s.device || intl.formatMessage(t.sessionUnknownDevice)}
          badge={
            <>
              {s.current ? (
                <Badge variant="accent">{intl.formatMessage(t.thisBrowser)}</Badge>
              ) : null}
              {s.impersonator ? (
                <Badge variant="warning">{intl.formatMessage(t.sessionImpersonated)}</Badge>
              ) : null}
            </>
          }
          meta={
            <FormattedMessage
              {...t.sessionMeta}
              values={{
                how: signedInWith(intl, s),
                started: <TimeText value={s.createdAt} />,
                seen: <TimeText value={s.lastSeenAt} />,
                ip: s.ip || "none",
                ends: <TimeText value={s.expiresAt} />,
              }}
            />
          }
          actions={
            onEnd && !s.current ? (
              <Button variant="ghost" size="sm" onClick={() => onEnd(s)}>
                {intl.formatMessage(t.sessionEnd)}
              </Button>
            ) : null
          }
        />
      ))}
    </ItemList>
  );
}

function Sessions() {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { session } = useSession();
  // Someone viewing the panel as the person sees their sessions but can't
  // sign them out anywhere.
  const viewing = Boolean(session?.impersonation);
  const sessions = useQuery({ queryKey: ["me", "sessions"], queryFn: () => listMySessions(api) });
  const reload = () => client.invalidateQueries({ queryKey: ["me", "sessions"] });
  const others = viewing ? 0 : (sessions.data ?? []).filter((s) => !s.current).length;
  const end = async (s: SessionView) => {
    try {
      await endMySession(api, s.id);
      toast.success(
        intl.formatMessage(t.sessionEnded, {
          device: s.device || intl.formatMessage(t.sessionUnknownDevice),
        }),
      );
      await reload();
    } catch (error) {
      toast.error(failureMessage(intl, error));
    }
  };
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.sessionsTitle)}
        description={intl.formatMessage(t.sessionsDescription)}
        actions={
          others > 0 ? (
            <ConfirmDialog
              trigger={<Button variant="outline">{intl.formatMessage(t.sessionsEndOthers)}</Button>}
              title={intl.formatMessage(t.sessionsEndOthersTitle)}
              description={intl.formatMessage(t.sessionsEndOthersDescription, { count: others })}
              confirmLabel={intl.formatMessage(t.sessionsEndOthers)}
              onConfirm={async () => {
                try {
                  const ended = await endMyOtherSessions(api);
                  toast.success(intl.formatMessage(t.sessionsEndedOthers, { count: ended }));
                  await reload();
                } catch (error) {
                  toast.error(failureMessage(intl, error));
                }
              }}
            />
          ) : null
        }
      />
      <Section
        title={intl.formatMessage(t.sessionsActive)}
        description={intl.formatMessage(t.sessionsActiveDescription)}
      >
        {sessions.isPending ? <ListSkeleton rows={3} /> : null}
        {sessions.isError ? (
          <ErrorState
            title={intl.formatMessage(t.sessionsFailed)}
            error={sessions.error}
            onRetry={() => void sessions.refetch()}
          />
        ) : null}
        {sessions.data ? (
          <SessionList
            sessions={sessions.data}
            {...(viewing ? {} : { onEnd: (s: SessionView) => void end(s) })}
          />
        ) : null}
      </Section>
    </div>
  );
}

/** Where you're signed in, and signing out there. */
export default function SessionsPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={3} />}>
      <Sessions />
    </IdentityMessages>
  );
}
