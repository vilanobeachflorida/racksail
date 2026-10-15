import { EditorView } from "@codemirror/view";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/render";
import { CodeEditor, languageForFile } from ".";
import { languages } from "./languages";

describe("languageForFile", () => {
  it("knows a file's language from its name", () => {
    expect(languageForFile("index.php")).toBe("php");
    expect(languageForFile("/etc/nginx/sites-available/example.com.conf")).toBe("nginx");
    expect(languageForFile("public/.htaccess")).toBe("ini");
    expect(languageForFile("Dockerfile")).toBe("dockerfile");
    expect(languageForFile("app.dockerfile")).toBe("dockerfile");
    expect(languageForFile("compose.YML")).toBe("yaml");
    expect(languageForFile(".env")).toBe("ini");
    expect(languageForFile("filter.sieve")).toBe("sieve");
    expect(languageForFile("README")).toBeNull();
    expect(languageForFile("photo.jpg")).toBeNull();
  });

  it("loads every language it names", async () => {
    for (const load of Object.values(languages)) {
      await expect(load()).resolves.toBeDefined();
    }
  });
});

function viewOf(content: HTMLElement): EditorView {
  const view = EditorView.findFromDOM(content);
  if (!view) {
    throw new Error("no editor");
  }
  return view;
}

describe("the code editor", () => {
  it("is a labelled text box that reports changes and saves with the keyboard", async () => {
    const onChange = vi.fn();
    const onSave = vi.fn();
    const { rerender } = renderWithProviders(
      <CodeEditor
        label="nginx.conf"
        value={"server {\n  listen 80;\n}\n"}
        language="nginx"
        onChange={onChange}
        onSave={onSave}
      />,
    );
    const content = await screen.findByRole("textbox", { name: "nginx.conf" });
    expect(content).toHaveAttribute("contenteditable", "true");
    expect(content).toHaveAccessibleDescription("Press Esc, then Tab, to move out of the editor.");
    expect(content).toHaveTextContent("listen 80;");

    const view = viewOf(content);
    act(() => view.dispatch({ changes: { from: 0, insert: "# example.com\n" } }));
    expect(onChange).toHaveBeenLastCalledWith("# example.com\nserver {\n  listen 80;\n}\n");

    fireEvent.keyDown(content, { key: "s", code: "KeyS", ctrlKey: true });
    expect(onSave).toHaveBeenCalledWith("# example.com\nserver {\n  listen 80;\n}\n");

    // A new value from outside replaces the text without rebuilding the editor.
    rerender(
      <CodeEditor label="nginx.conf" value={"server {}\n"} language="nginx" onChange={onChange} />,
    );
    expect(viewOf(content).state.doc.toString()).toBe("server {}\n");
  });

  it("can be read-only", async () => {
    renderWithProviders(<CodeEditor label="Log" value="started" readOnly />);
    const content = await screen.findByRole("textbox", { name: "Log" });
    expect(content).toHaveAttribute("contenteditable", "false");
    expect(viewOf(content).state.readOnly).toBe(true);
  });

  it("underlines problems it's given", async () => {
    const { rerender } = renderWithProviders(
      <CodeEditor label="php.ini" value="memory_limit = lots" />,
    );
    const content = await screen.findByRole("textbox", { name: "php.ini" });
    rerender(
      <CodeEditor
        label="php.ini"
        value="memory_limit = lots"
        diagnostics={[
          { from: 15, to: 19, severity: "error", message: "Use a size, such as 256M." },
        ]}
      />,
    );
    await waitFor(() => expect(content.querySelector(".cm-lintRange-error")).not.toBeNull());
  });
});
