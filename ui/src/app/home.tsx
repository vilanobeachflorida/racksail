import { lazy, Suspense } from "react";
import { useIntl } from "react-intl";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Skeleton } from "@/design/components/ui/skeleton";
import { cn } from "@/lib/utils";
import type { HomeCard, LazyComponent } from "@/modules/types";
import { QuickActions, RecentActivity } from "./activity";
import { AddonFrame, addonEntryKey, useAddonEntries } from "./addons";
import { messages } from "./messages";
import { homeCards, useActiveModules, usePermission } from "./modules";
import { PanelStatus } from "./status";

/** Who may see the event timeline, as internal/api checks it. */
const eventReadPermission = "core.event.read";

// The inbox's code loads apart from the shell; its words are the shell's.
const InboxCard = lazy(() => import("./screens/inbox/card"));

// One lazy component per card loader, so re-rendering the home screen never
// loads or remounts a card.
const lazyCards = new WeakMap<LazyComponent, ReturnType<typeof lazy>>();

function lazyCard(load: LazyComponent) {
  let card = lazyCards.get(load);
  if (!card) {
    card = lazy(load);
    lazyCards.set(load, card);
  }
  return card;
}

const spans: Record<NonNullable<HomeCard["size"]>, string> = {
  small: "",
  medium: "md:col-span-2",
  large: "md:col-span-2 xl:col-span-3",
};

/**
 * The home screen: what needs the person's attention first, then the
 * panel's status, quick actions, recent activity and the cards modules and
 * add-ons add.
 */
export function Home() {
  const intl = useIntl();
  const modules = useActiveModules();
  const allowed = usePermission();
  const addonCards = useAddonEntries("ui.card");
  const loading = <Skeleton className="h-40 w-full rounded-lg" />;
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6">
      <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.homeTitle)}</h1>
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <div className={spans.large}>
          <Suspense fallback={loading}>
            <InboxCard />
          </Suspense>
        </div>
        <PanelStatus />
        <QuickActions />
        {allowed(eventReadPermission) ? <RecentActivity /> : null}
        {homeCards(modules, intl, allowed).map((card) => {
          const Card = lazyCard(card.card);
          return (
            <div key={card.key} className={cn(spans[card.size ?? "small"])}>
              <Suspense fallback={loading}>
                <Card />
              </Suspense>
            </div>
          );
        })}
        {addonCards.map((entry) => (
          <Card key={addonEntryKey(entry)} aria-label={entry.ui.label}>
            <CardHeader>
              <CardTitle>{entry.ui.label}</CardTitle>
            </CardHeader>
            <CardContent>
              <AddonFrame
                addon={entry.addon}
                path={entry.ui.path ?? ""}
                title={entry.ui.label}
                className="h-48"
              />
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}
