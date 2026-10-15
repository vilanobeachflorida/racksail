import { Container } from "lucide-react";
import { messageIds } from "@/lib/messages";
import { defineModule } from "../types";
import type messages from "./messages/en.json";

const m = messageIds<typeof messages>();

export default defineModule({
  id: "containers",
  name: m("containers.name"),
  description: m("containers.description"),
  icon: Container,
});
