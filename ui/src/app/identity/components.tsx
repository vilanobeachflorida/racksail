import { Check, Copy, Download, Eye, EyeOff } from "lucide-react";
import { type ComponentProps, type ReactNode, useState } from "react";
import { useIntl } from "react-intl";
import { Button } from "@/design/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/design/components/ui/card";
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
import { Skeleton } from "@/design/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { messages as shell } from "../messages";
import { t } from "./messages";
import { copyText, formatRelative, formatTime } from "./util";

/** A screen's heading, what it's for, and its main actions. */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div className="flex min-w-0 flex-col gap-1">
        <h1 className="font-semibold text-2xl">{title}</h1>
        {description ? <p className="max-w-prose text-muted-foreground">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap gap-2">{actions}</div> : null}
    </div>
  );
}

/** A part of a screen, as a card with a heading. */
export function Section({
  title,
  description,
  actions,
  children,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <Card className={className}>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <CardTitle>{title}</CardTitle>
          {description ? <CardDescription>{description}</CardDescription> : null}
        </div>
        {actions ? <div className="flex flex-wrap gap-2">{actions}</div> : null}
      </CardHeader>
      {children ? <CardContent className="flex flex-col gap-4">{children}</CardContent> : null}
    </Card>
  );
}

/** A moment, as how long ago it was, with the exact time on hover and for screen readers. */
export function TimeText({ value, empty }: { value?: string | null; empty?: ReactNode }) {
  const intl = useIntl();
  const exact = formatTime(intl, value);
  if (!exact) {
    return <>{empty ?? intl.formatMessage(t.never)}</>;
  }
  return (
    <time dateTime={value ?? undefined} title={exact}>
      {formatRelative(intl, value)}
    </time>
  );
}

/** A password input with a button that shows what's typed. */
export function PasswordInput(props: Omit<ComponentProps<typeof Input>, "type">) {
  const intl = useIntl();
  const [shown, setShown] = useState(false);
  return (
    <div className="relative">
      <Input
        {...props}
        type={shown ? "text" : "password"}
        className={cn("pe-10", props.className)}
      />
      <Button
        variant="ghost"
        size="icon-sm"
        className="absolute end-1 top-1/2 -translate-y-1/2"
        aria-label={intl.formatMessage(shown ? t.passwordHide : t.passwordShow)}
        aria-pressed={shown}
        onClick={() => setShown(!shown)}
      >
        {shown ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
      </Button>
    </div>
  );
}

/** A value to copy, such as a token or a link, with a button that copies it. */
export function CopyField({
  label,
  value,
  description,
}: {
  label: ReactNode;
  value: string;
  description?: ReactNode;
}) {
  const intl = useIntl();
  const [copied, setCopied] = useState(false);
  return (
    <Field label={label} description={description}>
      <div className="flex gap-2">
        <Input
          readOnly
          value={value}
          className="font-mono"
          onFocus={(e) => e.currentTarget.select()}
        />
        <Button
          variant="outline"
          onClick={async () => setCopied(await copyText(value))}
          aria-label={intl.formatMessage(copied ? t.copied : t.copy)}
        >
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          <span className="hidden sm:inline">{intl.formatMessage(copied ? t.copied : t.copy)}</span>
        </Button>
      </div>
    </Field>
  );
}

/** Recovery codes, to write down or save, with buttons that copy and download them. */
export function RecoveryCodes({ codes }: { codes: readonly string[] }) {
  const intl = useIntl();
  const [copied, setCopied] = useState(false);
  const text = codes.join("\n");
  const download = () => {
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: "text/plain" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = "racksail-recovery-codes.txt";
    link.click();
    URL.revokeObjectURL(url);
  };
  return (
    <div className="flex flex-col gap-3">
      <ul
        aria-label={intl.formatMessage(t.recoveryCodesList)}
        className="grid grid-cols-2 gap-x-6 gap-y-1 rounded-md bg-surface-sunken p-4 font-mono text-sm"
      >
        {codes.map((code) => (
          <li key={code}>{code}</li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={async () => setCopied(await copyText(text))}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          {intl.formatMessage(copied ? t.copied : t.copyAll)}
        </Button>
        <Button variant="outline" size="sm" onClick={download}>
          <Download aria-hidden="true" />
          {intl.formatMessage(t.download)}
        </Button>
      </div>
    </div>
  );
}

/**
 * Shows something the panel only shows once, such as a new API token, and
 * asks the person to keep it before closing.
 */
export function ShownOnce({
  open,
  title,
  description,
  children,
  onClose,
}: {
  open: boolean;
  title: ReactNode;
  description: ReactNode;
  children: ReactNode;
  onClose: () => void;
}) {
  const intl = useIntl();
  return (
    <Dialog open={open} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          {children}
          <Alert tone="warning">{intl.formatMessage(t.shownOnce)}</Alert>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{intl.formatMessage(t.keptIt)}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Rows of a list still loading. */
export function ListSkeleton({ rows = 3 }: { rows?: number }) {
  const intl = useIntl();
  return (
    <div
      role="status"
      aria-label={intl.formatMessage(shell.loading)}
      className="flex flex-col gap-2"
    >
      {Array.from({ length: rows }, (_, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: placeholders have nothing else to tell them apart
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  );
}

/** A list of simple items, each a row with its own actions. */
export function ItemList({ label, children }: { label: string; children: ReactNode }) {
  return (
    <ul
      aria-label={label}
      className="flex flex-col divide-y divide-border rounded-md border border-border"
    >
      {children}
    </ul>
  );
}

/** One item of an ItemList: what it is, more about it and what can be done with it. */
export function Item({
  icon,
  title,
  meta,
  badge,
  actions,
}: {
  icon?: ReactNode;
  title: ReactNode;
  meta?: ReactNode;
  badge?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      {icon ? (
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground [&_svg]:size-5">
          {icon}
        </span>
      ) : null}
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="flex flex-wrap items-center gap-2 font-medium">
          <span className="min-w-0 break-words">{title}</span>
          {badge}
        </span>
        {meta ? <span className="text-muted-foreground text-sm">{meta}</span> : null}
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap gap-2">{actions}</div> : null}
    </li>
  );
}
