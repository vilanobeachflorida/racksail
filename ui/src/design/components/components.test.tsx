import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Inbox } from "lucide-react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { renderWithProviders } from "@/test/render";
import { Logo, Mark, Wordmark } from "./mark";
import { Button } from "./ui/button";
import { Checkbox, RadioGroup, RadioGroupItem, Switch } from "./ui/choice";
import { ConfirmDialog } from "./ui/confirm-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
  Sheet,
  SheetContent,
  SheetTitle,
} from "./ui/dialog";
import {
  Alert,
  Badge,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
  Progress,
} from "./ui/display";
import { Field } from "./ui/field";
import { Input } from "./ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Tooltip,
} from "./ui/overlay";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { EmptyState, ErrorState, StatTile, StatusDot } from "./ui/states";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";
import { Toaster, toastWithUndo } from "./ui/toaster";

describe("buttons", () => {
  it("are buttons by default and refuse presses while busy", async () => {
    const onClick = vi.fn();
    renderWithProviders(
      <>
        <Button onClick={onClick}>Save</Button>
        <Button busy onClick={onClick}>
          Saving
        </Button>
        <Button asChild>
          <a href="/x">Go</a>
        </Button>
      </>,
    );
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toHaveAttribute("type", "button");
    await userEvent.click(save);
    expect(onClick).toHaveBeenCalledTimes(1);
    const saving = screen.getByRole("button", { name: "Saving" });
    expect(saving).toBeDisabled();
    expect(saving).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("link", { name: "Go" })).toHaveAttribute("data-slot", "button");
  });
});

describe("fields", () => {
  it("connect the label, the help and the error to the control", () => {
    renderWithProviders(
      <Field
        label="Domain"
        description="The address visitors type."
        error="Use a domain you own."
        required
        requiredLabel="required"
      >
        <Input />
      </Field>,
    );
    const input = screen.getByRole("textbox", { name: /Domain/ });
    expect(input).toHaveAccessibleDescription("The address visitors type. Use a domain you own.");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("alert")).toHaveTextContent("Use a domain you own.");
  });

  it("pass the connection to controls given as a function", () => {
    renderWithProviders(
      <Field label="Mode">
        {(control) => (
          <Select defaultValue="fast">
            <SelectTrigger {...control}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="fast">Fast</SelectItem>
              <SelectItem value="safe">Safe</SelectItem>
            </SelectContent>
          </Select>
        )}
      </Field>,
    );
    expect(screen.getByRole("combobox", { name: "Mode" })).toHaveTextContent("Fast");
  });
});

describe("choices", () => {
  it("toggle by click and keyboard", async () => {
    function Choices() {
      const [mode, setMode] = useState("a");
      return (
        <>
          <Checkbox aria-label="Agree" />
          <Switch aria-label="Always on" />
          <Checkbox aria-label="Partly" checked="indeterminate" />
          <RadioGroup value={mode} onValueChange={setMode} aria-label="Mode">
            <RadioGroupItem value="a" aria-label="A" />
            <RadioGroupItem value="b" aria-label="B" />
          </RadioGroup>
        </>
      );
    }
    renderWithProviders(<Choices />);
    const agree = screen.getByRole("checkbox", { name: "Agree" });
    await userEvent.click(agree);
    expect(agree).toBeChecked();
    const always = screen.getByRole("switch", { name: "Always on" });
    always.focus();
    await userEvent.keyboard(" ");
    expect(always).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Partly" })).toHaveAttribute(
      "aria-checked",
      "mixed",
    );
    await userEvent.click(screen.getByRole("radio", { name: "B" }));
    expect(screen.getByRole("radio", { name: "B" })).toBeChecked();
  });
});

