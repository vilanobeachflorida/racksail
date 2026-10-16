import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, KeyRound, LogIn, ShieldCheck } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { type IntlConfig, useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import { Checkbox } from "@/design/components/ui/choice";
import { Alert, Separator } from "@/design/components/ui/display";
import { Field } from "@/design/components/ui/field";
import { Input } from "@/design/components/ui/input";
import { ErrorState } from "@/design/components/ui/states";
import { ApiError } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import {
  beginPasskeySignIn,
  beginSecondStepMfa,
  beginSecondStepPasskey,
  finishPasskeySignIn,
  getSignInOptions,
  lookUpSignInLink,
  providerStartURL,
  redeemSignInLink,
  type SignInAnswer,
  type SignInOptions,
  setUpFirstAdmin,
  signIn,
  signInSecondStep,
} from "./api";
import { ListSkeleton, PasswordInput } from "./components";
import { AuthFrame } from "./frame";
import { StandaloneMessages } from "./i18n";
import { t } from "./messages";
import { failureMessage, formatTime } from "./util";
import { askForPasskey, passkeysSupported } from "./webauthn";

/** Where the browser was sent to, as the sign-in screens read it. */
type Arrival =
  | { kind: "sign-in" }
  | { kind: "activate"; code: string }
  | { kind: "second-step"; challenge: string; methods: string[] }
  | { kind: "error"; code: string };

/**
 * Reads what the address says: a sign-in link's code after /activate#, or
 * what an outside provider sent back after /sign-in#. The part after the #
 * never reaches the server, so codes stay out of its logs.
 */
export function readArrival(location: Pick<Location, "pathname" | "hash">): Arrival {
  const hash = location.hash.replace(/^#/, "");
  if (location.pathname === "/activate" && hash !== "") {
    return { kind: "activate", code: decodeURIComponent(hash) };
  }
  const params = new URLSearchParams(hash);
  const challenge = params.get("challenge");
  if (challenge) {
    return {
      kind: "second-step",
      challenge,
      methods: (params.get("methods") ?? "").split(",").filter(Boolean),
    };
  }
  const error = params.get("error");
  if (error) {
    return { kind: "error", code: error };
  }
  return { kind: "sign-in" };
}

function Problem({ error }: { error: unknown }) {
  const intl = useIntl();
  return error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null;
}

const minPasswordLength = 10;

/** The first step: a username and password, a passkey, or an outside provider. */
function FirstStep({
  options,
  initialError,
  onAnswer,
}: {
  options: SignInOptions;
  initialError: unknown;
  onAnswer: (answer: SignInAnswer) => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState<"password" | "passkey" | null>(null);
  const [error, setError] = useState<unknown>(initialError);
  const passkeys = options.passkeys && passkeysSupported();
  const providers = (options.providers ?? []).filter((p) => p.method === "redirect");

  const run = async (kind: "password" | "passkey", step: () => Promise<SignInAnswer>) => {
    setBusy(kind);
    setError(null);
    try {
      onAnswer(await step());
    } catch (e) {
      setError(e);
      setBusy(null);
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    void run("password", () => signIn(api, { username: username.trim(), password, remember }));
  };

  const withPasskey = () =>
    void run("passkey", async () => {
      const { ceremony, options: request } = await beginPasskeySignIn(api);
      const credential = await askForPasskey(request);
      return finishPasskeySignIn(api, { ceremony: ceremony ?? "", credential, remember });
    });

  return (
    <div className="flex flex-col gap-5">
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field label={intl.formatMessage(t.fieldUsername)}>
          <Input
            name="username"
            autoComplete="username webauthn"
            autoCapitalize="none"
            spellCheck={false}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
            autoFocus
          />
        </Field>
        <Field label={intl.formatMessage(t.fieldPassword)}>
          <PasswordInput
            name="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </Field>
        <Remember checked={remember} onChange={setRemember} />
        <Problem error={error} />
        <Button type="submit" busy={busy === "password"} disabled={busy !== null}>
          <LogIn aria-hidden="true" />
          {intl.formatMessage(t.signInButton)}
        </Button>
      </form>
      {passkeys || providers.length > 0 ? (
        <>
          <div className="flex items-center gap-3 text-muted-foreground text-xs">
            <Separator className="flex-1" />
            {intl.formatMessage(t.signInOr)}
            <Separator className="flex-1" />
          </div>
          <div className="flex flex-col gap-2">
            {passkeys ? (
              <Button
                variant="outline"
                onClick={withPasskey}
                busy={busy === "passkey"}
                disabled={busy !== null}
              >
                <KeyRound aria-hidden="true" />
                {intl.formatMessage(t.signInWithPasskey)}
              </Button>
            ) : null}
            {providers.map((p) => (
              <Button key={p.id} variant="outline" asChild>
                <a href={providerStartURL(p.id, remember)}>
                  {intl.formatMessage(t.signInWithProvider, { provider: p.title })}
                </a>
              </Button>
            ))}
          </div>
        </>
      ) : null}
    </div>
  );
}

function Remember({ checked, onChange }: { checked: boolean; onChange: (value: boolean) => void }) {
  const intl = useIntl();
  return (
    <div className="flex items-start gap-2">
      <Checkbox id="remember" checked={checked} onCheckedChange={(v) => onChange(v === true)} />
      <label htmlFor="remember" className="flex flex-col gap-0.5 text-sm leading-tight">
        {intl.formatMessage(t.signInRemember)}
        <span className="text-muted-foreground text-xs">
          {intl.formatMessage(t.signInRememberHelp)}
        </span>
      </label>
    </div>
  );
}

type SecondMethod = "totp" | "passkey" | "recovery" | `mfa:${string}`;

function methodLabel(
  intl: ReturnType<typeof useIntl>,
  method: string,
  titles: Record<string, string> | undefined,
): string {
  switch (method) {
    case "totp":
      return intl.formatMessage(t.secondTotp);
    case "passkey":
      return intl.formatMessage(t.secondPasskey);
    case "recovery":
      return intl.formatMessage(t.secondRecovery);
  }
  return titles?.[method] ?? intl.formatMessage(t.secondAddon);
}

/** The second step: an authenticator code, a passkey, an add-on's method or a recovery code. */
function SecondStep({
  challenge,
  methods,
  titles,
  name,
  onAnswer,
  onBack,
}: {
  challenge: string;
  methods: string[];
  titles?: Record<string, string>;
  name?: string;
  onAnswer: (answer: SignInAnswer) => void;
  onBack: () => void;
}) {
  const intl = useIntl();
  const api = useApi();
  const usable = methods.filter((m) => m !== "passkey" || passkeysSupported()) as SecondMethod[];
  const first = usable.find((m) => m !== "recovery") ?? usable[0] ?? "totp";
  const [method, setMethod] = useState<SecondMethod>(first);
  const [code, setCode] = useState("");
  const [trust, setTrust] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [prompt, setPrompt] = useState<{ text: string; input: boolean } | null>(null);
  const polling = useRef<number | null>(null);
  // The approval poll reads these as they are when it asks.
  const latest = useRef({ trust, onAnswer });
  latest.current = { trust, onAnswer };

  const stopPolling = useCallback(() => {
    if (polling.current !== null) {
      window.clearInterval(polling.current);
      polling.current = null;
    }
  }, []);
  useEffect(() => stopPolling, [stopPolling]);

  const answer = async (step: () => Promise<SignInAnswer>) => {
    setBusy(true);
    setError(null);
    try {
      const result = await step();
      stopPolling();
      onAnswer(result);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  // An add-on's method starts as soon as it's chosen: it may send a code,
  // or ask the person to approve the sign-in elsewhere.
  useEffect(() => {
    stopPolling();
    setPrompt(null);
    if (!method.startsWith("mfa:")) {
      return;
    }
    let live = true;
    beginSecondStepMfa(api, challenge, method.slice("mfa:".length))
      .then((started) => {
        if (!live) {
          return;
        }
        setPrompt({ text: started.prompt, input: started.input });
        if (!started.input) {
          // Ask every few seconds whether it was approved.
          polling.current = window.setInterval(() => {
            signInSecondStep(api, { challenge, method, trust: latest.current.trust })
              .then((result) => {
                stopPolling();
                latest.current.onAnswer(result);
              })
              .catch((e: unknown) => {
                if (!(e instanceof ApiError && e.code === "auth.pending")) {
                  stopPolling();
                  setError(e);
                }
              });
          }, 3000);
        }
      })
      .catch((e: unknown) => live && setError(e));
    return () => {
      live = false;
      stopPolling();
    };
  }, [api, challenge, method, stopPolling]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (method === "passkey") {
      void answer(async () => {
        const { options } = await beginSecondStepPasskey(api, challenge);
        const credential = await askForPasskey(options);
        return signInSecondStep(api, { challenge, method, credential, trust });
      });
      return;
    }
    // Authenticator codes are often written in groups, as 123 456.
    const value = method === "totp" ? code.replace(/\s/g, "") : code.trim();
    void answer(() => signInSecondStep(api, { challenge, method, code: value, trust }));
  };

  const others = usable.filter((m) => m !== method);
  const needsCode =
    method === "totp" ||
    method === "recovery" ||
    (method.startsWith("mfa:") && prompt?.input === true);

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      {name ? <p className="text-sm">{intl.formatMessage(t.secondGreeting, { name })}</p> : null}
      <p className="flex items-center gap-2 font-medium">
        <ShieldCheck className="size-4 text-muted-foreground" aria-hidden="true" />
        {methodLabel(intl, method, titles)}
      </p>
      {method === "totp" ? (
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
            autoFocus
          />
        </Field>
      ) : null}
      {method === "recovery" ? (
        <Field
          label={intl.formatMessage(t.fieldRecoveryCode)}
          description={intl.formatMessage(t.fieldRecoveryCodeHelp)}
        >
          <Input
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            maxLength={32}
            required
            autoFocus
          />
        </Field>
      ) : null}
      {method === "passkey" ? (
        <p className="text-muted-foreground text-sm">{intl.formatMessage(t.secondPasskeyHelp)}</p>
      ) : null}
      {method.startsWith("mfa:") ? (
        prompt ? (
          <>
            <p className="text-sm">{prompt.text}</p>
            {prompt.input ? (
              <Field label={intl.formatMessage(t.fieldAnswer)}>
                <Input
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  required
                  autoFocus
                  autoComplete="one-time-code"
                />
              </Field>
            ) : (
              <p role="status" className="text-muted-foreground text-sm">
                {intl.formatMessage(t.secondWaiting)}
              </p>
            )}
          </>
        ) : (
          <ListSkeleton rows={1} />
        )
      ) : null}
      <div className="flex items-start gap-2">
        <Checkbox id="trust" checked={trust} onCheckedChange={(v) => setTrust(v === true)} />
        <label htmlFor="trust" className="flex flex-col gap-0.5 text-sm leading-tight">
          {intl.formatMessage(t.secondTrust)}
          <span className="text-muted-foreground text-xs">
            {intl.formatMessage(t.secondTrustHelp)}
          </span>
        </label>
      </div>
      <Problem error={error} />
      {needsCode || method === "passkey" ? (
        <Button type="submit" busy={busy}>
          {method === "passkey" ? <KeyRound aria-hidden="true" /> : null}
          {intl.formatMessage(method === "passkey" ? t.secondUsePasskey : t.secondVerify)}
        </Button>
      ) : null}
      {others.length > 0 ? (
        <div className="flex flex-col gap-1">
          <p className="text-muted-foreground text-xs">{intl.formatMessage(t.secondOther)}</p>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {others.map((m) => (
              <Button
                key={m}
                variant="link"
                size="sm"
                onClick={() => {
                  setMethod(m);
                  setCode("");
                  setError(null);
                }}
              >
                {methodLabel(intl, m, titles)}
              </Button>
            ))}
          </div>
        </div>
      ) : null}
      <Button variant="ghost" size="sm" className="self-start" onClick={onBack}>
        <ArrowLeft className="rtl:rotate-180" aria-hidden="true" />
        {intl.formatMessage(t.secondBack)}
      </Button>
    </form>
  );
}

/** Signing in: the first step, then the second when the person has one. */
function SignIn({
  options,
  arrival,
  onSignedIn,
}: {
  options: SignInOptions;
  arrival: Arrival;
  onSignedIn: () => void;
}) {
  const intl = useIntl();
  const [second, setSecond] = useState<{
    challenge: string;
    methods: string[];
    titles?: Record<string, string>;
    name?: string;
  } | null>(() =>
    arrival.kind === "second-step"
      ? { challenge: arrival.challenge, methods: arrival.methods }
      : null,
  );
  const initialError = useMemo(
    () =>
      arrival.kind === "error"
        ? new ApiError(401, { type: "about:blank", status: 401, code: arrival.code })
        : null,
    [arrival],
  );
  const onAnswer = (answer: SignInAnswer) => {
    if (answer.signedIn) {
      onSignedIn();
      return;
    }
    if (answer.challenge) {
      setSecond({
        challenge: answer.challenge,
        methods: answer.methods ?? [],
        ...(answer.titles ? { titles: answer.titles } : {}),
        ...(answer.name ? { name: answer.name } : {}),
      });
    }
  };
  if (second) {
    return (
      <AuthFrame
        title={intl.formatMessage(t.secondTitle)}
        description={intl.formatMessage(t.secondDescription)}
      >
        <SecondStep {...second} onAnswer={onAnswer} onBack={() => setSecond(null)} />
      </AuthFrame>
    );
  }
  return (
    <AuthFrame
      title={intl.formatMessage(t.signInTitle)}
      description={intl.formatMessage(t.signInDescription)}
    >
      <FirstStep options={options} initialError={initialError} onAnswer={onAnswer} />
    </AuthFrame>
  );
}

/** Making the panel's first admin, with the setup code it printed. */
function Setup({ onSignedIn }: { onSignedIn: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await setUpFirstAdmin(api, {
        code: code.trim(),
        username: username.trim(),
        password,
        ...(name.trim() ? { name: name.trim() } : {}),
        ...(email.trim() ? { email: email.trim() } : {}),
      });
      onSignedIn();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };
  return (
    <AuthFrame
      title={intl.formatMessage(t.setupTitle)}
      description={intl.formatMessage(t.setupDescription)}
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field
          label={intl.formatMessage(t.fieldSetupCode)}
          description={intl.formatMessage(t.fieldSetupCodeHelp)}
        >
          <Input
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            required
            autoFocus
          />
        </Field>
        <Field label={intl.formatMessage(t.fieldUsername)}>
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
          />
        </Field>
        <Field label={intl.formatMessage(t.fieldName)}>
          <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
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
          />
        </Field>
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
          />
        </Field>
        <Problem error={error} />
        <Button type="submit" busy={busy}>
          {intl.formatMessage(t.setupButton)}
        </Button>
      </form>
    </AuthFrame>
  );
}

/** Choosing a password with a sign-in link, for a new user or one helped back in. */
function Activate({ code, onSignedIn }: { code: string; onSignedIn: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const info = useQuery({
    queryKey: ["sign-in-link", code],
    queryFn: () => lookUpSignInLink(api, code),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });
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
      await redeemSignInLink(api, code, password);
      onSignedIn();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  if (info.isPending) {
    return (
      <AuthFrame title={intl.formatMessage(t.activateTitle)}>
        <ListSkeleton rows={2} />
      </AuthFrame>
    );
  }
  if (info.isError) {
    return (
      <AuthFrame title={intl.formatMessage(t.activateTitle)}>
        <ErrorState title={intl.formatMessage(t.activateInvalid)} error={info.error} />
        <Button variant="outline" asChild>
          <a href="/">{intl.formatMessage(t.activateToSignIn)}</a>
        </Button>
      </AuthFrame>
    );
  }
  const link = info.data;
  const reset = link.purpose === "reset";
  return (
    <AuthFrame
      title={intl.formatMessage(reset ? t.resetTitle : t.activateTitle)}
      description={intl.formatMessage(reset ? t.resetDescription : t.activateDescription, {
        username: link.username,
        expires: formatTime(intl, link.expiresAt),
      })}
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        <input
          type="text"
          name="username"
          autoComplete="username"
          value={link.username}
          readOnly
          hidden
        />
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
        <Problem error={error} />
        <Button type="submit" busy={busy} disabled={mismatch}>
          {intl.formatMessage(t.activateButton)}
        </Button>
      </form>
    </AuthFrame>
  );
}

function Screens({ onSignedIn }: { onSignedIn: () => void }) {
  const intl = useIntl();
  const api = useApi();
  const [arrival] = useState(() => {
    const read = readArrival(window.location);
    // What the provider or the link put after the # is read once, so it
    // isn't used again after a reload.
    if (read.kind !== "sign-in" && read.kind !== "activate") {
      window.history.replaceState(window.history.state, "", window.location.pathname);
    }
    return read;
  });
  const options = useQuery({
    queryKey: ["sign-in-options"],
    queryFn: () => getSignInOptions(api),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
    enabled: arrival.kind !== "activate",
  });
  const done = () => {
    // The sign-in screens' own addresses aren't screens of the panel.
    if (["/sign-in", "/activate", "/setup"].includes(window.location.pathname)) {
      window.history.replaceState(null, "", "/");
    }
    onSignedIn();
  };
  if (arrival.kind === "activate") {
    return <Activate code={arrival.code} onSignedIn={done} />;
  }
  if (options.isPending) {
    return (
      <AuthFrame title={intl.formatMessage(t.signInTitle)}>
        <ListSkeleton rows={3} />
      </AuthFrame>
    );
  }
  if (options.isError) {
    return (
      <AuthFrame title={intl.formatMessage(t.signInTitle)}>
        <ErrorState
          title={intl.formatMessage(t.signInUnavailable)}
          error={options.error}
          onRetry={() => void options.refetch()}
        />
      </AuthFrame>
    );
  }
  if (options.data.setupRequired) {
    return <Setup onSignedIn={done} />;
  }
  return <SignIn options={options.data} arrival={arrival} onSignedIn={done} />;
}

export interface AuthScreensProps {
  /** The locale to show. */
  locale: string;
  /** Called once the person is signed in, to read the session again. */
  onSignedIn: () => void;
  onIntlError?: IntlConfig["onError"];
}

/**
 * The screens of signing in: the sign-in screen with its second step, the
 * first admin's setup, and choosing a password with a sign-in link. They
 * show before the panel, with their own messages.
 */
export default function AuthScreens({ locale, onSignedIn, onIntlError }: AuthScreensProps) {
  return (
    <StandaloneMessages locale={locale} onError={onIntlError}>
      <Screens onSignedIn={onSignedIn} />
    </StandaloneMessages>
  );
}
