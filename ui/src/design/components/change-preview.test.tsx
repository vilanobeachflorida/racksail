import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ChangeSet } from "@/lib/changes";
import { renderWithProviders } from "@/test/render";
import { ChangePreview } from "./change-preview";

const base: ChangeSet = {
  id: "chg_1",
  title: "Create example.com",
  state: "planned",
  risk: "low",
  createdAt: "2026-10-15T12:00:00Z",
  estimate: { downloadBytes: 26_000_000, diskBytes: 120_000_000, memoryBytes: 64 * 1024 * 1024 },
  changes: [
    {
      op: "create",
      kind: "web.site",
      id: "site_01",
      scope: { level: "account", id: "acct_1" },
      source: { type: "request" },
      diff: [
        { path: "domains[0]", op: "add", after: "example.com" },
        { path: "php.version", op: "replace", before: "8.3", after: "8.4" },
        { path: "database.password", op: "replace", sensitive: true },
      ],
    },
    {
      op: "update",
      kind: "backups.policy",
      id: "bpol_1",
      scope: { level: "account", id: "acct_1" },
      source: { type: "addon", name: "Backup policy 1.2" },
    },
  ],
  effects: [
    {
      type: "download",
      action: "download",
      target: "php8.4-fpm",
      source: "https://packages.sury.org/php",
      downloadBytes: 26_000_000,
    },
    { type: "package", action: "install", target: "php8.4-fpm", detail: "8.4.12" },
    { type: "port", action: "open", target: "tcp/443" },
    {
      type: "service",
      action: "start",
      target: "racksail-site-site_01.service",
      memoryBytes: 64 * 1024 * 1024,
    },
  ],
  contributions: [
    {
      source: { type: "addon", name: "Backup policy 1.2" },
      summary: "Sends backups to a second destination",
      changes: [1],
    },
  ],
};

describe("the change preview", () => {
  it("shows changes field by field, effects on the server and what add-ons added", async () => {
    const onApply = vi.fn();
    renderWithProviders(
      <ChangePreview
        changeSet={base}
        onApply={onApply}
        kindLabel={(k) => (k === "web.site" ? "Site" : k)}
      />,
    );
    expect(screen.getByRole("heading", { name: "Create example.com" })).toBeInTheDocument();
    expect(screen.getByText("Low risk")).toBeInTheDocument();
    expect(
      screen.getByText("2 changes to resources and 4 changes on the server."),
    ).toBeInTheDocument();
    expect(screen.getByText("26 MB", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("64 MiB", { selector: "dd" })).toBeInTheDocument();

    const fields = screen.getByRole("list", { name: "Changed fields" });
    expect(within(fields).getByText("domains[0]")).toBeInTheDocument();
    expect(within(fields).getByText("8.3").tagName).toBe("DEL");
    expect(within(fields).getByText("8.4").tagName).toBe("INS");
    expect(within(fields).getByText("hidden")).toBeInTheDocument();
    expect(screen.getAllByText("Added by Backup policy 1.2")).toHaveLength(1);

    expect(screen.getByRole("heading", { name: "Downloads" })).toBeInTheDocument();
    expect(screen.getByText("From https://packages.sury.org/php")).toBeInTheDocument();
    expect(screen.getByText("tcp/443")).toBeInTheDocument();
    expect(screen.getByText("Sends backups to a second destination")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(onApply).toHaveBeenCalled();
  });

  it("won't apply while contributions conflict", () => {
    renderWithProviders(
      <ChangePreview
        changeSet={{
          ...base,
          conflicts: [
            {
              resource: "site_01",
              path: "php.version",
              message: "Both set the PHP version",
              sources: [
                { type: "addon", name: "a" },
                { type: "addon", name: "b" },
              ],
            },
          ],
        }}
        onApply={() => {}}
      />,
    );
    const conflict = screen.getByRole("alert");
    expect(conflict).toHaveAttribute("data-critical");
    expect(conflict).toHaveTextContent("1 conflict between hooks and add-ons");
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
  });

  it("asks before removing data, with the name typed for critical changes", async () => {
    const onApply = vi.fn();
    renderWithProviders(
      <ChangePreview
        changeSet={{
          ...base,
          title: "Delete example.com",
          risk: "critical",
          snapshot: true,
          effects: [
            {
              type: "file",
              action: "delete",
              target: "/srv/racksail/accounts/acct/sites/example.com",
              destructive: true,
            },
          ],
        }}
        onApply={onApply}
      />,
    );
    expect(screen.getByText("This change removes data in 1 place")).toBeInTheDocument();
    expect(
      screen.getByText("A snapshot is taken first, so what's removed can be restored."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Apply Delete example.com?" });
    expect(dialog).toHaveTextContent("A snapshot is taken first, so it can be restored.");
    const confirm = within(dialog).getByRole("button", { name: "Apply" });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByRole("textbox"), "Delete example.com");
    await userEvent.click(confirm);
    expect(onApply).toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("offers undo once applied", async () => {
    const onUndo = vi.fn();
    renderWithProviders(
      <ChangePreview changeSet={{ ...base, state: "applied" }} onUndo={onUndo} />,
    );
    expect(screen.getByText("Applied")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(onUndo).toHaveBeenCalled();
  });
});