describe("dialogs and sheets", () => {
  it("trap focus, close on Escape and give focus back", async () => {
    renderWithProviders(
      <Dialog>
        <DialogTrigger asChild>
          <Button>Open</Button>
        </DialogTrigger>
        <DialogContent>
          <DialogTitle>Rename</DialogTitle>
          <DialogDescription>Choose a new name.</DialogDescription>
          <Input aria-label="Name" />
        </DialogContent>
      </Dialog>,
    );
    const open = screen.getByRole("button", { name: "Open" });
    await userEvent.click(open);
    const dialog = await screen.findByRole("dialog", { name: "Rename" });
    expect(dialog).toHaveAccessibleDescription("Choose a new name.");
    expect(within(dialog).getByRole("button", { name: "Close" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(open).toHaveFocus();
  });

  it("slide in from a side", async () => {
    renderWithProviders(
      <Sheet defaultOpen>
        <SheetContent side="bottom" aria-describedby={undefined}>
          <SheetTitle>Menu</SheetTitle>
        </SheetContent>
      </Sheet>,
    );
    expect(await screen.findByRole("dialog", { name: "Menu" })).toHaveAttribute(
      "data-slot",
      "sheet-content",
    );
  });
});

describe("the confirm dialog", () => {
  it("asks for the typed phrase before a destructive action and uses the protected palette", async () => {
    const onConfirm = vi.fn();
    renderWithProviders(
      <ConfirmDialog
        trigger={<Button>Delete site</Button>}
        title="Delete example.com?"
        description="Its files and database are removed."
        confirmLabel="Delete site"
        typedConfirmation="example.com"
        onConfirm={onConfirm}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Delete site" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete example.com?" });
    expect(dialog).toHaveAttribute("data-critical");
    expect(dialog.className).toContain("critical-area");
    const confirm = within(dialog).getByRole("button", { name: "Delete site" });
    expect(confirm).toBeDisabled();
    const field = within(dialog).getByRole("textbox", { name: "Type example.com to confirm" });
    await userEvent.type(field, "example.org");
    expect(confirm).toBeDisabled();
    expect(field).toHaveAttribute("aria-invalid", "true");
    await userEvent.clear(field);
    await userEvent.type(field, "example.com");
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("stays open when the action fails", async () => {
    renderWithProviders(
      <ConfirmDialog
        open
        title="Restart?"
        description="Visitors see an error for a moment."
        confirmLabel="Restart"
        destructive={false}
        onConfirm={() => Promise.reject(new Error("no"))}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Restart" }));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  });
});

describe("menus, tooltips and tabs", () => {
  it("open menus from the keyboard", async () => {
    const onSelect = vi.fn();
    renderWithProviders(
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button>Actions</Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem onSelect={onSelect}>Restart</DropdownMenuItem>
          <DropdownMenuItem destructive>Delete</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    screen.getByRole("button", { name: "Actions" }).focus();
    await userEvent.keyboard("{Enter}");
    const menu = await screen.findByRole("menu");
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Restart" }));
    expect(onSelect).toHaveBeenCalled();
  });

  it("show tooltips on focus", async () => {
    renderWithProviders(
      <Tooltip content="Jobs">
        <Button aria-label="Jobs">J</Button>
      </Tooltip>,
    );
    await userEvent.tab();
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Jobs");
  });

  it("switch tabs with the arrow keys", async () => {
    renderWithProviders(
      <Tabs defaultValue="a">
        <TabsList>
          <TabsTrigger value="a">Overview</TabsTrigger>
          <TabsTrigger value="b">Logs</TabsTrigger>
        </TabsList>
        <TabsContent value="a">First</TabsContent>
        <TabsContent value="b">Second</TabsContent>
      </Tabs>,
    );
    screen.getByRole("tab", { name: "Overview" }).focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Logs" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveTextContent("Second");
  });
});

describe("states and display", () => {
  it("explain empty places and failures", async () => {
    const onRetry = vi.fn();
    renderWithProviders(
      <>
        <EmptyState
          icon={Inbox}
          title="No sites yet"
          description="Sites you create show up here."
          action={<Button>Create a site</Button>}
        />
        <ErrorState
          title="The sites couldn't load"
          error={
            new ApiError(503, {
              type: "about:blank",
              code: "core.unavailable",
              params: { what: "The panel" },
            })
          }
          onRetry={onRetry}
          details="GET /api/v1/web/sites 503"
        />
      </>,
    );
    expect(screen.getByText("No sites yet")).toBeInTheDocument();
    const failure = screen.getByRole("alert");
    expect(failure).toHaveTextContent(
      "The panel isn't available right now. Try again in a moment.",
    );
    await userEvent.click(within(failure).getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalled();
    await userEvent.click(within(failure).getByRole("button", { name: "Details" }));
    expect(within(failure).getByText("GET /api/v1/web/sites 503")).toBeVisible();
  });

  it("never rest on color alone", () => {
    renderWithProviders(
      <>
        <StatusDot status="error" label="backups failing" />
        <StatusDot status="ok" label="web" hideLabel />
        <StatTile label="Requests" value="12,480" delta="+12%" trend="up-good" />
        <Badge variant="warning">Degraded</Badge>
        <Alert tone="danger" title="Disk almost full">
          Free some space.
        </Alert>
        <Progress value={40} aria-label="Deploy" />
      </>,
    );
    expect(screen.getByText("backups failing")).toBeVisible();
    expect(screen.getByText("web")).toHaveClass("sr-only");
    expect(screen.getByText("+12%").parentElement).toHaveTextContent("↑");
    expect(screen.getByRole("alert")).toHaveTextContent("Disk almost full");
    expect(screen.getByRole("progressbar", { name: "Deploy" })).toHaveAttribute(
      "aria-valuenow",
      "40",
    );
  });

  it("collapse and expand", async () => {
    renderWithProviders(
      <Collapsible>
        <CollapsibleTrigger>Advanced</CollapsibleTrigger>
        <CollapsibleContent>Hidden settings</CollapsibleContent>
      </Collapsible>,
    );
    const trigger = screen.getByRole("button", { name: "Advanced" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(trigger);
    expect(screen.getByText("Hidden settings")).toBeVisible();
  });
});

describe("toasts", () => {
  it("offer undo and report how it went", async () => {
    const undo = vi.fn(() => Promise.resolve());
    renderWithProviders(<Toaster />);
    toastWithUndo({
      message: "Deleted example.com",
      undo,
      labels: { undo: "Undo", undoing: "Undoing", undone: "Undone", failed: () => "Failed" },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
    expect(undo).toHaveBeenCalled();
    expect(await screen.findByText("Undone")).toBeInTheDocument();
  });
});

describe("the logo", () => {
  it("is decorative unless named", () => {
    const { container } = renderWithProviders(
      <>
        <Mark />
        <Mark small label="Racksail" />
        <Wordmark label="Racksail" />
        <Logo stacked label="Racksail" />
      </>,
    );
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(screen.getAllByRole("img", { name: "Racksail" })).toHaveLength(3);
    expect(screen.getAllByRole("img")[0]?.querySelectorAll("path")).toHaveLength(2);
  });
});
