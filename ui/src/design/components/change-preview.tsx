import {
  Activity,
  Cpu,
  Download,
  FileText,
  HardDrive,
  KeyRound,
  type LucideIcon,
  Network,
  Package,
  Plug,
  Server,
  Shield,
  TriangleAlert,
  Undo2,
  User,
} from "lucide-react";
import { useState } from "react";
import { useIntl } from "react-intl";
import {
  type ChangeSet,
  confirmationTarget,
  type Effect,
  type EffectType,
  type FieldDiff,
  needsConfirmation,
  type ResourceChange,
  type Risk,
  sourceName,
} from "@/lib/changes";
import { useFormat } from "@/lib/format";
import { cn, withKeys } from "@/lib/utils";
import { designMessages as m } from "../messages";
import { Button } from "./ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "./ui/card";
import { ConfirmDialog } from "./ui/confirm-dialog";
import { Badge } from "./ui/display";

const effectIcons: Record<EffectType, LucideIcon> = {
  download: Download,
  package: Package,
  port: Plug,
  service: Server,
  file: FileText,
  user: User,
  storage: HardDrive,
  network: Network,
  firewall: Shield,
  kernel: Cpu,
  certificate: KeyRound,
  process: Activity,
};

const effectOrder: EffectType[] = [
  "download",
  "package",
  "service",
  "port",
  "firewall",
  "network",
  "file",
  "user",
  "storage",
  "certificate",
  "kernel",
  "process",
];

const riskVariants: Record<Risk, "neutral" | "info" | "warning" | "danger"> = {
  read: "neutral",
  low: "info",
  medium: "warning",
  high: "danger",
  critical: "danger",
};

function show(value: unknown): string {
  if (value === undefined) {
    return "";
  }
  return typeof value === "string" ? value : JSON.stringify(value, null, 1);
}

/** The risk of a change set, as a badge. */
export function RiskBadge({ risk }: { risk: Risk }) {
  const intl = useIntl();
  return (
    <Badge variant={riskVariants[risk]}>
      {risk === "high" || risk === "critical" ? <TriangleAlert aria-hidden="true" /> : null}
      {intl.formatMessage(m.previewRisk, { risk })}
    </Badge>
  );
}

/** One field a change touches, with its value before and after. */
export function DiffRow({ diff }: { diff: FieldDiff }) {
  const intl = useIntl();
  const sign = { add: "+", remove: "−", replace: "~" }[diff.op];
  return (
    <li className="grid grid-cols-[1.25rem_minmax(8rem,auto)_1fr] items-start gap-x-2 py-1 text-sm">
      <span
        aria-hidden="true"
        className={cn(
          "font-mono font-semibold",
          diff.op === "add"
            ? "text-success"
            : diff.op === "remove"
              ? "text-danger"
              : "text-warning",
        )}
      >
        {sign}
      </span>
      <code className="font-mono text-xs leading-5">{diff.path}</code>
      <span className="min-w-0 break-words">
        <span className="sr-only">{intl.formatMessage(m.previewDiffOp, { op: diff.op })} </span>
        {diff.sensitive ? (
          <span className="text-muted-foreground italic">
            {intl.formatMessage(m.previewSensitive)}
          </span>
        ) : diff.op === "replace" ? (
          intl.formatMessage(m.previewChangedTo, {
            before: (
              <del
                key="before"
                className="whitespace-pre-wrap font-mono text-muted-foreground text-xs"
              >
                {show(diff.before)}
              </del>
            ),
            after: (
              <ins key="after" className="whitespace-pre-wrap font-mono text-xs no-underline">
                {show(diff.after)}
              </ins>
            ),
            // The words around it already say what changed.
            arrow: (chunks) => (
              <span key="arrow" aria-hidden="true">
                {chunks}
              </span>
            ),
          })
        ) : (
          <span className="whitespace-pre-wrap font-mono text-xs">
            {show(diff.op === "add" ? diff.after : diff.before)}
          </span>
        )}
      </span>
    </li>
  );
}

