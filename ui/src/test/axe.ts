import axe from "axe-core";

const wcag = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

/**
 * Runs axe on the document or part of it and lists what fails, one line per
 * element. Whole pages also get axe's best practices, such as landmarks and
 * heading order, which don't apply to a component on its own. jsdom has no
 * layout or styles, so color contrast is left to the theme checks, which
 * test every pairing the tokens make.
 */
export async function axeViolations(
  context: Element | Document = document,
  { page = false }: { page?: boolean } = {},
): Promise<string[]> {
  const results = await axe.run(context, {
    runOnly: { type: "tag", values: page ? [...wcag, "best-practice"] : wcag },
    rules: { "color-contrast": { enabled: false } },
    resultTypes: ["violations"],
  });
  return results.violations.flatMap((v) =>
    v.nodes.map((n) => `${v.id}: ${v.help} (${n.target.join(" ")})`),
  );
}
