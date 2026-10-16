import { cn } from "@/lib/utils";
import { addonPath } from "./addons";

/** The address of one of an add-on's pages, which its frame loads. */
export function addonPageSrc(addon: string, path: string, params?: Record<string, string>): string {
  const rest = addonPath(path).split("/").map(encodeURIComponent).join("/");
  const query = params && Object.keys(params).length > 0 ? `?${new URLSearchParams(params)}` : "";
  return `/api/v1/addons/${encodeURIComponent(addon)}/ui/${rest}${query}`;
}

/**
 * One of an add-on's pages, in a frame that runs its scripts without the
 * panel's origin, so it can't reach the panel's cookies or pages; its own
 * address lets it load what it serves and call the add-on's routes.
 */
export function AddonFrame({
  addon,
  path,
  title,
  params,
  className,
}: {
  addon: string;
  path: string;
  title: string;
  params?: Record<string, string>;
  className?: string;
}) {
  return (
    <iframe
      src={addonPageSrc(addon, path, params)}
      title={title}
      sandbox="allow-scripts allow-forms"
      referrerPolicy="no-referrer"
      loading="lazy"
      className={cn("w-full rounded-md border border-border bg-background", className)}
    />
  );
}
