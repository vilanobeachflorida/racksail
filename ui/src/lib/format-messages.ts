import type catalog from "@/app/messages/en.json";
import { messageIds } from "./messages";

const m = messageIds<typeof catalog>();

/** Units the formatters can't get from Intl, such as binary sizes. */
export const formatMessages = {
  bytes: m("format.bytes"),
  kibibytes: m("format.kibibytes"),
  mebibytes: m("format.mebibytes"),
  gibibytes: m("format.gibibytes"),
  tebibytes: m("format.tebibytes"),
  pebibytes: m("format.pebibytes"),
  perSecond: m("format.perSecond"),
};
