import { StreamLanguage } from "@codemirror/language";
import type { Extension } from "@codemirror/state";

/**
 * The languages the editor highlights, each loaded the first time a file
 * in it is opened, so the panel only downloads what it shows.
 */
export const languages = {
  css: () => import("@codemirror/lang-css").then((l) => l.css()),
  html: () => import("@codemirror/lang-html").then((l) => l.html()),
  javascript: () => import("@codemirror/lang-javascript").then((l) => l.javascript({ jsx: true })),
  typescript: () =>
    import("@codemirror/lang-javascript").then((l) =>
      l.javascript({ jsx: true, typescript: true }),
    ),
  json: () => import("@codemirror/lang-json").then((l) => l.json()),
  markdown: () => import("@codemirror/lang-markdown").then((l) => l.markdown()),
  php: () => import("@codemirror/lang-php").then((l) => l.php()),
  python: () => import("@codemirror/lang-python").then((l) => l.python()),
  sql: () => import("@codemirror/lang-sql").then((l) => l.sql()),
  xml: () => import("@codemirror/lang-xml").then((l) => l.xml()),
  yaml: () => import("@codemirror/lang-yaml").then((l) => l.yaml()),
  shell: () =>
    import("@codemirror/legacy-modes/mode/shell").then((m) => StreamLanguage.define(m.shell)),
  nginx: () =>
    import("@codemirror/legacy-modes/mode/nginx").then((m) => StreamLanguage.define(m.nginx)),
  dockerfile: () =>
    import("@codemirror/legacy-modes/mode/dockerfile").then((m) =>
      StreamLanguage.define(m.dockerFile),
    ),
  toml: () =>
    import("@codemirror/legacy-modes/mode/toml").then((m) => StreamLanguage.define(m.toml)),
  ini: () =>
    import("@codemirror/legacy-modes/mode/properties").then((m) =>
      StreamLanguage.define(m.properties),
    ),
  diff: () =>
    import("@codemirror/legacy-modes/mode/diff").then((m) => StreamLanguage.define(m.diff)),
  sieve: () =>
    import("@codemirror/legacy-modes/mode/sieve").then((m) => StreamLanguage.define(m.sieve)),
} satisfies Record<string, () => Promise<Extension>>;

export type Language = keyof typeof languages;

const byExtension: Record<string, Language> = {
  css: "css",
  scss: "css",
  html: "html",
  htm: "html",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "javascript",
  ts: "typescript",
  tsx: "typescript",
  json: "json",
  md: "markdown",
  markdown: "markdown",
  php: "php",
  py: "python",
  sql: "sql",
  xml: "xml",
  svg: "xml",
  yaml: "yaml",
  yml: "yaml",
  sh: "shell",
  bash: "shell",
  conf: "nginx",
  toml: "toml",
  ini: "ini",
  env: "ini",
  htaccess: "ini",
  diff: "diff",
  patch: "diff",
  sieve: "sieve",
};

/** The language of a file by its name, such as .htaccess or index.php; null when unknown. */
export function languageForFile(name: string): Language | null {
  const base = name.split("/").pop()?.toLowerCase() ?? "";
  if (base === "dockerfile" || base.endsWith(".dockerfile")) {
    return "dockerfile";
  }
  const dot = base.lastIndexOf(".");
  const ext = dot >= 0 ? base.slice(dot + 1) : base;
  return byExtension[ext] ?? null;
}
