import { KeyRound } from "lucide-react";
import { type FormEvent, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
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
import { Input } from "@/design/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/design/components/ui/tabs";
import { useApi } from "@/lib/api/context";
import { beginStepUpPasskey, stepUp } from "./api";
import { IdentityMessages } from "./i18n";
import { t } from "./messages";
import { useSession } from "./session";
import { failureMessage } from "./util";
import { askForPasskey, passkeysSupported } from "./webauthn";

type Method = "password" | "totp" | "passkey";

function StepUpForm({ onDone }: { onDone: (ok: boolean) => void }) {
  const intl = useIntl();
  const api = useApi();
  const { session, refresh } = useSession();
  const offered = (session?.stepUpMethods ?? ["password"]).filter(
    (m): m is Method =>
      (m === "password" || m === "totp" || m === "passkey") &&
      (m !== "passkey" || passkeysSupported()),
  );
  const methods: Method[] = offered.length > 0 ? offered : ["password"];
  const [method, setMethod] = useState<Method>(methods[0] ?? "password");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const finish = async (run: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await run();
      await refresh();
      onDone(true);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (method === "password") {
      void finish(() => stepUp(api, { password }));
    } else if (method === "totp") {
      void finish(() => stepUp(api, { code: code.replace(/\s/g, "") }));
    } else {
      void finish(async () => {
        const { ceremony, options } = await beginStepUpPasskey(api);
        const credential = await askForPasskey(options);
        await stepUp(api, { ceremony: ceremony ?? "", credential });
      });
    }
  };

  const labels: Record<Method, string> = {
    password: intl.formatMessage(t.stepUpPassword),
    totp: intl.formatMessage(t.stepUpCode),
    passkey: intl.formatMessage(t.stepUpPasskey),
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <DialogHeader>
        <DialogTitle>{intl.formatMessage(t.stepUpTitle)}</DialogTitle>
        <DialogDescription>
          {intl.formatMessage(session?.impersonation ? t.stepUpImpersonating : t.stepUpDescription)}
        </DialogDescription>
      </DialogHeader>
      <Tabs value={method} onValueChange={(v) => setMethod(v as Method)}>
        {methods.length > 1 ? (
          <TabsList aria-label={intl.formatMessage(t.stepUpHow)}>
            {methods.map((m) => (
              <TabsTrigger key={m} value={m}>
                {labels[m]}
              </TabsTrigger>
            ))}
          </TabsList>
        ) : null}
        <TabsContent value="password" className="pt-3">
          <Field label={intl.formatMessage(t.fieldPassword)}>
            <Input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
              required
            />
          </Field>
        </TabsContent>
        <TabsContent value="totp" className="pt-3">
          <Field
            label={intl.formatMessage(t.fieldCode)}
            description={intl.formatMessage(t.fieldCodeHelp)}
          >
            <Input
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              autoFocus
              required
              maxLength={16}
            />
          </Field>
        </TabsContent>
        <TabsContent value="passkey" className="pt-3">
          <p className="text-muted-foreground text-sm">{intl.formatMessage(t.stepUpPasskeyHelp)}</p>
        </TabsContent>
      </Tabs>
      {error ? <Alert tone="danger">{failureMessage(intl, error)}</Alert> : null}
      <DialogFooter>
        <Button variant="outline" onClick={() => onDone(false)} disabled={busy}>
          {intl.formatMessage(t.cancel)}
        </Button>
        <Button type="submit" busy={busy}>
          {method === "passkey" ? <KeyRound aria-hidden="true" /> : null}
          {intl.formatMessage(method === "passkey" ? t.stepUpUsePasskey : t.stepUpConfirm)}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** Asks the person to confirm it's them before a sensitive action. */
export default function StepUpDialog({ onDone }: { onDone: (ok: boolean) => void }) {
  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onDone(false))}>
      <DialogContent>
        <IdentityMessages>
          <StepUpForm onDone={onDone} />
        </IdentityMessages>
      </DialogContent>
    </Dialog>
  );
}
