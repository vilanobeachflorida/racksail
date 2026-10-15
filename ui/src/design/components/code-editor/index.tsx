import { lazy, Suspense } from "react";
import { Skeleton } from "../ui/skeleton";
import type { CodeEditorProps } from "./editor";

export type { CodeEditorProps } from "./editor";
export { type Language, languageForFile } from "./languages";

const Editor = lazy(() => import("./editor"));

/**
 * The code editor, loaded the first time one is shown so CodeMirror stays
 * out of the panel's first download.
 */
export function CodeEditor(props: CodeEditorProps) {
  return (
    <Suspense
      fallback={<Skeleton className="w-full" style={{ height: props.height ?? "20rem" }} />}
    >
      <Editor {...props} />
    </Suspense>
  );
}
