import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Inbox } from "lucide-react";
import { describe, expect, it } from "vitest";
import type { ChangeSet, Job } from "@/lib/changes";
import { axeViolations } from "@/test/axe";
import { renderWithProviders } from "@/test/render";
import { ChangePreview } from "./change-preview";
import { Sparkline } from "./charts/sparkline";
import { TimeSeries } from "./charts/time-series";
import { CodeEditor } from "./code-editor";
import { DataTable } from "./data-table/data-table";
import { JobLog, JobsDrawer } from "./jobs-drawer";
import { Logo } from "./mark";
import { SchemaForm } from "./schema-form";
import { Button } from "./ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Checkbox, RadioGroup, RadioGroupItem, Switch } from "./ui/choice";
import { ConfirmDialog } from "./ui/confirm-dialog";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./ui/dialog";
import {
  Alert,
  Badge,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
  Kbd,
  Progress,
} from "./ui/display";
import { Field, Label } from "./ui/field";
import { Input, Textarea } from "./ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "./ui/overlay";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { EmptyState, ErrorState, StatTile, StatusDot } from "./ui/states";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";

// axe checks what jsdom can show: names, roles, states and relations. The
// theme checks cover color contrast for every pairing the tokens make.
describe("the components' accessibility", () => {
  it("is checked by a check that finds problems", async () => {
    const { container } = renderWithProviders(
      <div>
        <input />
        <button type="button" />
      </div>,
    );
    const found = await axeViolations(container);
    expect(found.some((v) => v.startsWith("label:"))).toBe(true);
    expect(found.some((v) => v.startsWith("button-name:"))).toBe(true);
  });

  it("holds for controls and displays", async () => {
    const { container } = renderWithProviders(
      <main>
        <Logo label="Racksail" />
        <Button>Save</Button>
        <Button variant="destructive">Delete</Button>
        <Button busy>Saving</Button>
        <Field
          label="Domain"
          description="The address visitors type."
          error="Use a domain you own."
        >
          <Input />
        </Field>
        <Field label="Notes">
          <Textarea />
        </Field>
        <Field label="Runtime">
          {(control) => (
            <Select defaultValue="php">
              <SelectTrigger {...control}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="php">PHP</SelectItem>
              </SelectContent>
            </Select>
          )}
        </Field>
        <div className="flex items-center gap-2">
          <Checkbox id="www" />
          <Label htmlFor="www">Redirect www</Label>
        </div>
        <div className="flex items-center gap-2">
          <Switch id="on" />
          <Label htmlFor="on">Always on</Label>
        </div>
        <RadioGroup aria-label="Mode" defaultValue="light">
          <div className="flex items-center gap-2">
            <RadioGroupItem id="light" value="light" />
            <Label htmlFor="light">Light</Label>
          </div>
          <div className="flex items-center gap-2">
            <RadioGroupItem id="dark" value="dark" />
            <Label htmlFor="dark">Dark</Label>
          </div>
        </RadioGroup>
        <Tabs defaultValue="a">
          <TabsList>
            <TabsTrigger value="a">Overview</TabsTrigger>
            <TabsTrigger value="b">Logs</TabsTrigger>
          </TabsList>
          <TabsContent value="a">First</TabsContent>
          <TabsContent value="b">Second</TabsContent>
        </Tabs>
        <Table>
          <TableCaption>Sites</TableCaption>
          <TableHeader>
            <TableRow>
              <TableHead>Domain</TableHead>
              <TableHead>State</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow>
              <TableCell>example.com</TableCell>
              <TableCell>
                <StatusDot status="ok" label="Running" />
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
        <Card aria-labelledby="card-title">
          <CardHeader>
            <CardTitle id="card-title">Backups</CardTitle>
            <CardDescription>Nightly, kept for 30 days.</CardDescription>
          </CardHeader>
        </Card>
        <Alert tone="warning" title="The certificate expires in 6 days">
          It renews by itself.
        </Alert>
        <Badge variant="success">Running</Badge>
        <Progress value={40} aria-label="Deploy" />
        <StatTile label="Requests" value="12,480" delta="+12%" trend="up-good" />
        <EmptyState icon={Inbox} title="No sites yet" action={<Button>Create a site</Button>} />
        <ErrorState title="The sites couldn't load" error={new Error("boom")} onRetry={() => {}} />
        <Collapsible>
          <CollapsibleTrigger>Advanced</CollapsibleTrigger>
          <CollapsibleContent>Hidden settings</CollapsibleContent>
        </Collapsible>
        <p>
          Press <Kbd>Ctrl</Kbd> <Kbd>K</Kbd>
        </p>
      </main>,
    );
    expect(await axeViolations(container)).toEqual([]);
  });

  it("holds for dialogs and menus while open", async () => {
    const user = userEvent.setup();
    const { unmount } = renderWithProviders(
      <Dialog defaultOpen>
        <DialogContent>
          <DialogTitle>Rename</DialogTitle>
          <DialogDescription>Choose a new name.</DialogDescription>
          <Field label="Name">
            <Input />
          </Field>
        </DialogContent>
      </Dialog>,
    );
    await screen.findByRole("dialog", { name: "Rename" });
    expect(await axeViolations(document.body)).toEqual([]);
    unmount();

    const confirm = renderWithProviders(
      <ConfirmDialog
        open
        title="Delete example.com?"
        description="Its files and database are removed."
        confirmLabel="Delete site"
        typedConfirmation="example.com"
        onConfirm={() => {}}
      />,
    );
    await screen.findByRole("alertdialog");
    expect(await axeViolations(document.body)).toEqual([]);
    confirm.unmount();

    renderWithProviders(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button>Actions</Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Restart</DropdownMenuItem>
          <DropdownMenuItem destructive>Delete</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    await user.click(screen.getByRole("button", { name: "Actions" }));
    await screen.findByRole("menu");
    expect(await axeViolations(document.body)).toEqual([]);
  });

  it("holds for tables, forms, change previews, jobs, charts and the editor", async () => {
    const user = userEvent.setup();
    const changeSet: ChangeSet = {
      id: "chg_1",
      title: "Create example.com",
      state: "planned",
      risk: "medium",
      estimate: { downloadBytes: 26_000_000, diskBytes: 120_000_000, memoryBytes: 0 },
      createdAt: "2026-10-15T12:00:00Z",
      changes: [
        {
          op: "create",
          kind: "web.site",
          id: "site_01",
          scope: { level: "account", id: "acct_1" },
          source: { type: "request" },
          diff: [{ path: "php.version", op: "replace", before: "8.3", after: "8.4" }],
        },
      ],
      effects: [{ type: "package", action: "install", target: "php8.4-fpm" }],
    };
    const job: Job = {
      id: "job_1",
      type: "changeset.apply",
      title: "Apply chg_1",
      state: "running",
      progress: { done: 1, total: 4 },
      attempt: 1,
      createdAt: "2026-10-15T12:00:00Z",
    };
    const { container } = renderWithProviders(
      <main>
        <DataTable
          id="sites"
          label="Sites"
          rows={[{ id: "s1", domain: "example.com" }]}
          rowKey={(r) => r.id}
          columns={[{ id: "domain", header: "Domain", value: (r) => r.domain, sortable: true }]}
          bulkActions={() => <Button size="sm">Restart</Button>}
        />
        <SchemaForm
          schema={{
            type: "object",
            required: ["domain"],
            properties: {
              domain: { type: "string", format: "hostname" },
              alwaysOn: { type: "boolean" },
              aliases: { type: "array", items: { type: "string" } },
            },
          }}
          submitLabel="Create site"
          onSubmit={() => {}}
        />
        <ChangePreview changeSet={changeSet} onApply={() => {}} />
        <TimeSeries
          title="Requests"
          times={[0, 60_000]}
          series={[
            { label: "a", values: [1, 2] },
            { label: "b", values: [3, 4] },
          ]}
        />
        <Sparkline label="CPU, 23% now" values={[1, 2, 3]} />
        <CodeEditor label="nginx.conf" value="server {}" />
        <JobLog
          label="Apply chg_1"
          lines={[{ time: "2026-10-15T12:00:01Z", level: "info", message: "ok" }]}
        />
      </main>,
    );
    await screen.findByRole("textbox", { name: "nginx.conf" });
    // Errors show once the form is submitted.
    await user.click(screen.getByRole("button", { name: "Create site" }));
    expect(await axeViolations(container)).toEqual([]);

    renderWithProviders(
      <JobsDrawer open onOpenChange={() => {}} jobs={[job]} renderLog={() => null} />,
    );
    await screen.findByRole("dialog", { name: "Jobs" });
    await waitFor(async () => expect(await axeViolations(document.body)).toEqual([]));
  });
});
