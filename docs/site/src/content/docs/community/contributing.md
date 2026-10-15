---
title: "Contributing"
description: "Set up a development environment and send your first change."
---

Bug reports, documentation, translations, recipes and code are all welcome.

## Before you start

- For anything bigger than a small fix, open an issue first, so we can agree on the approach before you spend time on it.
- [docs/architecture](https://github.com/vilanobeachflorida/racksail/tree/main/docs/architecture) is the plan. [features.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/features.md) lists what 1.0 includes, and [decisions.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/decisions.md) records settled choices. To change a decision, open a pull request that edits that file and explains why.
- Security problems never go in public issues. See [Reporting a vulnerability](../../security/reporting/).

## Set up

```sh
git clone https://github.com/vilanobeachflorida/racksail.git
cd racksail
make dev
```

[Developing Racksail](../../architecture/development/) covers what you need installed, running the panel against a simulated server, and the checks.

## Make your change

- Keep a pull request to one topic.
- Add tests: table-driven Go tests, golden files for anything that renders configuration, and flows against the simulated server for anything that changes a machine.
- Update the docs when behaviour changes. User-visible strings go through the message catalogs.
- Run `make check` before you push. After changing an API operation, an agent operation, the add-on protocol or the default theme, run `make gen` too.

The conventions are in [conventions.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/conventions.md). The short version:

- **Go**: gofmt, go vet and staticcheck clean, no CGO. Linux-only code sits in `internal/system/linux` or behind a build tag, and everything also builds on macOS. Programs run only through agent operations, with argument lists, never a shell.
- **API**: every operation has a summary and a description written for a person. Errors carry a stable code with a message in the UI's catalogs.
- **UI**: TypeScript strict with no `any`. Colors, spacing and type come from theme tokens. CSS logical properties, keyboard access and visible focus everywhere.
- **Comments** explain why, not what, and stay short.
- **Docs** are written for the person using Racksail: sentence-case headings, plain words, short paragraphs and examples.

## Commits

- Write the subject in the imperative, under 72 characters: "Add DNS zone import". Add a body when the reason isn't obvious.
- Sign off every commit to certify the [Developer Certificate of Origin](https://developercertificate.org/): that you wrote the change or have the right to submit it under the project's licenses. `git commit -s` adds the `Signed-off-by:` line, the only trailer we use. CI checks that every commit of a pull request has one for its author, and tells you how to add the ones you forgot: `git rebase --signoff` from where your branch started, then push again.

We use the DCO instead of a contributor license agreement, so no single party can relicense the project.

## Dependencies

New dependencies need a reason, and a license approved by the Open Source Initiative that is compatible with AGPL-3.0. `make licenses` checks every Go module and npm package, and CI runs it on every pull request. Prefer the standard library.

## Recipes and the catalog

Recipes for the official catalog live in [catalog/](https://github.com/vilanobeachflorida/racksail/tree/main/catalog). Every item names its maintainers, has an icon and health checks, declares an install test with a value for every input it requires, and pins its downloads by checksum. `make catalog-check` checks your item the way CI does, the install test's inputs included. CI runs the install tests themselves once the marketplace module installs recipes. See [Recipes](../../recipes/overview/).

## Licenses

Code is licensed under the GNU Affero General Public License v3.0 or later. Recipes in the official catalog are licensed under MIT, and the documentation under CC BY 4.0. By contributing, you agree your contribution is licensed the same way.

## Conduct

Everyone taking part follows the [code of conduct](../code-of-conduct/).
