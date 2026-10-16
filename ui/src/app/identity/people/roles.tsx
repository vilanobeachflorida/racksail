import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useMemo, useState } from "react";
import { useIntl } from "react-intl";
import { Card, CardDescription, CardHeader, CardTitle } from "@/design/components/ui/card";
import { Badge } from "@/design/components/ui/display";
import { Input } from "@/design/components/ui/input";
import { ErrorState } from "@/design/components/ui/states";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/design/components/ui/table";
import { useApi } from "@/lib/api/context";
import { listRoles } from "../api";
import { ListSkeleton, PageHeader, Section } from "../components";
import { roleName } from "../grants";
import { IdentityMessages } from "../i18n";
import { t } from "../messages";

const order = ["admin", "reseller", "owner", "collaborator", "readonly"];

function Reach({ access }: { access: string | undefined }) {
  const intl = useIntl();
  switch (access) {
    case "everywhere":
      return <Badge variant="success">{intl.formatMessage(t.reachEverywhere)}</Badge>;
    case "scope":
      return <Badge variant="accent">{intl.formatMessage(t.reachScope)}</Badge>;
    case "named":
      return <Badge variant="outline">{intl.formatMessage(t.reachNamed)}</Badge>;
  }
  return <span className="text-muted-foreground">{intl.formatMessage(t.reachNone)}</span>;
}

function Roles() {
  const intl = useIntl();
  const api = useApi();
  const info = useQuery({ queryKey: ["roles"], queryFn: () => listRoles(api), staleTime: 300_000 });
  const [search, setSearch] = useState("");
  const rows = useMemo(() => {
    const q = search.trim().toLowerCase();
    const all = info.data?.matrix ?? [];
    return q === ""
      ? all
      : all.filter((r) => r.permission.includes(q) || r.description.toLowerCase().includes(q));
  }, [info.data, search]);
  if (info.isPending) {
    return <ListSkeleton rows={6} />;
  }
  if (info.isError) {
    return (
      <ErrorState
        title={intl.formatMessage(t.rolesFailed)}
        error={info.error}
        onRetry={() => void info.refetch()}
      />
    );
  }
  const roles = [...info.data.roles].sort((a, b) => order.indexOf(a.role) - order.indexOf(b.role));
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={intl.formatMessage(t.rolesTitle)}
        description={intl.formatMessage(t.rolesDescription)}
      />
      <ul className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {roles.map((r) => (
          <li key={r.role}>
            <Card className="h-full">
              <CardHeader>
                <CardTitle>{roleName(intl, r.role)}</CardTitle>
                <CardDescription>{r.description}</CardDescription>
              </CardHeader>
              <dl className="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
                <dt className="text-muted-foreground">{intl.formatMessage(t.roleGivenOn)}</dt>
                <dd className="flex flex-wrap gap-1">
                  {(r.levels ?? []).map((l) => (
                    <Badge key={l} variant="outline">
                      {intl.formatMessage(
                        l === "server"
                          ? t.levelServer
                          : l === "reseller"
                            ? t.levelReseller
                            : t.levelAccount,
                      )}
                    </Badge>
                  ))}
                </dd>
                <dt className="text-muted-foreground">{intl.formatMessage(t.roleMayDo)}</dt>
                <dd className="flex flex-wrap gap-1">
                  {(r.patterns ?? []).map((p) => (
                    <Badge key={p} variant="neutral" className="font-mono">
                      {p}
                    </Badge>
                  ))}
                </dd>
              </dl>
            </Card>
          </li>
        ))}
      </ul>
      <Section
        title={intl.formatMessage(t.matrixTitle)}
        description={intl.formatMessage(t.matrixDescription)}
      >
        <div className="relative max-w-sm">
          <Search
            className="pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label={intl.formatMessage(t.matrixSearch)}
            placeholder={intl.formatMessage(t.matrixSearch)}
            className="ps-9"
          />
        </div>
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{intl.formatMessage(t.matrixPermission)}</TableHead>
                {roles.map((r) => (
                  <TableHead key={r.role}>{roleName(intl, r.role)}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.permission}>
                  <TableCell className="min-w-64">
                    <span className="block font-mono text-xs">{row.permission}</span>
                    <span className="block text-muted-foreground text-xs">{row.description}</span>
                  </TableCell>
                  {roles.map((r) => (
                    <TableCell key={r.role}>
                      <Reach access={(row.roles as Record<string, string>)[r.role]} />
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        {rows.length === 0 ? (
          <p className="text-muted-foreground">{intl.formatMessage(t.matrixNone)}</p>
        ) : null}
      </Section>
    </div>
  );
}

/** The roles, what each may do and where: the permission matrix. */
export default function RolesPage() {
  return (
    <IdentityMessages fallback={<ListSkeleton rows={6} />}>
      <Roles />
    </IdentityMessages>
  );
}
