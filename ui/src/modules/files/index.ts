import { FolderOpen } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "files",
  name: m("files.name"),
  description: m("files.description"),
  icon: FolderOpen,
});
