import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { IntlProvider, type IntlShape, useIntl } from "react-intl";
import { describe, expect, it } from "vitest";
import inboxCatalog from "@/app/screens/inbox/messages/en.json";
import screensCatalog from "@/app/screens/messages/en.json";
import { shellMessages } from "@/lib/i18n";
import { failOnIntlError } from "@/test/render";
import { severityLine } from "./activity";
import { newTabLabel } from "./screens/inbox/card";
import { SafeModeSourceLine } from "./settings/rules";

// The voice guide asks for whole sentences in one message. These messages
// are in a language that orders the parts of each sentence otherwise than
// English does, with punctuation of its own: a sentence written as one
// message follows them, and one put together in code doesn't.
const reordered: Record<string, string> = {
  "app.activity.line":
    "{text} <hidden>({severity, select, error {error} other {something else}})</hidden>",
  "app.inbox.newTab": "<hidden>New tab:</hidden> {label}",
  "app.safeMode.sourceLine":
    "{hasSince, select, yes {<since>From {when}</since>, } other {}}{source}",
};

function Formatted({ children }: { children: (intl: IntlShape) => ReactNode }) {
  return <>{children(useIntl())}</>;
}

function renderInOtherOrder(ui: ReactNode) {
  return render(
    <IntlProvider
      locale="en"
      messages={{ ...shellMessages, ...screensCatalog, ...inboxCatalog, ...reordered }}
      onError={failOnIntlError}
    >
      {ui}
    </IntlProvider>,
  );
}

describe("sentences with parts", () => {
  it("put the parts where their message says", () => {
    renderInOtherOrder(
      <>
        <p data-testid="event">
          <Formatted>{(intl) => severityLine(intl, "error", "The backup failed")}</Formatted>
        </p>
        <button type="button">
          <Formatted>{(intl) => newTabLabel(intl, "How updates work")}</Formatted>
        </button>
        <ul>
          <SafeModeSourceLine
            source={{
              kind: "console",
              reason: "An add-on keeps failing",
              since: "2026-10-15T12:00:00Z",
            }}
          />
        </ul>
      </>,
    );
    expect(screen.getByTestId("event")).toHaveTextContent(/^The backup failed \(error\)$/);
    expect(screen.getByRole("button")).toHaveAccessibleName("New tab: How updates work");
    expect(screen.getByRole("listitem")).toHaveTextContent(
      /^From .*2026.*, Turned on from the console or this panel: An add-on keeps failing$/,
    );
  });
});