function ChangeItem({
  change,
  kindLabel,
}: {
  change: ResourceChange;
  kindLabel: (kind: string) => string;
}) {
  const intl = useIntl();
  const op = change.op === "action" ? (change.action ?? "action") : change.op;
  return (
    <li className="flex flex-col gap-2 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge
          variant={
            change.op === "delete" ? "danger" : change.op === "create" ? "success" : "neutral"
          }
        >
          {intl.formatMessage(m.previewOp, { op: change.op, action: op })}
        </Badge>
        <span className="font-medium text-sm">{kindLabel(change.kind)}</span>
        <code className="text-muted-foreground text-xs">{change.id}</code>
        {change.source.type !== "request" ? (
          <Badge variant="outline">
            {intl.formatMessage(m.previewAddedBy, { source: sourceName(change.source) })}
          </Badge>
        ) : null}
      </div>
      {change.diff?.length ? (
        <ul aria-label={intl.formatMessage(m.previewFields)} className="divide-y divide-border">
          {change.diff.map((d) => (
            <DiffRow key={`${d.op}:${d.path}`} diff={d} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

function EffectItem({ effect }: { effect: Effect }) {
  const intl = useIntl();
  const f = useFormat();
  const Icon = effectIcons[effect.type] ?? Activity;
  const sizes = [
    effect.downloadBytes
      ? intl.formatMessage(m.previewDownload, { size: f.bytes(effect.downloadBytes) })
      : null,
    effect.diskBytes
      ? intl.formatMessage(m.previewDisk, { size: f.bytes(effect.diskBytes) })
      : null,
    effect.memoryBytes
      ? intl.formatMessage(m.previewMemory, { size: f.bytes(effect.memoryBytes, { binary: true }) })
      : null,
  ].filter(Boolean);
  return (
    <li
      data-critical={effect.destructive ? "" : undefined}
      className={cn(
        "flex items-start gap-3 py-2 text-sm",
        effect.destructive && "critical-area -mx-2 rounded-md border px-2",
      )}
    >
      <Icon
        className={cn(
          "mt-0.5 size-4 shrink-0",
          effect.destructive ? "text-critical-danger" : "text-muted-foreground",
        )}
        aria-hidden="true"
      />
      <div className="flex min-w-0 flex-col gap-0.5">
        <p>
          {intl.formatMessage(m.previewEffectLine, {
            action: (
              <span key="action" className="font-medium">
                {intl.formatMessage(m.previewEffect, { action: effect.action })}
              </span>
            ),
            target: (
              <code key="target" className="break-all font-mono text-xs">
                {effect.target}
              </code>
            ),
            hasDetail: effect.detail ? "yes" : "no",
            detail: (
              <span key="detail" className="text-muted-foreground">
                {effect.detail}
              </span>
            ),
          })}
        </p>
        {effect.source ? (
          <p className="break-all text-muted-foreground text-xs">
            {intl.formatMessage(m.previewFrom, { source: effect.source })}
          </p>
        ) : null}
        {sizes.length ? (
          <ul className="flex flex-wrap gap-x-3 text-muted-foreground text-xs">
            {sizes.map((size) => (
              <li key={size}>{size}</li>
            ))}
          </ul>
        ) : null}
        {effect.destructive ? (
          <p className="font-medium text-critical-danger text-xs">
            {intl.formatMessage(m.previewDestructive)}
          </p>
        ) : null}
        {effect.contributor ? (
          <Badge variant="outline" className="mt-1">
            {intl.formatMessage(m.previewAddedBy, { source: effect.contributor })}
          </Badge>
        ) : null}
      </div>
    </li>
  );
}

export interface ChangePreviewProps {
  changeSet: ChangeSet;
  /** A kind's name for people, such as "Site" for web.site; the kind itself by default. */
  kindLabel?: (kind: string) => string;
  /**
   * A resource's name for people, such as a site's domain, which critical
   * changes ask to be typed; resourceTitle reads it from the kind's title
   * field. The resource's id by default.
   */
  resourceName?: (change: ResourceChange) => string;
  /**
   * Makes the person type this before applying, as destructive actions and
   * deletes of what matters ask, whatever the change set's risk.
   */
  confirmText?: string;
  onApply?: () => void | Promise<void>;
  onCancel?: () => void;
  onUndo?: () => void | Promise<void>;
  applying?: boolean;
  className?: string;
}

/**
 * The preview every change goes through: what changes in which resources,
 * field by field; what happens on the server, with downloads and their
 * sizes; what hooks and add-ons added, labelled by source; and conflicts,
 * which stop the change until they're resolved. Warnings and the
 * confirmation of destructive changes use the protected palette.
 */
export function ChangePreview({
  changeSet: cs,
  kindLabel = (kind) => kind,
  resourceName = (change) => change.id,
  confirmText,
  onApply,
  onCancel,
  onUndo,
  applying,
  className,
}: ChangePreviewProps) {
  const intl = useIntl();
  const f = useFormat();
  const [confirming, setConfirming] = useState(false);
  const conflicts = cs.conflicts ?? [];
  const contributions = cs.contributions ?? [];
  const changes = cs.changes ?? [];
  const effects = cs.effects ?? [];
  const destructive = effects.filter((e) => e.destructive);
  const grouped = effectOrder
    .map((type) => ({ type, effects: effects.filter((e) => e.type === type) }))
    .filter((g) => g.effects.length > 0);
  const est = cs.estimate;
  // Critical changes ask for the name of the resource they're about.
  const target = confirmationTarget(cs);
  const typedName =
    confirmText ??
    (cs.risk === "critical" ? (target ? resourceName(target) : cs.title) : undefined);
  const confirm = needsConfirmation(cs) || typedName !== undefined;
  const apply = async () => {
    await onApply?.();
  };

  return (
    <Card
      data-slot="change-preview"
      className={cn("gap-5", className)}
      aria-labelledby={`preview-${cs.id}`}
    >
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle id={`preview-${cs.id}`}>{cs.title}</CardTitle>
          <RiskBadge risk={cs.risk} />
          {cs.state !== "planned" ? (
            <Badge variant="outline">
              {intl.formatMessage(m.previewState, { state: cs.state })}
            </Badge>
          ) : null}
        </div>
        <p className="text-muted-foreground text-sm">
          {intl.formatMessage(m.previewSummary, {
            changes: changes.length,
            effects: effects.length,
          })}
        </p>
        {est.downloadBytes || est.diskBytes || est.memoryBytes ? (
          <dl className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
            {est.downloadBytes ? (
              <div className="flex gap-1">
                <dt className="text-muted-foreground">{intl.formatMessage(m.previewDownloads)}</dt>
                <dd className="font-medium tabular-nums">{f.bytes(est.downloadBytes)}</dd>
              </div>
            ) : null}
            {est.diskBytes ? (
              <div className="flex gap-1">
                <dt className="text-muted-foreground">{intl.formatMessage(m.previewDiskTotal)}</dt>
                <dd className="font-medium tabular-nums">{f.bytes(est.diskBytes)}</dd>
              </div>
            ) : null}
            {est.memoryBytes ? (
              <div className="flex gap-1">
                <dt className="text-muted-foreground">
                  {intl.formatMessage(m.previewMemoryTotal)}
                </dt>
                <dd className="font-medium tabular-nums">
                  {f.bytes(est.memoryBytes, { binary: true })}
                </dd>
              </div>
            ) : null}
          </dl>
        ) : null}
      </CardHeader>

      <CardContent className="flex flex-col gap-5">
        {conflicts.length > 0 ? (
          <section
            data-critical=""
            role="alert"
            className="critical-area flex flex-col gap-2 rounded-md border-2 border-critical-warning p-3"
          >
            <h3 className="flex items-center gap-2 font-semibold">
              <TriangleAlert className="size-4 text-critical-warning" aria-hidden="true" />
              {intl.formatMessage(m.previewConflicts, { count: conflicts.length })}
            </h3>
            <ul className="flex flex-col gap-1 text-sm">
              {conflicts.map((c) => (
                <li key={`${c.resource}:${c.path}`}>
                  {intl.formatMessage(m.previewConflict, {
                    path: (
                      <code key="path" className="font-mono text-xs">
                        {c.path}
                      </code>
                    ),
                    message: c.message,
                    sources: intl.formatList((c.sources ?? []).map(sourceName), {
                      type: "conjunction",
                    }),
                  })}
                </li>
              ))}
            </ul>
          </section>
        ) : null}

        {destructive.length > 0 || cs.snapshot ? (
          <section
            data-critical=""
            className="critical-area flex flex-col gap-1 rounded-md border-2 border-critical-danger p-3 text-sm"
          >
            <h3 className="flex items-center gap-2 font-semibold">
              <TriangleAlert className="size-4 text-critical-danger" aria-hidden="true" />
              {intl.formatMessage(m.previewRemovesData, { count: destructive.length })}
            </h3>
            {cs.snapshot ? <p>{intl.formatMessage(m.previewSnapshot)}</p> : null}
          </section>
        ) : null}

        <section className="flex flex-col gap-2">
          <h3 className="font-semibold text-sm">{intl.formatMessage(m.previewResources)}</h3>
          <ul className="flex flex-col gap-2">
            {withKeys(changes, (c) => `${c.op}:${c.id}:${c.action ?? ""}`).map(([key, c]) => (
              <ChangeItem key={key} change={c} kindLabel={kindLabel} />
            ))}
          </ul>
        </section>

        {grouped.length > 0 ? (
          <section className="flex flex-col gap-2">
            <h3 className="font-semibold text-sm">{intl.formatMessage(m.previewServer)}</h3>
            {grouped.map((g) => (
              <div key={g.type}>
                <h4 className="text-muted-foreground text-xs">
                  {intl.formatMessage(m.previewEffectType, { type: g.type })}
                </h4>
                <ul className="divide-y divide-border px-2">
                  {withKeys(g.effects, (e) => `${e.action}:${e.target}`).map(([key, e]) => (
                    <EffectItem key={key} effect={e} />
                  ))}
                </ul>
              </div>
            ))}
          </section>
        ) : null}

        {contributions.length > 0 ? (
          <section className="flex flex-col gap-2">
            <h3 className="font-semibold text-sm">{intl.formatMessage(m.previewContributions)}</h3>
            <ul className="flex flex-col gap-1 text-sm">
              {withKeys(contributions, (c) => `${sourceName(c.source)}:${c.summary}`).map(
                ([key, c]) => (
                  <li key={key} className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline">{sourceName(c.source)}</Badge>
                    <span>{c.summary}</span>
                  </li>
                ),
              )}
            </ul>
          </section>
        ) : null}

        {cs.error ? (
          <p className="text-danger text-sm" role="alert">
            {cs.error}
          </p>
        ) : null}
      </CardContent>

      <CardFooter className="justify-end">
        {cs.state === "planned" ? (
          <>
            {onCancel ? (
              <Button variant="outline" onClick={onCancel}>
                {intl.formatMessage(m.cancel)}
              </Button>
            ) : null}
            {onApply ? (
              <Button
                variant={confirm ? "destructive" : "default"}
                disabled={conflicts.length > 0}
                busy={applying}
                onClick={() => (confirm ? setConfirming(true) : void apply())}
              >
                {intl.formatMessage(m.previewApply)}
              </Button>
            ) : null}
          </>
        ) : null}
        {(cs.state === "applied" || cs.state === "failed") && onUndo ? (
          <Button variant="outline" onClick={() => void onUndo()}>
            <Undo2 aria-hidden="true" />
            {intl.formatMessage(m.previewUndo)}
          </Button>
        ) : null}
      </CardFooter>

      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={intl.formatMessage(m.previewConfirmTitle, { title: cs.title })}
        description={intl.formatMessage(m.previewConfirmBody, {
          count: destructive.length,
          snapshot: cs.snapshot ? "yes" : "no",
        })}
        confirmLabel={intl.formatMessage(m.previewApply)}
        typedConfirmation={typedName}
        busy={applying}
        onConfirm={apply}
      >
        {destructive.length > 0 ? (
          <ul className="flex list-disc flex-col gap-1 ps-5 text-sm">
            {withKeys(destructive, (e) => `${e.action}:${e.target}`).map(([key, e]) => (
              <li key={key}>
                {intl.formatMessage(m.previewEffectLine, {
                  action: intl.formatMessage(m.previewEffect, { action: e.action }),
                  target: (
                    <code key="target" className="font-mono text-xs">
                      {e.target}
                    </code>
                  ),
                  hasDetail: "no",
                  detail: "",
                })}
              </li>
            ))}
          </ul>
        ) : null}
      </ConfirmDialog>
    </Card>
  );
}
