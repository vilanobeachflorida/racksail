import "@xterm/xterm/css/xterm.css";
import "./terminal.css";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { type ITheme, Terminal as XTerm } from "@xterm/xterm";
import { useEffect, useRef, useState } from "react";
import { useIntl } from "react-intl";
import { useOptionalTheme } from "@/design/theme/provider";
import { readToken } from "@/design/tokens";
import { cn } from "@/lib/utils";
import { designMessages as m } from "../../messages";
import { Button } from "../ui/button";
import { StatusDot } from "../ui/states";
import type { TerminalTransport } from "./transport";

/** The terminal's colors, from theme tokens: status colors for the ANSI ones. */
function terminalTheme(): ITheme {
  const t = (name: Parameters<typeof readToken>[0]) => readToken(name) || undefined;
  return {
    background: t("surface-sunken"),
    foreground: t("text"),
    cursor: t("text"),
    cursorAccent: t("surface-sunken"),
    selectionBackground: t("accent-subtle"),
    selectionForeground: t("text"),
    black: t("text-muted"),
    red: t("danger"),
    green: t("success"),
    yellow: t("warning"),
    blue: t("info"),
    magenta: t("chart5"),
    cyan: t("chart3"),
    white: t("text"),
    brightBlack: t("border-strong"),
    brightRed: t("danger"),
    brightGreen: t("success"),
    brightYellow: t("warning"),
    brightBlue: t("accent"),
    brightMagenta: t("chart7"),
    brightCyan: t("chart3"),
    brightWhite: t("text"),
  };
}

export interface TerminalProps {
  /** Names the terminal for screen readers: "Shell on web1". */
  label: string;
  /** Opens a session; called again by Reconnect. */
  connect: () => TerminalTransport;
  /** Has the terminal read output aloud with screen readers. */
  screenReaderMode?: boolean;
  className?: string;
}

type State = "connecting" | "open" | "closed";

/**
 * A terminal in the browser (xterm.js) connected to a shell on the server.
 * It takes the theme's colors and font, fits its container, makes links
 * clickable and can reconnect when the session ends.
 */
export default function Terminal({
  label,
  connect,
  screenReaderMode = false,
  className,
}: TerminalProps) {
  const intl = useIntl();
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<XTerm | null>(null);
  const [state, setState] = useState<State>("connecting");
  const [reason, setReason] = useState("");
  const [attempt, setAttempt] = useState(0);
  const tokensKey = useOptionalTheme()?.tokensKey;
  const connectRef = useRef(connect);
  useEffect(() => {
    connectRef.current = connect;
  });

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new attempt reconnects
  useEffect(() => {
    const parent = host.current;
    if (!parent) {
      return;
    }
    const terminal = new XTerm({
      fontFamily: readToken("font-mono") || "monospace",
      fontSize: 13,
      cursorBlink: true,
      scrollback: 5000,
      screenReaderMode,
      theme: terminalTheme(),
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.loadAddon(new WebLinksAddon());
    terminal.open(parent);
    terminal.textarea?.setAttribute("aria-label", label);
    term.current = terminal;
    setState("connecting");
    const transport = connectRef.current();
    const fitNow = () => {
      try {
        fit.fit();
        transport.resize(terminal.cols, terminal.rows);
      } catch {
        // A hidden terminal has no size to fit to.
      }
    };
    const offOpen = transport.onOpen(() => {
      setState("open");
      fitNow();
    });
    const offData = transport.onData((data) => terminal.write(data));
    const offClose = transport.onClose((why) => {
      setState("closed");
      setReason(why);
    });
    const input = terminal.onData((data) => transport.send(data));
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(fitNow);
    observer?.observe(parent);
    fitNow();
    return () => {
      observer?.disconnect();
      input.dispose();
      offOpen();
      offData();
      offClose();
      transport.close();
      terminal.dispose();
      term.current = null;
    };
  }, [attempt, label, screenReaderMode]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: tokensKey changes when the theme's colors do
  useEffect(() => {
    if (term.current) {
      term.current.options.theme = terminalTheme();
    }
  }, [tokensKey]);

  const status = {
    connecting: { dot: "pending" as const, text: intl.formatMessage(m.terminalConnecting) },
    open: { dot: "ok" as const, text: intl.formatMessage(m.terminalConnected) },
    closed: {
      dot: "off" as const,
      text: reason
        ? intl.formatMessage(m.terminalClosedReason, { reason })
        : intl.formatMessage(m.terminalClosed),
    },
  }[state];

  return (
    <div
      data-slot="terminal"
      className={cn(
        "flex min-h-64 flex-col overflow-hidden rounded-lg border border-border-strong",
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2 border-border border-b bg-muted px-3 py-1.5">
        <StatusDot status={status.dot} label={status.text} className="text-xs" />
        {state === "closed" ? (
          <Button size="sm" variant="outline" onClick={() => setAttempt((a) => a + 1)}>
            {intl.formatMessage(m.terminalReconnect)}
          </Button>
        ) : null}
      </div>
      <div ref={host} className="min-h-0 flex-1 bg-surface-sunken" />
    </div>
  );
}
