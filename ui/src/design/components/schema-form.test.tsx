import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import {
  buildForm,
  defaultValue,
  getAt,
  humanize,
  parsePath,
  setAt,
  withoutEmptyObjects,
} from "@/lib/forms/model";
import type { JSONSchema } from "@/lib/schema";
import { renderWithProviders } from "@/test/render";
import { SchemaForm } from "./schema-form";

const schema: JSONSchema = {
  type: "object",
  required: ["domain", "runtime", "password"],
  $defs: {
    limits: {
      type: "object",
      properties: { memory: { type: "integer", minimum: 0 }, timeout: { type: "integer" } },
    },
  },
  properties: {
    domain: {
      type: "string",
      title: "Domain",
      description: "The address visitors type.",
      format: "hostname",
      examples: ["example.com"],
    },
    runtime: { enum: ["php", "node", "static"], default: "php" },
    alwaysOn: {
      type: "boolean",
      default: false,
      description: "Keep the site running when nobody visits.",
    },
    aliases: { type: "array", items: { type: "string" }, maxItems: 3 },
    password: { type: "string", description: "The site's admin password." },
    workers: { type: "integer", minimum: 1, maximum: 16, default: 2 },
    limits: { $ref: "#/$defs/limits" },
    region: { type: "string", readOnly: true },
  },
};

const hints = {
  groups: [{ name: "Resources", fields: ["limits"], advanced: true }],
  fields: {
    password: { sensitive: true },
    domain: { createOnly: true, order: -1 },
    workers: { advanced: true },
    "limits.memory": { widget: "size" as const },
    "limits.timeout": { widget: "duration" as const },
  },
};

describe("the form model", () => {
  it("groups fields as the hints say, advanced ones last", () => {
    const groups = buildForm(schema, hints);
    expect(groups.map((g) => [g.name, g.advanced, g.fields.map((f) => f.path)])).toEqual([
      ["", false, ["domain", "runtime", "alwaysOn", "aliases", "password"]],
      ["Resources", true, ["limits"]],
      ["", true, ["workers"]],
    ]);
    const limits = groups[1]?.fields[0];
    expect(limits?.kind).toBe("object");
    expect(limits?.children?.map((c) => [c.path, c.hint.widget])).toEqual([
      ["limits.memory", "size"],
      ["limits.timeout", "duration"],
    ]);
  });

  it("starts from the schema's defaults", () => {
    expect(defaultValue(schema, schema)).toEqual({
      runtime: "php",
      alwaysOn: false,
      workers: 2,
      limits: {},
    });
  });

  it("reads and writes values by path", () => {
    expect(parsePath("spec.domains[0].name")).toEqual(["spec", "domains", 0, "name"]);
    const v = setAt({ a: { b: [1] } }, "a.b[1]", 2);
    expect(v).toEqual({ a: { b: [1, 2] } });
    expect(getAt(v, "a.b[1]")).toBe(2);
    expect(setAt({ a: 1, b: 2 }, "a", undefined)).toEqual({ b: 2 });
    expect(humanize("alwaysOn")).toBe("Always on");
  });

  it("leaves out optional objects nobody filled in, and keeps required ones", () => {
    const owner: JSONSchema = {
      type: "object",
      properties: {
        owner: {
          type: "object",
          properties: { level: { type: "string" }, id: { type: "string" } },
          required: ["level"],
        },
        limits: { $ref: "#/$defs/limits" },
        name: { type: "string" },
      },
      required: ["limits"],
      $defs: { limits: { type: "object", properties: { memory: { type: "integer" } } } },
    };
    expect(withoutEmptyObjects(owner, { owner: {}, limits: {}, name: "a" }, owner)).toEqual({
      limits: {},
      name: "a",
    });
    expect(withoutEmptyObjects(owner, { owner: { level: "account" } }, owner)).toEqual({
      owner: { level: "account" },
    });
  });
});

