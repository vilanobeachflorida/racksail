import "./design/globals.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/app";
import { applyStoredPreferences } from "./design/theme/prefs";
import { createApiClient } from "./lib/api/client";
import { applyLocale, initialLocale } from "./lib/i18n";

// Set the mode, contrast, size and language before the first paint, so the
// page never flashes in the wrong ones.
applyStoredPreferences();
applyLocale(initialLocale());

const container = document.getElementById("root");
if (!container) {
  throw new Error("index.html has no #root element");
}
createRoot(container).render(
  <StrictMode>
    <App api={createApiClient()} />
  </StrictMode>,
);
