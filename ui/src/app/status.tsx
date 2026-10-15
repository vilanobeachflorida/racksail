import { useQuery } from "@tanstack/react-query";
import { CircleAlert, CircleCheck } from "lucide-react";
import { useIntl } from "react-intl";
import { Card, CardContent, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Skeleton } from "@/design/components/ui/skeleton";
import { type ApiClient, ApiError, type Schemas, toProblem, unwrap } from "@/lib/api/client";
import { useApi } from "@/lib/api/context";
import { messages } from "./messages";

type HealthReport = Schemas["HealthReport"];

function isHealthReport(body: unknown): body is HealthReport {
  return (
    typeof body === "object" &&
    body !== null &&
    "status" in body &&
    (body.status === "ok" || body.status === "degraded")
  );
}

/**
 * Asks the panel for its health. A failing check makes the panel answer 503
 * with the report, which is an answer to show rather than an error.
 */
export async function fetchHealth(api: ApiClient): Promise<HealthReport> {
  const { data, error, response } = await api.GET("/api/v1/health");
  if (data) {
    return data;
  }
  if (isHealthReport(error)) {
    return error;
  }
  throw new ApiError(response.status, toProblem(response.status, error));
}

/** The home card that says whether the panel is working, and its version. */
export function PanelStatus() {
  const intl = useIntl();
  const api = useApi();
  const health = useQuery({ queryKey: ["health"], queryFn: () => fetchHealth(api) });
  const version = useQuery({
    queryKey: ["version"],
    queryFn: () => unwrap(api.GET("/api/v1/version")),
    staleTime: Number.POSITIVE_INFINITY,
  });

  return (
    <Card aria-labelledby="panel-status-title" aria-busy={health.isPending || version.isPending}>
      <CardHeader>
        <CardTitle id="panel-status-title">{intl.formatMessage(messages.statusTitle)}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {health.isPending ? (
          <Skeleton className="h-5 w-48" />
        ) : health.isError ? (
          <p className="flex items-start gap-2 text-danger">
            <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            {intl.formatMessage(messages.statusUnreachable)}
          </p>
        ) : (
          <>
            <p className="flex items-center gap-2 font-medium">
              {health.data.status === "ok" ? (
                <CircleCheck className="size-4 shrink-0 text-success" aria-hidden="true" />
              ) : (
                <CircleAlert className="size-4 shrink-0 text-warning" aria-hidden="true" />
              )}
              {intl.formatMessage(
                health.data.status === "ok" ? messages.statusOk : messages.statusDegraded,
              )}
            </p>
            {health.data.checks?.length ? (
              <ul
                aria-label={intl.formatMessage(messages.statusChecks)}
                className="flex flex-col gap-1"
              >
                {health.data.checks.map((check) => (
                  <li
                    key={check.name}
                    className={check.status === "ok" ? "text-muted-foreground" : "text-danger"}
                  >
                    {check.status === "ok"
                      ? intl.formatMessage(messages.statusCheckOk, { name: check.name })
                      : intl.formatMessage(messages.statusCheckFailing, {
                          name: check.name,
                          message: check.message ?? "",
                        })}
                  </li>
                ))}
              </ul>
            ) : null}
          </>
        )}
        {version.isPending ? (
          <Skeleton className="h-4 w-36" />
        ) : version.isSuccess ? (
          <div className="flex flex-col gap-0.5 text-muted-foreground">
            <p>
              {intl.formatMessage(messages.statusVersion, {
                version: version.data.version,
                channel: version.data.channel,
              })}
            </p>
            <p className="font-mono text-xs">
              {intl.formatMessage(messages.statusBuild, {
                goVersion: version.data.goVersion,
                os: version.data.os,
                arch: version.data.arch,
              })}
            </p>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
