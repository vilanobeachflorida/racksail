# Add-on SDKs

This directory is for the libraries add-on authors use, in Go, TypeScript and Python. An add-on runs as its own sandboxed systemd service and talks to the panel over a Unix socket: the panel calls the add-on for its hooks, and the add-on calls the panel's API with a token limited to the permissions it was granted.

| Directory | For |
|---|---|
| [go/](go/) | Go |
| [ts/](ts/) | TypeScript on Node.js |
| [python/](python/) | Python |

Each SDK provides the same things:

- serving the add-on protocol, [spec/addon-protocol.json](../spec/addon-protocol.json), on the socket systemd hands the add-on: its health, and hook calls dispatched to the add-on's handlers by hook point
- types for what hooks receive and return: change sets, resource changes, configuration patches and denials
- a client for the panel API, generated from [spec/openapi.json](../spec/openapi.json)
- reading the add-on's manifest, `addon.yaml` ([spec/schemas/addon.schema.json](../spec/schemas/addon.schema.json))
- a local test harness that plays the panel, so hooks can be tested without a server

The SDKs follow the hook API version of the protocol document. When it changes, every SDK supports the new version in the same release, and deprecated versions keep working for the published period.

How hooks, permissions and manifests work is described in [docs/architecture/extensibility.md](../docs/architecture/extensibility.md).
