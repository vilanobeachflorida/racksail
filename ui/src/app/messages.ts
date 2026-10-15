import { messageIds } from "@/lib/messages";
import type catalog from "./messages/en.json";

const m = messageIds<typeof catalog>();

/** Descriptors for the shell's own messages. */
export const messages = {
  name: m("app.name"),
  tagline: m("app.tagline"),
  documentTitle: m("app.documentTitle"),
  skipToContent: m("app.skipToContent"),
  loading: m("app.loading"),
  navLabel: m("app.nav.label"),
  navHome: m("app.nav.home"),
  homeTitle: m("app.home.title"),
  statusTitle: m("app.status.title"),
  statusOk: m("app.status.ok"),
  statusDegraded: m("app.status.degraded"),
  statusChecks: m("app.status.checks"),
  statusCheckOk: m("app.status.checkOk"),
  statusCheckFailing: m("app.status.checkFailing"),
  statusUnreachable: m("app.status.unreachable"),
  statusVersion: m("app.status.version"),
  statusBuild: m("app.status.build"),
  notFoundTitle: m("app.notFound.title"),
  notFoundBody: m("app.notFound.body"),
  notFoundHome: m("app.notFound.home"),
  errorTitle: m("app.error.title"),
  errorReload: m("app.error.reload"),
  bootTitle: m("app.boot.title"),
  bootBody: m("app.boot.body"),
};
