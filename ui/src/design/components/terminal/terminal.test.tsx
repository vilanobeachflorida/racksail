import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/render";
import { Terminal } from ".";
import { type TerminalTransport, webSocketTransport } from "./transport";

// xterm.js needs a real canvas and layout, so the tests swap in a stand-in
// that records what the component does with it.
const terminals: FakeXTerm[] = [];

class FakeXTerm {
  options: Record<string, unknown>;
  cols = 80;
  rows = 24;
  textarea: HTMLTextAreaElement | undefined;
  written: (string | Uint8Array)[] = [];
  disposed = false;
  private listeners = new Set<(data: string) => void>();

  constructor(options: Record<string, unknown>) {
    this.options = options;
    terminals.push(this);
  }
  loadAddon() {}
  open(parent: HTMLElement) {
    this.textarea = document.createElement("textarea");
    parent.append(this.textarea);
  }
  write(data: string | Uint8Array) {
    this.written.push(data);
  }
  onData(listener: (data: string) => void) {
    this.listeners.add(listener);
    return { dispose: () => this.listeners.delete(listener) };
  }
  /** Someone types in the terminal. */
  type(data: string) {
    for (const listener of this.listeners) {
      listener(data);
    }
  }
  dispose() {
    this.disposed = true;
  }
}

vi.mock("@xterm/xterm", () => ({ Terminal: FakeXTerm }));
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
  },
}));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));

function fakeTransport() {
  const listeners = {
    data: new Set<(data: string | Uint8Array) => void>(),
    open: new Set<() => void>(),
    close: new Set<(reason: string) => void>(),
  };
  const on =
    <T,>(set: Set<T>) =>
    (listener: T) => {
      set.add(listener);
      return () => {
        set.delete(listener);
      };
    };
  const transport = {
    send: vi.fn(),
    resize: vi.fn(),
    close: vi.fn(),
    onData: on(listeners.data),
    onOpen: on(listeners.open),
    onClose: on(listeners.close),
  } satisfies TerminalTransport;
  const emit = <T extends unknown[]>(set: Set<(...args: T) => void>, ...args: T) =>
    act(() => {
      for (const listener of set) {
        listener(...args);
      }
    });
  return {
    transport,
    open: () => emit(listeners.open),
    output: (data: string) => emit(listeners.data, data),
    end: (reason: string) => emit(listeners.close, reason),
  };
}

afterEach(() => {
  terminals.length = 0;
  vi.unstubAllGlobals();
});

describe("the terminal", () => {
  it("connects, shows output, sends input and fits the window", async () => {
    const session = fakeTransport();
    const { unmount } = renderWithProviders(
      <Terminal label="Shell on web1" connect={() => session.transport} />,
    );
    expect(await screen.findByText("Connecting")).toBeInTheDocument();
    const xterm = terminals[0] as FakeXTerm;
    expect(xterm.textarea).toHaveAccessibleName("Shell on web1");
    expect(xterm.options.fontSize).toBe(13);

    session.open();
    expect(screen.getByText("Connected")).toBeInTheDocument();
    expect(session.transport.resize).toHaveBeenCalledWith(80, 24);
    session.output("racksail@web1:~$ ");
    expect(xterm.written).toEqual(["racksail@web1:~$ "]);
    xterm.type("ls\r");
    expect(session.transport.send).toHaveBeenCalledWith("ls\r");

    unmount();
    expect(session.transport.close).toHaveBeenCalled();
    expect(xterm.disposed).toBe(true);
  });

  it("says why the session ended and reconnects", async () => {
    const first = fakeTransport();
    const second = fakeTransport();
    const connect = vi
      .fn()
      .mockReturnValueOnce(first.transport)
      .mockReturnValueOnce(second.transport);
    renderWithProviders(<Terminal label="Shell" connect={connect} />);
    await screen.findByText("Connecting");
    first.open();
    first.end("the server restarted");
    expect(screen.getByText("Disconnected: the server restarted")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    expect(connect).toHaveBeenCalledTimes(2);
    expect(first.transport.close).toHaveBeenCalled();
    expect(screen.getByText("Connecting")).toBeInTheDocument();
    second.open();
    expect(screen.getByText("Connected")).toBeInTheDocument();
  });
});

class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static sockets: FakeWebSocket[] = [];
  readonly url: string;
  readyState = FakeWebSocket.CONNECTING;
  binaryType = "blob";
  sent: string[] = [];
  closedWith: number | undefined;
  private target = new EventTarget();

  constructor(url: URL | string) {
    this.url = String(url);
    FakeWebSocket.sockets.push(this);
  }
  addEventListener(type: string, listener: EventListener) {
    this.target.addEventListener(type, listener);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close(code: number) {
    this.closedWith = code;
  }
  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.target.dispatchEvent(new Event("open"));
  }
  receive(data: string | ArrayBuffer) {
    this.target.dispatchEvent(new MessageEvent("message", { data }));
  }
  end(reason: string) {
    const event = new Event("close") as Event & { reason: string };
    Object.defineProperty(event, "reason", { value: reason });
    this.target.dispatchEvent(event);
  }
}

describe("the WebSocket transport", () => {
  it("connects to the panel's address over ws or wss", () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    webSocketTransport("/api/v1/terminal/sessions/t1", "https://panel.example.com/files");
    webSocketTransport("/api/v1/terminal/sessions/t2", "http://192.0.2.10:7205/");
    expect(FakeWebSocket.sockets.map((s) => s.url)).toEqual([
      "wss://panel.example.com/api/v1/terminal/sessions/t1",
      "ws://192.0.2.10:7205/api/v1/terminal/sessions/t2",
    ]);
    expect(FakeWebSocket.sockets[0]?.binaryType).toBe("arraybuffer");
  });

  it("queues input until the connection opens and passes output on", () => {
    vi.stubGlobal("WebSocket", FakeWebSocket);
    FakeWebSocket.sockets.length = 0;
    const transport = webSocketTransport("/t", "https://panel.example.com/");
    const socket = FakeWebSocket.sockets[0] as FakeWebSocket;
    const opened = vi.fn();
    const output: (string | Uint8Array)[] = [];
    const ended: string[] = [];
    transport.onOpen(opened);
    transport.onData((d) => output.push(d));
    transport.onClose((r) => ended.push(r));

    transport.send("ls\r");
    transport.resize(120, 32);
    expect(socket.sent).toEqual([]);
    socket.open();
    expect(opened).toHaveBeenCalled();
    expect(socket.sent).toEqual([
      '{"type":"input","data":"ls\\r"}',
      '{"type":"resize","cols":120,"rows":32}',
    ]);
    transport.send("\u0003");
    expect(socket.sent.at(-1)).toBe('{"type":"input","data":"\\u0003"}');

    socket.receive("hello\r\n");
    socket.receive(new Uint8Array([0x1b, 0x5b, 0x48]).buffer);
    expect(output[0]).toBe("hello\r\n");
    expect(output[1]).toEqual(new Uint8Array([0x1b, 0x5b, 0x48]));

    socket.end("idle for 30 minutes");
    expect(ended).toEqual(["idle for 30 minutes"]);
    transport.close();
    expect(socket.closedWith).toBe(1000);
  });
});
