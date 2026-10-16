# Add-on SDKs

This directory is for the libraries add-on authors use, in Go, TypeScript and Python. An add-on runs as its own sandboxed systemd service and talks to the panel over a Unix socket: the panel calls the add-on for its hooks, and the add-on calls the panel's API with a token limited to the permissions it was granted.

| Directory | For |
|---|---|
| [go/](go/) | Go |
| [ts/](ts/) | TypeScript on Node.js |
| [python/](python/) | Python |

Each SDK has the same things, under the names its language expects:

| What | Go | TypeScript | Python |
|---|---|---|---|
| The hook API versions the SDK speaks, newest first | `addon.APIVersions` | `apiVersions` | `racksail_addon.API_VERSIONS` |
| Whether the SDK speaks a version | `addon.Supports` | `supports` | `racksail_addon.supports` |

That is all they have. An add-on serves the protocol itself: [spec/addon-protocol.json](../spec/addon-protocol.json) describes what the panel calls on the add-on's socket, and the [SDKs page](https://vilanobeachflorida.github.io/racksail/addons/sdks/) of the documentation shows how to answer it with each language's standard library. The official add-ons in [addons/](../addons/) do it in Go.

The SDKs follow the hook API version of the protocol document. When it changes, every SDK supports the new version in the same release, and deprecated versions keep working for the published period. A test in each keeps the newest version it lists equal to the protocol document's, and `docs/sdk_test.go` keeps this table, and the examples in each SDK's README, to what the SDKs define.

`make sdk-check`, part of `make check`, runs the tests of all three: `go test` in [go/](go/), which is a Go module of its own, the type check and tests of [ts/](ts/) with Node.js, and the tests of [python/](python/) with the standard library's unittest, on Python 3.11 or later.

How hooks, permissions and manifests work is described in [docs/architecture/extensibility.md](../docs/architecture/extensibility.md).
