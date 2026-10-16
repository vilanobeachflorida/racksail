---
title: "SDKs"
description: "Write an add-on in Go, TypeScript or Python: what it serves on its socket, how it calls the panel, and the SDK packages."
---

An add-on is a program that answers HTTP and JSON on a Unix socket. The panel calls it there for its hooks and for the capabilities it provides, and it calls the panel's API back through a socket of the panel's. Any language that can serve HTTP on a socket can write one. The protocol is described by an OpenAPI document, [spec/addon-protocol.json](https://github.com/vilanobeachflorida/racksail/blob/main/spec/addon-protocol.json), which `racksail openapi --addon` prints for the version you run.

## What an add-on serves

| Request | When the panel sends it | The answer |
|---|---|---|
| `GET /health` | After starting the add-on, and from its health checks | `{"status": "ok", "version": "1.0.0", "apiVersion": "v1"}`, or `degraded` when it can't take calls |
| `POST /hooks/<point>` | A hook of the add-on runs | What the hook decided, below |
| `POST /provide/<capability>/<method>` | The panel uses a capability the add-on provides | What the method returns |
| `POST /kinds/<kind>/plan` | A change to a resource of a kind the add-on [adds](../hooks/#kinds-an-add-on-adds) is previewed, and again when it is applied | Its plan, [below](#kind-calls) |
| `POST /kinds/<kind>/apply` | The steps of that plan have run | The resource's status, below |
| `/routes/<path>`, any method | Someone calls `/api/v1/addons/<id>/<path>`, one of the add-on's [routes](../ui-extensions/#api-routes) | Whatever the route answers |
| `GET /ui/<path>` | The panel shows one of the add-on's [screens](../ui-extensions/#screens) | The page |

Every request carries `Racksail-Hook-API`, the hook API version the panel speaks. An add-on answers within its hook's deadline, with at most 8 MiB, and answers an error as [problem details](https://www.rfc-editor.org/rfc/rfc9457) whose `detail` says what went wrong in plain words; the panel shows it, and the hook's [failure mode](../hooks/#order-deadlines-and-failures) decides what happens to the change.

### The socket

systemd listens on the add-on's socket and starts the add-on the first time the panel connects, handing it the socket as file descriptor 3, with `LISTEN_PID` set to the add-on's process ID and `LISTEN_FDS` to `1`. Serve on that descriptor. To run the add-on by hand, let it listen on a path of your choosing instead, as the examples below do with `--socket`.

### Hook calls

The panel posts a hook call to `/hooks/<point>`:

| Field | What it holds |
|---|---|
| `point` | The point the hook runs at, such as `core.webhook.create.validate` |
| `kind` | `validate`, `modify`, `observe` or `system` |
| `actor` | Who makes the change and through what: `type`, `id`, `name`, `user` and `source` |
| `changeSet` | At points of the change pipeline, the change set, with only what the add-on [may read](../permissions/#how-the-panel-holds-an-add-on-to-them) of each change. Each change has its `op`, `kind`, `id`, `scope` and, as far as it may see them, the settings `before` and `after` and their `diff` |
| `change` | For a validate hook, the index in `changeSet.changes` of the change to decide on |
| `changes` | For modify and observe hooks, the indexes of the changes the point is about |
| `config` | For a system hook, the configuration to patch |
| `data` | At lifecycle points, such as `core.boot`, what happened |

It answers with:

| Kind | Answer |
|---|---|
| validate | `{}` to let the change through, or `{"deny": true, "reason": "…"}` to refuse it with a reason the person making it sees |
| modify | `{"changeSet": …}`: the change set it was sent, with new settings for the changes it is about and any changes or effects it adds. An update it adds holds the resource's settings as the add-on reads them, with its changes; the panel takes only [what it may write](../permissions/#how-the-panel-holds-an-add-on-to-them) |
| observe | Anything; the panel doesn't wait for it to finish its work |
| system | `{"config": …}`: the patched configuration |

### Kind calls

To plan and apply a resource of a kind the add-on adds, the panel posts to `/kinds/<kind>/plan` and then `/kinds/<kind>/apply`:

| Field | What it holds |
|---|---|
| `resource` | The resource: its `id`, `kind`, `scope`, `labels`, `generation`, its settings in `spec`, its `status`, and `deleted` while it is deleted |
| `action` | When an action of the resource is planned or applied rather than its settings, the action's `name` and its `input` |
| `dir` | The only directory the plan's steps may touch, the add-on's own, which it can read |
| `results` | For apply, what each step of the plan returned |

The plan answers `{"steps": […], "effects": […]}`: the agent operations to run, `WriteFile`, `EnsureDir`, `RemovePath`, `ReadFile`, `StatPath` and `ListDir` of paths in `dir`, and what applying does besides, for the preview; an action that changes the resource's settings also answers its new `spec`. Planning must not change anything. Apply answers `{"status": {…}}`, the kind's own status fields, or for an action `{"output": …}`; an error answer fails the change.

## An example

This add-on refuses webhooks that would send events over plain HTTP. Its manifest asks for a validate hook on every operation on webhooks, and to read their `url`, nothing else:

```yaml
format: 1
id: webhook-guard
name: Webhook guard
version: 1.0.0
license: MIT
source: https://github.com/example/webhook-guard
author: Example Hosting
description: Refuses webhooks that would send events over plain HTTP.
apiVersions: [v1]
hooks:
  - point: core.webhook.*.validate
    kind: validate
    security: true
permissions:
  hooks: [validate]
  read:
    - kind: core.webhook
      fields: [url]
```

Because the hook is a security policy, the panel refuses new webhooks while the add-on doesn't answer, rather than let one through unchecked.

In Go, with nothing but the standard library:

```go
// Webhook guard refuses webhooks that would send events over plain HTTP.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// hookCall is the part of a hook call this add-on reads: the change set,
// and which of its changes to decide on.
type hookCall struct {
	ChangeSet struct {
		Changes []struct {
			After struct {
				URL string `json:"url"`
			} `json:"after"`
		} `json:"changes"`
	} `json:"changeSet"`
	Change *int `json:"change"`
}

func main() {
	socket := flag.String("socket", "", "the socket to listen on when systemd passes none")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, map[string]string{"status": "ok", "version": "1.0.0", "apiVersion": "v1"})
	})
	mux.HandleFunc("POST /hooks/{point}", func(w http.ResponseWriter, r *http.Request) {
		var call hookCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil || call.Change == nil || *call.Change >= len(call.ChangeSet.Changes) {
			reply(w, http.StatusBadRequest, map[string]any{"status": 400, "detail": "This add-on only has a validate hook."})
			return
		}
		if strings.HasPrefix(call.ChangeSet.Changes[*call.Change].After.URL, "http://") {
			reply(w, http.StatusOK, map[string]any{"deny": true, "reason": "Webhooks send events over HTTPS only, so nobody can read them on the way."})
			return
		}
		reply(w, http.StatusOK, map[string]any{})
	})

	l, err := listen(*socket)
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.Serve(l, mux))
}

func reply(w http.ResponseWriter, status int, body any) {
	if status < 300 {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "application/problem+json")
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// listen returns the socket systemd passed the add-on, or one at path when
// it runs by hand.
func listen(path string) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) {
		return net.FileListener(os.NewFile(3, "panel"))
	}
	if path == "" {
		return nil, errors.New("systemd passed no socket; give one with -socket")
	}
	os.Remove(path)
	return net.Listen("unix", path)
}
```

In Python, with nothing but the standard library:

```python
"""Webhook guard refuses webhooks that would send events over plain HTTP."""

import argparse
import json
import os
import socket
import socketserver
from http.server import BaseHTTPRequestHandler


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/health":
            return self.reply(404, {"status": 404, "detail": "There is nothing here."})
        self.reply(200, {"status": "ok", "version": "1.0.0", "apiVersion": "v1"})

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        call = json.loads(body) if self.path.startswith("/hooks/") and body else {}
        changes = (call.get("changeSet") or {}).get("changes") or []
        index = call.get("change")
        if index is None or index >= len(changes):
            return self.reply(400, {"status": 400, "detail": "This add-on only has a validate hook."})
        if (changes[index].get("after") or {}).get("url", "").startswith("http://"):
            return self.reply(200, {"deny": True, "reason": "Webhooks send events over HTTPS only, so nobody can read them on the way."})
        self.reply(200, {})

    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json" if status < 300 else "application/problem+json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def address_string(self):
        # Unix sockets have no client address to log.
        return "panel"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", help="the socket to listen on when systemd passes none")
    args = parser.parse_args()
    if os.environ.get("LISTEN_PID") == str(os.getpid()):
        server = socketserver.UnixStreamServer("", Handler, bind_and_activate=False)
        server.socket.close()
        server.socket = socket.socket(fileno=3)
    elif args.socket:
        if os.path.exists(args.socket):
            os.remove(args.socket)
        server = socketserver.UnixStreamServer(args.socket, Handler)
    else:
        raise SystemExit("systemd passed no socket; give one with --socket")
    server.serve_forever()


if __name__ == "__main__":
    main()
```

In TypeScript on Node.js:

```ts
// Webhook guard refuses webhooks that would send events over plain HTTP.
import { rmSync } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { parseArgs } from "node:util";

interface HookCall {
  changeSet?: { changes?: { after?: { url?: string } }[] };
  change?: number;
}

function reply(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, {
    "Content-Type": status < 300 ? "application/json" : "application/problem+json",
  });
  res.end(JSON.stringify(body));
}

async function read(req: IncomingMessage): Promise<string> {
  let body = "";
  for await (const chunk of req) {
    body += chunk;
  }
  return body;
}

const server = createServer(async (req, res) => {
  if (req.method === "GET" && req.url === "/health") {
    return reply(res, 200, { status: "ok", version: "1.0.0", apiVersion: "v1" });
  }
  const call = (req.method === "POST" && req.url?.startsWith("/hooks/") ? JSON.parse(await read(req)) : {}) as HookCall;
  const change = call.change === undefined ? undefined : call.changeSet?.changes?.[call.change];
  if (!change) {
    return reply(res, 400, { status: 400, detail: "This add-on only has a validate hook." });
  }
  if (change.after?.url?.startsWith("http://")) {
    return reply(res, 200, {
      deny: true,
      reason: "Webhooks send events over HTTPS only, so nobody can read them on the way.",
    });
  }
  reply(res, 200, {});
});

const { values } = parseArgs({ options: { socket: { type: "string" } } });
if (process.env.LISTEN_PID === String(process.pid)) {
  server.listen({ fd: 3 });
} else if (values.socket) {
  rmSync(values.socket, { force: true });
  server.listen(values.socket);
} else {
  console.error("systemd passed no socket; give one with --socket");
  process.exit(1);
}
```

The documentation's tests build and run each of these, and call them the way the panel does.

## Try it by hand

Run the add-on on a socket of your own and call it as the panel would, with curl:

```sh
go run . -socket /tmp/guard.sock
curl --unix-socket /tmp/guard.sock http://addon/health
curl --unix-socket /tmp/guard.sock http://addon/hooks/core.webhook.create.validate \
  -H 'Content-Type: application/json' \
  -d '{"point": "core.webhook.create.validate", "kind": "validate", "change": 0,
       "changeSet": {"changes": [{"op": "create", "kind": "core.webhook", "after": {"url": "http://hooks.example.com/"}}]}}'
```

The second call answers `{"deny":true,"reason":"Webhooks send events over HTTPS only, so nobody can read them on the way."}`.

## Capabilities

A capability's methods are posted to `/provide/<capability>/<method>` with the settings an admin saved when they connected the add-on, its secret settings filled in, next to what the method takes:

```json
{"input": {"settings": {"url": "ldaps://ldap.example.com"}, "input": {"username": "ada", "password": "…"}}}
```

So an add-on needs no settings of its own: everything a call needs comes with it, and what a sign-in in progress needs, the panel keeps and sends back.

| Capability | Method | Takes | Answers |
|---|---|---|---|
| `auth-provider` | `describe` | nothing | `{"method": "password"}` for a username and password typed on the panel's own screen, or `{"method": "redirect"}` for the provider's own page |
| | `check-password` | `{"username", "password"}` | `{"identity": {...}}`, or `{"refused": true, "reason"}` |
| | `begin` | `{"callback"}`, where the provider sends the browser back to | `{"redirect", "state"}`: where to send the browser, and what the panel keeps until it comes back |
| | `complete` | `{"state", "callback", "query"}` | `{"identity": {...}}`, or `{"refused": true, "reason"}` |
| `mfa` | `challenge` | `{"user": {"id", "username", "name", "email"}}` | `{"prompt", "input", "state"}`: what to ask the person, whether they type an answer, and what the panel keeps |
| | `verify` | `{"user", "state", "response"}` | `{"result": "passed"}`, `"pending"` while the person hasn't answered yet, or `"failed"` |
| `notifier` | `send` | A message: `id`, the same on every attempt to deliver it, `title`, `body`, `severity`, `url` and `actionLabel`, the `event` it is about when there is one, and in a summary of quiet hours the messages it stands for, as `digest` | Nothing |

An identity is `{"subject", "username", "name", "email", "groups"}`, where `subject` never changes for the person at the provider. A refusal is a wrong password, or someone the provider doesn't know or turned down; a provider that can't answer, or settings that can't work, answer with problem details instead. The official [LDAP and OpenID Connect add-ons](https://github.com/vilanobeachflorida/racksail/tree/main/addons) implement `auth-provider` in Go.

## Call the panel

An add-on calls the panel's API over the socket `RACKSAIL_PANEL_SOCKET` names, with the token systemd hands it, which it reads from `$CREDENTIALS_DIRECTORY/token`:

```sh
curl --unix-socket "$RACKSAIL_PANEL_SOCKET" \
  -H "Authorization: Bearer $(cat "$CREDENTIALS_DIRECTORY/token")" \
  http://panel/api/v1/core/webhooks
```

It calls the same API as everyone, described in [Using the API](../../api/overview/), as an actor whose grants are its [permissions](../permissions/): answers hold only what it may read, and the changes it makes go through the change pipeline, with their previews, rules and hooks. An add-on allowed to read secrets reads one at `GET /secrets/<reference>`, with the reference a resource's secret field holds, as long as it may read that resource. `RACKSAIL_ADDON` holds the add-on's own name on the server.

## Package it

The panel installs an add-on from an archive that holds its program, an executable called `addon`, at its top. The program has to run on a Debian 13 server as it is: a Go add-on is one static binary; one in TypeScript or Python is built into a single executable with its runtime, or brings its runtime in the archive. [Publishing add-ons](../publishing/) says how to build the archive and offer it in a catalog.

## The SDK packages

The SDKs in [sdk/](https://github.com/vilanobeachflorida/racksail/tree/main/sdk) follow the protocol's hook API versions: each lists the versions it speaks, newest first, and says whether it speaks one. They have nothing else, so the examples above don't need them.

| Language | Package | The versions it speaks | Whether it speaks one |
|---|---|---|---|
| Go | `github.com/vilanobeachflorida/racksail/sdk/go`, imported as `addon` | `addon.APIVersions` | `addon.Supports("v1")` |
| TypeScript | `@racksail/addon-sdk` | `apiVersions` | `supports("v1")` |
| Python | `racksail-addon`, imported as `racksail_addon` | `racksail_addon.API_VERSIONS` | `racksail_addon.supports("v1")` |

When the hook API gets a new version, every SDK speaks it in the same release, and deprecated versions keep working for a published period.
