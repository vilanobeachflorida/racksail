import { openLintPanel } from "@codemirror/lint";
import { openSearchPanel } from "@codemirror/search";
import { EditorView } from "@codemirror/view";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { ChangeSet, Job } from "@/lib/changes";
import { shellMessages } from "@/lib/i18n";
import { pseudoMessage, pseudoMessages } from "@/lib/pseudo";
import type { JSONSchema } from "@/lib/schema";
import { untranslatedText } from "@/test/pseudo";
import { renderWithProviders } from "@/test/render";
import { ChangePreview } from "./change-preview";
import { TimeSeries } from "./charts/time-series";
import { CodeEditor } from "./code-editor";
import { DataTable } from "./data-table/data-table";
import { JobLog, JobsDrawer } from "./jobs-drawer";
import { SchemaForm } from "./schema-form";
import { ConfirmDialog } from "./ui/confirm-dialog";
import { Alert, Badge } from "./ui/display";
import { EmptyState, ErrorState, StatTile, StatusDot } from "./ui/states";

// Every message accented and bracketed; the data below is Greek, so any
// Latin letter or separator outside brackets skipped the catalogs.
const pseudo = { messages: pseudoMessages(shellMessages, "en-XA"), locale: "en-XA" };

const changeSet: ChangeSet = {
  id: "chg_1",
  title: "Διαγραφή ιστότοπου",
  state: "planned",
  risk: "critical",
  snapshot: true,
  createdAt: "2026-10-15T12:00:00Z",
  estimate: { downloadBytes: 0, diskBytes: 0, memoryBytes: 0 },
  changes: [
    {
      op: "delete",
      kind: "ιστότοπος",
      id: "ιστότοπος_1",
      scope: { level: "account", id: "λογαριασμός" },
      source: { type: "request" },
      diff: [{ path: "τομέας", op: "replace", before: "α.ελ", after: "β.ελ" }],
    },
    {
      op: "update",
      kind: "πολιτική",
      id: "πολιτική_1",
      scope: { level: "account", id: "λογαριασμός" },
      source: { type: "addon", name: "Αντίγραφα 1.2" },
    },
  ],
  effects: [
    { type: "package", action: "install", target: "πακέτο", detail: "8.4", downloadBytes: 0 },
    { type: "file", action: "delete", target: "/σβ/αρχεία", destructive: true },
  ],
  contributions: [{ source: { type: "addon", name: "Αντίγραφα 1.2" }, summary: "Κάτι ακόμα" }],
  conflicts: [
    {
      resource: "ιστότοπος_1",
      path: "τομέας",
      message: "Διαφωνία",
      sources: [
        { type: "addon", name: "Α" },
        { type: "addon", name: "Β" },
      ],
    },
  ],
};

const job = (id: string, state: Job["state"]): Job => ({
  id,
  type: "changeset.apply",
  title: `Εργασία ${id}`,
  state,
  progress: { done: 1, total: 3, message: "Εγκατάσταση" },
  attempt: 2,
  createdAt: "2026-10-15T12:00:00Z",
  error: state === "failed" ? { code: "agent.failed", message: "Απέτυχε" } : undefined,
});

