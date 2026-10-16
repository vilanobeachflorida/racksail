/**
 * The connection behind a terminal: a shell on the server, reached over a
 * WebSocket. Output arrives as frames of text or bytes; input and size
 * changes go out as small JSON messages:
 *
 *   {"type":"input","data":"ls\r"}
 *   {"type":"resize","cols":120,"rows":32}
 */
export interface TerminalTransport {
  send: (data: string) => void;
  resize: (cols: number, rows: number) => void;
  onData: (listener: (data: string | Uint8Array) => void) => () => void;
  /** Called once the connection is open. */
  onOpen: (listener: () => void) => () => void;
  /** Called when the connection ends, with the server's reason if it gave one. */
  onClose: (listener: (reason: string) => void) => () => void;
  close: () => void;
}

type Listener<T extends unknown[]> = (...args: T) => void;

class Listeners<T extends unknown[]> {
  private items = new Set<Listener<T>>();

  add(listener: Listener<T>): () => void {
    this.items.add(listener);
    return () => this.items.delete(listener);
  }

  emit(...args: T): void {
    for (const listener of this.items) {
      listener(...args);
    }
  }
}

/** A transport over a WebSocket; relative URLs are resolved against the panel. */
export function webSocketTransport(
  url: string,
  base: string = window.location.href,
): TerminalTransport {
  const target = new URL(url, base);
  target.protocol =
    target.protocol === "https:" ? "wss:" : target.protocol === "http:" ? "ws:" : target.protocol;
  const socket = new WebSocket(target);
  socket.binaryType = "arraybuffer";
  const data = new Listeners<[string | Uint8Array]>();
  const opened = new Listeners<[]>();
  const closed = new Listeners<[string]>();
  const queue: string[] = [];
  socket.addEventListener("open", () => {
    for (const message of queue.splice(0)) {
      socket.send(message);
    }
    opened.emit();
  });
  socket.addEventListener("message", (event: MessageEvent) => {
    data.emit(
      typeof event.data === "string" ? event.data : new Uint8Array(event.data as ArrayBuffer),
    );
  });
  socket.addEventListener("close", (event: CloseEvent) => closed.emit(event.reason));
  const post = (message: object) => {
    const text = JSON.stringify(message);
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(text);
    } else if (socket.readyState === WebSocket.CONNECTING) {
      queue.push(text);
    }
  };
  return {
    send: (input) => post({ type: "input", data: input }),
    resize: (cols, rows) => post({ type: "resize", cols, rows }),
    onData: (listener) => data.add(listener),
    onOpen: (listener) => opened.add(listener),
    onClose: (listener) => closed.add(listener),
    close: () => socket.close(1000),
  };
}
