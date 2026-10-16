import { KeyRound } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Alert } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import {
  beginMfa,
  beginPasskeyRegistration,
  beginTotp,
  confirmTotp,
  finishMfa,
  finishPasskeyRegistration,
  type MFAOption,
  type PasskeyView,
} from "./api";
import { CopyField, ListSkeleton, RecoveryCodes } from "./components";
import { t } from "./messages";
import { useStepUp } from "./step-up";
import { failureMessage } from "./util";
import { createPasskey, passkeysSupported } from "./webauthn";

function Problem({ error }: { error: unknown }) {
  const intl = useIntl();
  return error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null;
}

/** Groups a TOTP secret in fours, the way people type it. */
function grouped(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

/**
 * Setting up an authenticator app: the QR code to scan, or the secret to
 * type in, then a code from the app to turn it on, then the recovery codes
 * to keep.
 */
export function TotpSetup({ onDone, onCancel }: { onDone: () => void; onCancel?: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  const [key, setKey] = useState<{ secret: string; uri: string; qr: string } | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const started = useRef(false);

  useEffect(() => {
    if (started.current) {
      return;
    }
    started.current = true;
    guard(() => beginTotp(api)).then(setKey, setError);
  }, [api, guard]);

  const confirm = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setCodes(await confirmTotp(api, code.replace(/\s/g, "")));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  if (codes) {
    return (
      <div className="flex flex-col gap-4">
        <Alert tone="success" title={intl.formatMessage(t.totpOn)}>
          {intl.formatMessage(t.recoveryCodesKeep)}
        </Alert>
        <RecoveryCodes codes={codes} />
        <Button className="self-start" onClick={onDone}>
          {intl.formatMessage(t.keptIt)}
        </Button>
      </div>
    );
  }
  if (!key) {
    return error ? (
      <div className="flex flex-col gap-3">
        <Problem error={error} />
        {onCancel ? (
          <Button variant="outline" className="self-start" onClick={onCancel}>
            {intl.formatMessage(t.cancel)}
          </Button>
        ) : null}
      </div>
    ) : (
      <ListSkeleton rows={2} />
    );
  }
  return (
    <form onSubmit={confirm} className="flex flex-col gap-4">
      <ol className="flex list-decimal flex-col gap-1 ps-5 text-sm">
        <li>{intl.formatMessage(t.totpStepInstall)}</li>
        <li>{intl.formatMessage(t.totpStepScan)}</li>
        <li>{intl.formatMessage(t.totpStepCode)}</li>
      </ol>
      <div className="flex flex-wrap items-start gap-4">
        <img
          src={key.qr}
          width={176}
          height={176}
          alt={intl.formatMessage(t.totpQr)}
          className="rounded-md border border-border bg-card p-2"
        />
        <div className="flex min-w-0 flex-1 flex-col gap-3">
          <CopyField
            label={intl.formatMessage(t.totpSecret)}
            value={grouped(key.secret)}
            description={intl.formatMessage(t.totpSecretHelp)}
          />
          <a href={key.uri} className="text-primary text-sm underline-offset-4 hover:underline">
            {intl.formatMessage(t.totpOpenApp)}
          </a>
        </div>
      </div>
      <Field
        label={intl.formatMessage(t.fieldCode)}
        description={intl.formatMessage(t.fieldCodeHelp)}
      >
        <Input
          inputMode="numeric"
          autoComplete="one-time-code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          maxLength={16}
          required
          className="max-w-48"
        />
      </Field>
      <Problem error={error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" busy={busy}>
          {intl.formatMessage(t.totpTurnOn)}
        </Button>
        {onCancel ? (
          <Button variant="outline" onClick={onCancel} disabled={busy}>
            {intl.formatMessage(t.cancel)}
          </Button>
        ) : null}
      </div>
    </form>
  );
}

/** Adding a passkey: a name for it, then the browser's own prompt. */
export function AddPasskey({
  onDone,
  onCancel,
}: {
  onDone: (passkey: PasskeyView) => void;
  onCancel?: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const add = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const { ceremony, options } = await guard(() => beginPasskeyRegistration(api));
      const credential = await createPasskey(options);
      onDone(
        await finishPasskeyRegistration(api, {
          ceremony: ceremony ?? "",
          credential,
          ...(name.trim() ? { name: name.trim() } : {}),
        }),
      );
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  if (!passkeysSupported()) {
    return <Alert tone="info">{intl.formatMessage(t.passkeyUnsupported)}</Alert>;
  }
  return (
    <form onSubmit={add} className="flex flex-col gap-4">
      <Field
        label={intl.formatMessage(t.fieldPasskeyName)}
        description={intl.formatMessage(t.fieldPasskeyNameHelp)}
      >
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={64}
          className="max-w-sm"
        />
      </Field>
      <Problem error={error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" busy={busy}>
          <KeyRound aria-hidden="true" />
          {intl.formatMessage(t.passkeyAdd)}
        </Button>
        {onCancel ? (
          <Button variant="outline" onClick={onCancel} disabled={busy}>
            {intl.formatMessage(t.cancel)}
          </Button>
        ) : null}
      </div>
    </form>
  );
}

/**
 * Turning on a second step an add-on provides: it asks for something, such
 * as a code it sent, or waits for the person to approve elsewhere.
 */
export function AddonSetup({
  option,
  onDone,
  onCancel,
}: {
  option: MFAOption;
  onDone: () => void;
  onCancel: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const { guard } = useStepUp();
  const [started, setStarted] = useState<{
    ceremony: string;
    prompt: string;
    input: boolean;
  } | null>(null);
  const [response, setResponse] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const latest = useRef(onDone);
  latest.current = onDone;

  useEffect(() => {
    let live = true;
    guard(() => beginMfa(api, option.id)).then(
      (s) => live && setStarted({ ceremony: s.ceremony ?? "", prompt: s.prompt, input: s.input }),
      (e: unknown) => live && setError(e),
    );
    return () => {
      live = false;
    };
  }, [api, guard, option.id]);

  // Methods that wait for an approval elsewhere are asked every few seconds.
  useEffect(() => {
    if (!started || started.input) {
      return;
    }
    const timer = window.setInterval(() => {
      finishMfa(api, option.id, started.ceremony, "").then(
        () => {
          window.clearInterval(timer);
          latest.current();
        },
        (e: unknown) => {
          if (!(e instanceof ApiError && e.code === "auth.pending")) {
            window.clearInterval(timer);
            setError(e);
          }
        },
      );
    }, 3000);
    return () => window.clearInterval(timer);
  }, [api, option.id, started]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!started) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await finishMfa(api, option.id, started.ceremony, response.trim());
      onDone();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      {started ? (
        <>
          <p className="text-sm">{started.prompt}</p>
          {started.input ? (
            <Field label={intl.formatMessage(t.fieldAnswer)}>
              <Input
                value={response}
                onChange={(e) => setResponse(e.target.value)}
                autoComplete="one-time-code"
                required
                className="max-w-sm"
              />
            </Field>
          ) : (
            <p role="status" className="text-muted-foreground text-sm">
              {intl.formatMessage(t.secondWaiting)}
            </p>
          )}
        </>
      ) : error ? null : (
        <ListSkeleton rows={1} />
      )}
      <Problem error={error} />
      <div className="flex flex-wrap gap-2">
        {started?.input ? (
          <Button type="submit" busy={busy}>
            {intl.formatMessage(t.addonTurnOn)}
          </Button>
        ) : null}
        <Button variant="outline" onClick={onCancel} disabled={busy}>
          {intl.formatMessage(t.cancel)}
        </Button>
      </div>
    </form>
  );
}
