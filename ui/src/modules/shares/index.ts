import { FolderTree } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "shares",
  name: m("shares.name"),
  description: m("shares.description"),
  icon: FolderTree,
});