describe("the schema form", () => {
  it("validates fields as people leave them and keeps secrets out of the spec", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(
      <SchemaForm schema={schema} hints={hints} submitLabel="Create site" onSubmit={onSubmit} />,
    );
    const domain = screen.getByRole("textbox", { name: /Domain/ });
    expect(domain).toHaveAccessibleDescription("The address visitors type.");
    expect(domain).toHaveAttribute("placeholder", "example.com");
    await userEvent.type(domain, "not a host!");
    await userEvent.tab();
    expect(domain).toHaveAccessibleDescription(/Enter a host name, such as mail.example.com./);

    await userEvent.click(screen.getByRole("button", { name: "Create site" }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText("2 fields need attention.")).toBeInTheDocument();

    await userEvent.clear(domain);
    await userEvent.type(domain, "example.com");
    await userEvent.type(screen.getByLabelText(/Password/), "s3cret-pass");
    await userEvent.click(screen.getByRole("switch", { name: "Always on" }));
    await userEvent.click(screen.getByRole("button", { name: "Add to Aliases" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Aliases 1" }), "www.example.com");
    await userEvent.click(screen.getByRole("button", { name: "Create site" }));
    // Limits nobody filled in stay out of the spec.
    expect(onSubmit).toHaveBeenCalledWith({
      spec: {
        domain: "example.com",
        runtime: "php",
        alwaysOn: true,
        workers: 2,
        aliases: ["www.example.com"],
      },
      secrets: { password: "s3cret-pass" },
    });
  });

  it("keeps advanced settings folded away until needed", async () => {
    const onChange = vi.fn();
    renderWithProviders(
      <SchemaForm
        schema={schema}
        hints={hints}
        submitLabel="Save"
        onSubmit={() => {}}
        onChange={onChange}
      />,
    );
    expect(screen.queryByRole("spinbutton", { name: "Workers" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
    expect(screen.getByRole("spinbutton", { name: "Workers" })).toHaveValue(2);
    const limits = screen.getByRole("group", { name: "Limits" });
    expect(within(limits).getByRole("combobox", { name: "Unit for Timeout" })).toHaveTextContent(
      "sec",
    );

    // A unit picked after the number applies to it.
    await userEvent.type(within(limits).getByRole("spinbutton", { name: "Memory" }), "512");
    const unit = within(limits).getByRole("combobox", { name: "Unit for Memory" });
    expect(unit).toHaveTextContent("byte");
    await userEvent.click(unit);
    await userEvent.click(await screen.findByRole("option", { name: "MB" }));
    expect(within(limits).getByRole("spinbutton", { name: "Memory" })).toHaveValue(512);
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ limits: { memory: 512_000_000 } }),
    );
  });

  it("locks create-only fields when editing and shows the server's problems", () => {
    renderWithProviders(
      <SchemaForm
        schema={schema}
        hints={hints}
        mode="update"
        initialValue={{ domain: "example.com", runtime: "node" }}
        problems={[
          {
            code: "web.domain_taken",
            message: "example.com is already hosted here.",
            location: "body.spec.runtime",
          },
        ]}
        problemPrefix="body.spec"
        submitLabel="Save"
        onSubmit={() => {}}
      />,
    );
    expect(screen.getByRole("textbox", { name: /Domain/ })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: /Domain/ })).toHaveAccessibleDescription(
      "Set when it's created; it can't be changed.",
    );
    expect(screen.getByRole("combobox", { name: /Runtime/ })).toHaveAccessibleDescription(
      "example.com is already hosted here.",
    );
    expect(screen.getByLabelText(/Password/)).toHaveAttribute(
      "placeholder",
      "Leave empty to keep the current value",
    );
  });

  it("opens Advanced and goes to a problem in it on submit", async () => {
    const small: JSONSchema = {
      type: "object",
      properties: {
        name: { type: "string" },
        workers: { type: "integer", maximum: 16 },
      },
    };
    const onSubmit = vi.fn();
    renderWithProviders(
      <SchemaForm
        schema={small}
        hints={{ fields: { workers: { advanced: true } } }}
        initialValue={{ name: "shop", workers: 99 }}
        submitLabel="Save"
        onSubmit={onSubmit}
      />,
    );
    expect(screen.queryByRole("spinbutton", { name: "Workers" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText("One field needs attention.")).toBeInTheDocument();
    const workers = await screen.findByRole("spinbutton", { name: "Workers" });
    expect(screen.getByRole("button", { name: "Advanced" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(workers).toHaveAccessibleDescription("Use 16 or less.");
    await waitFor(() => expect(workers).toHaveFocus());
  });

  it("finds problems in fields nested in an Advanced group", async () => {
    renderWithProviders(
      <SchemaForm
        schema={schema}
        hints={hints}
        initialValue={{ domain: "example.com", runtime: "php", limits: { memory: -1 } }}
        submitLabel="Save"
        onSubmit={() => {}}
      />,
    );
    await userEvent.type(screen.getByLabelText(/Password/), "s3cret-pass");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    const limits = await screen.findByRole("group", { name: "Limits" });
    const memory = within(limits).getByRole("spinbutton", { name: "Memory" });
    await waitFor(() => expect(memory).toHaveFocus());
    expect(memory).toHaveAttribute("aria-invalid", "true");
  });

  it("shows the server's reasons for Advanced fields that arrive after a submit", async () => {
    const props = {
      schema,
      hints,
      mode: "update" as const,
      initialValue: { domain: "example.com", runtime: "php", workers: 4 },
      problemPrefix: "body.spec",
      submitLabel: "Save",
      onSubmit: () => {},
    };
    const { rerender } = renderWithProviders(<SchemaForm {...props} />);
    expect(screen.queryByRole("spinbutton", { name: "Workers" })).not.toBeInTheDocument();
    rerender(
      <SchemaForm
        {...props}
        problems={[
          {
            code: "web.too_many_workers",
            message: "This plan allows 2 workers.",
            location: "body.spec.workers",
          },
        ]}
      />,
    );
    const workers = await screen.findByRole("spinbutton", { name: "Workers" });
    expect(workers).toHaveAccessibleDescription("This plan allows 2 workers.");
    await waitFor(() => expect(workers).toHaveFocus());
  });

  it("moves focus for new problems only, not when its parent renders again", async () => {
    const problems = [
      { code: "web.domain_taken", message: "Taken.", location: "body.spec.runtime" },
    ];
    function Parent() {
      const [, setCount] = useState(0);
      return (
        <SchemaForm
          // New hints and problems objects on every render, as callers write them.
          schema={schema}
          hints={{ ...hints }}
          problems={[...problems]}
          problemPrefix="body.spec"
          initialValue={{ domain: "example.com", runtime: "php" }}
          submitLabel="Save"
          onSubmit={() => {}}
          onChange={() => setCount((n) => n + 1)}
        />
      );
    }
    renderWithProviders(<Parent />);
    await waitFor(() => expect(screen.getByRole("combobox", { name: /Runtime/ })).toHaveFocus());
    const domain = screen.getByRole("textbox", { name: /Domain/ });
    await userEvent.click(domain);
    await userEvent.type(domain, "x");
    expect(domain).toHaveFocus();
    expect(domain).toHaveValue("example.comx");
  });

  it("reports each change once, even when React renders twice", async () => {
    const onChange = vi.fn();
    renderWithProviders(
      <StrictMode>
        <SchemaForm
          schema={schema}
          hints={hints}
          submitLabel="Save"
          onSubmit={() => {}}
          onChange={onChange}
        />
      </StrictMode>,
    );
    await userEvent.type(screen.getByRole("textbox", { name: /Domain/ }), "abc");
    expect(onChange).toHaveBeenCalledTimes(3);
    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({ domain: "abc" }));
  });
});
