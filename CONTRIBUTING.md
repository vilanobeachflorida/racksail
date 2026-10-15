# Contributing to Racksail

Thanks for helping. Bug reports, documentation, translations, recipes and code are all welcome.

## Before you start

- For anything bigger than a small fix, open an issue first so we can agree on the approach before you spend time on it.
- [docs/architecture/](docs/architecture/) is the plan. [features.md](docs/architecture/features.md) lists what 1.0 includes, and [decisions.md](docs/architecture/decisions.md) records settled choices. To change a decision, open a pull request that edits that file and explains why.
- Security problems never go in public issues. See [SECURITY.md](SECURITY.md).

## Setting up

You need Go 1.27, Node.js 22.22.2 or later with npm (24.15 or later on Node.js 24), git and make, on macOS or Linux.

```sh
git clone https://github.com/vilanobeachflorida/racksail.git
cd racksail
make dev
```

`make dev` runs `racksail dev`, which is the panel, the agent and the edge in one process against a simulated server, on http://127.0.0.1:7070, and the UI dev server with hot reload on http://127.0.0.1:5173. Nothing touches your machine: the simulated server and the panel's state live in memory, so every start begins with a freshly installed server, and `.dev/` only holds empty working directories and the sockets of the simulated agent and the edge.

[docs/architecture/internals.md](docs/architecture/internals.md) explains how the code fits together: the packages, the module contract, reconcilers and agent operations, the simulated backend and where to add things.

## Checking your work

```sh
make check
```

It runs gofmt, go vet and staticcheck (for darwin and Linux), builds for darwin, linux/amd64 and linux/arm64, the Go tests, and the UI's type check, lint, tests and build. A pull request needs it to pass on Linux and macOS. CI also runs `make test-race` and `make docs`.

Other useful targets:

- `make test` runs the Go and UI tests; `make test-race` runs the Go tests with the race detector, which needs a C compiler.
- `make gen` regenerates `spec/openapi.json`, `spec/agent-protocol.json`, `spec/addon-protocol.json`, the UI's API types and its theme CSS. Tests fail when these drift from the code, so run it after changing an API operation, an agent operation, the add-on protocol or the default theme.
- `make fmt` formats Go and UI code.
- `make docs` builds the documentation site.

## Conventions

The full list is in [conventions.md](docs/architecture/conventions.md). The short version:

- **Go:** gofmt, go vet and staticcheck clean, no CGO. Linux-only code lives in `internal/system/linux` or behind a `linux` build tag, and everything also builds for darwin. External programs run only through agent operations, with argument vectors. Rendering configuration is pure and has golden-file tests.
- **API:** every operation has a summary and a description written for a person. Errors carry a stable code, and every code has a message in the UI's catalogs.
- **UI:** TypeScript strict with no `any`. Colors, spacing, radii and type come from theme tokens only. Every user-visible string goes through react-intl. CSS logical properties, keyboard access and visible focus everywhere.
- **Tests:** table-driven Go tests, golden files for renderers, and tests against the simulated backend for anything that changes a machine.
- **Comments** explain why, not what, and stay short.
- **Docs** are written for the person using Racksail: sentence-case headings, plain words, short paragraphs and examples.

## Commits and pull requests

- Write the subject line in the imperative, under 72 characters ("Add DNS zone import"). Add a body when the reason for the change isn't obvious.
- Keep a pull request to one topic. Include tests, and update the docs when behaviour changes. Screenshots help for UI changes.
- Sign off every commit to certify the [Developer Certificate of Origin](https://developercertificate.org/): that you wrote the change or otherwise have the right to submit it under the project's licenses. `git commit -s` adds the `Signed-off-by:` line. It's the only trailer we use; please don't add others. We use the DCO instead of a contributor license agreement, so no single party can relicense the project.

## Licenses

Code is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE). Recipes in the official catalog are licensed under MIT, and the documentation under CC BY 4.0. By contributing, you agree that your contribution is licensed the same way. New dependencies need an OSI-approved license compatible with AGPL-3.0, and a reason.

## Conduct

Everyone taking part is expected to follow the [code of conduct](CODE_OF_CONDUCT.md).