describe("components in a pseudo-locale", () => {
  it("take every word and separator from the catalogs", async () => {
    renderWithProviders(
      <>
        <ChangePreview
          changeSet={{ ...changeSet, conflicts: [] }}
          onApply={() => {}}
          onCancel={() => {}}
        />
        <ChangePreview changeSet={{ ...changeSet, state: "applied" }} onUndo={() => {}} />
        <StatTile label="Αιτήματα" value="12" delta="+3%" trend="up-good" />
        <StatusDot status="ok" label="ιστός" />
        <Badge variant="warning">Προσοχή</Badge>
        <Alert tone="danger" title="Δίσκος">
          Γεμάτος
        </Alert>
        <EmptyState title="Τίποτα" description="Ακόμα τίποτα" />
        <ErrorState title="Σφάλμα" error={new Error("Απέτυχε")} onRetry={() => {}} />
        <JobLog
          lines={[{ seq: 1, time: "2026-10-15T12:00:01Z", level: "ERROR", message: "Απέτυχε" }]}
          label="Αρχείο"
        />
      </>,
      pseudo,
    );
    expect(untranslatedText()).toEqual([]);

    // The confirmation of a critical change.
    await userEvent.click(screen.getByRole("button", { name: pseudoMessage("Apply", "en-XA") }));
    await screen.findByRole("alertdialog");
    expect(untranslatedText()).toEqual([]);
  });

  it("include the jobs drawer", async () => {
    renderWithProviders(
      <JobsDrawer
        open
        onOpenChange={() => {}}
        jobs={[job("1", "running"), job("2", "failed")]}
        renderLog={(j) => <JobLog lines={[]} label={j.title} />}
        actions={{ "2": { error: { action: "retry", message: "Όχι" } } }}
        onCancel={() => {}}
        onRetry={() => {}}
      />,
      pseudo,
    );
    await screen.findByRole("dialog");
    expect(untranslatedText()).toEqual([]);
  });

  it("include forms, tables and charts", async () => {
    const schema: JSONSchema = {
      type: "object",
      required: ["τομέας"],
      properties: {
        τομέας: { type: "string", title: "Τομέας", description: "Η διεύθυνση" },
        εργάτες: { type: "integer", title: "Εργάτες", maximum: 4 },
        μνήμη: { type: "integer", title: "Μνήμη" },
        ενεργό: { type: "boolean", title: "Ενεργό" },
      },
    };
    renderWithProviders(
      <>
        <SchemaForm
          schema={schema}
          hints={{ fields: { εργάτες: { advanced: true }, μνήμη: { widget: "size" } } }}
          initialValue={{ εργάτες: 9 }}
          submitLabel="Αποθήκευση"
          onSubmit={() => {}}
          onCancel={() => {}}
        />
        <DataTable
          id="πίνακας"
          label="Ιστότοποι"
          rows={[{ id: "α", name: "αλφα" }]}
          rowKey={(r) => r.id}
          columns={[
            { id: "name", header: "Όνομα", value: (r) => r.name, sortable: true, filter: "text" },
          ]}
          bulkActions={() => null}
        />
        <TimeSeries
          title="Κίνηση"
          times={[Date.UTC(2026, 9, 15, 12, 0), Date.UTC(2026, 9, 15, 12, 1)]}
          series={[
            { label: "Αιτήματα", values: [1, null] },
            { label: "Σφάλματα", values: [0, 2] },
          ]}
        />
      </>,
      pseudo,
    );
    await userEvent.click(screen.getByRole("button", { name: "Αποθήκευση" }));
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: pseudoMessage("Advanced", "en-XA") }),
      ).toHaveAttribute("aria-expanded", "true"),
    );
    const x = (text: string) => pseudoMessage(text, "en-XA");
    await userEvent.click(screen.getByRole("button", { name: x("Show the data as a table") }));
    await userEvent.click(screen.getByRole("button", { name: new RegExp(`^\\${x("Filters")}`) }));
    // Unit names come from Intl, as the size field shows them.
    const units = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"].map(
      (unit) =>
        new Intl.NumberFormat("en-XA", { style: "unit", unit, unitDisplay: "short" })
          .formatToParts(1)
          .find((p) => p.type === "unit")?.value ?? unit,
    );
    expect(untranslatedText(document.body, units)).toEqual([]);
  });

  it("include the code editor's own panels", async () => {
    renderWithProviders(
      <CodeEditor
        label="αρχείο"
        value="α = β"
        diagnostics={[{ from: 0, to: 1, severity: "error", message: "Λάθος" }]}
      />,
      pseudo,
    );
    const content = await screen.findByRole("textbox", { name: "αρχείο" });
    const view = EditorView.findFromDOM(content) as EditorView;
    act(() => {
      openSearchPanel(view);
      openLintPanel(view);
    });
    await waitFor(() => expect(document.querySelector(".cm-search")).not.toBeNull());
    expect(untranslatedText()).toEqual([]);
  });

  it("include confirmations", async () => {
    renderWithProviders(
      <ConfirmDialog
        open
        title="Διαγραφή"
        description="Δεν αναιρείται"
        confirmLabel="Διαγραφή"
        typedConfirmation="α.ελ"
        onConfirm={() => {}}
      />,
      pseudo,
    );
    await screen.findByRole("alertdialog");
    expect(untranslatedText()).toEqual([]);
  });
});
