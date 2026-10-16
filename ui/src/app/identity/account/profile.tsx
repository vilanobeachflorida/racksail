import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Checkbox } from "@/design/components/ui/choice";
import { Alert, Badge } from "@/design/components/ui/display";
import { Field, Label } from "@/design/components/ui/field";
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
import { useApi } from "@/lib/api/context";
import { availableLocales, useLocaleChoice } from "@/lib/i18n";
import { changeMyPassword, getMe, type Me, updateMe } from "../api";
import { ListSkeleton, PageHeader, PasswordInput, Section } from "../components";
import { GrantSummary } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";
import { useSession } from "../session";
import { useStepUp } from "../step-up";
import { failureMessage, ownerLabel } from "../util";

const browser = "browser";
const minPasswordLength = 10;

function Details({ me }: { me: Me }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { refresh } = useSession();
  const [, setLocaleChoice] = useLocaleChoice();
  const [name, setName] = useState(me.name);
  const [email, setEmail] = useState(me.email ?? "");
  const [locale, setLocale] = useState(me.locale || browser);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    setName(me.name);
    setEmail(me.email ?? "");
    setLocale(me.locale || browser);
  }, [me]);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const chosen = locale === browser ? "" : locale;
      // The profile changes through a change set of the person's own user,
      // which is in place once it's answered.
      await updateMe(api, {
        name: name.trim(),
        email: email.trim(),
        locale: chosen,
      });
      await client.invalidateQueries({ queryKey: ["me"] });
      setLocaleChoice(chosen === "" ? null : chosen);
      await refresh();
      toast.success(intl.formatMessage(t.profileSaved));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Section
      title={intl.formatMessage(t.profileDetails)}
      description={intl.formatMessage(t.profileDetailsDescription)}
    >
      <form onSubmit={save} className="flex flex-col gap-4">
        <Field
          label={intl.formatMessage(t.fieldUsername)}
          description={intl.formatMessage(t.profileUsernameHelp)}
        >
          <Input value={me.username} readOnly className="max-w-sm" />
        </Field>
        <Field label={intl.formatMessage(t.fieldName)}>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoComplete="name"
            maxLength={128}
            className="max-w-sm"
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldEmail)}
          description={intl.formatMessage(t.fieldEmailHelp)}
        >
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="email"
            maxLength={254}
            className="max-w-sm"
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldLanguage)}
          description={intl.formatMessage(t.fieldLanguageHelp)}
        >
          <Select value={locale} onValueChange={setLocale}>
            <SelectTrigger className="max-w-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={browser}>{intl.formatMessage(t.languageBrowser)}</SelectItem>
              {availableLocales().map((l) => (
                <SelectItem key={l.tag} value={l.tag}>
                  {l.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
        <Button type="submit" busy={busy} className="self-start">
          {intl.formatMessage(t.save)}
        </Button>
      </form>
    </Section>
  );
}

function Password({ me }: { me: Me }) {
  const intl = useIntl();
  const api = useApi();
  const client = useQueryClient();
  const { guard } = useStepUp();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [others, setOthers] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const mismatch = again !== "" && again !== next;
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (mismatch) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await guard(() =>
        changeMyPassword(api, {
          ...(me.passwordSet ? { current } : {}),
          new: next,
          endOtherSessions: others,
        }),
      );
      setCurrent("");
      setNext("");
      setAgain("");
      await client.invalidateQueries({ queryKey: ["me"] });
      toast.success(intl.formatMessage(t.passwordChanged));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Section
      title={intl.formatMessage(me.passwordSet ? t.passwordTitle : t.passwordAddTitle)}
      description={intl.formatMessage(
        me.passwordSet ? t.passwordDescription : t.passwordAddDescription,
      )}
    >
      <form onSubmit={save} className="flex flex-col gap-4">
        <input
          type="text"
          name="username"
          autoComplete="username"
          value={me.username}
          readOnly
          hidden
        />
        {me.passwordSet ? (
          <Field label={intl.formatMessage(t.fieldCurrentPassword)}>
            <PasswordInput
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              autoComplete="current-password"
              required
              className="max-w-sm"
            />
          </Field>
        ) : null}
        <Field
          label={intl.formatMessage(t.fieldNewPassword)}
          description={intl.formatMessage(t.passwordRules, { min: minPasswordLength })}
        >
          <PasswordInput
            value={next}
            onChange={(e) => setNext(e.target.value)}
            autoComplete="new-password"
            minLength={minPasswordLength}
            required
            className="max-w-sm"
          />
        </Field>
        <Field
          label={intl.formatMessage(t.fieldPasswordAgain)}
          error={mismatch ? intl.formatMessage(t.passwordMismatch) : undefined}
        >
          <PasswordInput
            value={again}
            onChange={(e) => setAgain(e.target.value)}
            autoComplete="new-password"
            required
            className="max-w-sm"
          />
        </Field>
        <div className="flex items-center gap-2">
          <Checkbox
            id="end-others"
            checked={others}
            onCheckedChange={(v) => setOthers(v === true)}
          />
          <Label htmlFor="end-others" className="font-normal">
            {intl.formatMessage(t.passwordEndOthers)}
          </Label>
        </div>
        {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
        <Button type="submit" busy={busy} disabled={mismatch} className="self-start">
          {intl.formatMessage(me.passwordSet ? t.passwordChange : t.passwordAdd)}
        </Button>
      </form>
    </Section>
  );
}

function Access({ me }: { me: Me }) {
  const intl = useIntl();
  return (
    <Section
      title={intl.formatMessage(t.profileAccess)}
      description={intl.formatMessage(t.profileAccessDescription)}
    >
      <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-[max-content_1fr]">
        <dt className="text-muted-foreground">{intl.formatMessage(t.profileHome)}</dt>
        <dd>{ownerLabel(intl, me.home)}</dd>
        <dt className="text-muted-foreground">{intl.formatMessage(t.profileRoles)}</dt>
        <dd>
          <GrantSummary grants={me.grants ?? []} />
        </dd>
        {(me.allowFrom ?? []).length > 0 ? (
          <>
            <dt className="text-muted-foreground">{intl.formatMessage(t.profileAllowFrom)}</dt>
            <dd className="flex flex-wrap gap-1">
              {(me.allowFrom ?? []).map((a) => (
                <Badge key={a} variant="outline" className="font-mono">
                  {a}
                </Badge>
              ))}
            </dd>
          </>
        ) : null}
      </dl>
    </Section>
  );
}

function Profile() {
  const intl = useIntl();
  const api = useApi();
  const me = useQuery({ queryKey: ["me"], queryFn: () => getMe(api) });
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.profileTitle)}
        description={intl.formatMessage(t.profileDescription)}
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
          <Details me={me.data} />
          <Password me={me.data} />
          <Access me={me.data} />
        </>
      ) : null}
    </div>
  );
}

/** Your own details, language and password. */
export default function ProfilePage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={4} />}>
      <Profile />
    </IdentityMessages>
  );
}
