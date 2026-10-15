import { lazy, Suspense } from "react";
import { useIntl } from "react-intl";
import { Skeleton } from "@/design/components/ui/skeleton";
import { cn } from "@/lib/utils";
import type { HomeCard, LazyComponent } from "@/modules/types";
import { messages } from "./messages";
import { homeCards, useModules } from "./modules";
import { PanelStatus } from "./status";

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

/** The home screen: the panel's status and the cards modules add. */
export function Home() {
  const intl = useIntl();
  const modules = useModules();
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6">
      <h1 className="font-semibold text-2xl">{intl.formatMessage(messages.homeTitle)}</h1>
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <PanelStatus />
        {homeCards(modules, intl).map((card) => {
          const Card = lazyCard(card.card);
          return (
            <div key={card.key} className={cn(spans[card.size ?? "small"])}>
              <Suspense fallback={<Skeleton className="h-40 w-full rounded-lg" />}>
                <Card />
              </Suspense>
            </div>
          );
        })}
      </div>
    </div>
  );
}
