import { messageIds } from "@/lib/messages";
import type catalog from "./messages/en.json";

const m = messageIds<typeof catalog>();

// What an item says is looked up by its code, inbox.<code>, so those
// messages have no descriptors here.

/** The attention inbox, the home screen's first card. */
export const inboxMessages = {
  title: m("app.inbox.title"),
  count: m("app.inbox.count"),
  showDismissed: m("app.inbox.showDismissed"),
  failed: m("app.inbox.failed"),
  empty: m("app.inbox.empty"),
  emptyHelp: m("app.inbox.emptyHelp"),
  checking: m("app.inbox.checking"),
  source: m("app.inbox.source"),
  unavailable: m("app.inbox.unavailable"),
  category: m("app.inbox.category"),
  times: m("app.inbox.times"),
  dismissedNote: m("app.inbox.dismissedNote"),
  snoozedUntil: m("app.inbox.snoozedUntil"),
  more: m("app.inbox.more"),
  dismiss: m("app.inbox.dismiss"),
  snoozeDay: m("app.inbox.snoozeDay"),
  snoozeWeek: m("app.inbox.snoozeWeek"),
  restore: m("app.inbox.restore"),
  dismissed: m("app.inbox.dismissed"),
  snoozed: m("app.inbox.snoozed"),
  undo: m("app.inbox.undo"),
  restoring: m("app.inbox.restoring"),
  restored: m("app.inbox.restored"),
  restoreFailed: m("app.inbox.restoreFailed"),
  dismissFailed: m("app.inbox.dismissFailed"),
  retryFailed: m("app.inbox.retryFailed"),
  all: m("app.inbox.all"),
  fewer: m("app.inbox.fewer"),
  newTab: m("app.inbox.newTab"),
  actionOpen: m("app.inbox.actionOpen"),
  actionLog: m("app.inbox.actionLog"),
  actionChange: m("app.inbox.actionChange"),
  actionRetry: m("app.inbox.actionRetry"),
  actionUndo: m("app.inbox.actionUndo"),
  actionEvents: m("app.inbox.actionEvents"),
  actionAbout: m("app.inbox.actionAbout"),
  actionUpdates: m("app.inbox.actionUpdates"),
};
