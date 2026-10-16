import { useQuery } from "@tanstack/react-query";
import { KeyRound, LogOut, Smartphone } from "lucide-react";
import { type FormEvent, useState } from "react";
import { type IntlConfig, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Alert } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { useApi } from "@/lib/api/context";
import type { PanelSession } from "@/lib/api/session";
import { changeMyPassword, listMyMfa, type MFAOption, signOut } from "./api";
import { PasswordInput } from "./components";
import { AuthFrame } from "./frame";
import { StandaloneMessages } from "./i18n";
import { t } from "./messages";
import { AddonSetup, AddPasskey, TotpSetup } from "./two-factor";
import { failureMessage } from "./util";
import { passkeysSupported } from "./webauthn";

const minPasswordLength = 10;

/** Choosing a new password, when an admin asked for one or the old one was set by someone else. */
function NewPassword({ onDone }: { onDone: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const mismatch = again !== "" && again !== password;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (mismatch) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await changeMyPassword(api, { new: password });
      onDone();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <Field
        label={intl.formatMessage(t.fieldNewPassword)}
        description={intl.formatMessage(t.passwordRules, { min: minPasswordLength })}
      >
        <PasswordInput
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          minLength={minPasswordLength}
          required
          autoFocus
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
        />
      </Field>
      {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
      <Button type="submit" busy={busy} disabled={mismatch}>
        {intl.formatMessage(t.passwordSave)}
      </Button>
    </form>
  );
}

type Choice = { kind: "totp" } | { kind: "passkey" } | { kind: "addon"; option: MFAOption };

/** Setting up a second step, which the person's role needs before anything else. */
function SecondStepRequired({ onDone }: { onDone: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const [choice, setChoice] = useState<Choice | null>(null);
  const addons = useQuery({ queryKey: ["me", "mfa"], queryFn: () => listMyMfa(api), retry: false });
  if (choice?.kind === "totp") {
    return <TotpSetup onDone={onDone} onCancel={() => setChoice(null)} />;
  }
  if (choice?.kind === "passkey") {
    return <AddPasskey onDone={onDone} onCancel={() => setChoice(null)} />;
  }
  if (choice?.kind === "addon") {
    return <AddonSetup option={choice.option} onDone={onDone} onCancel={() => setChoice(null)} />;
  }
  return (
    <div className="flex flex-col gap-2">
      <Button
        variant="outline"
        className="h-auto justify-start py-3"
        onClick={() => setChoice({ kind: "totp" })}
      >
        <Smartphone aria-hidden="true" />
        <span className="flex flex-col items-start gap-0.5 whitespace-normal text-start">
          {intl.formatMessage(t.totpTitle)}
          <span className="font-normal text-muted-foreground text-xs">
            {intl.formatMessage(t.totpTeaser)}
          </span>
        </span>
      </Button>
      {passkeysSupported() ? (
        <Button
          variant="outline"
          className="h-auto justify-start py-3"
          onClick={() => setChoice({ kind: "passkey" })}
        >
          <KeyRound aria-hidden="true" />
          <span className="flex flex-col items-start gap-0.5 whitespace-normal text-start">
            {intl.formatMessage(t.passkeysTitle)}
            <span className="font-normal text-muted-foreground text-xs">
              {intl.formatMessage(t.passkeysTeaser)}
            </span>
          </span>
        </Button>
      ) : null}
      {(addons.data ?? []).map((option) => (
        <Button
          key={option.id}
          variant="outline"
          className="justify-start"
          onClick={() => setChoice({ kind: "addon", option })}
        >
          {option.title}
        </Button>
      ))}
    </div>
  );
}

function Pending({ session, onDone }: { session: PanelSession; onDone: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const [signingOut, setSigningOut] = useState(false);
  const leave = async () => {
    setSigningOut(true);
    try {
      await signOut(api);
    } finally {
      onDone();
    }
  };
  const password = session.pending === "password";
  return (
    <AuthFrame
      title={intl.formatMessage(password ? t.pendingPasswordTitle : t.pendingTwoFactorTitle)}
      description={intl.formatMessage(
        password ? t.pendingPasswordDescription : t.pendingTwoFactorDescription,
        {
          name: session.user.name,
        },
      )}
      footer={
        <Button variant="link" size="sm" onClick={() => void leave()} busy={signingOut}>
          <LogOut aria-hidden="true" />
          {intl.formatMessage(t.signOut)}
        </Button>
      }
    >
      {password ? <NewPassword onDone={onDone} /> : <SecondStepRequired onDone={onDone} />}
    </AuthFrame>
  );
}

export interface PendingScreensProps {
  locale: string;
  session: PanelSession;
  /** Called once the person did what was pending, or signed out, to read the session again. */
  onDone: () => void;
  onIntlError?: IntlConfig["onError"];
}

/**
 * What the person must do before the panel opens: set up a second step
 * their role needs, or choose a new password.
 */
export default function PendingScreens({
  locale,
  session,
  onDone,
  onIntlError,
}: PendingScreensProps) {
  return (
    <StandaloneMessages locale={locale} onError={onIntlError}>
      <Pending session={session} onDone={onDone} />
    </StandaloneMessages>
  );
}
